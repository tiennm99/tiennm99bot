package wordledaily

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
	// dailyPushCronName must be unique across all modules' crons.
	dailyPushCronName = "wordledaily_daily_push"
	// dailyPushSchedule is UTC; 00:00 UTC is 07:00 ICT, when a puzzle starts.
	dailyPushSchedule = "0 0 * * *"
	// lastPushDateKey records the number of the last puzzle pushed, so a
	// second trigger on the same day sends nothing.
	lastPushDateKey = "daily_push:last_date"
)

// dailyPushHandler is the cron entry point.
func (s *service) dailyPushHandler(ctx context.Context, deps modules.Deps) error {
	if deps.Bot == nil {
		return errors.New("wordledaily daily push: deps.Bot is nil (BuildOptions.Bot not wired)")
	}
	return s.runDailyPush(ctx, deps.Bot)
}

// runDailyPush sends each subscriber yesterday's group results (when there
// is something to say) and then today's card, once per puzzle.
func (s *service) runDailyPush(ctx context.Context, api telegramAPI) error {
	num := s.puzzleNum(s.cfg.now())
	// Pin today's answer now, before anyone plays.
	if _, err := s.resolvePuzzle(ctx, num); err != nil {
		return fmt.Errorf("wordledaily daily push: resolve puzzle: %w", err)
	}
	s.pruneDays(ctx, num)
	subs, err := subscription.List(ctx, s.subscribers)
	if err != nil {
		return fmt.Errorf("wordledaily daily push: list subscribers: %w", err)
	}
	if len(subs) == 0 {
		log.Info("wordledaily daily push: no subscribers, skipping")
		return nil
	}
	won, err := subscription.ClaimDay(ctx, s.pushDate, lastPushDateKey, strconv.Itoa(num))
	if err != nil {
		return fmt.Errorf("wordledaily daily push: claim day: %w", err)
	}
	if !won {
		log.Info("wordledaily daily push: already pushed, skipping", "puzzle", num)
		return nil
	}
	res, err := subscription.FanoutFunc(ctx, "wordledaily daily", s.subscribers, &s.subscribersMu, subs, func(ctx context.Context, sub subscription.Subscriber) error {
		return s.pushTo(ctx, api, num, sub)
	})
	if err != nil {
		return err
	}
	log.Info("wordledaily daily push complete", "puzzle", num, "subscribers", len(subs),
		"sent", res.Sent, "failed", res.Failed, "pruned", res.Pruned, "throttled", res.Throttled)
	return nil
}

// pushTo sends one subscriber its recap and card. A recap that fails to send
// skips the card, so FanoutFunc classifies that error and prunes a dead chat.
func (s *service) pushTo(ctx context.Context, api telegramAPI, num int, sub subscription.Subscriber) error {
	if num > 1 {
		text, err := s.renderRecap(ctx, num-1, sub.ChatID, sub.ThreadID)
		if err != nil {
			log.Warn("wordledaily recap render failed", "err", err)
		}
		if text != "" {
			if _, err := api.SendMessage(ctx, &bot.SendMessageParams{ChatID: sub.ChatID, MessageThreadID: sub.ThreadID, Text: text}); err != nil {
				return err
			}
		}
	}
	_, err := api.SendGame(ctx, &bot.SendGameParams{ChatID: sub.ChatID, MessageThreadID: sub.ThreadID, GameShorName: ShortName})
	return err
}

// renderRecap is puzzle num's results in a group chat topic: the answer
// first, then the finishers grouped by guess count with the best line
// crowned, and the group streak, which only a win keeps going. It is "" in a
// private chat.
func (s *service) renderRecap(ctx context.Context, num int, chatID int64, threadID int) (string, error) {
	if chatID >= 0 {
		return "", nil
	}
	answer, err := s.resolvePuzzle(ctx, num)
	if err != nil {
		return "", err
	}
	cd, _, err := s.chatDays.Get(ctx, chatDayKey(num, chatID, threadID))
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return "", err
	}
	results, err := s.chatResults(ctx, cd)
	if err != nil {
		return "", err
	}
	finished := results[:0]
	for _, r := range results {
		if r.p.finished() {
			finished = append(finished, r)
		}
	}
	st, err := s.chatStreakAt(ctx, chatID, threadID)
	if err != nil {
		return "", err
	}
	label := "Wordle Daily #" + strconv.Itoa(num)
	head := label + " — yesterday's results\nAnswer: " + strings.ToUpper(answer) + "\n"
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
		line := resultLine(finished[i].p)
		for j < len(finished) && resultLine(finished[j].p) == line {
			j++
		}
		prefix := line + ": "
		if i == 0 && finished[i].p.Status == statusWon {
			prefix = "👑 " + prefix
		}
		lines = append(lines, prefix+nameList(finished[i:j]))
		i = j
	}
	return joinLimited(head, lines, "\n"), nil
}

// maxLineUnits caps one recap line, so even all seven result lines fit in
// one message.
const maxLineUnits = 500

// nameList joins the players' names, ending with "+N more" past
// maxLineUnits.
func nameList(rs []chatResult) string {
	var b strings.Builder
	for i, r := range rs {
		if i > 0 && utf16Len(b.String())+utf16Len(r.name) > maxLineUnits {
			return b.String() + ", +" + strconv.Itoa(len(rs)-i) + " more"
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(r.name)
	}
	return b.String()
}
