package engine

import (
	"slices"
	"testing"
	"time"
)

func newMatch(t *testing.T, seats int) *Match {
	t.Helper()
	m, err := NewMatch(testDict(t), seats, "hoa hồng", limit, grace, t0)
	if err != nil {
		t.Fatalf("NewMatch: %v", err)
	}
	return m
}

// late is just past the first turn's deadline and grace.
var late = t0.Add(limit + grace + time.Nanosecond)

func TestNewMatchSeatBounds(t *testing.T) {
	for _, n := range []int{0, 1, MaxSeats + 1} {
		if _, err := NewMatch(testDict(t), n, "hoa hồng", limit, grace, t0); err == nil {
			t.Errorf("NewMatch with %d seats accepted", n)
		}
	}
	if _, err := NewMatch(testDict(t), 2, "sự cố", limit, grace, t0); err == nil {
		t.Error("dead-end opening accepted")
	}
	m := newMatch(t, MaxSeats)
	if m.Seats() != MaxSeats || m.Turn() != 0 || m.Winner() != -1 || m.Over() {
		t.Fatalf("fresh match: seats %d turn %d winner %d over %v", m.Seats(), m.Turn(), m.Winner(), m.Over())
	}
}

func TestMatchWordRulesAreTheBoards(t *testing.T) {
	m := newMatch(t, 2)
	cases := []struct {
		seat int
		word string
		want Reason
	}{
		{1, "hồng hào", ReasonNotYourTurn},
		{0, "hồng", ReasonTooFewSyllables},
		{0, "hồng xyz", ReasonNotInDictionary},
		{0, "tâm sự", ReasonWrongLink},
	}
	for _, c := range cases {
		if _, got := m.Submit(c.seat, c.word, t0); got != c.want {
			t.Errorf("Submit(%d, %q) = %q, want %q", c.seat, c.word, got, c.want)
		}
	}
	mv, r := m.Submit(0, "Hồng  HÀO", t0)
	if r != ReasonNone || mv.Word != "hồng hào" || mv.Seat != 0 || m.Score(0) != mv.Points || m.Turn() != 1 {
		t.Fatalf("valid move = %+v %q; score %d turn %d", mv, r, m.Score(0), m.Turn())
	}
	m.Submit(1, "hào hoa", t0)
	if _, r := m.Submit(0, "hoa hồng", t0); r != ReasonAlreadyUsed {
		t.Fatalf("reused opening = %q", r)
	}
}

func TestMatchTimeoutEliminatesAndPlaysOn(t *testing.T) {
	m := newMatch(t, 3)
	if m.Timeout(t0.Add(limit + grace)) {
		t.Fatal("timeout inside the grace window")
	}
	if !m.Timeout(late) {
		t.Fatal("timeout did not fire")
	}
	if m.Over() || m.Alive(0) || m.OutReason(0) != EndTimeout || m.Turn() != 1 {
		t.Fatalf("after timeout: over %v alive0 %v reason %q turn %d", m.Over(), m.Alive(0), m.OutReason(0), m.Turn())
	}
	// The position survives the player: the next seat answers the same
	// syllable, with a full turn that starts when the expired one ended.
	if m.Current() != "hồng" {
		t.Errorf("current = %q, want hồng", m.Current())
	}
	if want := t0.Add(limit + grace + limit); !m.Deadline().Equal(want) {
		t.Errorf("deadline = %v, want %v", m.Deadline(), want)
	}
}

func TestMatchSyllableAndUsedWordsSurviveAnEliminatedPlayer(t *testing.T) {
	m := newMatch(t, 3)
	m.Submit(0, "hồng tâm", t0)
	at := t0.Add(limit + grace + time.Nanosecond)
	if !m.Timeout(at) || m.Alive(1) {
		t.Fatal("seat 1 not eliminated")
	}
	if m.Turn() != 2 || m.Current() != "tâm" || !m.Used("hồng tâm") {
		t.Fatalf("turn %d current %q used %v", m.Turn(), m.Current(), m.Used("hồng tâm"))
	}
	if _, r := m.Submit(2, "tâm sự", at); r != ReasonNone {
		t.Fatalf("next seat answering the same syllable = %q", r)
	}
	if m.Score(0) == 0 {
		t.Fatal("seat 0's points were lost")
	}
}

func TestMatchTurnSkipsEliminatedSeats(t *testing.T) {
	m := newMatch(t, 3)
	m.Submit(0, "hồng tâm", t0)
	deadline := m.Deadline()
	// Seat 2 leaves while seat 1 is thinking: seat 1 keeps its turn and clock.
	if !m.Resign(2, t0.Add(time.Second)) || m.OutReason(2) != EndGaveUp {
		t.Fatal("out-of-turn resign failed")
	}
	if m.Turn() != 1 || !m.Deadline().Equal(deadline) {
		t.Fatalf("turn %d deadline %v, want 1 / %v", m.Turn(), m.Deadline(), deadline)
	}
	m.Submit(1, "tâm sự", t0.Add(2*time.Second))
	if m.Turn() != 0 {
		t.Fatalf("turn = %d, want 0: the eliminated seat 2 is skipped", m.Turn())
	}
	if _, r := m.Submit(2, "sự cố", t0.Add(3*time.Second)); r != ReasonNotYourTurn {
		t.Fatalf("eliminated seat submit = %q", r)
	}
	if m.Resign(2, t0) {
		t.Fatal("an eliminated seat resigned twice")
	}
}

func TestMatchLastStandingWins(t *testing.T) {
	m := newMatch(t, 3)
	m.Submit(0, "hồng hào", t0)
	// Nobody else plays. One Timeout per expired turn catches up even when
	// the caller settles late.
	at := t0.Add(3 * (limit + grace))
	for m.Timeout(at) {
	}
	if !m.Over() || m.Winner() != 0 || m.Alive(1) || m.Alive(2) || m.Turn() != 0 {
		t.Fatalf("over %v winner %d", m.Over(), m.Winner())
	}
	if got := m.Standings(); !slices.Equal(got, []int{0, 2, 1}) {
		t.Fatalf("standings = %v, want [0 2 1]", got)
	}
	if _, r := m.Submit(0, "hào hoa", at); r != ReasonGameOver {
		t.Fatalf("move after the end = %q", r)
	}
}

func TestMatchDeadEndSettlesEveryoneBehind(t *testing.T) {
	m := newMatch(t, 3)
	m.Submit(0, "hồng tâm", t0)
	m.Submit(1, "tâm sự", t0)
	m.Submit(2, "sự cố", t0) // "cố" starts nothing
	if m.Over() || m.Turn() != 0 {
		t.Fatal("a dead end ended the match before the next seat had its turn")
	}
	if !m.Timeout(late) {
		t.Fatal("timeout did not fire")
	}
	if !m.Over() || m.Winner() != 2 || m.OutReason(0) != EndNoLegalMove || m.OutReason(1) != EndNoLegalMove {
		t.Fatalf("over %v winner %d reasons %q %q", m.Over(), m.Winner(), m.OutReason(0), m.OutReason(1))
	}
	if got := m.Standings(); !slices.Equal(got, []int{2, 1, 0}) {
		t.Fatalf("standings = %v", got)
	}
}

func TestMatchResignInADeadEndIsNoLegalMove(t *testing.T) {
	m := newMatch(t, 2)
	m.Submit(0, "hồng tâm", t0)
	m.Submit(1, "tâm sự", t0)
	m.Submit(0, "sự cố", t0)
	if !m.Resign(1, t0) || m.OutReason(1) != EndNoLegalMove || m.Winner() != 0 || m.EndReason() != EndNoLegalMove {
		t.Fatalf("reason %q winner %d", m.OutReason(1), m.Winner())
	}
}

func TestMatchEndMaxMovesRanksSurvivorsByPoints(t *testing.T) {
	m := newMatch(t, 3)
	// Seat 0 answers on the buzzer, seats 1 and 2 at once, so the points run
	// against seat order: 2 > 1 > 0.
	at := t0.Add(limit - time.Second)
	m.Submit(0, "hồng tâm", at)
	m.Submit(1, "tâm sự", at)
	m.Submit(2, "sự cố", at)
	if m.Score(2) <= m.Score(1) || m.Score(1) <= m.Score(0) {
		t.Fatalf("scores %d %d %d do not run against seat order", m.Score(0), m.Score(1), m.Score(2))
	}
	if !m.EndMaxMoves() || m.Winner() != 2 || m.EndReason() != EndMaxMoves || m.Turn() != 2 {
		t.Fatalf("winner %d reason %q", m.Winner(), m.EndReason())
	}
	if got := m.Standings(); !slices.Equal(got, []int{2, 1, 0}) {
		t.Fatalf("standings = %v, want [2 1 0] by points", got)
	}
	if m.EndMaxMoves() {
		t.Fatal("ended twice")
	}
}

func TestMatchLateSubmitStartsTheNextTurnWhenTheExpiredOneEnded(t *testing.T) {
	m := newMatch(t, 3)
	at := t0.Add(2 * time.Minute)
	if _, r := m.Submit(0, "hồng hào", at); r != ReasonTimeout || m.Alive(0) || m.Turn() != 1 {
		t.Fatalf("late submit = %q alive %v turn %d", r, m.Alive(0), m.Turn())
	}
	if want := t0.Add(limit + grace + limit); !m.Deadline().Equal(want) {
		t.Fatalf("deadline = %v, want %v", m.Deadline(), want)
	}
}

func TestMatchOnTurnResignRestartsTheClock(t *testing.T) {
	m := newMatch(t, 3)
	at := t0.Add(10 * time.Second)
	if !m.Resign(0, at) || m.Turn() != 1 || m.OutReason(0) != EndGaveUp {
		t.Fatalf("turn %d reason %q", m.Turn(), m.OutReason(0))
	}
	if want := at.Add(limit); !m.Deadline().Equal(want) {
		t.Fatalf("deadline = %v, want %v", m.Deadline(), want)
	}
}

// Only the seat facing a dead end can claim it; anyone else leaving gives up,
// and the seat to act keeps its own turn.
func TestMatchOutOfTurnResignInADeadEndIsGivingUp(t *testing.T) {
	m := newMatch(t, 3)
	m.Submit(0, "hồng tâm", t0)
	m.Submit(1, "tâm sự", t0)
	m.Submit(2, "sự cố", t0) // seat 0 faces "cố"
	if !m.Resign(1, t0) || m.OutReason(1) != EndGaveUp {
		t.Fatalf("out-of-turn reason = %q", m.OutReason(1))
	}
	if m.Over() || m.Turn() != 0 {
		t.Fatalf("over %v turn %d: the dead end was settled out of turn", m.Over(), m.Turn())
	}
}
