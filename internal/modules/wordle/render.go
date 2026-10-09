package wordle

import (
	"strings"

	"github.com/tiennm99/tiennm99bot/internal/modules/wordle/wordlist"
)

// renderGuess formats one guess as the NYT share-pattern: word on one line,
// emoji marker row below.
//
//	CRANE
//	🟩🟨⬜🟩🟩
func renderGuess(word string, results []LetterScore) string {
	return strings.ToUpper(word) + "\n" + wordlist.EmojiRow(results)
}

// renderBoard joins all prior guesses, blank-line separated. Used when a
// player asks for `/wordle` mid-round.
func renderBoard(guesses []GuessRecord) string {
	if len(guesses) == 0 {
		return "No guesses yet. Reply with `/wordle <word>`."
	}
	rows := make([]string, len(guesses))
	for i, g := range guesses {
		rows[i] = renderGuess(g.Word, g.Results)
	}
	return strings.Join(rows, "\n\n")
}
