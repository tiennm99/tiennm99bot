package engine

import (
	"fmt"
	"slices"
	"time"
)

// MinSeats and MaxSeats bound the players in one Match.
const (
	MinSeats = 2
	MaxSeats = 8
)

// Match is one game between people taking turns in seat order, ported from
// the multiplayer rules of tiennm99/noitu server/internal/game/engine.go.
//
// A player who fails their turn (the clock runs out, or they give up) is
// eliminated, and the rest play on from the same syllable against the same
// used words; the last one standing wins. Words, checks and scoring are the
// board's, the same as in a game against the bot.
//
// Not safe for concurrent use; the caller serializes.
type Match struct {
	board
	// alive is per seat. An eliminated seat keeps its score, its words and
	// its place in the turn order.
	alive     []bool
	aliveN    int
	turn      int
	scores    []int
	outOrder  []int
	outReason []EndReason

	over      bool
	winner    int
	endReason EndReason
}

// NewMatch opens a game for seats players on opening. Seat 0 moves first,
// with a full turn from now.
func NewMatch(d Dictionary, seats int, opening string, turnLimit, grace time.Duration, now time.Time) (*Match, error) {
	if seats < MinSeats || seats > MaxSeats {
		return nil, fmt.Errorf("engine: a match needs %d to %d players, got %d", MinSeats, MaxSeats, seats)
	}
	b, err := newBoard(d, opening, turnLimit, grace, now)
	if err != nil {
		return nil, err
	}
	m := &Match{
		board:     b,
		alive:     make([]bool, seats),
		aliveN:    seats,
		scores:    make([]int, seats),
		outReason: make([]EndReason, seats),
		winner:    -1,
	}
	for i := range m.alive {
		m.alive[i] = true
	}
	return m, nil
}

// Seats is the number of players the match started with.
func (m *Match) Seats() int { return len(m.alive) }

// Turn is the seat to act: always a seat still in the game, and the winner
// once the match is over.
func (m *Match) Turn() int { return m.turn }

// Alive reports whether seat is still in the game.
func (m *Match) Alive(seat int) bool { return m.valid(seat) && m.alive[seat] }

// Score is seat's points over the words it played.
func (m *Match) Score(seat int) int {
	if !m.valid(seat) {
		return 0
	}
	return m.scores[seat]
}

// OutReason reports why seat was eliminated, EndNone while it is still in.
func (m *Match) OutReason(seat int) EndReason {
	if !m.valid(seat) {
		return EndNone
	}
	return m.outReason[seat]
}

// Over reports whether the match has finished.
func (m *Match) Over() bool { return m.over }

// Winner is the winning seat once the match is over, -1 before.
func (m *Match) Winner() int { return m.winner }

// EndReason is the reason of the elimination that ended the match, or
// EndMaxMoves.
func (m *Match) EndReason() EndReason { return m.endReason }

// Standings lists the seats best first once the match is over: the winner,
// then the eliminated seats, last out first. Outlasting a player beats them,
// whatever the scores.
func (m *Match) Standings() []int {
	if !m.over {
		return nil
	}
	out := make([]int, 0, len(m.alive))
	out = append(out, m.winner)
	// Only a max-moves end leaves other seats standing. They survived, so
	// they rank ahead of every eliminated seat, by points, the earlier seat
	// on a tie, the same order that picked the winner.
	var survivors []int
	for s, alive := range m.alive {
		if alive && s != m.winner {
			survivors = append(survivors, s)
		}
	}
	slices.SortStableFunc(survivors, func(a, b int) int { return m.scores[b] - m.scores[a] })
	out = append(out, survivors...)
	for i := len(m.outOrder) - 1; i >= 0; i-- {
		out = append(out, m.outOrder[i])
	}
	return out
}

// Submit validates a word from seat and, when legal, plays it. Checks run in
// this order: game over, turn, expiry, then the word rules (see board.check).
// An expired turn eliminates the seat and reports ReasonTimeout. A rejected
// word keeps the turn and the clock.
func (m *Match) Submit(seat int, raw string, now time.Time) (Move, Reason) {
	if m.over {
		return Move{}, ReasonGameOver
	}
	if seat != m.turn {
		return Move{}, ReasonNotYourTurn
	}
	if m.Expired(now) {
		m.expire(m.deadline.Add(m.grace))
		return Move{}, ReasonTimeout
	}
	move, last, reason := m.check(raw, now)
	if reason != ReasonNone {
		return Move{}, reason
	}
	move.Seat = seat
	m.play(move, last, now)
	m.scores[seat] += move.Points
	m.advance()
	// A dead end does not end anything here: the next player gets the turn
	// and loses it on the clock (or by giving up), and settle then takes out
	// everyone behind them who faces the same board.
	return move, ReasonNone
}

// Timeout eliminates the seat whose turn expired and reports whether it did.
// The next turn starts when the expired one ended, grace included, not at
// now: a caller that settles late (a poll, a sweep) then replays the clock
// as a timer would have, and one call per expired turn catches up.
func (m *Match) Timeout(now time.Time) bool {
	if m.over || !m.Expired(now) {
		return false
	}
	m.expire(m.deadline.Add(m.grace))
	return true
}

// Resign eliminates seat, which need not be the seat to act: a player may
// leave while somebody else is thinking. On their own turn in a dead end it
// counts as no legal move rather than giving up. The clock restarts only when
// the turn moved on. Reports whether it changed anything.
func (m *Match) Resign(seat int, now time.Time) bool {
	if m.over || !m.Alive(seat) {
		return false
	}
	before := m.turn
	reason := EndGaveUp
	if seat == m.turn && !m.HasLegalMove() {
		reason = EndNoLegalMove
	}
	m.eliminate(seat, reason)
	if !m.over && m.turn != before {
		m.settle()
		if !m.over {
			m.deadline = now.Add(m.turnLimit)
		}
	}
	return true
}

// EndMaxMoves ends the match once the chain reached the cap the caller
// enforces. Every seat still in survived; the one with the most points wins,
// the earlier seat on a tie.
func (m *Match) EndMaxMoves() bool {
	if m.over {
		return false
	}
	best := -1
	for s, alive := range m.alive {
		if alive && (best < 0 || m.scores[s] > m.scores[best]) {
			best = s
		}
	}
	m.over, m.winner, m.endReason = true, best, EndMaxMoves
	m.turn = best
	return true
}

// expire eliminates the seat to act, whose turn ended at end: as no legal
// move when the position was a dead end, so the result does not blame the
// clock, otherwise as a timeout.
func (m *Match) expire(end time.Time) {
	reason := EndTimeout
	if !m.HasLegalMove() {
		reason = EndNoLegalMove
	}
	m.eliminate(m.turn, reason)
	m.settle()
	if !m.over {
		m.deadline = end.Add(m.turnLimit)
	}
}

// settle takes out everyone a dead end leaves with nothing. The first player
// to face it lost it on their own turn; everyone behind them has seen that
// board, so making each sit out a turn they cannot use would only stall a
// decided game. The player who closed the position is left standing.
func (m *Match) settle() {
	for !m.over && !m.HasLegalMove() {
		m.eliminate(m.turn, EndNoLegalMove)
	}
}

// eliminate takes seat out and ends the match when one seat is left.
func (m *Match) eliminate(seat int, reason EndReason) {
	m.alive[seat] = false
	m.aliveN--
	m.outOrder = append(m.outOrder, seat)
	m.outReason[seat] = reason
	m.endReason = reason
	if m.turn == seat {
		m.advance()
	}
	if m.aliveN <= 1 {
		m.over = true
		m.winner = m.turn
	}
}

// advance moves the turn to the next seat still in the game.
func (m *Match) advance() {
	for range m.alive {
		m.turn = (m.turn + 1) % len(m.alive)
		if m.alive[m.turn] {
			return
		}
	}
}

func (m *Match) valid(seat int) bool { return seat >= 0 && seat < len(m.alive) }
