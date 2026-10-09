package opponent

import (
	"math/rand/v2"
	"slices"
)

// medium plays greedily one move deep.
//
// It scores each legal move by how many continuations remain from the syllable
// it hands over, takes an immediate win when it sees one, and otherwise picks
// at random from the tightest quartile. Random rather than strictly best, so it
// does not play the same game every time.
//
// What separates it from hard is lookahead, not ruthlessness: medium sees only
// the move in front of it, while hard searches several plies. An earlier
// version withheld the winning move to feel gentler, and simulation showed it
// losing to the random bot 97% of the time — a strategy that declines to win
// loses to a coin flip. Difficulty has to come from how well a bot plays, not
// from it refusing to.
type medium struct {
	rng *rand.Rand
}

type scoredMove struct {
	word      string
	handsOver int // continuations left for the opponent
}

func (s *medium) Choose(b Board) (string, error) {
	moves := b.LegalMoves()
	if len(moves) == 0 {
		return "", ErrNoMove
	}

	scored := scoreMoves(b, moves)
	if len(scored) == 0 {
		return moves[s.rng.IntN(len(moves))], nil
	}

	slices.SortStableFunc(scored, func(a, b scoredMove) int {
		return a.handsOver - b.handsOver
	})

	// A move that leaves the opponent nothing wins immediately. Sorting puts
	// those first, so this is simply taking the best available move.
	if scored[0].handsOver == 0 {
		return scored[0].word, nil
	}

	// Otherwise pick at random from the tightest quartile.
	cut := max(len(scored)/4, 1)
	return scored[s.rng.IntN(cut)].word, nil
}

// scoreMoves annotates each candidate with how many replies it leaves. The
// move itself is counted as spent, since the opponent cannot replay it.
func scoreMoves(b Board, moves []string) []scoredMove {
	scored := make([]scoredMove, 0, len(moves))
	for _, word := range moves {
		last, ok := b.LastSyllable(word)
		if !ok {
			continue
		}
		scored = append(scored, scoredMove{
			word:      word,
			handsOver: remainingOutDegree(b, last, word),
		})
	}
	return scored
}
