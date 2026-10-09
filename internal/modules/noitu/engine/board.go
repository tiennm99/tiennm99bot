package engine

import (
	"errors"
	"fmt"
	"iter"
	"slices"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/dict"
)

// board is the state and the word rules every game shares, whoever plays it:
// the dictionary, the chain so far, the syllable to answer, the used words
// and the turn clock. Engine (the human against the bot) and Match (people
// against each other) embed it and add only their own turn order.
type board struct {
	dict      Dictionary
	opening   string
	used      map[string]struct{}
	current   string
	turnLimit time.Duration
	grace     time.Duration
	deadline  time.Time
	history   []Move
}

// newBoard opens a chain on opening, with the first turn starting now.
//
// grace is extra time after the deadline during which a submission still
// counts, to absorb network latency. It never earns speed points.
func newBoard(d Dictionary, opening string, turnLimit, grace time.Duration, now time.Time) (board, error) {
	if d == nil {
		return board{}, errors.New("engine: nil dictionary")
	}
	if turnLimit <= 0 || grace < 0 {
		return board{}, fmt.Errorf("engine: invalid turn limit %v / grace %v", turnLimit, grace)
	}
	canonical, ok := d.Resolve(opening)
	if !ok {
		return board{}, fmt.Errorf("engine: opening word %q is not in the dictionary", opening)
	}
	last, _ := d.LastSyllable(canonical)
	b := board{
		dict:      d,
		opening:   canonical,
		used:      map[string]struct{}{canonical: {}},
		current:   last,
		turnLimit: turnLimit,
		grace:     grace,
		deadline:  now.Add(turnLimit),
	}
	if !b.HasLegalMove() {
		return board{}, fmt.Errorf("engine: opening word %q ends on %q, which starts no other word", canonical, last)
	}
	return b, nil
}

// check validates a word against the chain without playing it. On success it
// returns the move (By and Seat left for the caller) and the word's last
// syllable.
//
// Checks run in this order: syllable count, dictionary, link, reuse.
// Resolving before the link check matters: the canonical spelling can change
// the first syllable ("sỹ hai" → "sĩ hai").
func (b *board) check(raw string, now time.Time) (Move, string, Reason) {
	normalized, syllables, err := dict.Normalize(raw)
	if err != nil || !dict.HasEnoughSyllables(syllables) {
		return Move{}, "", ReasonTooFewSyllables
	}
	canonical, ok := b.dict.Resolve(normalized)
	if !ok {
		return Move{}, "", ReasonNotInDictionary
	}
	first, _ := b.dict.FirstSyllable(canonical)
	if first != b.current {
		return Move{}, "", ReasonWrongLink
	}
	if _, played := b.used[canonical]; played {
		return Move{}, "", ReasonAlreadyUsed
	}
	last, _ := b.dict.LastSyllable(canonical)
	// Count the canonical word's syllables: a variant spelling never changes
	// the count, but the canonical form is what was played.
	n := len(syllablesOf(canonical))
	return Move{Word: canonical, Syllables: n, Points: b.pointsFor(n, first, now), At: now}, last, ReasonNone
}

// play records a move check accepted and hands the next player a full turn.
func (b *board) play(m Move, last string, now time.Time) {
	b.used[m.Word] = struct{}{}
	b.history = append(b.history, m)
	b.current = last
	b.deadline = now.Add(b.turnLimit)
}

func syllablesOf(word string) []string {
	_, s, _ := dict.Normalize(word)
	return s
}

// Opening is the word the chain started from.
func (b *board) Opening() string { return b.opening }

// Current is the syllable the next word must start with.
func (b *board) Current() string { return b.current }

// Deadline is when the current turn ends, before the grace period.
func (b *board) Deadline() time.Time { return b.deadline }

// TurnLimit is the length of one turn.
func (b *board) TurnLimit() time.Duration { return b.turnLimit }

// History returns the played moves after the opening word, oldest first.
func (b *board) History() []Move { return slices.Clone(b.history) }

// ChainLength counts the words played, opening word included.
func (b *board) ChainLength() int { return len(b.history) + 1 }

// Expired reports whether the current turn ran out, grace period included.
func (b *board) Expired(now time.Time) bool {
	return now.After(b.deadline.Add(b.grace))
}

// Used reports whether a canonical word has been played.
func (b *board) Used(word string) bool {
	_, ok := b.used[word]
	return ok
}

// WordsStartingWith iterates the dictionary words starting with syllable,
// played ones included.
func (b *board) WordsStartingWith(syllable string) iter.Seq[string] {
	return b.dict.WordsStartingWith(syllable)
}

// LastSyllable reports the last syllable of a canonical word.
func (b *board) LastSyllable(word string) (string, bool) {
	return b.dict.LastSyllable(word)
}

// LegalMoves lists every unused word that answers the current syllable.
func (b *board) LegalMoves() []string {
	var out []string
	for w := range b.dict.WordsStartingWith(b.current) {
		if !b.Used(w) {
			out = append(out, w)
		}
	}
	return out
}

// HasLegalMove reports whether the player to act has anything to play.
func (b *board) HasLegalMove() bool {
	for w := range b.dict.WordsStartingWith(b.current) {
		if !b.Used(w) {
			return true
		}
	}
	return false
}

// Suggestions lists up to n words the player to act could still play, sorted.
// Shown to a player who lost; empty means the position was a dead end.
func (b *board) Suggestions(n int) []string {
	if n <= 0 {
		return nil
	}
	moves := b.LegalMoves()
	slices.Sort(moves)
	return moves[:min(n, len(moves))]
}
