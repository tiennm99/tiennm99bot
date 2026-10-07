package subscription

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// TerminalKind classifies a permanent send failure by blast radius.
type TerminalKind int

const (
	// TerminalNone is a transient failure (rate limit, timeout, 5xx). The
	// subscriber stays on the list and the next push retries it.
	TerminalNone TerminalKind = iota
	// TerminalChatWide means the chat itself is unreachable (bot blocked,
	// chat deactivated, kicked, deleted, group upgraded). Every subscriber
	// entry for that ChatID must be pruned — sister topics are dead too.
	TerminalChatWide
	// TerminalTopicOnly means the bot lost send rights in the specific topic
	// (e.g. a topic-level permissions change). Only the (ChatID, ThreadID)
	// entry that failed should be pruned; other topics in the same chat may
	// still be valid.
	TerminalTopicOnly
)

// chatWideTerminalMarkers are substrings of Telegram API errors that mean
// the whole chat is gone, not just one topic. Detecting these lets a push
// prune every subscription for that ChatID at once.
//
// String matching is fragile by nature, but the bot library surfaces these
// directly in err.Error() and Telegram has used the same wording for years.
// The false-negative path (we miss a new wording, dead chat lingers) is
// strictly safer than the false-positive path (we wrongly prune a live chat).
var chatWideTerminalMarkers = []string{
	"bot was blocked by the user",
	"user is deactivated",
	"bot is not a member",
	"chat not found",
	"group chat was upgraded",
	"chat was deleted",
}

// topicOnlyTerminalMarkers are errors that scope to a single forum topic
// (or to the bot's per-topic permissions). Pruning only the offending
// (ChatID, ThreadID) keeps the chat's other topic subscriptions alive.
var topicOnlyTerminalMarkers = []string{
	"have no rights to send",
}

// ClassifyTerminal reports whether err is a permanent send failure and, if
// so, whether it kills the whole chat or only the originating topic.
func ClassifyTerminal(err error) TerminalKind {
	if err == nil {
		return TerminalNone
	}
	msg := err.Error()
	for _, m := range chatWideTerminalMarkers {
		if strings.Contains(msg, m) {
			return TerminalChatWide
		}
	}
	for _, m := range topicOnlyTerminalMarkers {
		if strings.Contains(msg, m) {
			return TerminalTopicOnly
		}
	}
	return TerminalNone
}

// DayDoc wraps the last-claimed day string so it can be stored as a named
// root field in a Mongo document (a bare scalar cannot be a root doc).
type DayDoc struct {
	Date string `json:"date" bson:"date"`
}

// DayStore is the typed store for last-claimed day documents.
type DayStore = storage.DocStore[DayDoc]

// ClaimDay atomically records that the push for day is happening and reports
// whether THIS caller won the claim. A winner proceeds to fan out; a loser
// (another trigger already claimed day) returns false and sends nothing. This
// defends against double-fire windows from rolling deploys that briefly run
// two containers.
//
// The claim uses version-based optimistic write (PutVersioned) on key so two
// simultaneous triggers cannot both win.
func ClaimDay(ctx context.Context, store DayStore, key, day string) (bool, error) {
	current, version, err := store.Get(ctx, key)
	switch {
	case err == nil:
		if current.Date == day {
			return false, nil
		}
	case errors.Is(err, storage.ErrNotFound):
		version = 0
	default:
		return false, err
	}
	if err := store.PutVersioned(ctx, key, version, DayDoc{Date: day}); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// rateLimitThreshold is the subscriber count above which sends are throttled
// to stay under Telegram's global 30 msg/sec cap. Below it we send hot.
const rateLimitThreshold = 30

// rateLimitDelay is the inter-send pause when above the threshold. 50ms =
// ~20 msg/sec, well clear of the 30/s ceiling with margin for jitter.
const rateLimitDelay = 50 * time.Millisecond

// Sender is the subset of *bot.Bot a push uses. Defining it as an interface
// lets tests inject a mock without spinning up a fake Telegram API server.
type Sender interface {
	SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error)
}

// Result summarizes one fan-out.
type Result struct {
	Sent, Failed, Pruned int
	Throttled            bool
}

// Fanout sends params(sub) to every subscriber. Per-chat send failures are
// logged but do not abort the batch — one bad chat does not deny the rest.
// ChatID and MessageThreadID are filled in from each subscriber so forum
// subscribers receive the push in their topic, not in General.
//
// Unreachable subscribers are pruned afterwards, best-effort, while holding
// mu (the same mutex that guards Add and Remove). A failed prune just leaves
// the dead chats listed, and the next push fails on them and retries.
// The only returned error is ctx cancellation while throttling.
func Fanout(ctx context.Context, name string, store Store, mu *sync.Mutex, subs []Subscriber, sender Sender, params bot.SendMessageParams) (Result, error) {
	res := Result{Throttled: len(subs) > rateLimitThreshold}
	deadChats := map[int64]struct{}{}
	var deadTopics []Subscriber
	for i, sub := range subs {
		if res.Throttled && i > 0 {
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(rateLimitDelay):
			}
		}
		p := params
		p.ChatID = sub.ChatID
		p.MessageThreadID = sub.ThreadID
		if _, err := sender.SendMessage(ctx, &p); err != nil {
			log.Warn(name+" push send failed", "chat", sub.ChatID, "thread", sub.ThreadID, "err", err)
			res.Failed++
			switch ClassifyTerminal(err) {
			case TerminalChatWide:
				deadChats[sub.ChatID] = struct{}{}
			case TerminalTopicOnly:
				deadTopics = append(deadTopics, sub)
			}
			continue
		}
		res.Sent++
	}
	res.Pruned = prune(ctx, name, store, mu, deadChats, deadTopics)
	return res, nil
}

// prune removes entries flagged unreachable. Chat-wide failures drop every
// subscription for the chat; topic-only failures drop just the one
// (ChatID, ThreadID). Returns total entries removed.
func prune(ctx context.Context, name string, store Store, mu *sync.Mutex, chatWide map[int64]struct{}, topicOnly []Subscriber) int {
	if len(chatWide) == 0 && len(topicOnly) == 0 {
		return 0
	}
	mu.Lock()
	defer mu.Unlock()
	removed := 0
	for chatID := range chatWide {
		n, err := RemoveAllForChat(ctx, store, chatID)
		if err != nil {
			log.Warn(name+" prune dead chat failed", "chat", chatID, "err", err)
			continue
		}
		removed += n
	}
	for _, sub := range topicOnly {
		// The whole chat was already pruned above.
		if _, ok := chatWide[sub.ChatID]; ok {
			continue
		}
		ok, err := Remove(ctx, store, sub.ChatID, sub.ThreadID)
		if err != nil {
			log.Warn(name+" prune dead topic failed", "chat", sub.ChatID, "thread", sub.ThreadID, "err", err)
			continue
		}
		if ok {
			removed++
		}
	}
	return removed
}
