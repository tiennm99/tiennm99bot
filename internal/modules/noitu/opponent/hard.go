package opponent

import (
	"math"
	"math/rand/v2"
	"slices"
)

// Tuning. These are constants rather than structure precisely so the bot can be
// made easier or harder without touching the search.
const (
	// hardKillRate is how often Hard takes an available instant win. A bot that
	// always wins is not a game, so it sometimes plays on instead.
	hardKillRate = 0.85
	// searchDepth is plies of lookahead. Directed Edge Geography is
	// PSPACE-complete, so this is a heuristic cutoff, not a solver.
	searchDepth = 4
	// nodeCap bounds the search on hub syllables with a large branching
	// factor, keeping Choose inside its latency budget.
	nodeCap = 20000
	// branchCap limits how many candidates are explored per node. Moves are
	// ordered tightest-first, so the pruned tail is the least interesting.
	branchCap = 12

	// A win is the negation of a child's loss, so only the losing terminal
	// needs a constant. A loss found with more depth still to search is
	// scored lower than one found later, so a lost position prefers the
	// slower loss and a won one the faster win.
	loseScore = -1000.0
)

// hard looks ahead.
//
// First it checks for an immediate kill: a move that leaves the opponent with
// nothing. Failing that it runs a depth-limited negamax over the remaining
// edges, valuing a position by how few replies the opponent has. If the search
// finds nothing useful it falls back to Medium's heuristic.
type hard struct {
	rng *rand.Rand
	// lastNodes is how many search nodes the latest Choose visited, so a test
	// can pin the node cap that bounds a move request's CPU time.
	lastNodes int
}

func (s *hard) Choose(b Board) (string, error) {
	s.lastNodes = 0
	moves := b.LegalMoves()
	if len(moves) == 0 {
		return "", ErrNoMove
	}

	scored := scoreMoves(b, moves)
	if len(scored) == 0 {
		return moves[s.rng.IntN(len(moves))], nil
	}

	// An instant win: the opponent has no reply at all.
	var kills []string
	for _, m := range scored {
		if m.handsOver == 0 {
			kills = append(kills, m.word)
		}
	}
	if len(kills) > 0 {
		if s.rng.Float64() < hardKillRate {
			return kills[s.rng.IntN(len(kills))], nil
		}
		// Declined. The killing moves must be taken off the table, not just
		// skipped here: the search below would otherwise rediscover the same
		// win and play it anyway, making hardKillRate do nothing at all.
		var spared []scoredMove
		for _, m := range scored {
			if m.handsOver > 0 {
				spared = append(spared, m)
			}
		}
		if len(spared) > 0 {
			scored = spared
		}
	}

	// Order tightest-first so alpha-beta prunes early.
	slices.SortStableFunc(scored, func(a, b scoredMove) int { return a.handsOver - b.handsOver })

	st := &search{board: b, used: map[string]bool{}, budget: nodeCap}

	best := ""
	alpha := math.Inf(-1)
	for i, m := range scored {
		if i >= branchCap || st.budget <= 0 {
			// Out of budget: keep the best move found so far rather than
			// scoring the rest against an exhausted search.
			break
		}
		last, ok := b.LastSyllable(m.word)
		if !ok {
			continue
		}

		st.used[m.word] = true
		// Negamax: the opponent's best outcome, negated, is ours. Alpha carries
		// across children so later subtrees can be pruned.
		score := -st.negamax(last, searchDepth-1, math.Inf(-1), -alpha)
		delete(st.used, m.word)

		if score > alpha {
			alpha, best = score, m.word
		}
	}

	s.lastNodes = nodeCap - st.budget

	if best == "" {
		// Nothing scored; fall back to the tightest-first heuristic, which is
		// Medium's judgement.
		return scored[0].word, nil
	}
	return best, nil
}

// search carries the mutable state of one lookahead. Moves are made and unmade
// on `used` rather than copying the position at every node.
type search struct {
	board  Board
	used   map[string]bool
	budget int
}

// spent reports whether a word is unavailable: already played in the real game,
// or played earlier in this search line.
func (s *search) spent(word string) bool {
	return s.used[word] || s.board.Used(word)
}

// negamax scores the position for the player to move at `current`.
//
// Returns a value from that player's point of view, so the caller negates it.
func (s *search) negamax(current string, depth int, alpha, beta float64) float64 {
	if s.budget <= 0 {
		// Fail soft: return the bound already established rather than a neutral
		// 0, which reads as "this line is fine" and can push the root toward a
		// losing move when the budget bites.
		return alpha
	}
	s.budget--

	var candidates []string
	for word := range s.board.WordsStartingWith(current) {
		if !s.spent(word) {
			candidates = append(candidates, word)
		}
	}

	// No reply: the player to move has lost. The remaining depth breaks the
	// tie between losses, so when every line is lost the search still avoids
	// the ones that end at once — a human may not find the slower refutation.
	if len(candidates) == 0 {
		return loseScore - float64(depth)
	}
	if depth <= 0 {
		// Negamax evaluates from the perspective of the player to move, so
		// mobility is a positive: more replies available is better for them.
		// The caller negates this, which is what turns it into "leave the
		// opponent with as little as possible". Getting this sign backwards
		// makes the search prefer positions where it is about to be trapped —
		// measured at a 29% win rate against the one-ply bot.
		//
		// Log rather than the raw count so one hub syllable cannot dominate.
		return math.Log(1 + float64(len(candidates)))
	}

	// Order tightest-first: explore the moves most likely to be strong.
	type cand struct {
		word      string
		last      string
		handsOver int
	}
	ordered := make([]cand, 0, len(candidates))
	for _, w := range candidates {
		last, ok := s.board.LastSyllable(w)
		if !ok {
			continue
		}
		n := 0
		for reply := range s.board.WordsStartingWith(last) {
			if reply != w && !s.spent(reply) {
				n++
			}
		}
		ordered = append(ordered, cand{word: w, last: last, handsOver: n})
	}
	slices.SortStableFunc(ordered, func(a, b cand) int { return a.handsOver - b.handsOver })

	// Starts at the losing score: if every candidate is pruned or unresolvable,
	// this position is no better than lost, which is the honest floor.
	best := loseScore
	for i, c := range ordered {
		if i >= branchCap {
			break
		}

		s.used[c.word] = true
		score := -s.negamax(c.last, depth-1, -beta, -alpha)
		delete(s.used, c.word)

		if score > best {
			best = score
		}
		if best > alpha {
			alpha = best
		}
		if alpha >= beta {
			break // the opponent would avoid this line
		}
	}

	return best
}
