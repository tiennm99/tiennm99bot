package opponent

import (
	"errors"
	"iter"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/dict"
	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/engine"
)

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func realGame(t *testing.T, seed uint64) *engine.Engine {
	t.Helper()
	store, err := dict.Default()
	if err != nil {
		t.Fatalf("dict: %v", err)
	}
	opening := store.RandomOpening(rand.New(rand.NewPCG(seed, seed)), 20)
	e, err := engine.New(store, opening, 30*time.Second, 2*time.Second, t0)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return e
}

func TestParseDifficulty(t *testing.T) {
	for in, want := range map[string]Difficulty{"": Medium, "easy": Easy, "medium": Medium, "hard": Hard} {
		if got, ok := ParseDifficulty(in); !ok || got != want {
			t.Errorf("ParseDifficulty(%q) = %q %v", in, got, ok)
		}
	}
	if _, ok := ParseDifficulty("insane"); ok {
		t.Error("unknown difficulty accepted")
	}
	if _, err := New(Easy, nil); err == nil {
		t.Error("nil rng accepted")
	}
}

// Every strategy plays legal moves for both sides until the game ends, and
// each choice passes the engine's own validation.
func TestStrategiesPlayLegalMoves(t *testing.T) {
	for _, d := range []Difficulty{Easy, Medium, Hard} {
		t.Run(string(d), func(t *testing.T) {
			e := realGame(t, 7)
			rng := rand.New(rand.NewPCG(1, 2))
			strategy, _ := New(d, rng)
			side := engine.Human
			for range 40 {
				word, err := strategy.Choose(e)
				if errors.Is(err, ErrNoMove) {
					if e.HasLegalMove() {
						t.Fatal("ErrNoMove while a legal move exists")
					}
					return
				}
				if !slices.Contains(e.LegalMoves(), word) {
					t.Fatalf("%q is not a legal move", word)
				}
				if _, r := e.Submit(side, word, t0); r != engine.ReasonNone {
					t.Fatalf("engine rejected %q: %q", word, r)
				}
				if side == engine.Human {
					side = engine.Bot
				} else {
					side = engine.Human
				}
			}
		})
	}
}

// deadBoard has no legal move at all.
type deadBoard struct{}

func (deadBoard) LegalMoves() []string { return nil }
func (deadBoard) Used(string) bool     { return false }
func (deadBoard) WordsStartingWith(string) iter.Seq[string] {
	return func(func(string) bool) {}
}
func (deadBoard) LastSyllable(string) (string, bool) { return "", false }

func TestStrategiesReportNoMoveOnDeadEnd(t *testing.T) {
	for _, d := range []Difficulty{Easy, Medium, Hard} {
		strategy, _ := New(d, rand.New(rand.NewPCG(1, 1)))
		if _, err := strategy.Choose(deadBoard{}); !errors.Is(err, ErrNoMove) {
			t.Errorf("%s: err = %v, want ErrNoMove", d, err)
		}
	}
}

func TestMediumTakesImmediateWin(t *testing.T) {
	store, err := dict.Parse("hoa hồng\nhồng hào\nhồng tâm\ntâm sự\ntâm hồn\nhồn nhiên\nhồn ma\nhào hoa\nhoa lá\n")
	if err != nil {
		t.Fatal(err)
	}
	// After "hồng tâm" the bot picks between "tâm hồn" (two replies) and "tâm sự"
	// (none on "sự"): the winning move must be taken.
	e, err := engine.New(store, "hoa hồng", time.Minute, 0, t0)
	if err != nil {
		t.Fatal(err)
	}
	e.Submit(engine.Human, "hồng tâm", t0)
	strategy, _ := New(Medium, rand.New(rand.NewPCG(3, 3)))
	if w, err := strategy.Choose(e); err != nil || w != "tâm sự" {
		t.Fatalf("medium chose %q %v, want the winning tâm sự", w, err)
	}
}

func TestHardRespectsNodeCap(t *testing.T) {
	maxSeen := 0
	for seed := range uint64(12) {
		e := realGame(t, seed)
		h := &hard{rng: rand.New(rand.NewPCG(seed, 99))}
		if _, err := h.Choose(e); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if h.lastNodes > nodeCap {
			t.Fatalf("seed %d: searched %d nodes, cap %d", seed, h.lastNodes, nodeCap)
		}
		maxSeen = max(maxSeen, h.lastNodes)
	}
	if maxSeen == 0 {
		t.Fatal("hard never searched; the node counter is not wired")
	}
}
