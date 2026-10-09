package wordledaily

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

func TestPuzzleNum_RollsOverAt0700ICT(t *testing.T) {
	h := newHarness(t)
	s := h.svc
	at := func(y int, m time.Month, d, hh, mm, ss int) time.Time { return time.Date(y, m, d, hh, mm, ss, 0, ict) }
	cases := []struct {
		at   time.Time
		want int
	}{
		{at(2026, 10, 9, 7, 0, 0), 1},
		{at(2026, 10, 10, 6, 59, 59), 1},
		{at(2026, 10, 10, 7, 0, 0), 2},
		{at(2026, 10, 11, 0, 30, 0), 2},
		{at(2026, 10, 19, 7, 0, 0), 11},
		{at(2026, 1, 1, 0, 0, 0), 1}, // before the epoch
	}
	for _, c := range cases {
		if got := s.puzzleNum(c.at); got != c.want {
			t.Errorf("puzzleNum(%s) = %d, want %d", c.at, got, c.want)
		}
	}
	if got := s.puzzleDate(2); got != "2026-10-10" {
		t.Errorf("puzzleDate(2) = %s", got)
	}
	if got := s.puzzleStart(2); !got.Equal(at(2026, 10, 10, 7, 0, 0)) {
		t.Errorf("puzzleStart(2) = %s", got)
	}
}

func TestAnswerFor_KeyedPermutationWithoutRepeats(t *testing.T) {
	h := newHarness(t)
	n := len(testAnswers)
	seen := make(map[string]bool, n)
	var first []string
	for num := 1; num <= n; num++ {
		a := h.svc.answerFor(num)
		if seen[a] {
			t.Fatalf("answer %q repeats within the first cycle (puzzle %d)", a, num)
		}
		seen[a] = true
		if num <= 10 {
			first = append(first, a)
		}
	}
	if len(seen) != n {
		t.Fatalf("cycle covers %d answers, want %d", len(seen), n)
	}
	if slices.Equal(first, testAnswers[:10]) {
		t.Fatal("answers follow the sorted list order")
	}
	// The same key gives the same order; another key another order.
	same := newService(testConfig(h.clock, h.coll, h.rep, nil, h.sched))
	other := testConfig(h.clock, h.coll, h.rep, nil, h.sched)
	other.rootKey = []byte("another-root-key-another-root-key")
	diff := newService(other)
	var sameSeq, diffSeq []string
	for num := 1; num <= 10; num++ {
		sameSeq = append(sameSeq, same.answerFor(num))
		diffSeq = append(diffSeq, diff.answerFor(num))
	}
	if !slices.Equal(sameSeq, first) {
		t.Fatal("same key, different sequence")
	}
	if slices.Equal(diffSeq, first) {
		t.Fatal("different key, same sequence")
	}
	// The next cycle is another permutation.
	if h.svc.answerFor(n+1) == h.svc.answerFor(1) && h.svc.answerFor(n+2) == h.svc.answerFor(2) {
		t.Fatal("second cycle repeats the first")
	}
}

func TestResolvePuzzle_StoredAnswerWinsOverKeyChange(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first, err := h.svc.resolvePuzzle(ctx, 1)
	if err != nil || first != h.svc.answerFor(1) {
		t.Fatalf("resolve = %q, %v", first, err)
	}
	cfg := testConfig(h.clock, h.coll, h.rep, nil, h.sched)
	cfg.rootKey = []byte("another-root-key-another-root-key")
	rotated := newService(cfg)
	if rotated.answerFor(1) == first {
		t.Skip("rotated key happens to pick the same answer")
	}
	if got, err := rotated.resolvePuzzle(ctx, 1); err != nil || got != first {
		t.Fatalf("after key change = %q, %v; want stored %q", got, err, first)
	}
	doc, _, err := storage.Typed[puzzleDoc](h.coll).Get(ctx, puzzleKey(1))
	if err != nil || doc.Num != 1 || doc.Answer != first || doc.CreatedAt == 0 {
		t.Fatalf("stored puzzle = %+v, %v", doc, err)
	}
}

func TestResolvePuzzle_ConcurrentFirstResolutionAgrees(t *testing.T) {
	h := newHarness(t)
	var wg sync.WaitGroup
	got := make([]string, 16)
	for i := range got {
		// A service per goroutine, so no in-memory cache hides the race.
		svc := newService(testConfig(h.clock, h.coll, h.rep, nil, h.sched))
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := svc.resolvePuzzle(context.Background(), 3)
			if err != nil {
				t.Error(err)
			}
			got[i] = a
		}()
	}
	wg.Wait()
	for _, a := range got {
		if a != got[0] || a == "" {
			t.Fatalf("answers disagree: %v", got)
		}
	}
}
