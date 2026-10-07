package lol

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/subscription"
)

// state captures everything a lol handler needs at runtime.
type state struct {
	subscribers subscription.Store
	pushDate    PushDateStore
	cache       CacheStore
	client      *Client
	// nowFn allows tests to inject a deterministic clock. Production code
	// uses time.Now via the default zero-value.
	nowFn func() time.Time
	// subscribersMu serializes Get→mutate→Put on the single subscribers store
	// slot. Two concurrent /lol_subscribe calls in the same
	// millisecond would otherwise race and drop one append.
	subscribersMu sync.Mutex
}

func (s *state) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now()
}

// handleSchedule is /lol [date] — matches for one ICT day.
// Empty arg → today.
func (s *state) handleSchedule(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	arg := chathelper.ArgAfterCommand(msg.Text)
	parsed := ParseScheduleDate(arg, s.now())
	if !parsed.OK {
		return chathelper.Reply(ctx, b, msg, parsed.Error)
	}
	useFallback := strings.TrimSpace(arg) == ""
	emptyLine := "No major LoL matches on this day."
	if useFallback {
		emptyLine = "No major LoL matches today."
	}
	return s.replyForRange(ctx, b, msg, parsed.Date, addDays(parsed.Date, 1), false, useFallback,
		emptyLine, "")
}

// handleTomorrow is /lol_tomorrow — matches for tomorrow's ICT day.
func (s *state) handleTomorrow(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	from := addDays(ictDayStartOf(s.now()), 1)
	return s.replyForRange(ctx, b, msg, from, addDays(from, 1), false, true,
		"No major LoL matches tomorrow.", "Could not fetch tomorrow's matches. Try again later.")
}

// handleWeek is /lol_this_week — the current ICT calendar week
// (Monday 00:00 ICT through the following Monday 00:00 ICT, exclusive).
func (s *state) handleWeek(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	from := ictWeekStartOf(s.now())
	return s.replyForRange(ctx, b, msg, from, addDays(from, 7), true, true,
		"No major LoL matches this week.", "")
}

// handleNextWeek is /lol_next_week — the next ICT calendar week
// (Monday 00:00 ICT through the following Monday 00:00 ICT, exclusive).
func (s *state) handleNextWeek(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	from := ictNextWeekStartOf(s.now())
	return s.replyForRange(ctx, b, msg, from, addDays(from, 7), true, true,
		"No major LoL matches next week.", "Could not fetch next week's matches. Try again later.")
}

// replyForRange fetches, filters, and renders a date window. week=true groups
// by league and day; false groups by league only.
func (s *state) replyForRange(ctx context.Context, b *bot.Bot, msg *models.Message, from, to time.Time, week, useFallback bool, emptyLine, fetchErrorHint string) error {
	var (
		events []ScheduleEvent
		err    error
	)
	if useFallback {
		events, err = s.client.GetEventsWithFallback(ctx, s.cache, from, to)
	} else {
		events, err = s.client.GetEventsLive(ctx, from, to)
	}
	if err != nil {
		log.Error("lol_fetch_fail", "err", err, "from", from, "to", to)
		if fetchErrorHint == "" {
			fetchErrorHint = "Could not fetch matches. Try again later."
			if week {
				fetchErrorHint = "Could not fetch this week's matches. Try again later."
			}
		}
		return chathelper.Reply(ctx, b, msg, fetchErrorHint)
	}
	filtered := FilterMajor(events)
	var text string
	if week {
		text = renderWeek(filtered, from, to, emptyLine)
	} else {
		text = renderDay(filtered, from, emptyLine)
	}
	return chathelper.ReplyHTML(ctx, b, msg, text)
}

// handleSubscribe is /lol_subscribe — opt the chat into the daily
// digest delivered by the in-process cron handler.
func (s *state) handleSubscribe(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	s.subscribersMu.Lock()
	defer s.subscribersMu.Unlock()
	added, err := subscription.Add(ctx, s.subscribers, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		return err
	}
	scope := subscription.Scope(msg)
	if added {
		return chathelper.Reply(ctx, b, msg,
			"✅ Subscribed "+scope+" to the daily LoL schedule at 08:00 ICT.\n"+
				"If you block the bot, you'll be auto-unsubscribed on the next push.")
	}
	return chathelper.Reply(ctx, b, msg, "Already subscribed in "+scope+".")
}

// handleUnsubscribe is /lol_unsubscribe — opt out.
func (s *state) handleUnsubscribe(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	s.subscribersMu.Lock()
	defer s.subscribersMu.Unlock()
	removed, err := subscription.Remove(ctx, s.subscribers, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		return err
	}
	scope := subscription.Scope(msg)
	if removed {
		return chathelper.Reply(ctx, b, msg, "Unsubscribed "+scope+".")
	}
	return chathelper.Reply(ctx, b, msg, subscription.ScopeSubject(msg)+" wasn't subscribed.")
}
