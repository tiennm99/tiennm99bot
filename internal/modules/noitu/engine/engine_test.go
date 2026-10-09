package engine

import (
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/dict"
)

const (
	limit = 30 * time.Second
	grace = 2 * time.Second
)

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// testDict is a tiny graph:
//
//	hoa hồng → hồng hào → hào hoa (back to hoa)
//	hồng tâm → tâm sự → sự cố → (cố is a dead end)
//	sĩ quan  → quan tâm
func testDict(t *testing.T) *dict.Store {
	t.Helper()
	s, err := dict.Parse("hoa hồng\nhồng hào\nhào hoa\nhồng tâm\ntâm sự\nsự cố\nsĩ quan\nquan tâm\nhào quang lấp lánh\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return s
}

func newGame(t *testing.T) *Engine {
	t.Helper()
	e, err := New(testDict(t), "hoa hồng", limit, grace, t0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func TestNewRejectsDeadOpening(t *testing.T) {
	if _, err := New(testDict(t), "sự cố", limit, grace, t0); err == nil {
		t.Fatal("dead-end opening accepted")
	}
	if _, err := New(testDict(t), "không có", limit, grace, t0); err == nil {
		t.Fatal("unknown opening accepted")
	}
}

func TestSubmitReasons(t *testing.T) {
	e := newGame(t)
	if e.Current() != "hồng" || e.Turn() != Human {
		t.Fatalf("start: current %q turn %v", e.Current(), e.Turn())
	}
	now := t0.Add(time.Second)
	cases := []struct {
		side Side
		word string
		want Reason
	}{
		{Bot, "hồng hào", ReasonNotYourTurn},
		{Human, "hồng", ReasonTooFewSyllables},
		{Human, "   ", ReasonTooFewSyllables},
		{Human, "hồng xyz", ReasonNotInDictionary},
		{Human, "tâm sự", ReasonWrongLink},
	}
	for _, c := range cases {
		if _, got := e.Submit(c.side, c.word, now); got != c.want {
			t.Errorf("Submit(%v, %q) = %q, want %q", c.side, c.word, got, c.want)
		}
	}
	if m, r := e.Submit(Human, "Hồng  HÀO", now); r != ReasonNone || m.Word != "hồng hào" {
		t.Fatalf("valid move = %+v %q", m, r)
	}
	if _, r := e.Submit(Bot, "hào hoa", now); r != ReasonNone {
		t.Fatalf("bot move rejected: %q", r)
	}
	if _, r := e.Submit(Human, "hoa hồng", now); r != ReasonAlreadyUsed {
		t.Fatalf("reused opening = %q, want already_used", r)
	}
}

func TestVariantSpellingLinksOnCanonical(t *testing.T) {
	e, err := New(testDict(t), "hào hoa", limit, grace, t0) // ends on "hoa"
	if err != nil {
		t.Fatal(err)
	}
	e.current = "sĩ" // force a position answered only by "sĩ quan"
	if m, r := e.Submit(Human, "sỹ quan", t0); r != ReasonNone || m.Word != "sĩ quan" {
		t.Fatalf("variant = %+v %q, want canonical sĩ quan", m, r)
	}
}

func TestTimeoutAfterGrace(t *testing.T) {
	e := newGame(t)
	// Inside the grace window the move still counts, with no speed points.
	if m, r := e.Submit(Human, "hồng hào", t0.Add(limit+grace)); r != ReasonNone {
		t.Fatalf("move in grace = %q", r)
	} else if m.Points != 10+2*1+0+0+rarity(e, "hồng") {
		t.Fatalf("points in grace = %d", m.Points)
	}
	e.Submit(Bot, "hào hoa", t0.Add(limit+grace))
	late := t0.Add(limit + grace).Add(limit + grace + time.Millisecond)
	if e.Timeout(late.Add(-2 * time.Millisecond)) {
		t.Fatal("timeout fired inside the grace window")
	}
	if _, r := e.Submit(Human, "hoa hồng", late); r != ReasonTimeout {
		t.Fatalf("late move = %q, want timeout", r)
	}
	// "hoa" only starts the used opening, so the expired turn was a dead end.
	if !e.Over() || e.Winner() != Bot || e.EndReason() != EndNoLegalMove {
		t.Fatalf("after timeout: over %v winner %v reason %q", e.Over(), e.Winner(), e.EndReason())
	}
	if _, r := e.Submit(Human, "hoa hồng", late); r != ReasonGameOver {
		t.Fatalf("move after end = %q", r)
	}
}

func rarity(e *Engine, link string) int { return e.rarityPoints(link) }

func TestTimeoutSettlesWithoutMove(t *testing.T) {
	e := newGame(t)
	if e.Timeout(t0.Add(limit)) {
		t.Fatal("timeout at the deadline itself")
	}
	if !e.Timeout(t0.Add(limit + grace + time.Nanosecond)) {
		t.Fatal("timeout not settled after grace")
	}
}

func TestBotStuckAndGiveUp(t *testing.T) {
	e := newGame(t)
	e.Submit(Human, "hồng tâm", t0)
	if e.BotStuck() {
		t.Fatal("bot stuck while it can answer tâm")
	}
	e.Submit(Bot, "tâm sự", t0)
	e.Submit(Human, "sự cố", t0)
	if !e.BotStuck() || e.Winner() != Human || e.EndReason() != EndBotStuck {
		t.Fatalf("bot stuck: over %v winner %v reason %q", e.Over(), e.Winner(), e.EndReason())
	}

	g := newGame(t)
	if !g.GiveUp() || g.Winner() != Bot || g.EndReason() != EndGaveUp {
		t.Fatal("give up did not end the game for the bot")
	}
	if g.GiveUp() {
		t.Fatal("second give up reported a change")
	}
}

func TestScoreSumsHumanPointsOnly(t *testing.T) {
	e := newGame(t)
	m1, _ := e.Submit(Human, "hồng tâm", t0)                  // instant: full speed bonus
	e.Submit(Bot, "tâm sự", t0.Add(time.Second))              // bot points do not count
	m2, _ := e.Submit(Human, "sự cố", t0.Add(16*time.Second)) // half the turn left
	if e.Score() != m1.Points+m2.Points {
		t.Fatalf("score %d, want %d + %d", e.Score(), m1.Points, m2.Points)
	}
	// hồng tâm: base 10 + chain 2*1 + syllables 0 + speed 10 + rarity for "hồng" (2 words → 12).
	if m1.Points != 10+2+0+10+12 {
		t.Fatalf("m1 points = %d", m1.Points)
	}
	// sự cố: chain 2*3, speed 10*15/30 = 5, rarity for "sự" (1 word → 15).
	if m2.Points != 10+6+0+5+15 {
		t.Fatalf("m2 points = %d", m2.Points)
	}
}

func TestPointsCapped(t *testing.T) {
	e := newGame(t)
	e.history = make([]Move, 40) // chain bonus at its cap
	if got := e.pointsFor(30, "sự", t0); got != MaxPointsPerWord {
		t.Fatalf("points = %d, want cap %d", got, MaxPointsPerWord)
	}
}

func TestSuggestions(t *testing.T) {
	e := newGame(t)
	got := e.Suggestions(3)
	if len(got) != 2 || got[0] != "hồng hào" || got[1] != "hồng tâm" {
		t.Fatalf("Suggestions = %v", got)
	}
	if e.Suggestions(0) != nil {
		t.Fatal("Suggestions(0) not nil")
	}
}

// deadEndGame leaves the human to answer "cố", which starts no word.
func deadEndGame(t *testing.T) *Engine {
	t.Helper()
	e := newGame(t)
	e.current = "cố"
	return e
}

func TestDeadEndExpiresAsNoLegalMove(t *testing.T) {
	late := t0.Add(limit + grace + time.Millisecond)

	e := deadEndGame(t)
	if !e.Timeout(late) || e.Winner() != Bot || e.EndReason() != EndNoLegalMove {
		t.Fatalf("Timeout in a dead end: winner %v reason %q", e.Winner(), e.EndReason())
	}

	e = deadEndGame(t)
	if _, r := e.Submit(Human, "cố gắng", late); r != ReasonTimeout || e.EndReason() != EndNoLegalMove {
		t.Fatalf("late Submit in a dead end: %q / %q", r, e.EndReason())
	}

	// A playable position still ends as an ordinary timeout.
	e = newGame(t)
	if !e.Timeout(late) || e.EndReason() != EndTimeout {
		t.Fatalf("Timeout with moves left: reason %q", e.EndReason())
	}
}

func TestNoMoveClaimsOnlyADeadEnd(t *testing.T) {
	e := newGame(t)
	if e.NoMove() || e.Over() {
		t.Fatal("NoMove ended a playable position")
	}
	e = deadEndGame(t)
	if !e.NoMove() || e.Winner() != Bot || e.EndReason() != EndNoLegalMove {
		t.Fatalf("NoMove in a dead end: over %v reason %q", e.Over(), e.EndReason())
	}
	if e.NoMove() || e.GiveUp() {
		t.Fatal("a finished game ended a second time")
	}
}
