package wordlist

import "strings"

// Marker maps a LetterScore.Result to the NYT-Wordle share emoji.
func Marker(result string) string {
	switch result {
	case ResultCorrect:
		return "🟩"
	case ResultPartial:
		return "🟨"
	default:
		return "⬜"
	}
}

// EmojiRow renders one scored guess as its spoiler-free emoji row.
func EmojiRow(scores []LetterScore) string {
	var b strings.Builder
	for _, s := range scores {
		b.WriteString(Marker(s.Result))
	}
	return b.String()
}
