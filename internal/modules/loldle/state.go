package loldle

import (
	"context"
	"errors"
	"fmt"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// MaxGuesses is the default unlimited round length and the fixed daily one.
// /loldle_setmax overrides the unlimited length per chat, up to
// MaxGuessesCap.
const (
	MaxGuesses    = 8
	MaxGuessesCap = 10
)

// roundConfig stores a chat's unlimited round length. Stored only when
// /loldle_setmax has been run; the absence of this record means "use
// default". The subject is the chat id in a group and the user id in a
// private chat.
type roundConfig struct {
	MaxGuesses int `json:"maxGuesses" bson:"maxGuesses"`
}

// ConfigStore is the loldle module's typed config store.
type ConfigStore = storage.DocStore[roundConfig]

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
	return normalizeMaxGuesses(c.MaxGuesses), nil
}

// setMaxGuesses validates and persists the per-subject override.
func setMaxGuesses(ctx context.Context, cfg ConfigStore, subject string, n int) error {
	if !validMaxGuesses(n) {
		return fmt.Errorf("loldle: maxGuesses must be in [1, %d], got %d", MaxGuessesCap, n)
	}
	if err := cfg.Put(ctx, configKey(subject), roundConfig{MaxGuesses: n}); err != nil {
		return fmt.Errorf("loldle setMaxGuesses: %w", err)
	}
	return nil
}
