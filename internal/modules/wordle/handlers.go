package wordle

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/keylock"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

// state captures everything a wordle command needs at handler-time. Built
// once in New and shared across all four handlers via closure.
type state struct {
	games GameStore
	stats StatsStore
	words []string
	set   map[string]struct{}
	locks keylock.Map // per-subject mutex; serialises Get→mutate→Put
}

// rejectMessage maps a validation failure into the user-facing reply.
func rejectMessage(reason rejectReason) string {
	switch reason {
	case reasonEmpty:
		return fmt.Sprintf("Please provide a %d-letter word.", WordLength)
	case reasonLength:
		return fmt.Sprintf("Word must be exactly %d letters.", WordLength)
	default:
		return "Not in the word list."
	}
}

// startFresh writes a brand-new round and returns it. Errors propagate to
// the caller (not swallowed — a store failure means subsequent ops will lie).
func (s *state) startFresh(ctx context.Context, subject string) (*GameState, error) {
	target, err := pickRandom(s.words, nil)
	if err != nil {
		return nil, fmt.Errorf("wordle startFresh: %w", err)
	}
	g := &GameState{
		Target:    target,
		Guesses:   []GuessRecord{},
		Solved:    false,
		Giveup:    false,
		StartedAt: chathelper.NowMillis(),
	}
	if err := saveGame(ctx, s.games, subject, g); err != nil {
		return nil, err
	}
	return g, nil
}

func (s *state) getOrInit(ctx context.Context, subject string) (*GameState, error) {
	g, err := loadGame(ctx, s.games, subject)
	if err != nil {
		return nil, err
	}
	if g != nil {
		return g, nil
	}
	return s.startFresh(ctx, subject)
}

// handleWordle is /wordle [word] — show board if no arg, else submit a guess.
func (s *state) handleWordle(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	subject := chathelper.SubjectFor(msg)
	if subject == "" {
		return chathelper.Reply(ctx, b, msg, "Cannot identify chat.")
	}
	defer s.locks.Acquire(subject)()
	arg := chathelper.ArgAfterCommand(msg.Text)

	g, err := s.getOrInit(ctx, subject)
	if err != nil {
		return err
	}

	if arg == "" {
		var header string
		switch {
		case g.Solved:
			header = fmt.Sprintf("🎉 Solved in %d/%d. /wordle_new for another.", len(g.Guesses), MaxGuesses)
		case g.Giveup:
			header = fmt.Sprintf("🏳️ Gave up. Answer was %s. /wordle_new for another.", strings.ToUpper(g.Target))
		default:
			header = fmt.Sprintf("Guess %d/%d. Use `/wordle <word>`.", len(g.Guesses), MaxGuesses)
		}
		return chathelper.Reply(ctx, b, msg, header+"\n\n"+renderBoard(g.Guesses))
	}

	if isFinished(g) {
		return chathelper.Reply(ctx, b, msg,
			fmt.Sprintf("Current round is over. Use /wordle_new to start another. Answer was %s.", strings.ToUpper(g.Target)))
	}

	v := validateGuess(s.set, arg)
	if !v.OK {
		return chathelper.Reply(ctx, b, msg, rejectMessage(v.Reason))
	}

	results := CompareWords(v.Word, g.Target)
	g.Guesses = append(g.Guesses, GuessRecord{Word: v.Word, Results: results})
	won := v.Word == g.Target
	if won {
		g.Solved = true
	}
	if err := saveGame(ctx, s.games, subject, g); err != nil {
		return err
	}

	rendered := renderGuess(v.Word, results)
	switch {
	case won:
		st, err := recordResult(ctx, s.stats, subject, true, chathelper.NowMillis())
		if err != nil {
			return err
		}
		return chathelper.Reply(ctx, b, msg, fmt.Sprintf("%s\n\n🎉 Solved in %d/%d! Streak: %d. /wordle_new for another.",
			rendered, len(g.Guesses), MaxGuesses, st.Streak))
	case len(g.Guesses) >= MaxGuesses:
		if _, err := recordResult(ctx, s.stats, subject, false, chathelper.NowMillis()); err != nil {
			return err
		}
		return chathelper.Reply(ctx, b, msg, fmt.Sprintf("%s\n\n❌ Out of guesses. Answer was %s. /wordle_new to retry.",
			rendered, strings.ToUpper(g.Target)))
	default:
		return chathelper.Reply(ctx, b, msg, fmt.Sprintf("%s\n\nGuess %d/%d.", rendered, len(g.Guesses), MaxGuesses))
	}
}

// handleNew is /wordle_new — abandons any in-progress round (counts as
// giveup → stats hit) and starts fresh.
func (s *state) handleNew(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	subject := chathelper.SubjectFor(msg)
	if subject == "" {
		return chathelper.Reply(ctx, b, msg, "Cannot identify chat.")
	}
	defer s.locks.Acquire(subject)()

	prelude := ""
	prior, err := loadGame(ctx, s.games, subject)
	if err != nil {
		return err
	}
	if prior != nil && !isFinished(prior) {
		if _, err := recordResult(ctx, s.stats, subject, false, chathelper.NowMillis()); err != nil {
			return err
		}
		prelude = fmt.Sprintf("🏳️ Previous round abandoned (auto-giveup). Answer was %s.\n\n",
			strings.ToUpper(prior.Target))
	}

	if _, err := s.startFresh(ctx, subject); err != nil {
		return err
	}
	return chathelper.Reply(ctx, b, msg, prelude+"🆕 New round started. Use `/wordle <word>` to guess.")
}

// handleGiveup is /wordle_giveup — reveals answer for the current round.
// Calls on a finished round (solved, given up, or out of guesses) echo the
// answer without touching stats.
func (s *state) handleGiveup(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	subject := chathelper.SubjectFor(msg)
	if subject == "" {
		return chathelper.Reply(ctx, b, msg, "Cannot identify chat.")
	}
	defer s.locks.Acquire(subject)()
	g, err := loadGame(ctx, s.games, subject)
	if err != nil {
		return err
	}
	if g == nil {
		return chathelper.Reply(ctx, b, msg, "No active round. /wordle_new to start one.")
	}
	if g.Solved {
		return chathelper.Reply(ctx, b, msg, fmt.Sprintf("Already solved — %s.", strings.ToUpper(g.Target)))
	}
	if g.Giveup {
		return chathelper.Reply(ctx, b, msg, fmt.Sprintf("Already gave up — %s.", strings.ToUpper(g.Target)))
	}
	// A round lost by running out of guesses already recorded its loss in
	// handleWordle; giving up on it must not count a second one.
	if isFinished(g) {
		return chathelper.Reply(ctx, b, msg,
			fmt.Sprintf("Current round is over. Use /wordle_new to start another. Answer was %s.", strings.ToUpper(g.Target)))
	}
	g.Giveup = true
	if err := saveGame(ctx, s.games, subject, g); err != nil {
		return err
	}
	if _, err := recordResult(ctx, s.stats, subject, false, chathelper.NowMillis()); err != nil {
		return err
	}
	return chathelper.Reply(ctx, b, msg, fmt.Sprintf("🏳️ Answer was %s. /wordle_new for another.", strings.ToUpper(g.Target)))
}

// handleStats is /wordle_stats — shows lifetime score for the subject.
func (s *state) handleStats(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	subject := chathelper.SubjectFor(msg)
	if subject == "" {
		return chathelper.Reply(ctx, b, msg, "Cannot identify chat.")
	}
	st, err := loadStats(ctx, s.stats, subject)
	if err != nil {
		return err
	}
	scope := "group"
	if msg.Chat.Type == models.ChatTypePrivate {
		scope = "your"
	}
	return chathelper.Reply(ctx, b, msg, fmt.Sprintf(
		"📊 Wordle %s stats\nPlayed: %d\nWins: %d (%d%%)\nCurrent streak: %d\nBest streak: %d",
		scope, st.Played, st.Wins, chathelper.WinRate(st.Wins, st.Played), st.Streak, st.BestStreak,
	))
}
