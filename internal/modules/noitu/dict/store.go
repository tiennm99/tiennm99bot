package dict

import (
	"fmt"
	"iter"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
)

// maxSenses caps the meanings shown per word. The corpus builder already
// enforces it; repeating the cap here keeps a hand-edited corpus from sending
// an unbounded list to the page.
const maxSenses = 5

// Sense is one meaning of a word: an optional part of speech and a gloss.
// Both are plain text and must be rendered as text, never as markup.
type Sense struct {
	POS   string `json:"pos"`
	Gloss string `json:"gloss"`
}

// entry is one headword. senses holds the raw "pos|gloss" cells, still
// tab-separated, so meanings are only parsed for words a game actually shows.
type entry struct {
	first, last string
	syllables   int
	senses      string
}

// Store is the read-only word graph. Safe for concurrent use once built.
type Store struct {
	words   map[string]entry
	byFirst map[string][]string // first syllable → words, in corpus (sorted) order
	// openers lists every word sorted by how many words start with its last
	// syllable, most first, so RandomOpening can cut the list at a threshold.
	openers   []string
	openerOut []int // parallel to openers: that out-degree
	// SourceSHA256 is the corpus header's #@source_sha256 value, for logs.
	SourceSHA256 string
}

// Parse builds a Store from corpus text: '#' header lines, then one line per
// word, `word<TAB>pos|gloss<TAB>…`. Every headword must already be in
// normalized form with at least MinSyllables syllables; anything else is an
// error, so a bad corpus fails at load rather than as an unplayable word.
func Parse(raw string) (*Store, error) {
	s := &Store{words: map[string]entry{}, byFirst: map[string][]string{}}
	for line := range strings.Lines(raw) {
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if v, ok := strings.CutPrefix(line, "#@source_sha256 "); ok {
				s.SourceSHA256 = strings.TrimSpace(v)
			}
			continue
		}
		word, senses, _ := strings.Cut(line, "\t")
		normalized, syllables, err := Normalize(word)
		if err != nil || normalized != word || !HasEnoughSyllables(syllables) {
			return nil, fmt.Errorf("dict: corpus headword %q is not a normalized word of %d+ syllables", word, MinSyllables)
		}
		if _, dup := s.words[word]; dup {
			return nil, fmt.Errorf("dict: duplicate corpus headword %q", word)
		}
		e := entry{first: syllables[0], last: syllables[len(syllables)-1], syllables: len(syllables), senses: senses}
		s.words[word] = e
		s.byFirst[e.first] = append(s.byFirst[e.first], word)
	}
	if len(s.words) == 0 {
		return nil, fmt.Errorf("dict: corpus has no words")
	}
	s.buildOpeners()
	return s, nil
}

func (s *Store) buildOpeners() {
	s.openers = make([]string, 0, len(s.words))
	for w := range s.words {
		s.openers = append(s.openers, w)
	}
	// Sorting by word first makes the order independent of map iteration, so
	// a seeded RandomOpening is reproducible.
	slices.Sort(s.openers)
	sort.SliceStable(s.openers, func(i, j int) bool {
		return s.OutDegree(s.words[s.openers[i]].last) > s.OutDegree(s.words[s.openers[j]].last)
	})
	s.openerOut = make([]int, len(s.openers))
	for i, w := range s.openers {
		s.openerOut[i] = s.OutDegree(s.words[w].last)
	}
}

// Len reports how many words the store holds.
func (s *Store) Len() int { return len(s.words) }

// Contains reports whether word is a canonical headword.
func (s *Store) Contains(word string) bool {
	_, ok := s.words[word]
	return ok
}

// Resolve maps a normalized word to its canonical headword. An exact match
// wins; otherwise the word's spelling variants (tone placement in oa/oe/uy,
// i/y after certain onsets) are tried, and the lookup succeeds only when
// exactly one of them is a word, so an ambiguous spelling is never guessed.
func (s *Store) Resolve(word string) (string, bool) {
	if _, ok := s.words[word]; ok {
		return word, true
	}
	match := ""
	for _, v := range variantsFor(word) {
		if _, ok := s.words[v]; !ok {
			continue
		}
		if match != "" {
			return "", false
		}
		match = v
	}
	return match, match != ""
}

// FirstSyllable reports the first syllable of a canonical word.
func (s *Store) FirstSyllable(word string) (string, bool) {
	e, ok := s.words[word]
	return e.first, ok
}

// LastSyllable reports the last syllable of a canonical word.
func (s *Store) LastSyllable(word string) (string, bool) {
	e, ok := s.words[word]
	return e.last, ok
}

// Syllables reports how many syllables a canonical word has.
func (s *Store) Syllables(word string) (int, bool) {
	e, ok := s.words[word]
	return e.syllables, ok
}

// WordsStartingWith iterates the words whose first syllable is syllable, in
// corpus order.
func (s *Store) WordsStartingWith(syllable string) iter.Seq[string] {
	words := s.byFirst[syllable]
	return func(yield func(string) bool) {
		for _, w := range words {
			if !yield(w) {
				return
			}
		}
	}
}

// OutDegree reports how many words start with syllable.
func (s *Store) OutDegree(syllable string) int { return len(s.byFirst[syllable]) }

// RandomOpening picks a random word whose last syllable starts at least
// minOut words, so the first reply is never forced. Falls back to the best
// connected word when nothing reaches minOut.
func (s *Store) RandomOpening(rng *rand.Rand, minOut int) string {
	// openerOut is sorted descending, so the qualifying words are a prefix.
	n := sort.Search(len(s.openerOut), func(i int) bool { return s.openerOut[i] < minOut })
	if n == 0 {
		return s.openers[0]
	}
	return s.openers[rng.IntN(n)]
}

// Meanings returns up to maxSenses senses of a canonical word, in corpus
// order. The slice is freshly built, so callers may keep or modify it.
func (s *Store) Meanings(word string) []Sense {
	e, ok := s.words[word]
	if !ok || e.senses == "" {
		return nil
	}
	var out []Sense
	for cell := range strings.SplitSeq(e.senses, "\t") {
		pos, gloss, _ := strings.Cut(cell, "|")
		gloss = strings.TrimSpace(gloss)
		if gloss == "" {
			continue
		}
		out = append(out, Sense{POS: strings.TrimSpace(pos), Gloss: gloss})
		if len(out) == maxSenses {
			break
		}
	}
	return out
}
