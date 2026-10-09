package wordlist

import "strings"

// Normalize lowercases input and strips anything outside a-z.
func Normalize(input string) string {
	lower := strings.ToLower(input)
	out := make([]byte, 0, len(lower))
	for i := 0; i < len(lower); i++ {
		c := lower[i]
		if c >= 'a' && c <= 'z' {
			out = append(out, c)
		}
	}
	return string(out)
}

// Reason classifies why Validate returned not-ok. Each game maps it to its
// own user-facing reply.
type Reason string

// Validation failure reasons.
const (
	ReasonEmpty   Reason = "empty"
	ReasonLength  Reason = "length"
	ReasonUnknown Reason = "unknown"
)

// Result is the Validate outcome. Word is always populated with the
// normalized form, even on failure, so callers can include it in error
// messages without re-normalizing.
type Result struct {
	OK     bool
	Word   string
	Reason Reason
}

// Validate normalizes input then checks length + dictionary membership.
//
// Reasons in priority order: empty (post-normalize blank) > length > unknown.
func Validate(dict map[string]struct{}, input string) Result {
	w := Normalize(input)
	if w == "" {
		return Result{OK: false, Word: w, Reason: ReasonEmpty}
	}
	if len(w) != WordLength {
		return Result{OK: false, Word: w, Reason: ReasonLength}
	}
	if _, ok := dict[w]; !ok {
		return Result{OK: false, Word: w, Reason: ReasonUnknown}
	}
	return Result{OK: true, Word: w}
}
