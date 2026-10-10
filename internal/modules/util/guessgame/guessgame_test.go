package guessgame

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/systemstate"
)

// fakeRules plays single letters: a guess must be one lowercase letter, and
// its mark is c when it is the answer, u when the answer is later in the
// alphabet, d when earlier.
type fakeRules struct{ max int }

var errNotALetter = errors.New("not a letter")

func (fakeRules) Label() string     { return "Letter Daily" }
func (r fakeRules) MaxGuesses() int { return r.max }

func (fakeRules) Judge(input, answer string, prior []Guess) (Guess, error) {
	if len(input) != 1 || input[0] < 'a' || input[0] > 'z' {
		return Guess{}, errNotALetter
	}
	for _, g := range prior {
		if g.Word == input {
			return Guess{}, errors.New("repeated")
		}
	}
	switch {
	case input == answer:
		return Guess{Word: input, Marks: "c"}, nil
	case input < answer:
		return Guess{Word: input, Marks: "u"}, nil
	}
	return Guess{Word: input, Marks: "d"}, nil
}

func (fakeRules) Marker(mark byte) string {
	if mark == 'c' {
		return "🟩"
	}
	return "🟥"
}

func (fakeRules) AnswerText(answer string) string { return strings.ToUpper(answer) }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newRounds(t *testing.T, clk *clock) *Rounds {
	t.Helper()
	targets := map[string]string{"": "m", "m": "q", "q": "m"}
	return NewRounds(RoundsConfig{
		Store:   storage.NewMemoryProvider().Collection("letters"),
		Rules:   fakeRules{},
		Pick:    func(prev string) string { return targets[prev] },
		DistLen: 4,
		Now:     clk.now,
	})
}

func TestRounds_LoadStartsTheFirstRoundLazily(t *testing.T) {
	clk := &clock{t: Epoch}
	r := newRounds(t, clk)
	ctx := context.Background()
	if _, err := r.GiveUp(ctx, 1); !errors.Is(err, ErrNoRound) {
		t.Fatalf("give up without a round = %v", err)
	}
	s, err := r.Load(ctx, 1, 3)
	if err != nil || s.Round.Seq != 1 || s.Round.Target != "m" || s.Round.MaxGuesses != 3 || s.Round.Status != StatusPlaying ||
		s.Round.StartedAt != 0 || len(s.Stats.Dist) != 4 {
		t.Fatalf("first load = %+v, %v", s, err)
	}
	// The budget is frozen when the round starts.
	if s, _ := r.Load(ctx, 1, 9); s.Round.MaxGuesses != 3 || s.Round.Seq != 1 {
		t.Fatalf("reload = %+v", s.Round)
	}
	if NewRounds(RoundsConfig{}) != nil {
		t.Fatal("rounds without storage")
	}
}

func TestRounds_WinLossStreakAndDistribution(t *testing.T) {
	clk := &clock{t: Epoch}
	r := newRounds(t, clk)
	ctx := context.Background()
	s, err := r.Guess(ctx, 1, 1, "a", 3)
	if err != nil || len(s.Round.Guesses) != 1 || s.Round.Guesses[0].Marks != "u" || s.Round.StartedAt != Epoch.UnixMilli() || s.Ended {
		t.Fatalf("first guess = %+v, %v", s, err)
	}
	if _, err := r.Guess(ctx, 1, 1, "a", 3); err == nil || err.Error() != "repeated" {
		t.Fatalf("judge error = %v", err)
	}
	clk.advance(time.Minute)
	s, err = r.Guess(ctx, 1, 1, "m", 3)
	if err != nil || !s.Ended || s.Round.Status != StatusWon || s.Round.FinishedAt != Epoch.Add(time.Minute).UnixMilli() ||
		s.Stats.Played != 1 || s.Stats.Wins != 1 || s.Stats.CurStreak != 1 || s.Stats.Dist[1] != 1 || s.Stats.LastSeq != 1 {
		t.Fatalf("win = %+v, %v", s, err)
	}
	if _, err := r.Guess(ctx, 1, 1, "b", 3); !errors.Is(err, ErrFinished) {
		t.Fatalf("guess after the win = %v", err)
	}
	s, _ = r.New(ctx, 1, 1, 3)
	if s.Round.Seq != 2 || s.Round.Target != "q" || s.Abandoned != nil {
		t.Fatalf("new after a win = %+v", s)
	}
	// Seq 0 plays the current round; three misses lose it.
	for _, l := range []string{"a", "b", "c"} {
		s, err = r.Guess(ctx, 1, 0, l, 3)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !s.Ended || s.Round.Status != StatusLost || s.Round.GaveUp || s.Stats.Played != 2 || s.Stats.CurStreak != 0 || s.Stats.MaxStreak != 1 {
		t.Fatalf("loss = %+v", s)
	}
	if _, err := r.Guess(ctx, 1, 1, "q", 3); !errors.Is(err, ErrNewRound) {
		t.Fatalf("stale seq = %v", err)
	}
	// Another player is untouched.
	if st, _ := r.LoadStats(ctx, 2); st.Played != 0 {
		t.Fatalf("player 2 = %+v", st)
	}
}

func TestRounds_NewAndGiveUp(t *testing.T) {
	r := newRounds(t, &clock{t: Epoch})
	ctx := context.Background()
	r.Load(ctx, 1, 5)
	// Nobody guessed: replaced, no loss.
	s, err := r.New(ctx, 1, 1, 5)
	if err != nil || s.Round.Seq != 2 || s.Abandoned != nil || s.Stats.Played != 0 {
		t.Fatalf("new on a fresh round = %+v, %v", s, err)
	}
	// A stale seq, such as a double click, starts nothing.
	if s, _ := r.New(ctx, 1, 1, 5); s.Round.Seq != 2 {
		t.Fatalf("stale new = %+v", s.Round)
	}
	r.Guess(ctx, 1, 2, "a", 5)
	s, _ = r.New(ctx, 1, 2, 5)
	if s.Round.Seq != 3 || s.Abandoned == nil || s.Abandoned.Target != "q" || !s.Abandoned.GaveUp || s.Stats.Played != 1 || s.Stats.Wins != 0 {
		t.Fatalf("abandon = %+v", s)
	}
	s, err = r.GiveUp(ctx, 1)
	if err != nil || !s.Ended || !s.Round.GaveUp || s.Round.Status != StatusLost || s.Stats.Played != 2 {
		t.Fatalf("give up = %+v, %v", s, err)
	}
	s, _ = r.GiveUp(ctx, 1)
	if s.Ended || s.Stats.Played != 2 {
		t.Fatalf("second give up = %+v", s)
	}
	s, _ = r.Replace(ctx, 1, 5)
	if s.Round.Seq != 4 || s.Stats.Played != 2 {
		t.Fatalf("replace = %+v", s)
	}
}

// flakyStats fails the next Put.
type flakyStats struct {
	storage.DocStore[RoundStats]
	fail bool
}

func (f *flakyStats) Put(ctx context.Context, id string, v RoundStats) error {
	if f.fail {
		f.fail = false
		return errors.New("store down")
	}
	return f.DocStore.Put(ctx, id, v)
}

// A round saved as finished whose stats write failed counts on the next
// request, once.
func TestRounds_FailedStatsWriteIsRepairedOnce(t *testing.T) {
	r := newRounds(t, &clock{t: Epoch})
	ctx := context.Background()
	flaky := &flakyStats{DocStore: r.stats}
	r.stats = flaky
	flaky.fail = true
	if _, err := r.Guess(ctx, 1, 1, "m", 3); err == nil {
		t.Fatal("stats write failure hidden")
	}
	for range 2 {
		s, err := r.Load(ctx, 1, 3)
		if err != nil || s.Round.Status != StatusWon || s.Stats.Played != 1 || s.Stats.Wins != 1 {
			t.Fatalf("repaired = %+v, %v", s, err)
		}
	}
}

func TestRounds_ConcurrentGuessesKeepOneBoard(t *testing.T) {
	r := newRounds(t, &clock{t: Epoch})
	ctx := context.Background()
	var wg sync.WaitGroup
	for _, l := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.Guess(ctx, 1, 0, l, 10)
		}()
	}
	wg.Wait()
	s, err := r.Load(ctx, 1, 10)
	if err != nil || len(s.Round.Guesses) != 8 {
		t.Fatalf("board = %+v, %v", s.Round, err)
	}
}

func TestCards_RecordLookupTouchAndCleanup(t *testing.T) {
	clk := &clock{t: Epoch}
	c := NewCards(storage.NewMemoryProvider().Collection("letters"), clk.now, "letters")
	ctx := context.Background()
	if _, ok, err := c.Lookup(ctx, -1, 5); ok || err != nil {
		t.Fatalf("unrecorded = %v, %v", ok, err)
	}
	if err := c.Record(ctx, -1, 5, 7); err != nil {
		t.Fatal(err)
	}
	if err := c.Record(ctx, -1, 6, 0); err != nil {
		t.Fatal(err)
	}
	// Card 5 is played every 20 days, so it never expires; card 6 is not.
	for range 3 {
		clk.advance(20 * 24 * time.Hour)
		if rec, ok, err := c.Lookup(ctx, -1, 5); !ok || err != nil || rec.ThreadID != 7 {
			t.Fatalf("lookup = %+v %v %v", rec, ok, err)
		}
		if err := c.Cleanup(ctx, modules.Deps{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, _ := c.Lookup(ctx, -1, 5); !ok {
		t.Fatal("a card in use expired")
	}
	if _, ok, _ := c.Lookup(ctx, -1, 6); ok {
		t.Fatal("an idle card outlived the TTL")
	}
	var none *Cards
	if _, ok, err := none.Lookup(ctx, 1, 1); ok || err != nil || none.Record(ctx, 1, 1, 0) == nil || none.Cleanup(ctx, modules.Deps{}) != nil {
		t.Fatal("nil cards")
	}
}

func TestDaily_FakeRulesLabelMarkersAndStatsPrefix(t *testing.T) {
	clk := &clock{t: Epoch.Add(time.Hour)}
	coll := storage.NewMemoryProvider().Collection("letters")
	var reports []int
	d := NewDaily(DailyConfig{
		Rules: fakeRules{max: 4}, Answers: []string{"m"}, AnswerKey: []byte("k"), Store: coll,
		StatsPrefix: "dstats:", Now: clk.now, Async: func(f func()) { f() },
		Reporter: reporterFunc(func(_ htmlgame.Address, _ int64, score int) { reports = append(reports, score) }),
		LogName:  "letters", AnswerPrefix: "Letter: ",
	})
	ctx := context.Background()
	c := Claims{UserID: 1, Name: "Ann", ChatID: -5, MessageID: 9, Expiry: 1}
	if _, err := d.SubmitGuess(ctx, c, 1, "1"); !errors.Is(err, errNotALetter) {
		t.Fatalf("judge error = %v", err)
	}
	if _, err := d.SubmitGuess(ctx, c, 2, "a"); !errors.Is(err, ErrNewPuzzle) {
		t.Fatalf("stale puzzle = %v", err)
	}
	d.SubmitGuess(ctx, c, 1, "a")
	o, err := d.SubmitGuess(ctx, c, 1, "m")
	if err != nil || o.P.Status != StatusWon || o.Stats.Dist[1] != 1 || len(o.Stats.Dist) != 4 {
		t.Fatalf("win = %+v, %v", o, err)
	}
	if len(reports) != 1 || reports[0] != 3 {
		t.Fatalf("score = %v, want 4+1-2", reports)
	}
	if _, _, err := d.Stats.Get(ctx, "dstats:1"); err != nil {
		t.Fatalf("stats key: %v", err)
	}
	if got := d.ShareText(o.P); got != "Letter Daily #1 2/4\n\n🟥\n🟩" {
		t.Fatalf("share = %q", got)
	}
	cd, _, _ := d.ChatDays.Get(ctx, ChatDayKey(1, -5, 0))
	live, _ := d.RenderLive(ctx, cd)
	if live != "Letter Daily #1 · live results\n\nAnn 2/4\n🟥\n🟩" {
		t.Fatalf("live = %q", live)
	}
	clk.advance(24 * time.Hour)
	recap, err := d.RenderRecap(ctx, 1, -5, 0)
	if err != nil || recap != "Letter Daily #1 — yesterday's results\nLetter: M\n👑 2/4: Ann" {
		t.Fatalf("recap = %q, %v", recap, err)
	}
}

type reporterFunc func(a htmlgame.Address, userID int64, score int)

func (f reporterFunc) Report(_ context.Context, a htmlgame.Address, userID int64, score int) error {
	f(a, userID, score)
	return nil
}

func TestMigrationHelpers(t *testing.T) {
	ctx := context.Background()
	p := storage.NewMemoryProvider()
	for key, want := range map[string]int64{"stats:7": 7, "stats:-100": 0, "stats:x": 0} {
		uid, ok := LegacyUserID(key, "stats:")
		if (want > 0) != ok || (ok && uid != want) {
			t.Errorf("LegacyUserID(%q) = %d, %v", key, uid, ok)
		}
	}
	store := storage.Typed[RoundStats](p.Collection("letters"))
	if added, err := PutIfMissing(ctx, store, "k", RoundStats{Played: 1}); !added || err != nil {
		t.Fatalf("first put = %v, %v", added, err)
	}
	if added, err := PutIfMissing(ctx, store, "k", RoundStats{Played: 2}); added || err != nil {
		t.Fatalf("second put = %v, %v", added, err)
	}
	if st, _, _ := store.Get(ctx, "k"); st.Played != 1 {
		t.Fatalf("overwritten: %+v", st)
	}
	runs := 0
	migrate := func() (int, error) { runs++; return 4, nil }
	fail := func() (int, error) { runs++; return 0, errors.New("boom") }
	sys := p.Collection("system")
	if err := RunOnce(ctx, sys, "m", fail); err == nil {
		t.Fatal("failure hidden")
	}
	for range 2 {
		if err := RunOnce(ctx, sys, "m", migrate); err != nil {
			t.Fatal(err)
		}
	}
	rec, ok, _ := systemstate.New(sys).Get(ctx, "m")
	if runs != 2 || !ok || rec.Status != "complete" || rec.Count != 4 {
		t.Fatalf("runs %d, marker %+v", runs, rec)
	}
}
