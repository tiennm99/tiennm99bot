// Package opponent chooses the bot's moves.
//
// Ported from tiennm99/noitu server/internal/bot. The strategies only read the
// position through Board; the chosen word still goes through the engine's
// validation like a human's. The source's "thinking delay" is dropped: the
// bot answers inside the move request and the page animates the pause.
package opponent

import (
	"errors"
	"iter"
	"math/rand/v2"
)

// Difficulty selects a strategy.
type Difficulty string

// Difficulties, in the wire form the game page sends.
const (
	Easy   Difficulty = "easy"
	Medium Difficulty = "medium"
	Hard   Difficulty = "hard"
)

// ParseDifficulty maps the wire value to a Difficulty. Empty means Medium.
func ParseDifficulty(s string) (Difficulty, bool) {
	switch Difficulty(s) {
	case "", Medium:
		return Medium, true
	case Easy:
		return Easy, true
	case Hard:
		return Hard, true
	}
	return "", false
}

// ErrNoMove means the bot has nothing legal to play, so it has lost.
var ErrNoMove = errors.New("opponent: no legal move")

// Board is what a strategy may look at.
type Board interface {
	LegalMoves() []string
	Used(word string) bool
	WordsStartingWith(syllable string) iter.Seq[string]
	LastSyllable(word string) (string, bool)
}

// Strategy picks a move for the position.
type Strategy interface {
	Choose(b Board) (string, error)
}

// New returns the strategy for a difficulty, drawing randomness from rng.
// The rng is per game, never shared, and not safe for concurrent use.
func New(d Difficulty, rng *rand.Rand) (Strategy, error) {
	if rng == nil {
		return nil, errors.New("opponent: nil rng")
	}
	switch d {
	case Easy:
		return &easy{rng: rng}, nil
	case Medium:
		return &medium{rng: rng}, nil
	case Hard:
		return &hard{rng: rng}, nil
	}
	return nil, errors.New("opponent: unknown difficulty")
}

// remainingOutDegree counts the unplayed continuations from syllable,
// ignoring excluding (the move being considered).
func remainingOutDegree(b Board, syllable, excluding string) int {
	n := 0
	for word := range b.WordsStartingWith(syllable) {
		if word != excluding && !b.Used(word) {
			n++
		}
	}
	return n
}
