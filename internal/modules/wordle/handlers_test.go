package wordle

import (
	"context"
	"strings"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// installWordle wires the wordle module to a recording bot with an
// in-memory store. seedTarget pre-seeds a game with the supplied target
// (skip dictionary randomness for deterministic tests). Empty seedTarget
// leaves the store blank so handlers spin up a fresh round on first call.
func installWordle(t *testing.T, ownerID int64, seedSubject, seedTarget string) (*testutil.RecordingBot, GameStore) {
	t.Helper()
	rb := testutil.NewRecordingBot(t)
	col := storage.NewMemoryProvider().Collection("wordle")
	games := storage.Typed[GameState](col)
	mod := New(modules.Deps{Store: col})
	reg := &modules.Registry{
		Modules:     []modules.Module{{Name: "wordle", Commands: mod.Commands}},
		AllCommands: map[string]modules.Command{},
	}
	for _, c := range mod.Commands {
		reg.AllCommands[c.Name] = c
	}
	modules.Install(rb.Bot, reg, modules.Auth{BotOwnerID: ownerID})

	if seedTarget != "" {
		g := &GameState{Target: seedTarget, Guesses: []GuessRecord{}, StartedAt: 1}
		if err := saveGame(context.Background(), games, seedSubject, g); err != nil {
			t.Fatalf("seed game: %v", err)
		}
	}
	return rb, games
}

func TestWordle_NoArgShowsBoard(t *testing.T) {
	rb, _ := installWordle(t, 0, "1", "crane")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/wordle"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "Guess 0/6") {
		t.Errorf("/wordle empty-arg reply missing 'Guess 0/6': %q", got)
	}
}

func TestWordle_Win(t *testing.T) {
	rb, _ := installWordle(t, 0, "1", "crane")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/wordle crane"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "Solved in 1/6") {
		t.Errorf("win reply missing 'Solved in 1/6': %q", got)
	}
	if !strings.Contains(got, "Streak: 1") {
		t.Errorf("win reply missing 'Streak: 1': %q", got)
	}
}

func TestWordle_InvalidWord_TooShort(t *testing.T) {
	rb, _ := installWordle(t, 0, "1", "crane")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/wordle hi"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "5 letters") {
		t.Errorf("short-word reject missing length hint: %q", got)
	}
}

func TestWordle_InvalidWord_NotInDict(t *testing.T) {
	rb, _ := installWordle(t, 0, "1", "crane")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/wordle qqqqq"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "Not in the word list") {
		t.Errorf("dict-miss reject wrong text: %q", got)
	}
}

func TestWordle_PartialGuessShowsProgress(t *testing.T) {
	rb, _ := installWordle(t, 0, "1", "crane")
	// "crate" shares 4 letters with "crane" — valid 5-letter dictionary word.
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/wordle crate"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "Guess 1/6") {
		t.Errorf("partial-guess reply missing 'Guess 1/6': %q", got)
	}
}

func TestWordleNew_StartsFreshRound(t *testing.T) {
	rb, _ := installWordle(t, 0, "1", "crane")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/wordle_new"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "New round started") {
		t.Errorf("/wordle_new reply: %q", got)
	}
	// Previous round was active (no guesses yet) → auto-giveup prelude expected.
	if !strings.Contains(got, "Previous round abandoned") {
		t.Errorf("/wordle_new should announce auto-giveup of prior active round; got %q", got)
	}
}

func TestWordleGiveup_RevealsAnswer(t *testing.T) {
	rb, _ := installWordle(t, 0, "1", "crane")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/wordle_giveup"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "CRANE") {
		t.Errorf("/wordle_giveup should reveal CRANE; got %q", got)
	}
}

func TestWordleGiveup_NoActiveRoundDoesNotStartOrRecordLoss(t *testing.T) {
	rb, games := installWordle(t, 0, "", "")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/wordle_giveup"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "No active round") {
		t.Fatalf("/wordle_giveup with no round: %q", got)
	}
	g, err := loadGame(context.Background(), games, "1")
	if err != nil {
		t.Fatalf("loadGame: %v", err)
	}
	if g != nil {
		t.Fatalf("giveup created game: %+v", g)
	}

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/wordle_stats"))
	if got := rb.LastSent().Text(); !strings.Contains(got, "Played: 0") {
		t.Fatalf("empty giveup recorded stats: %q", got)
	}
}

func TestWordleStats_Empty(t *testing.T) {
	rb, _ := installWordle(t, 0, "", "")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/wordle_stats"))

	got := rb.LastSent().Text()
	for _, want := range []string{"Played: 0", "Wins: 0 (0%)", "Best streak: 0"} {
		if !strings.Contains(got, want) {
			t.Errorf("/wordle_stats missing %q; got %q", want, got)
		}
	}
}

func TestWordleStats_AfterWin(t *testing.T) {
	rb, _ := installWordle(t, 0, "1", "crane")
	// Win: produces 1 played, 1 win, streak 1.
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/wordle crane"))
	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/wordle_stats"))

	got := rb.LastSent().Text()
	for _, want := range []string{"Played: 1", "Wins: 1 (100%)", "Current streak: 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("/wordle_stats post-win missing %q; got %q", want, got)
		}
	}
}

// Group chats key by chat id, so two different users both writing /wordle
// in the same group must mutate the same game.
func TestWordle_GroupSubjectIsChatID(t *testing.T) {
	rb, games := installWordle(t, 0, "-100", "crane")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewGroupMessage(-100, 7, "/wordle crate"))

	// After one guess in chat -100, subject "-100" should have a guess
	// recorded; subject "7" should have no game.
	gChat, err := loadGame(context.Background(), games, "-100")
	if err != nil {
		t.Fatalf("expected game at subject=-100: %v", err)
	}
	if gChat == nil || len(gChat.Guesses) != 1 {
		t.Errorf("group game guesses = %v, want 1", gChat)
	}

	gUser, err := loadGame(context.Background(), games, "7")
	if err != nil {
		t.Fatalf("loadGame for user subject: %v", err)
	}
	if gUser != nil {
		t.Errorf("user-keyed game leaked despite group context: %+v", gUser)
	}
}

// Running out of guesses already records the loss, so a later /wordle_giveup
// on that round must not count a second one.
func TestWordleGiveup_OutOfGuessesDoesNotRecordSecondLoss(t *testing.T) {
	rb, games := installWordle(t, 0, "", "")
	g := &GameState{Target: "crane", StartedAt: 1}
	for range MaxGuesses {
		g.Guesses = append(g.Guesses, GuessRecord{Word: "slate", Results: CompareWords("slate", "crane")})
	}
	if err := saveGame(context.Background(), games, "1", g); err != nil {
		t.Fatalf("seed game: %v", err)
	}

	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/wordle_giveup"))
	if got := rb.LastSent().Text(); !strings.Contains(got, "Current round is over") || !strings.Contains(got, "CRANE") {
		t.Fatalf("/wordle_giveup on exhausted round: %q", got)
	}

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/wordle_stats"))
	if got := rb.LastSent().Text(); !strings.Contains(got, "Played: 0") {
		t.Fatalf("giveup on exhausted round recorded stats: %q", got)
	}
}
