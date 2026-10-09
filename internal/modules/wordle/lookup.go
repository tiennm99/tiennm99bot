package wordle

import "github.com/tiennm99/tiennm99bot/internal/modules/wordle/wordlist"

// normalizeWord lowercases input and strips anything outside a-z.
func normalizeWord(input string) string { return wordlist.Normalize(input) }

// rejectReason classifies why validateGuess returned not-ok. rejectMessage in
// handlers.go maps each reason to its user-facing reply.
type rejectReason = wordlist.Reason

const (
	reasonEmpty   = wordlist.ReasonEmpty
	reasonLength  = wordlist.ReasonLength
	reasonUnknown = wordlist.ReasonUnknown
)

// guessResult is the validateGuess outcome; Word is always the normalized
// input.
type guessResult = wordlist.Result

// validateGuess normalizes input then checks length + dictionary membership.
// Reasons in priority order: empty > length > unknown.
func validateGuess(dict map[string]struct{}, input string) guessResult {
	return wordlist.Validate(dict, input)
}
