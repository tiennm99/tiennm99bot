package wordle

import (
	"context"
	"errors"
	"fmt"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// MaxGuesses is the standard wordle round length.
const MaxGuesses = 6

// Note: the store has no native per-document TTL — saved games linger
// until manually cleaned. Out of scope today; could be added via a sweep
// cron if storage cost ever matters.

// GuessRecord is one entry in a game's history:
//
//	{ "word": "crane", "results": [{"letter":"c","result":"correct"}, ...] }
type GuessRecord struct {
	Word    string        `json:"word" bson:"word"`
	Results []LetterScore `json:"results" bson:"results"`
}

// GameState is the per-subject record for an in-progress (or finished) round.
//
// `giveup` is always emitted (initialized to false on every fresh round). Do NOT
// add omitempty — the field is part of the stored document's shape, so
// emitting it unconditionally keeps already-saved games self-describing
// when inspected via raw dumps.
type GameState struct {
	Target    string        `json:"target" bson:"target"`
	Guesses   []GuessRecord `json:"guesses" bson:"guesses"`
	Solved    bool          `json:"solved" bson:"solved"`
	Giveup    bool          `json:"giveup" bson:"giveup"`
	StartedAt int64         `json:"startedAt" bson:"startedAt"` // ms-since-epoch (chathelper.NowMillis)
}

// Stats is the lifetime score record. lastResultAt is *int64 so an unplayed
// account marshals as `"lastResultAt": null` — distinguishes "never played"
// from "played at epoch zero".
type Stats struct {
	Played       int    `json:"played" bson:"played"`
	Wins         int    `json:"wins" bson:"wins"`
	Streak       int    `json:"streak" bson:"streak"`
	BestStreak   int    `json:"bestStreak" bson:"bestStreak"`
	LastResultAt *int64 `json:"lastResultAt" bson:"lastResultAt"` // ms-since-epoch | null
}

// GameStore is the wordle module's typed game store.
type GameStore = storage.DocStore[GameState]

// StatsStore is the wordle module's typed stats store.
type StatsStore = storage.DocStore[Stats]

func gameKey(subject string) string  { return "game:" + subject }
func statsKey(subject string) string { return "stats:" + subject }

// loadGame returns the active round, or (nil, nil) if none exists.
func loadGame(ctx context.Context, games GameStore, subject string) (*GameState, error) {
	g, _, err := games.Get(ctx, gameKey(subject))
	switch {
	case err == nil:
		return &g, nil
	case errors.Is(err, storage.ErrNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("wordle loadGame: %w", err)
	}
}

// saveGame writes the round.
func saveGame(ctx context.Context, games GameStore, subject string, g *GameState) error {
	if err := games.Put(ctx, gameKey(subject), *g); err != nil {
		return fmt.Errorf("wordle saveGame: %w", err)
	}
	return nil
}

// loadStats returns lifetime stats; missing → fresh-zero record (with
// LastResultAt=nil) so callers never need a nil check.
func loadStats(ctx context.Context, stats StatsStore, subject string) (*Stats, error) {
	s, _, err := stats.Get(ctx, statsKey(subject))
	switch {
	case err == nil:
		return &s, nil
	case errors.Is(err, storage.ErrNotFound):
		return &Stats{}, nil
	default:
		return nil, fmt.Errorf("wordle loadStats: %w", err)
	}
}

// recordResult bumps stats with the round outcome (won true → win + streak,
// false → reset streak). Returns the updated stats so callers can show the
// new streak in the win message.
func recordResult(ctx context.Context, stats StatsStore, subject string, won bool, nowMillis int64) (*Stats, error) {
	s, err := loadStats(ctx, stats, subject)
	if err != nil {
		return nil, err
	}
	s.Played++
	if won {
		s.Wins++
		s.Streak++
		if s.Streak > s.BestStreak {
			s.BestStreak = s.Streak
		}
	} else {
		s.Streak = 0
	}
	now := nowMillis
	s.LastResultAt = &now
	if err := stats.Put(ctx, statsKey(subject), *s); err != nil {
		return nil, fmt.Errorf("wordle recordResult: %w", err)
	}
	return s, nil
}

// isFinished is true when the round can no longer accept guesses: solved,
// gave up, or out of guesses.
func isFinished(g *GameState) bool {
	return g.Solved || g.Giveup || len(g.Guesses) >= MaxGuesses
}
