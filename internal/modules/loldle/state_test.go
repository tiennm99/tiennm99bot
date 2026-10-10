package loldle

import (
	"context"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

func newLoldleConfig() ConfigStore {
	return storage.Typed[roundConfig](storage.NewMemoryProvider().Collection("loldle"))
}

func TestGetMaxGuesses_DefaultsAndOverrides(t *testing.T) {
	ctx := context.Background()
	cfg := newLoldleConfig()

	if n, _ := getMaxGuesses(ctx, cfg, "u1"); n != MaxGuesses {
		t.Errorf("missing config → %d, want %d", n, MaxGuesses)
	}

	if err := setMaxGuesses(ctx, cfg, "u1", 5); err != nil {
		t.Fatal(err)
	}
	if n, _ := getMaxGuesses(ctx, cfg, "u1"); n != 5 {
		t.Errorf("after setMax(5): %d, want 5", n)
	}
}

func TestSetMaxGuesses_ValidatesRange(t *testing.T) {
	ctx := context.Background()
	cfg := newLoldleConfig()
	for _, n := range []int{0, -1, MaxGuessesCap + 1, 100} {
		if err := setMaxGuesses(ctx, cfg, "u1", n); err == nil {
			t.Errorf("setMaxGuesses(%d) should error", n)
		}
	}
	if err := setMaxGuesses(ctx, cfg, "u1", 1); err != nil {
		t.Errorf("setMaxGuesses(1) should succeed: %v", err)
	}
	if err := setMaxGuesses(ctx, cfg, "u1", MaxGuessesCap); err != nil {
		t.Errorf("setMaxGuesses(cap) should succeed: %v", err)
	}
}

func TestGetMaxGuesses_OutOfRangeIgnored(t *testing.T) {
	ctx := context.Background()
	cfg := newLoldleConfig()
	// Inject a corrupt config to simulate manual store tampering or a stale
	// schema; getMaxGuesses must fall back to the default rather than
	// returning a wild value to handlers.
	if err := cfg.Put(ctx, configKey("u1"), roundConfig{MaxGuesses: 100}); err != nil {
		t.Fatal(err)
	}
	if n, _ := getMaxGuesses(ctx, cfg, "u1"); n != MaxGuesses {
		t.Errorf("out-of-range config → %d, want %d (default)", n, MaxGuesses)
	}
}
