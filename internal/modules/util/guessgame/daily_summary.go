package guessgame

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	// SummaryInterval is the shortest gap between two updates of one
	// chat's live results message.
	SummaryInterval = 5 * time.Second
	// ResendInterval is the shortest gap between two new results messages
	// for one chat's day, when the old one was deleted.
	ResendInterval = 10 * time.Minute
	// maxTextUnits keeps a message under Telegram's 4096 UTF-16 unit cap,
	// leaving room for the "+N more" line.
	maxTextUnits = 4000
	// minStreakShown hides a one-day "streak".
	minStreakShown = 2
	// MaxSummaryRetries bounds the retries of one failed summary update.
	MaxSummaryRetries = 5
)

// flusher coalesces live summary updates: at most one is pending per chat
// day, and two never run closer than SummaryInterval. It is in-memory; a
// restart only delays the next update, because the message is always
// rendered from the store.
type flusher struct {
	mu    sync.Mutex
	state map[string]*flushState
}

type flushState struct {
	num       int
	scheduled bool
	last      time.Time
	retries   int // failed updates in a row
}

// TouchSummary schedules an update of the live results message of the chat
// topic's day. Only group chats (negative IDs) get one.
func (d *Daily) TouchSummary(num int, chatID int64, threadID int) {
	if d.cfg.API == nil || chatID >= 0 {
		return
	}
	key := ChatDayKey(num, chatID, threadID)
	now := d.cfg.Now()
	f := &d.flush
	f.mu.Lock()
	if f.state == nil {
		f.state = map[string]*flushState{}
	}
	st := f.state[key]
	if st == nil {
		for k, old := range f.state {
			if old.num < num-1 && !old.scheduled {
				delete(f.state, k)
			}
		}
		st = &flushState{num: num}
		f.state[key] = st
	}
	if st.scheduled {
		f.mu.Unlock()
		return
	}
	st.scheduled = true
	delay := max(SummaryInterval-now.Sub(st.last), 0)
	if st.last.IsZero() {
		delay = 0
	}
	f.mu.Unlock()
	d.cfg.Schedule(delay, func() { d.flushSummary(num, chatID, threadID) })
}

// flushSummary brings the chat topic's live results message up to date and
// schedules a bounded retry when that failed for a reason that may pass (a
// 429, a timeout, a 5xx, a store error) or when a deleted message may not be
// sent again yet.
func (d *Daily) flushSummary(num int, chatID int64, threadID int) {
	key := ChatDayKey(num, chatID, threadID)
	f := &d.flush
	f.mu.Lock()
	if st := f.state[key]; st != nil {
		st.scheduled, st.last = false, d.cfg.Now()
	}
	f.mu.Unlock()

	retry, after := d.syncSummary(key, chatID, threadID)

	f.mu.Lock()
	if f.state == nil {
		f.state = map[string]*flushState{}
	}
	st := f.state[key]
	if st == nil {
		st = &flushState{num: num, last: d.cfg.Now()}
		f.state[key] = st
	}
	if !retry {
		st.retries = 0
		f.mu.Unlock()
		return
	}
	if st.scheduled {
		// A newer update is already queued and renders the same state.
		f.mu.Unlock()
		return
	}
	if st.retries >= MaxSummaryRetries {
		st.retries = 0
		f.mu.Unlock()
		log.Warn(d.cfg.LogName+" summary update abandoned", "puzzle", num, "chat", chatID)
		return
	}
	delay := max(after, SummaryInterval<<st.retries)
	st.retries++
	st.scheduled = true
	f.mu.Unlock()
	d.cfg.Schedule(delay, func() { d.flushSummary(num, chatID, threadID) })
}

// syncSummary sends the chat day's results message the first time and
// edits it afterwards. A deleted message is sent again, at most once per
// ResendInterval. It reports whether the update must be retried, and the
// shortest wait before that.
//
// The lock is per chat topic, not per day: only the current day's message
// is updated, and keylock never frees a key, so a daily key would grow the
// lock map forever.
func (d *Daily) syncSummary(key string, chatID int64, threadID int) (bool, time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()
	defer d.Locks.Acquire("summary:" + chatTopic(chatID, threadID))()
	cd, _, err := d.ChatDays.Get(ctx, key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return false, 0
		}
		log.Warn(d.cfg.LogName+" summary read failed", "err", err)
		return true, 0
	}
	text, err := d.RenderLive(ctx, cd)
	if err != nil {
		log.Warn(d.cfg.LogName+" summary render failed", "err", err)
		return true, 0
	}
	if text == "" {
		return false, 0
	}
	now := d.cfg.Now()
	if cd.MsgID != 0 {
		_, err := d.cfg.API.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: cd.ChatID, MessageID: cd.MsgID, Text: text})
		switch {
		case err == nil || messageNotModified(err):
			return false, 0
		case !messageGone(err):
			log.Warn(d.cfg.LogName+" summary edit failed", "err", err)
			return retryAfter(err)
		}
		if wait := ResendInterval - now.Sub(time.UnixMilli(cd.MsgSentAt)); wait > 0 {
			// Deleted too recently to send again: try once the interval
			// has passed, so the final state still reaches the chat.
			return true, wait
		}
	}
	params := &bot.SendMessageParams{
		ChatID:              cd.ChatID,
		MessageThreadID:     cd.ThreadID,
		Text:                text,
		DisableNotification: true,
	}
	if cd.CardID != 0 {
		params.ReplyParameters = &models.ReplyParameters{MessageID: cd.CardID, AllowSendingWithoutReply: true}
	}
	msg, err := d.cfg.API.SendMessage(ctx, params)
	if err != nil {
		log.Warn(d.cfg.LogName+" summary send failed", "err", err)
		return retryAfter(err)
	}
	err = updateVersioned(ctx, d.ChatDays, key, func(v *ChatDay, found bool) bool {
		v.MsgID, v.MsgSentAt = msg.ID, now.UnixMilli()
		return found
	})
	if err != nil {
		log.Warn(d.cfg.LogName+" summary id save failed", "err", err)
	}
	return false, 0
}

// retryAfter classifies a failed Telegram call: a request Telegram refused
// (bad request, forbidden, not found, unauthorized) fails the same way
// again, while a 429 says how long to wait and a timeout or 5xx may pass.
func retryAfter(err error) (bool, time.Duration) {
	var tooMany *bot.TooManyRequestsError
	switch {
	case errors.As(err, &tooMany):
		return true, time.Duration(tooMany.RetryAfter) * time.Second
	case errors.Is(err, bot.ErrorBadRequest), errors.Is(err, bot.ErrorForbidden),
		errors.Is(err, bot.ErrorNotFound), errors.Is(err, bot.ErrorUnauthorized):
		return false, 0
	}
	return true, 0
}

func messageNotModified(err error) bool {
	return strings.Contains(err.Error(), "message is not modified")
}

// messageGone reports an edit that can never succeed, so the summary has to
// be sent again.
func messageGone(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "message to edit not found") ||
		strings.Contains(msg, "message can't be edited") ||
		strings.Contains(msg, "MESSAGE_ID_INVALID")
}

// chatResult is one player's game as a chat's results show it.
type chatResult struct {
	name string
	p    Progress
}

// chatResults loads the day's game of every player listed in cd who made a
// guess. Players are few per chat and this runs off the request path, so
// one read per player is fine.
func (d *Daily) chatResults(ctx context.Context, cd ChatDay) ([]chatResult, error) {
	out := make([]chatResult, 0, len(cd.Players))
	for _, pl := range cd.Players {
		p, _, err := d.Plays.Get(ctx, PlayKey(cd.Num, pl.UserID))
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(p.Guesses) == 0 {
			continue
		}
		name := p.Name
		if name == "" {
			name = pl.Name
		}
		out = append(out, chatResult{name: name, p: p})
	}
	return out, nil
}

// sortResults orders winners by fewer guesses then earlier finish, then
// losses by finish, then players still playing with the most guesses first.
func sortResults(rs []chatResult) {
	rank := func(r chatResult) int {
		switch r.p.Status {
		case StatusWon:
			return 0
		case StatusLost:
			return 1
		}
		return 2
	}
	slices.SortStableFunc(rs, func(a, b chatResult) int {
		if c := cmp.Compare(rank(a), rank(b)); c != 0 {
			return c
		}
		switch a.p.Status {
		case StatusWon:
			return cmp.Or(cmp.Compare(len(a.p.Guesses), len(b.p.Guesses)), cmp.Compare(a.p.FinishedAt, b.p.FinishedAt))
		case StatusLost:
			return cmp.Compare(a.p.FinishedAt, b.p.FinishedAt)
		}
		return cmp.Compare(len(b.p.Guesses), len(a.p.Guesses))
	})
}

// ChatStreakAt is the chat topic's win streak record.
func (d *Daily) ChatStreakAt(ctx context.Context, chatID int64, threadID int) (ChatStreak, error) {
	st, _, err := d.Streaks.Get(ctx, ChatStreakKey(chatID, threadID))
	if errors.Is(err, storage.ErrNotFound) {
		return ChatStreak{}, nil
	}
	return st, err
}

// RenderLive is the live results message: every player's colour grid,
// never a letter. It returns "" when nobody in the chat has guessed yet.
func (d *Daily) RenderLive(ctx context.Context, cd ChatDay) (string, error) {
	results, err := d.chatResults(ctx, cd)
	if err != nil || len(results) == 0 {
		return "", err
	}
	sortResults(results)
	st, err := d.ChatStreakAt(ctx, cd.ChatID, cd.ThreadID)
	if err != nil {
		return "", err
	}
	var head strings.Builder
	head.WriteString(d.cfg.Rules.Label() + " #" + strconv.Itoa(cd.Num) + " · live results\n")
	if n := st.liveAt(cd.Num); n >= minStreakShown {
		head.WriteString("🔥 Group streak: " + strconv.Itoa(n) + " days\n")
	}
	blocks := make([]string, len(results))
	for i, r := range results {
		var b strings.Builder
		b.WriteString(r.name + " ")
		if r.p.Finished() {
			b.WriteString(d.ResultLine(r.p))
		} else {
			b.WriteString("playing " + strconv.Itoa(len(r.p.Guesses)) + "/" + strconv.Itoa(d.MaxGuesses()))
		}
		for _, g := range r.p.Guesses {
			b.WriteString("\n" + EmojiRow(d.cfg.Rules, g.Marks))
		}
		blocks[i] = b.String()
	}
	return JoinLimited(head.String()+"\n", blocks, "\n"), nil
}

// JoinLimited appends parts to head, separated by sep, while the text stays
// under maxTextUnits UTF-16 units, then says how many were left out.
func JoinLimited(head string, parts []string, sep string) string {
	text := head
	for i, p := range parts {
		next := text
		if i > 0 {
			next += sep
		}
		next += p
		if UTF16Len(next) > maxTextUnits {
			return text + sep + "+" + strconv.Itoa(len(parts)-i) + " more"
		}
		text = next
	}
	return text
}

// UTF16Len counts s in UTF-16 units, the unit of Telegram's length limits.
func UTF16Len(s string) int { return len(utf16.Encode([]rune(s))) }
