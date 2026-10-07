package wordle

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

func newWordleGames() GameStore {
	return storage.Typed[GameState](storage.NewMemoryProvider().Collection("wordle"))
}

func newWordleStats() StatsStore {
	return storage.Typed[Stats](storage.NewMemoryProvider().Collection("wordle"))
}

func TestStats_DefaultLastResultAtIsNull(t *testing.T) {
	// Go's *int64 must marshal as null when nil, so unplayed accounts emit
	// `"lastResultAt": null` and the field stays distinguishable from
	// "played at ms-epoch 0".
	s := Stats{}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	want := `{"played":0,"wins":0,"streak":0,"bestStreak":0,"lastResultAt":null}`
	if got != want {
		t.Errorf("Stats marshal:\ngot  %s\nwant %s", got, want)
	}
}

func TestStats_WithResultMarshalsAsNumber(t *testing.T) {
	at := int64(1700000000000)
	s := Stats{Played: 1, Wins: 1, Streak: 1, BestStreak: 1, LastResultAt: &at}
	b, _ := json.Marshal(s)
	want := `{"played":1,"wins":1,"streak":1,"bestStreak":1,"lastResultAt":1700000000000}`
	if string(b) != want {
		t.Errorf("Stats marshal:\ngot  %s\nwant %s", b, want)
	}
}

func TestGameState_JSONShapeIsStable(t *testing.T) {
	g := GameState{
		Target: "crane",
		Guesses: []GuessRecord{
			{Word: "slate", Results: []LetterScore{
				{Letter: "s", Result: ResultCorrect},
				{Letter: "l", Result: ResultPartial},
				{Letter: "a", Result: ResultCorrect},
				{Letter: "t", Result: ResultWrong},
				{Letter: "e", Result: ResultCorrect},
			}},
		},
		Solved:    false,
		Giveup:    false,
		StartedAt: 1700000000000,
	}
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"target":"crane","guesses":[{"word":"slate","results":[{"letter":"s","result":"correct"},{"letter":"l","result":"partial"},{"letter":"a","result":"correct"},{"letter":"t","result":"wrong"},{"letter":"e","result":"correct"}]}],"solved":false,"giveup":false,"startedAt":1700000000000}`
	if string(b) != want {
		t.Errorf("GameState marshal:\ngot  %s\nwant %s", b, want)
	}
}

func TestRecordResult_WinIncrementsStreak(t *testing.T) {
	ctx := context.Background()
	stats := newWordleStats()

	s, err := recordResult(ctx, stats, "u1", true, 100)
	if err != nil {
		t.Fatalf("recordResult: %v", err)
	}
	if s.Played != 1 || s.Wins != 1 || s.Streak != 1 || s.BestStreak != 1 {
		t.Errorf("first win: %+v", s)
	}
	if s.LastResultAt == nil || *s.LastResultAt != 100 {
		t.Errorf("LastResultAt = %v, want *=100", s.LastResultAt)
	}

	// Second win bumps streak; bestStreak follows.
	s, _ = recordResult(ctx, stats, "u1", true, 200)
	if s.Streak != 2 || s.BestStreak != 2 {
		t.Errorf("two wins: streak=%d best=%d", s.Streak, s.BestStreak)
	}
}

func TestRecordResult_LossResetsStreakKeepsBest(t *testing.T) {
	ctx := context.Background()
	stats := newWordleStats()

	_, _ = recordResult(ctx, stats, "u1", true, 100)
	_, _ = recordResult(ctx, stats, "u1", true, 200)
	s, _ := recordResult(ctx, stats, "u1", false, 300)
	if s.Streak != 0 {
		t.Errorf("loss should reset streak, got %d", s.Streak)
	}
	if s.BestStreak != 2 {
		t.Errorf("best streak should persist, got %d", s.BestStreak)
	}
	if s.Played != 3 || s.Wins != 2 {
		t.Errorf("counters: %+v", s)
	}
}

func TestLoadGame_MissingReturnsNil(t *testing.T) {
	g, err := loadGame(context.Background(), newWordleGames(), "nobody")
	if err != nil {
		t.Errorf("missing game should not error: %v", err)
	}
	if g != nil {
		t.Errorf("expected nil game, got %+v", g)
	}
}

func TestSaveGame_RoundTrip(t *testing.T) {
	ctx := context.Background()
	games := newWordleGames()
	want := &GameState{Target: "crane", Guesses: []GuessRecord{}, StartedAt: 42}
	if err := saveGame(ctx, games, "u1", want); err != nil {
		t.Fatal(err)
	}
	got, err := loadGame(ctx, games, "u1")
	if err != nil || got == nil {
		t.Fatalf("loadGame: got=%v err=%v", got, err)
	}
	if got.Target != "crane" || got.StartedAt != 42 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestIsFinished(t *testing.T) {
	if !isFinished(&GameState{Solved: true}) {
		t.Error("solved should be finished")
	}
	if !isFinished(&GameState{Giveup: true}) {
		t.Error("giveup should be finished")
	}
	full := &GameState{Guesses: make([]GuessRecord, MaxGuesses)}
	if !isFinished(full) {
		t.Error("max-guesses should be finished")
	}
	if isFinished(&GameState{Guesses: make([]GuessRecord, MaxGuesses-1)}) {
		t.Error("under-max not finished")
	}
}
