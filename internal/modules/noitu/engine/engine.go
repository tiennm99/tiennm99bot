// Package engine holds the rules of one nối từ game between a human and the
// bot: what counts as a legal move, whose turn it is, when the turn expires
// and how a word is scored.
//
// It is a two-player cut of tiennm99/noitu server/internal/game/engine.go.
// The engine never reads the clock: every call that depends on time takes a
// now argument, so the rules are testable without a timer.
package engine

import (
	"errors"
	"fmt"
	"iter"
	"slices"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/dict"
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
	EndTimeout     EndReason = "timeout"       // the human ran out of time
	EndNoLegalMove EndReason = "no_legal_move" // the human was left a dead end
	EndGaveUp      EndReason = "gave_up"       // the human resigned
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
// from what the player typed.
type Move struct {
	By        Side
	Word      string
	Syllables int
	Points    int
	At        time.Time
}

// Engine is one game. Not safe for concurrent use; the caller serializes.
type Engine struct {
	dict      Dictionary
	opening   string
	used      map[string]struct{}
	current   string
	turn      Side
	turnLimit time.Duration
	grace     time.Duration
	deadline  time.Time
	history   []Move
	score     int

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
	if d == nil {
		return nil, errors.New("engine: nil dictionary")
	}
	if turnLimit <= 0 || grace < 0 {
		return nil, fmt.Errorf("engine: invalid turn limit %v / grace %v", turnLimit, grace)
	}
	canonical, ok := d.Resolve(opening)
	if !ok {
		return nil, fmt.Errorf("engine: opening word %q is not in the dictionary", opening)
	}
	last, _ := d.LastSyllable(canonical)
	e := &Engine{
		dict:      d,
		opening:   canonical,
		used:      map[string]struct{}{canonical: {}},
		current:   last,
		turn:      Human,
		turnLimit: turnLimit,
		grace:     grace,
		deadline:  now.Add(turnLimit),
	}
	if !e.HasLegalMove() {
		return nil, fmt.Errorf("engine: opening word %q ends on %q, which starts no other word", canonical, last)
	}
	return e, nil
}

// Opening is the bot's opening word.
func (e *Engine) Opening() string { return e.opening }

// Current is the syllable the next word must start with.
func (e *Engine) Current() string { return e.current }

// Turn reports whose move it is.
func (e *Engine) Turn() Side { return e.turn }

// Deadline is when the current turn ends, before the grace period.
func (e *Engine) Deadline() time.Time { return e.deadline }

// TurnLimit is the length of one turn.
func (e *Engine) TurnLimit() time.Duration { return e.turnLimit }

// Over reports whether the game has finished.
func (e *Engine) Over() bool { return e.over }

// Winner is the winning side once the game is over.
func (e *Engine) Winner() Side { return e.winner }

// EndReason reports how the game ended.
func (e *Engine) EndReason() EndReason { return e.endReason }

// Score is the human's total points.
func (e *Engine) Score() int { return e.score }

// History returns the played moves after the opening word, oldest first.
func (e *Engine) History() []Move { return slices.Clone(e.history) }

// ChainLength counts the words played, opening word included.
func (e *Engine) ChainLength() int { return len(e.history) + 1 }

// Expired reports whether the current turn ran out, grace period included.
func (e *Engine) Expired(now time.Time) bool {
	return now.After(e.deadline.Add(e.grace))
}

// Submit validates a word from side and, when legal, plays it.
//
// Checks run in this order: game over, turn, expiry, syllable count,
// dictionary, link, reuse. Resolving before the link check matters: the
// canonical spelling can change the first syllable ("sỹ hai" → "sĩ hai").
// An expired turn ends the game against the human (the bot never waits).
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
	normalized, syllables, err := dict.Normalize(raw)
	if err != nil || !dict.HasEnoughSyllables(syllables) {
		return Move{}, ReasonTooFewSyllables
	}
	canonical, ok := e.dict.Resolve(normalized)
	if !ok {
		return Move{}, ReasonNotInDictionary
	}
	first, _ := e.dict.FirstSyllable(canonical)
	if first != e.current {
		return Move{}, ReasonWrongLink
	}
	if _, played := e.used[canonical]; played {
		return Move{}, ReasonAlreadyUsed
	}
	last, _ := e.dict.LastSyllable(canonical)
	// Count the canonical word's syllables: a variant spelling never changes
	// the count, but the canonical form is what was played.
	n := len(syllablesOf(canonical))
	move := Move{By: side, Word: canonical, Syllables: n, Points: e.pointsFor(n, first, now), At: now}

	e.used[canonical] = struct{}{}
	e.history = append(e.history, move)
	if side == Human {
		e.score += move.Points
	}
	e.current = last
	e.turn = side.other()
	e.deadline = now.Add(e.turnLimit)
	// A dead end does not end the game here. The human keeps the turn and
	// loses it on the clock or claims it with NoMove; the caller settles the
	// bot's with BotStuck.
	return move, ReasonNone
}

func syllablesOf(word string) []string {
	_, s, _ := dict.Normalize(word)
	return s
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

// Used reports whether a canonical word has been played.
func (e *Engine) Used(word string) bool {
	_, ok := e.used[word]
	return ok
}

// WordsStartingWith iterates the dictionary words starting with syllable,
// played ones included.
func (e *Engine) WordsStartingWith(syllable string) iter.Seq[string] {
	return e.dict.WordsStartingWith(syllable)
}

// LastSyllable reports the last syllable of a canonical word.
func (e *Engine) LastSyllable(word string) (string, bool) {
	return e.dict.LastSyllable(word)
}

// LegalMoves lists every unused word that answers the current syllable.
func (e *Engine) LegalMoves() []string {
	var out []string
	for w := range e.dict.WordsStartingWith(e.current) {
		if !e.Used(w) {
			out = append(out, w)
		}
	}
	return out
}

// HasLegalMove reports whether the side to act has anything to play.
func (e *Engine) HasLegalMove() bool {
	for w := range e.dict.WordsStartingWith(e.current) {
		if !e.Used(w) {
			return true
		}
	}
	return false
}

// Suggestions lists up to n words the side to act could still play, sorted.
// Shown to a player who lost; empty means the position was a dead end.
func (e *Engine) Suggestions(n int) []string {
	if n <= 0 {
		return nil
	}
	moves := e.LegalMoves()
	slices.Sort(moves)
	return moves[:min(n, len(moves))]
}
