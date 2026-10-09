package wordlist

import (
	_ "embed"
	"strings"
)

// rawWords is the allowed-guess dictionary: one lowercase WordLength a-z word
// per line. /wordle also draws its random answers from it.
//
//go:embed data/words.txt
var rawWords string

// rawAnswers is the curated list of common words a daily puzzle can be. Every
// entry is also in words.txt; a test enforces that.
//
//go:embed data/answers.txt
var rawAnswers string

// Load parses the embedded dictionary into a slice plus a membership set. Both
// outputs share the same backing strings, so the word bytes (≈90 KiB) are
// held once rather than duplicated.
//
// Words are validated to be exactly WordLength a-z; any malformed line panics
// at startup so a bad regen of the data file is caught immediately, not on
// the first guess.
func Load() ([]string, map[string]struct{}) {
	words := parse(rawWords, "dictionary")
	set := make(map[string]struct{}, len(words))
	for _, w := range words {
		set[w] = struct{}{}
	}
	return words, set
}

// Answers returns the curated daily-answer list in file order (sorted). It
// panics on a malformed line, like Load.
func Answers() []string { return parse(rawAnswers, "answer list") }

func parse(raw, what string) []string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	words := make([]string, 0, len(lines))
	for _, w := range lines {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if !validWord(w) {
			panic("wordlist: invalid word in embedded " + what + ": " + w)
		}
		words = append(words, w)
	}
	return words
}

func validWord(w string) bool {
	if len(w) != WordLength {
		return false
	}
	for i := 0; i < len(w); i++ {
		c := w[i]
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}
