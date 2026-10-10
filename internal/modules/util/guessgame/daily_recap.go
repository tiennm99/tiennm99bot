package guessgame

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/subscription"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	// DailyPushSchedule is UTC; 00:00 UTC is 07:00 ICT, when a puzzle
	// starts.
	DailyPushSchedule = "0 0 * * *"
	// lastPushDateKey records the number of the last puzzle pushed, so a
	// second trigger on the same day sends nothing.
	lastPushDateKey = "daily_push:last_date"
	// maxLineUnits caps one recap line, so even every result line fits in
	// one message.
	maxLineUnits = 500
)

// PushHandler is the daily push cron entry point.
func (d *Daily) PushHandler(ctx context.Context, deps modules.Deps) error {
	if deps.Bot == nil {
		return errors.New(d.cfg.LogName + " daily push: deps.Bot is nil (BuildOptions.Bot not wired)")
	}
	return d.RunDailyPush(ctx, deps.Bot)
}

// RunDailyPush sends each subscriber yesterday's group results (when there
// is something to say) and then today's card, once per puzzle.
func (d *Daily) RunDailyPush(ctx context.Context, api TelegramAPI) error {
	name := d.cfg.LogName
	num := d.PuzzleNum(d.cfg.Now())
	// Pin today's answer now, before anyone plays.
	if _, err := d.ResolvePuzzle(ctx, num); err != nil {
		return fmt.Errorf("%s daily push: resolve puzzle: %w", name, err)
	}
	d.PruneDays(ctx, num)
	subs, err := subscription.List(ctx, d.Subscribers)
	if err != nil {
		return fmt.Errorf("%s daily push: list subscribers: %w", name, err)
	}
	if len(subs) == 0 {
		log.Info(name + " daily push: no subscribers, skipping")
		return nil
	}
	won, err := subscription.ClaimDay(ctx, d.PushDate, lastPushDateKey, strconv.Itoa(num))
	if err != nil {
		return fmt.Errorf("%s daily push: claim day: %w", name, err)
	}
	if !won {
		log.Info(name+" daily push: already pushed, skipping", "puzzle", num)
		return nil
	}
	res, err := subscription.FanoutFunc(ctx, name+" daily", d.Subscribers, &d.SubscribersMu, subs, func(ctx context.Context, sub subscription.Subscriber) error {
		return d.pushTo(ctx, api, num, sub)
	})
	if err != nil {
		return err
	}
	log.Info(name+" daily push complete", "puzzle", num, "subscribers", len(subs),
		"sent", res.Sent, "failed", res.Failed, "pruned", res.Pruned, "throttled", res.Throttled)
	return nil
}

// pushTo sends one subscriber its recap and card. A recap that fails to send
// skips the card, so FanoutFunc classifies that error and prunes a dead chat.
func (d *Daily) pushTo(ctx context.Context, api TelegramAPI, num int, sub subscription.Subscriber) error {
	if num > 1 {
		text, err := d.RenderRecap(ctx, num-1, sub.ChatID, sub.ThreadID)
		if err != nil {
			log.Warn(d.cfg.LogName+" recap render failed", "err", err)
		}
		if text != "" {
			if _, err := api.SendMessage(ctx, &bot.SendMessageParams{ChatID: sub.ChatID, MessageThreadID: sub.ThreadID, Text: text}); err != nil {
				return err
			}
		}
	}
	_, err := api.SendGame(ctx, &bot.SendGameParams{ChatID: sub.ChatID, MessageThreadID: sub.ThreadID, GameShorName: d.cfg.GameShortName})
	return err
}

// RenderRecap is puzzle num's results in a group chat topic: the answer
// first, then the group streak, which only a win keeps going, then the
// finishers grouped by guess count with the best line crowned. It is "" in
// a private chat.
func (d *Daily) RenderRecap(ctx context.Context, num int, chatID int64, threadID int) (string, error) {
	if chatID >= 0 {
		return "", nil
	}
	answer, err := d.ResolvePuzzle(ctx, num)
	if err != nil {
		return "", err
	}
	cd, _, err := d.ChatDays.Get(ctx, ChatDayKey(num, chatID, threadID))
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return "", err
	}
	results, err := d.chatResults(ctx, cd)
	if err != nil {
		return "", err
	}
	finished := results[:0]
	for _, r := range results {
		if r.p.Finished() {
			finished = append(finished, r)
		}
	}
	st, err := d.ChatStreakAt(ctx, chatID, threadID)
	if err != nil {
		return "", err
	}
	label := d.cfg.Rules.Label() + " #" + strconv.Itoa(num)
	head := label + " — yesterday's results\n" + d.cfg.AnswerPrefix + d.cfg.Rules.AnswerText(answer) + "\n"
	// The recap runs after puzzle num closed, so the streak survives only if
	// someone in the group solved num itself.
	switch {
	case st.LastNum >= num && st.Streak >= minStreakShown:
		head += "🔥 Your group is on a " + strconv.Itoa(st.Streak) + " day streak!\n"
	case st.LastNum == num-1 && st.Streak >= minStreakShown:
		head += "Nobody solved it. Group streak reset.\n"
	}
	if len(finished) == 0 {
		return strings.TrimSuffix(head, "\n"), nil
	}
	sortResults(finished)
	var lines []string
	for i := 0; i < len(finished); {
		j := i
		line := d.ResultLine(finished[i].p)
		for j < len(finished) && d.ResultLine(finished[j].p) == line {
			j++
		}
		prefix := line + ": "
		if i == 0 && finished[i].p.Status == StatusWon {
			prefix = "👑 " + prefix
		}
		lines = append(lines, prefix+nameList(finished[i:j]))
		i = j
	}
	return JoinLimited(head, lines, "\n"), nil
}

// nameList joins the players' names, ending with "+N more" past
// maxLineUnits.
func nameList(rs []chatResult) string {
	var b strings.Builder
	for i, r := range rs {
		if i > 0 && UTF16Len(b.String())+UTF16Len(r.name) > maxLineUnits {
			return b.String() + ", +" + strconv.Itoa(len(rs)-i) + " more"
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(r.name)
	}
	return b.String()
}
