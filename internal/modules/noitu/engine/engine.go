// Package engine holds the rules of one nối từ game, either a human against
// the bot or people against each other: what counts as a legal move, whose
// turn it is, when the turn expires and how a word is scored.
//
// It is ported from tiennm99/noitu server/internal/game/engine.go: Engine
// is the two-player cut against the bot, Match the game between people.
// The engine never reads the clock: every call that depends on time takes a
// now argument, so the rules are testable without a timer.
package engine

import (
	"iter"
	"time"
)

// Side is one of the two players.
type Side int

const (
	// Human is the Telegram player.
	Human Side = iota + 1
	// Bot is the computer opponent. It plays the opening word.
	Bot
)

func (s Side) other() Side {
	if s == Human {
		return Bot
	}
	return Human
}

// Reason says why a submission was rejected, or why it did not count.
type Reason string

// Reject reasons. ReasonNone means the word was accepted.
const (
	ReasonNone            Reason = ""
	ReasonGameOver        Reason = "game_over"
	ReasonNotYourTurn     Reason = "not_your_turn"
	ReasonTimeout         Reason = "timeout"
	ReasonTooFewSyllables Reason = "too_few_syllables"
	ReasonNotInDictionary Reason = "not_in_dictionary"
	ReasonWrongLink       Reason = "wrong_link"
	ReasonAlreadyUsed     Reason = "already_used"
)

// EndReason says how a finished game ended.
type EndReason string

// End reasons. EndNone means the game is still in play.
const (
	EndNone        EndReason = ""
	EndTimeout     EndReason = "timeout"       // the player to act ran out of time
	EndNoLegalMove EndReason = "no_legal_move" // the player to act was left a dead end
	EndGaveUp      EndReason = "gave_up"       // the player resigned
	EndBotStuck    EndReason = "bot_stuck"     // the bot had no legal move
	EndMaxMoves    EndReason = "max_moves"     // the chain reached the per-game cap
)

// Dictionary is the slice of the word store the engine needs.
type Dictionary interface {
	Resolve(word string) (string, bool)
	FirstSyllable(word string) (string, bool)
	LastSyllable(word string) (string, bool)
	WordsStartingWith(syllable string) iter.Seq[string]
	OutDegree(syllable string) int
}

// Move is one played word. Word is the canonical spelling, which may differ
// from what the player typed. By is set in an Engine game, Seat in a Match.
type Move struct {
	By        Side
	Seat      int
	Word      string
	Syllables int
	Points    int
	At        time.Time
}

// Engine is one game of the human against the bot. Not safe for concurrent
// use; the caller serializes.
type Engine struct {
	board
	turn  Side
	score int

	over      bool
	winner    Side
	endReason EndReason
}

// New starts a game from an opening word played by the bot. The human moves
// first, with a full turn from now.
//
// grace is extra time after the deadline during which a submission still
// counts, to absorb network latency. It never earns speed points.
func New(d Dictionary, opening string, turnLimit, grace time.Duration, now time.Time) (*Engine, error) {
	b, err := newBoard(d, opening, turnLimit, grace, now)
	if err != nil {
		return nil, err
	}
	return &Engine{board: b, turn: Human}, nil
}

// Turn reports whose move it is.
func (e *Engine) Turn() Side { return e.turn }

// Over reports whether the game has finished.
func (e *Engine) Over() bool { return e.over }

// Winner is the winning side once the game is over.
func (e *Engine) Winner() Side { return e.winner }

// EndReason reports how the game ended.
func (e *Engine) EndReason() EndReason { return e.endReason }

// Score is the human's total points.
func (e *Engine) Score() int { return e.score }

// Submit validates a word from side and, when legal, plays it.
//
// Checks run in this order: game over, turn, expiry, then the word rules
// (see board.check). An expired turn ends the game against the human (the
// bot never waits).
func (e *Engine) Submit(side Side, raw string, now time.Time) (Move, Reason) {
	if e.over {
		return Move{}, ReasonGameOver
	}
	if side != e.turn {
		return Move{}, ReasonNotYourTurn
	}
	if side == Human && e.Expired(now) {
		e.expire()
		return Move{}, ReasonTimeout
	}
	move, last, reason := e.check(raw, now)
	if reason != ReasonNone {
		return Move{}, reason
	}
	move.By = side
	e.play(move, last, now)
	if side == Human {
		e.score += move.Points
	}
	e.turn = side.other()
	// A dead end does not end the game here. The human keeps the turn and
	// loses it on the clock or claims it with NoMove; the caller settles the
	// bot's with BotStuck.
	return move, ReasonNone
}

// Timeout ends the game against the human when their turn has expired.
// Reports whether it did.
func (e *Engine) Timeout(now time.Time) bool {
	if e.over || e.turn != Human || !e.Expired(now) {
		return false
	}
	e.expire()
	return true
}

// expire ends the human's expired turn: as no legal move when the position
// was a dead end, so the result does not blame the clock, otherwise as a
// timeout.
func (e *Engine) expire() {
	if e.HasLegalMove() {
		e.end(Bot, EndTimeout)
		return
	}
	e.end(Bot, EndNoLegalMove)
}

// NoMove ends the game at once when it is the human's turn and the position
// leaves nothing to play, so a dead end need not wait out the clock. It is
// not a resignation: a playable position is left alone. Reports whether it
// ended the game.
func (e *Engine) NoMove() bool {
	if e.over || e.turn != Human || e.HasLegalMove() {
		return false
	}
	e.end(Bot, EndNoLegalMove)
	return true
}

// GiveUp ends the game as the human's resignation.
func (e *Engine) GiveUp() bool {
	if e.over {
		return false
	}
	e.end(Bot, EndGaveUp)
	return true
}

// BotStuck ends the game as a human win when it is the bot's turn and it has
// nothing to play. Reports whether it did.
func (e *Engine) BotStuck() bool {
	if e.over || e.turn != Bot || e.HasLegalMove() {
		return false
	}
	e.end(Human, EndBotStuck)
	return true
}

// EndMaxMoves ends the game as a human win once the chain reached the cap the
// caller enforces. Surviving that long beats the bot.
func (e *Engine) EndMaxMoves() bool {
	if e.over {
		return false
	}
	e.end(Human, EndMaxMoves)
	return true
}

func (e *Engine) end(winner Side, reason EndReason) {
	e.over = true
	e.winner = winner
	e.endReason = reason
}
