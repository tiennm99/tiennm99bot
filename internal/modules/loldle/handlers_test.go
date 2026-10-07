package loldle

import (
	"context"
	"strings"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// installLoldle wires the loldle module + auth (owner gates /loldle_setmax,
// which is private). seedTarget pre-seeds a game so guess outcomes are
// deterministic without hooking math/rand.
func installLoldle(t *testing.T, ownerID int64, seedSubject, seedTarget string) (*testutil.RecordingBot, GameStore, ConfigStore) {
	t.Helper()
	rb := testutil.NewRecordingBot(t)
	col := storage.NewMemoryProvider().Collection("loldle")
	games := storage.Typed[gameState](col)
	cfg := storage.Typed[roundConfig](col)
	mod := New(modules.Deps{Store: col})
	reg := &modules.Registry{
		Modules:     []modules.Module{{Name: "loldle", Commands: mod.Commands}},
		AllCommands: map[string]modules.Command{},
	}
	for _, c := range mod.Commands {
		reg.AllCommands[c.Name] = c
	}
	modules.Install(rb.Bot, reg, modules.Auth{BotOwnerID: ownerID})

	if seedTarget != "" {
		g := &gameState{Target: seedTarget, Guesses: []string{}}
		if err := saveGame(context.Background(), games, seedSubject, g); err != nil {
			t.Fatalf("seed game: %v", err)
		}
	}
	return rb, games, cfg
}

func TestLoldle_NoArgShowsBoard(t *testing.T) {
	rb, _, _ := installLoldle(t, 0, "1", "Aatrox")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/loldle"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "Guess 0/") {
		t.Errorf("/loldle no-arg reply missing 'Guess 0/...': %q", got)
	}
}

func TestLoldle_Win(t *testing.T) {
	rb, _, _ := installLoldle(t, 0, "1", "Aatrox")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/loldle aatrox"))

	got := rb.LastSent().Text()
	if !strings.Contains(strings.ToLower(got), "aatrox") {
		t.Errorf("win reply missing 'Aatrox': %q", got)
	}
	if !strings.Contains(got, "🎉") {
		t.Errorf("win reply missing celebration emoji: %q", got)
	}
}

func TestLoldle_UnknownChampion(t *testing.T) {
	rb, _, _ := installLoldle(t, 0, "1", "Aatrox")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/loldle ZilbeanZ"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "Champion not found") {
		t.Errorf("unknown champion reject: %q", got)
	}
}

func TestLoldle_AmbiguousChampionPrefix(t *testing.T) {
	rb, _, _ := installLoldle(t, 0, "1", "Aatrox")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/loldle A"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "Ambiguous champion") || !strings.Contains(got, "full champion name") {
		t.Errorf("ambiguous champion reject: %q", got)
	}
}

func TestLoldle_DuplicateGuessRejected(t *testing.T) {
	rb, _, _ := installLoldle(t, 0, "1", "Aatrox")
	// First guess: Ahri (not the target — round continues).
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/loldle ahri"))
	rb.Reset()
	// Second guess: Ahri again — duplicate path.
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/loldle ahri"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "already guessed") {
		t.Errorf("duplicate-guess reply: %q", got)
	}
}

func TestLoldleGiveup_RevealsAnswer(t *testing.T) {
	rb, _, _ := installLoldle(t, 0, "1", "Aatrox")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/loldle_giveup"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "Aatrox") {
		t.Errorf("/loldle_giveup should reveal Aatrox: %q", got)
	}
}

func TestLoldleGiveup_NoActiveRound(t *testing.T) {
	rb, _, _ := installLoldle(t, 0, "", "")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/loldle_giveup"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "No active round") {
		t.Errorf("/loldle_giveup with no round: %q", got)
	}
}

func TestLoldleStats_Empty(t *testing.T) {
	rb, _, _ := installLoldle(t, 0, "", "")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/loldle_stats"))

	got := rb.LastSent().Text()
	for _, want := range []string{"Played: 0", "Wins: 0 (0%)", "Best streak: 0"} {
		if !strings.Contains(got, want) {
			t.Errorf("/loldle_stats empty missing %q; got %q", want, got)
		}
	}
}

func TestLoldleSetMax_OwnerSucceeds(t *testing.T) {
	rb, _, cfg := installLoldle(t, 999, "", "")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(999, "/loldle_setmax 5"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "max guesses set to 5") {
		t.Errorf("/loldle_setmax reply: %q", got)
	}
	c, _, err := cfg.Get(context.Background(), configKey("999"))
	if err != nil {
		t.Fatalf("expected config persisted: %v", err)
	}
	if c.MaxGuesses != 5 {
		t.Errorf("MaxGuesses persisted = %d, want 5", c.MaxGuesses)
	}
}

func TestLoldleSetMax_AppliesNextRoundOnly(t *testing.T) {
	rb, _, _ := installLoldle(t, 999, "999", "Aatrox")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(999, "/loldle_setmax 1"))

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(999, "/loldle"))
	if got := rb.LastSent().Text(); !strings.Contains(got, "Guess 0/8") {
		t.Fatalf("active round should keep default max after setmax: %q", got)
	}

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(999, "/loldle_giveup"))
	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(999, "/loldle"))
	if got := rb.LastSent().Text(); !strings.Contains(got, "Guess 0/1") {
		t.Fatalf("new round should use changed max: %q", got)
	}
}

func TestLoldleSetMax_DeniedToNonOwner(t *testing.T) {
	rb, _, _ := installLoldle(t, 999, "", "")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/loldle_setmax 5"))

	if calls := rb.Sent(); len(calls) != 0 {
		t.Errorf("non-owner /loldle_setmax replied: %+v", calls)
	}
}

func TestLoldleSetMax_RejectsOutOfRange(t *testing.T) {
	rb, _, _ := installLoldle(t, 999, "", "")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(999, "/loldle_setmax 99"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "Usage:") {
		t.Errorf("out-of-range setmax should show usage; got %q", got)
	}
}
