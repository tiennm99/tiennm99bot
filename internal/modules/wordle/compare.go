// Package wordle implements the classic 5-letter word-guess game, scored
// letter-by-letter green/yellow/grey. The word data and scoring live in the
// wordlist subpackage, shared with wordledaily.
package wordle

import "github.com/tiennm99/tiennm99bot/internal/modules/wordle/wordlist"

// WordLength is wordle's fixed 5.
const WordLength = wordlist.WordLength

// Letter result categories stored in LetterScore.Result. The values are part
// of the stored game document's shape, so changing one orphans saved rounds.
const (
	ResultCorrect = wordlist.ResultCorrect
	ResultPartial = wordlist.ResultPartial
	ResultWrong   = wordlist.ResultWrong
)

// LetterScore is one scored letter, stored per guess (nested in GameState).
type LetterScore = wordlist.LetterScore

// CompareWords scores guess against the answer letter-by-letter, handling
// duplicate letters the NYT way. See wordlist.Compare.
func CompareWords(guess, answer string) []LetterScore { return wordlist.Compare(guess, answer) }
