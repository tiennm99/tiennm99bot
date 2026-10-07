package lol

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/subscription"
)

// dailyPushCronName is the cron's registry and in-process scheduler key; it
// must be unique across all modules' crons.
const dailyPushCronName = "lol_daily_push"

// dailyPushSchedule drives the in-process scheduler (internal/cron). Cron
// expression is UTC; 01:00 UTC == 08:00 ICT.
const dailyPushSchedule = "0 1 * * *"

// lastPushDateKey records the ICT date (YYYY-MM-DD) of the most recently
// claimed daily push. The handler claims this key before fanning out and
// no-ops if it is already today's schedule day, making the push idempotent per
// ICT date. This defends against double-fire windows from rolling deploys that
// briefly run two containers or operator misconfiguration.
const lastPushDateKey = "daily_push:last_date"

// PushDateStore is the typed store for last-push date documents.
type PushDateStore = subscription.DayStore

// dailyPushCron returns the cron registration; the in-process scheduler fires
// the handler on Schedule.
func (s *state) dailyPushCron() modules.Cron {
	return modules.Cron{
		Name:     dailyPushCronName,
		Schedule: dailyPushSchedule,
		Handler:  s.dailyPushHandler,
	}
}

// dailyPushHandler is invoked by the cron dispatcher. It pulls Bot from Deps
// (set in main.go via BuildOptions.Bot) and delegates to runDailyPush so the
// core logic is testable without an actual *bot.Bot.
func (s *state) dailyPushHandler(ctx context.Context, deps modules.Deps) error {
	if deps.Bot == nil {
		return errors.New("lol daily push: deps.Bot is nil (BuildOptions.Bot not wired)")
	}
	return runDailyPush(ctx, s, deps.Bot)
}

// runDailyPush is the testable core: fetch subscribers, fetch today's matches,
// fan out to every subscriber. Per-chat send failures are logged but do not
// abort the batch — one bad chat does not deny the rest.
//
// MessageThreadID is forwarded on every send so subscribers in a forum-topic
// receive the digest in that topic, not in General.
func runDailyPush(ctx context.Context, s *state, sender subscription.Sender) error {
	subs, err := subscription.List(ctx, s.subscribers)
	if err != nil {
		return fmt.Errorf("lol daily push: list subscribers: %w", err)
	}
	if len(subs) == 0 {
		log.Info("lol daily push: no subscribers, skipping")
		return nil
	}

	from := ictDayStartOf(s.now())
	to := addDays(from, 1)
	events, err := s.client.GetEventsWithFallback(ctx, s.cache, from, to)
	if err != nil {
		return fmt.Errorf("lol daily push: fetch matches: %w", err)
	}
	filtered := FilterMajor(events)
	text := RenderToday(filtered, from)

	// Idempotency gate: claim today's push before sending. A lost claim means
	// another trigger already pushed (or is pushing) for this ICT schedule day,
	// so we send nothing. Placed after the fetch so a transient fetch failure
	// does not consume the day's claim.
	pushDay := ictDayKey(from)
	won, err := subscription.ClaimDay(ctx, s.pushDate, lastPushDateKey, pushDay)
	if err != nil {
		return fmt.Errorf("lol daily push: claim date: %w", err)
	}
	if !won {
		log.Info("lol daily push: already pushed today, skipping", "date", pushDay)
		return nil
	}

	res, err := subscription.Fanout(ctx, "lol daily", s.subscribers, &s.subscribersMu, subs, sender, bot.SendMessageParams{
		Text:                text,
		ParseMode:           models.ParseModeHTML,
		DisableNotification: len(filtered) == 0,
	})
	if err != nil {
		return err
	}
	log.Info("lol daily push complete",
		"subscribers", len(subs),
		"sent", res.Sent,
		"failed", res.Failed,
		"pruned", res.Pruned,
		"throttled", res.Throttled)
	return nil
}
