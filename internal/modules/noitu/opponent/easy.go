package opponent

import "math/rand/v2"

// easy plays a uniformly random legal move, without looking at what it hands
// the opponent.
type easy struct {
	rng *rand.Rand
}

func (s *easy) Choose(b Board) (string, error) {
	moves := b.LegalMoves()
	if len(moves) == 0 {
		return "", ErrNoMove
	}
	return moves[s.rng.IntN(len(moves))], nil
}
