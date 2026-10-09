// Package dict is the nối từ word list: the embedded Wiktionary-derived
// corpus, the normalization player input goes through, the spelling variants
// a lookup accepts, and the word graph the engine and the bot opponent walk.
//
// Ported from tiennm99/noitu: Normalize from server/internal/vietnamese,
// the variant generator from server/cmd/build-dictionary/aliases.go, and the
// store from server/internal/dictionary (minus SQLite — the corpus is parsed
// into maps once, in memory).
package dict

import (
	"errors"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// MinSyllables is the shortest word the game accepts. Nối từ is played with
// compound words; a single syllable is not a valid move.
const MinSyllables = 2

// ErrEmpty is returned when input contains no usable characters.
var ErrEmpty = errors.New("dict: empty input")

// Normalize canonicalizes raw player or corpus input and splits it into
// syllables: NFC composition (so a base letter plus combining marks equals the
// precomposed form), lowercase, and whitespace collapsed to single spaces.
// Tone marks are kept exactly as typed; alternative tone placements are
// handled by Store.Resolve, not here.
func Normalize(raw string) (string, []string, error) {
	composed := norm.NFC.String(raw)
	// Composed again after lowering: case-folding can enable a composition that
	// only exists for the lowercase form, so the lowered string may no longer
	// be NFC.
	lowered := norm.NFC.String(strings.ToLower(composed))
	syllables := strings.Fields(lowered)
	if len(syllables) == 0 {
		return "", nil, ErrEmpty
	}
	return strings.Join(syllables, " "), syllables, nil
}

// HasEnoughSyllables reports whether a word is long enough to be a legal move.
func HasEnoughSyllables(syllables []string) bool {
	return len(syllables) >= MinSyllables
}
