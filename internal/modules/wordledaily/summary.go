package wordledaily

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
	// summaryInterval is the shortest gap between two updates of one
	// chat's live results message.
	summaryInterval = 5 * time.Second
	// resendInterval is the shortest gap between two new results messages
	// for one chat's day, when the old one was deleted.
	resendInterval = 10 * time.Minute
	// maxTextUnits keeps a message under Telegram's 4096 UTF-16 unit cap,
	// leaving room for the "+N more" line.
	maxTextUnits = 4000
	// minStreakShown hides a one-day "streak".
	minStreakShown = 2
	// maxSummaryRetries bounds the retries of one failed summary update.
	maxSummaryRetries = 5
)

// flusher coalesces live summary updates: at most one is pending per chat
// day, and two never run closer than summaryInterval. It is in-memory; a
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

// touchSummary schedules an update of the live results message of the chat
// topic's day. Only group chats (negative IDs) get one.
func (s *service) touchSummary(num int, chatID int64, threadID int) {
	if s.cfg.api == nil || chatID >= 0 {
		return
	}
	key := chatDayKey(num, chatID, threadID)
	now := s.cfg.now()
	f := &s.flush
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
	delay := max(summaryInterval-now.Sub(st.last), 0)
	if st.last.IsZero() {
		delay = 0
	}
	f.mu.Unlock()
	s.cfg.schedule(delay, func() { s.flushSummary(num, chatID, threadID) })
}

// flushSummary brings the chat topic's live results message up to date and
// schedules a bounded retry when that failed for a reason that may pass (a
// 429, a timeout, a 5xx, a store error) or when a deleted message may not be
// sent again yet.
func (s *service) flushSummary(num int, chatID int64, threadID int) {
	key := chatDayKey(num, chatID, threadID)
	f := &s.flush
	f.mu.Lock()
	if st := f.state[key]; st != nil {
		st.scheduled, st.last = false, s.cfg.now()
	}
	f.mu.Unlock()

	retry, after := s.syncSummary(key, chatID, threadID)

	f.mu.Lock()
	if f.state == nil {
		f.state = map[string]*flushState{}
	}
	st := f.state[key]
	if st == nil {
		st = &flushState{num: num, last: s.cfg.now()}
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
	if st.retries >= maxSummaryRetries {
		st.retries = 0
		f.mu.Unlock()
		log.Warn("wordledaily summary update abandoned", "puzzle", num, "chat", chatID)
		return
	}
	delay := max(after, summaryInterval<<st.retries)
	st.retries++
	st.scheduled = true
	f.mu.Unlock()
	s.cfg.schedule(delay, func() { s.flushSummary(num, chatID, threadID) })
}

// syncSummary sends the chat day's results message the first time and
// edits it afterwards. A deleted message is sent again, at most once per
// resendInterval. It reports whether the update must be retried, and the
// shortest wait before that.
//
// The lock is per chat topic, not per day: only the current day's message
// is updated, and keylock never frees a key, so a daily key would grow the
// lock map forever.
func (s *service) syncSummary(key string, chatID int64, threadID int) (bool, time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()
	defer s.locks.Acquire("summary:" + chatTopic(chatID, threadID))()
	cd, _, err := s.chatDays.Get(ctx, key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return false, 0
		}
		log.Warn("wordledaily summary read failed", "err", err)
		return true, 0
	}
	text, err := s.renderLive(ctx, cd)
	if err != nil {
		log.Warn("wordledaily summary render failed", "err", err)
		return true, 0
	}
	if text == "" {
		return false, 0
	}
	now := s.cfg.now()
	if cd.MsgID != 0 {
		_, err := s.cfg.api.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: cd.ChatID, MessageID: cd.MsgID, Text: text})
		switch {
		case err == nil || messageNotModified(err):
			return false, 0
		case !messageGone(err):
			log.Warn("wordledaily summary edit failed", "err", err)
			return retryAfter(err)
		}
		if wait := resendInterval - now.Sub(time.UnixMilli(cd.MsgSentAt)); wait > 0 {
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
	msg, err := s.cfg.api.SendMessage(ctx, params)
	if err != nil {
		log.Warn("wordledaily summary send failed", "err", err)
		return retryAfter(err)
	}
	err = updateVersioned(ctx, s.chatDays, key, func(v *chatDay, found bool) bool {
		v.MsgID, v.MsgSentAt = msg.ID, now.UnixMilli()
		return found
	})
	if err != nil {
		log.Warn("wordledaily summary id save failed", "err", err)
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
	p    progress
}

// chatResults loads the day's game of every player listed in cd who made a
// guess. Players are few per chat and this runs off the request path, so
// one read per player is fine.
func (s *service) chatResults(ctx context.Context, cd chatDay) ([]chatResult, error) {
	out := make([]chatResult, 0, len(cd.Players))
	for _, pl := range cd.Players {
		p, _, err := s.plays.Get(ctx, playKey(cd.Num, pl.UserID))
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
		case statusWon:
			return 0
		case statusLost:
			return 1
		}
		return 2
	}
	slices.SortStableFunc(rs, func(a, b chatResult) int {
		if c := cmp.Compare(rank(a), rank(b)); c != 0 {
			return c
		}
		switch a.p.Status {
		case statusWon:
			return cmp.Or(cmp.Compare(len(a.p.Guesses), len(b.p.Guesses)), cmp.Compare(a.p.FinishedAt, b.p.FinishedAt))
		case statusLost:
			return cmp.Compare(a.p.FinishedAt, b.p.FinishedAt)
		}
		return cmp.Compare(len(b.p.Guesses), len(a.p.Guesses))
	})
}

// resultLine is "3/6" for a win, "X/6" for a loss.
func resultLine(p progress) string {
	if p.Status == statusWon {
		return strconv.Itoa(len(p.Guesses)) + "/" + strconv.Itoa(maxGuesses)
	}
	return "X/" + strconv.Itoa(maxGuesses)
}

func (s *service) chatStreakAt(ctx context.Context, chatID int64, threadID int) (chatStreak, error) {
	st, _, err := s.streaks.Get(ctx, chatStreakKey(chatID, threadID))
	if errors.Is(err, storage.ErrNotFound) {
		return chatStreak{}, nil
	}
	return st, err
}

// renderLive is the live results message: every player's colour grid,
// never a letter. It returns "" when nobody in the chat has guessed yet.
func (s *service) renderLive(ctx context.Context, cd chatDay) (string, error) {
	results, err := s.chatResults(ctx, cd)
	if err != nil || len(results) == 0 {
		return "", err
	}
	sortResults(results)
	st, err := s.chatStreakAt(ctx, cd.ChatID, cd.ThreadID)
	if err != nil {
		return "", err
	}
	var head strings.Builder
	head.WriteString("Wordle Daily #" + strconv.Itoa(cd.Num) + " · live results\n")
	if n := st.liveAt(cd.Num); n >= minStreakShown {
		head.WriteString("🔥 Group streak: " + strconv.Itoa(n) + " days\n")
	}
	blocks := make([]string, len(results))
	for i, r := range results {
		var b strings.Builder
		b.WriteString(r.name + " ")
		if r.p.finished() {
			b.WriteString(resultLine(r.p))
		} else {
			b.WriteString("playing " + strconv.Itoa(len(r.p.Guesses)) + "/" + strconv.Itoa(maxGuesses))
		}
		for _, g := range r.p.Guesses {
			b.WriteString("\n" + emojiRow(g.Marks))
		}
		blocks[i] = b.String()
	}
	return joinLimited(head.String()+"\n", blocks, "\n"), nil
}

// joinLimited appends parts to head, separated by sep, while the text stays
// under maxTextUnits UTF-16 units, then says how many were left out.
func joinLimited(head string, parts []string, sep string) string {
	text := head
	for i, p := range parts {
		next := text
		if i > 0 {
			next += sep
		}
		next += p
		if utf16Len(next) > maxTextUnits {
			return text + sep + "+" + strconv.Itoa(len(parts)-i) + " more"
		}
		text = next
	}
	return text
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }
