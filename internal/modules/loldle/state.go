package loldle

import (
	"context"
	"errors"
	"fmt"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// MaxGuesses is the default round length. Per-subject overrides land via
// /loldle_setmax (capped by MaxGuessesCap).
const (
	MaxGuesses    = 8
	MaxGuessesCap = 10
)

// gameState is the per-subject record.
//
// StartedAt is *int64 because the timer doesn't start until the first guess;
// using time.Time would marshal as "0001-01-01T00:00:00Z" instead of null
// and lose that distinction.
//
// Guesses is just championNames; comparison rows are recomputed at render
// time against current champions.json so a data refresh updates
// historical board displays without migrating saved rounds.
type gameState struct {
	Target     string   `json:"target" bson:"target"`
	Guesses    []string `json:"guesses" bson:"guesses"`
	StartedAt  *int64   `json:"startedAt" bson:"startedAt"`                       // ms-since-epoch | null
	MaxGuesses int      `json:"maxGuesses,omitempty" bson:"maxGuesses,omitempty"` // frozen round budget
}

// stats lifetime score. No LastResultAt field by design (differs from
// wordle's Stats — loldle only ever needed running streaks, not "last
// played at").
type stats struct {
	Played     int `json:"played" bson:"played"`
	Wins       int `json:"wins" bson:"wins"`
	Streak     int `json:"streak" bson:"streak"`
	BestStreak int `json:"bestStreak" bson:"bestStreak"`
}

// roundConfig stores the per-subject MaxGuesses override. Stored only when
// /loldle_setmax has been run; the absence of this record means "use default".
type roundConfig struct {
	MaxGuesses int `json:"maxGuesses" bson:"maxGuesses"`
}

// GameStore is the loldle module's typed game store.
type GameStore = storage.DocStore[gameState]

// StatsStore is the loldle module's typed stats store.
type StatsStore = storage.DocStore[stats]

// ConfigStore is the loldle module's typed config store.
type ConfigStore = storage.DocStore[roundConfig]

func gameKey(subject string) string   { return "game:" + subject }
func statsKey(subject string) string  { return "stats:" + subject }
func configKey(subject string) string { return "config:" + subject }

func validMaxGuesses(n int) bool {
	return n >= 1 && n <= MaxGuessesCap
}

func normalizeMaxGuesses(n int) int {
	if validMaxGuesses(n) {
		return n
	}
	return MaxGuesses
}

func (g *gameState) roundMaxGuesses() int {
	if g == nil {
		return MaxGuesses
	}
	return normalizeMaxGuesses(g.MaxGuesses)
}

// loadGame returns the active round, or (nil, nil) if none exists.
func loadGame(ctx context.Context, games GameStore, subject string) (*gameState, error) {
	g, _, err := games.Get(ctx, gameKey(subject))
	switch {
	case err == nil:
		return &g, nil
	case errors.Is(err, storage.ErrNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("loldle loadGame: %w", err)
	}
}

func saveGame(ctx context.Context, games GameStore, subject string, g *gameState) error {
	if err := games.Put(ctx, gameKey(subject), *g); err != nil {
		return fmt.Errorf("loldle saveGame: %w", err)
	}
	return nil
}

// clearGame removes the round so the next /loldle starts fresh. Used after
// win / loss / giveup; the new round's timer should start on the player's
// next interaction, not at the moment the previous round ended.
func clearGame(ctx context.Context, games GameStore, subject string) error {
	if err := games.Delete(ctx, gameKey(subject)); err != nil {
		return fmt.Errorf("loldle clearGame: %w", err)
	}
	return nil
}

// loadStats returns lifetime score; missing → fresh-zero record so callers
// never need a nil check.
func loadStats(ctx context.Context, st StatsStore, subject string) (*stats, error) {
	s, _, err := st.Get(ctx, statsKey(subject))
	switch {
	case err == nil:
		return &s, nil
	case errors.Is(err, storage.ErrNotFound):
		return &stats{}, nil
	default:
		return nil, fmt.Errorf("loldle loadStats: %w", err)
	}
}

func recordResult(ctx context.Context, st StatsStore, subject string, won bool) (*stats, error) {
	s, err := loadStats(ctx, st, subject)
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
	if err := st.Put(ctx, statsKey(subject), *s); err != nil {
		return nil, fmt.Errorf("loldle recordResult: %w", err)
	}
	return s, nil
}

// getMaxGuesses returns the effective round length: the per-subject override
// if set and in range, otherwise MaxGuesses. Out-of-range values are
// silently ignored — better to serve the default than 500 the user.
func getMaxGuesses(ctx context.Context, cfg ConfigStore, subject string) (int, error) {
	c, _, err := cfg.Get(ctx, configKey(subject))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return MaxGuesses, nil
		}
		return 0, fmt.Errorf("loldle getMaxGuesses: %w", err)
	}
	if c.MaxGuesses < 1 || c.MaxGuesses > MaxGuessesCap {
		return MaxGuesses, nil
	}
	return c.MaxGuesses, nil
}

// setMaxGuesses validates and persists the per-subject override.
func setMaxGuesses(ctx context.Context, cfg ConfigStore, subject string, n int) error {
	if n < 1 || n > MaxGuessesCap {
		return fmt.Errorf("loldle: maxGuesses must be in [1, %d], got %d", MaxGuessesCap, n)
	}
	if err := cfg.Put(ctx, configKey(subject), roundConfig{MaxGuesses: n}); err != nil {
		return fmt.Errorf("loldle setMaxGuesses: %w", err)
	}
	return nil
}
