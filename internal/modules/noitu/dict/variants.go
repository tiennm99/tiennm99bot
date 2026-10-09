package dict

// Spelling variants, ported from tiennm99/noitu
// server/cmd/build-dictionary/aliases.go. The builder there turns them into an
// alias table; here Store.Resolve generates them at lookup time instead, which
// yields the same mappings because both swaps undo themselves.

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Vietnamese combining tone marks. These are the five tones; they are distinct
// from the combining marks that form letters (circumflex, breve, horn), which
// belong to their base letter and must never be moved.
var toneMarks = map[rune]bool{
	'̀': true, // huyền  (grave)
	'́': true, // sắc    (acute)
	'̃': true, // ngã    (tilde)
	'̉': true, // hỏi    (hook above)
	'̣': true, // nặng   (dot below)
}

// Marks that turn a base letter into a different Vietnamese letter (ă â ê ô ơ ư).
// A vowel carrying one of these is not a plain vowel and is excluded from the
// tone-shifting rule below.
var letterMarks = map[rune]bool{
	'̂': true, // circumflex: â ê ô
	'̆': true, // breve:      ă
	'̛': true, // horn:       ơ ư
}

// cluster is one base rune plus the combining marks attached to it.
type cluster struct {
	base rune
	mods []rune
}

func (c cluster) tone() (rune, bool) {
	for _, m := range c.mods {
		if toneMarks[m] {
			return m, true
		}
	}
	return 0, false
}

func (c cluster) hasLetterMark() bool {
	for _, m := range c.mods {
		if letterMarks[m] {
			return true
		}
	}
	return false
}

func (c cluster) without(target rune) cluster {
	out := cluster{base: c.base}
	for _, m := range c.mods {
		if m != target {
			out.mods = append(out.mods, m)
		}
	}
	return out
}

func (c cluster) with(extra rune) cluster {
	out := cluster{base: c.base, mods: append([]rune{}, c.mods...)}
	out.mods = append(out.mods, extra)
	return out
}

// decompose splits a syllable into base-plus-marks clusters, working on the NFD
// form so that every diacritic is visible as its own rune.
func decompose(syllable string) []cluster {
	var clusters []cluster
	for _, r := range norm.NFD.String(syllable) {
		if (toneMarks[r] || letterMarks[r]) && len(clusters) > 0 {
			last := len(clusters) - 1
			clusters[last].mods = append(clusters[last].mods, r)
			continue
		}
		clusters = append(clusters, cluster{base: r})
	}
	return clusters
}

func recompose(clusters []cluster) string {
	var b strings.Builder
	for _, c := range clusters {
		b.WriteRune(c.base)
		for _, m := range c.mods {
			b.WriteRune(m)
		}
	}
	return norm.NFC.String(b.String())
}

// shiftablePairs are the vowel clusters where Vietnamese has two competing tone
// placements in real use: the older style marks the first vowel ("hòa", "thúy"),
// the newer style marks the second ("hoà", "thuý"). Both spellings appear in
// dictionaries and both are typed by players, but they are different codepoint
// sequences, so one of them fails a lookup unless we record it as an alias.
var shiftablePairs = [][2]rune{
	{'o', 'a'},
	{'o', 'e'},
	{'u', 'y'},
}

// toneShiftVariant moves the tone mark to the other vowel of an oa/oe/uy cluster.
// Returns the variant and whether one was produced.
func toneShiftVariant(syllable string) (string, bool) {
	clusters := decompose(syllable)

	for i := 0; i+1 < len(clusters); i++ {
		first, second := clusters[i], clusters[i+1]

		// Only plain vowels take part; ơ/ư/ô/ê/ă follow different rules.
		if first.hasLetterMark() || second.hasLetterMark() {
			continue
		}
		if !isShiftablePair(first.base, second.base) {
			continue
		}
		// The two placements only compete in an OPEN syllable that ends on the
		// cluster. Add anything after it -- a final consonant ("hoàn") or a
		// third vowel ("hoài") -- and only one spelling is correct. Without this
		// check the builder invents words like "hòan" and "hòai".
		if i+1 != len(clusters)-1 {
			continue
		}
		// In "qu" the u is part of the consonant onset, not the vowel nucleus,
		// so "quý" must not become "qúy".
		if first.base == 'u' && i > 0 && clusters[i-1].base == 'q' {
			continue
		}

		if tone, ok := first.tone(); ok {
			shifted := append([]cluster{}, clusters...)
			shifted[i] = first.without(tone)
			shifted[i+1] = second.with(tone)
			return recompose(shifted), true
		}
		if tone, ok := second.tone(); ok {
			shifted := append([]cluster{}, clusters...)
			shifted[i] = first.with(tone)
			shifted[i+1] = second.without(tone)
			return recompose(shifted), true
		}
	}

	return "", false
}

func isShiftablePair(a, b rune) bool {
	for _, p := range shiftablePairs {
		if p[0] == a && p[1] == b {
			return true
		}
	}
	return false
}

// iyOnsets are the consonant onsets after which Vietnamese genuinely writes
// both i and y across the tone range: hi/hy, ki/ky, li/ly, mi/my, ti/ty, qui/quy.
//
// This is an allowlist rather than a general rule because most onsets take only
// one spelling. "gi" is a digraph, so "gì" has no "gỳ"; "chì" has no "chỳ".
//
// "v" and "s" were deliberately removed after inspecting real output: they
// produced non-words at scale ("chức vị" -> "chức vỵ", plus "vỳ", "vý", "sỵ",
// "sỳ"). Their genuine alternations are lexical rather than productive, so the
// few real ones are listed in iyPairs instead.
var iyOnsets = map[string]bool{
	"h": true, "k": true, "l": true, "m": true, "t": true, "qu": true,
}

// iyPairs are individual syllables that alternate even though their onset does
// not do so productively. Listed explicitly, both directions.
var iyPairs = map[string]string{
	"sĩ": "sỹ", "sỹ": "sĩ",
	"vĩ": "vỹ", "vỹ": "vĩ",
}

// iyVariant swaps a lone i/y nucleus: quý/quí, lý/lí, mỹ/mĩ.
//
// The nucleus must be the final cluster, so closed syllables are excluded
// ("tính" has no "týnh"), and the onset must be one that takes both spellings.
func iyVariant(syllable string) (string, bool) {
	if pair, ok := iyPairs[syllable]; ok {
		return pair, true
	}

	clusters := decompose(syllable)
	if len(clusters) < 2 {
		return "", false
	}

	last := len(clusters) - 1
	nucleus := clusters[last]
	if nucleus.hasLetterMark() {
		return "", false
	}

	var swapped rune
	switch nucleus.base {
	case 'i':
		swapped = 'y'
	case 'y':
		swapped = 'i'
	default:
		return "", false
	}

	var onset strings.Builder
	for _, c := range clusters[:last] {
		if c.hasLetterMark() {
			return "", false
		}
		onset.WriteRune(c.base)
	}
	if !iyOnsets[onset.String()] {
		return "", false
	}

	variant := append([]cluster{}, clusters...)
	variant[last] = cluster{base: swapped, mods: nucleus.mods}
	return recompose(variant), true
}

// maxVariantsPerWord caps the combination count for a word whose syllables are
// all variable. Real words reach at most a handful; the cap only guards against
// a pathological entry blowing up the alias table.
const maxVariantsPerWord = 32

// spellingsOf returns every accepted spelling of a single syllable, the
// original first.
func spellingsOf(syllable string) []string {
	out := []string{syllable}
	seen := map[string]bool{syllable: true}

	for _, gen := range []func(string) (string, bool){toneShiftVariant, iyVariant} {
		alt, ok := gen(syllable)
		if !ok || seen[alt] {
			continue
		}
		seen[alt] = true
		out = append(out, alt)
	}

	return out
}

// variantsFor returns every accepted alternative spelling of a whole word.
//
// Syllables must be varied together, not one at a time. "hoá lí" has two
// variable syllables, and a player is far more likely to type the fully modern
// "hóa lý" than either half-and-half form. Varying independently produced only
// the halves and rejected the spelling most people actually use.
func variantsFor(word string) []string {
	syllables := strings.Split(word, " ")

	// Cartesian product over each syllable's accepted spellings.
	combos := [][]string{{}}
	for _, syl := range syllables {
		spellings := spellingsOf(syl)
		next := make([][]string, 0, len(combos)*len(spellings))
		for _, prefix := range combos {
			for _, s := range spellings {
				if len(next) >= maxVariantsPerWord {
					break
				}
				next = append(next, append(append([]string{}, prefix...), s))
			}
		}
		combos = next
	}

	seen := map[string]bool{word: true}
	var out []string
	for _, combo := range combos {
		joined := strings.Join(combo, " ")
		if seen[joined] {
			continue
		}
		seen[joined] = true
		out = append(out, joined)
	}

	return out
}
