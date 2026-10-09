// Package wordlist holds the English 5-letter word data and the pure scoring
// logic shared by the wordle and wordledaily games.
package wordlist

// WordLength is the fixed word length, exposed so renderers and tests reuse it
// without magic numbers.
const WordLength = 5

// Letter result categories stored in LetterScore.Result. The values are part
// of the stored game document's shape, so changing one orphans saved rounds.
const (
	ResultCorrect = "correct"
	ResultPartial = "partial"
	ResultWrong   = "wrong"
)

// LetterScore is one scored letter, stored per guess (nested in GameState).
// bson tags mirror the json names so the document's field names are the same
// in either encoding.
type LetterScore struct {
	Letter string `json:"letter" bson:"letter"`
	Result string `json:"result" bson:"result"`
}

// Compare scores guess against target letter-by-letter. Both are assumed
// lowercase a-z and exactly WordLength long; callers validate via
// Validate before reaching here.
//
// Two-pass marking is required to handle duplicate letters correctly:
//   - pass 1: positional matches → "correct"; consume those slots from the
//     target's available pool.
//   - pass 2: remaining guess letters → "partial" if still in the pool (and
//     consume), else "wrong".
//
// Example: target "abbey", guess "babes" →
//
//	b@0 partial, a@1 partial, b@2 correct, e@3 correct, s@4 wrong.
func Compare(guess, target string) []LetterScore {
	out := make([]LetterScore, WordLength)
	pool := make([]byte, 0, WordLength)

	// Pass 1 — positional matches.
	for i := 0; i < WordLength; i++ {
		if guess[i] == target[i] {
			out[i] = LetterScore{Letter: string(guess[i]), Result: ResultCorrect}
		} else {
			pool = append(pool, target[i])
		}
	}

	// Pass 2 — partial matches against the remaining-pool, with consumption.
	for i := 0; i < WordLength; i++ {
		if out[i].Result == ResultCorrect {
			continue
		}
		idx := indexOfByte(pool, guess[i])
		if idx >= 0 {
			pool = append(pool[:idx], pool[idx+1:]...)
			out[i] = LetterScore{Letter: string(guess[i]), Result: ResultPartial}
		} else {
			out[i] = LetterScore{Letter: string(guess[i]), Result: ResultWrong}
		}
	}
	return out
}

// indexOfByte returns the first index of c in s, or -1.
// (bytes.IndexByte gives the same answer; inlined to keep this file
// dependency-free since the scoring algorithm is the whole point.)
func indexOfByte(s []byte, c byte) int {
	for i, b := range s {
		if b == c {
			return i
		}
	}
	return -1
}
