package dict

import (
	"math/rand/v2"
	"strings"
	"testing"
)

func mustDefault(t *testing.T) *Store {
	t.Helper()
	s, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	return s
}

func TestNormalize(t *testing.T) {
	// "ữ" as u + combining horn + combining tilde, uppercase, odd spacing.
	got, syl, err := Normalize("  Nữ    ĐẸP\t")
	if err != nil || got != "nữ đẹp" || len(syl) != 2 {
		t.Fatalf("Normalize = %q %v %v", got, syl, err)
	}
	if _, _, err := Normalize(" \t "); err != ErrEmpty {
		t.Fatalf("blank input err = %v, want ErrEmpty", err)
	}
	if HasEnoughSyllables([]string{"một"}) {
		t.Fatal("one syllable counted as enough")
	}
}

// Parse already rejects a headword that is not normalized or is too short, so
// loading the embedded corpus at all proves every entry is well formed.
func TestDefaultCorpusLoads(t *testing.T) {
	s := mustDefault(t)
	if s.Len() < 30000 {
		t.Fatalf("corpus has %d words, want at least 30000", s.Len())
	}
	if s.SourceSHA256 == "" {
		t.Error("corpus header #@source_sha256 missing")
	}
	rng := rand.New(rand.NewPCG(1, 2))
	opening := s.RandomOpening(rng, 20)
	last, _ := s.LastSyllable(opening)
	if s.OutDegree(last) < 20 {
		t.Fatalf("opening %q ends on %q with out-degree %d, want >= 20", opening, last, s.OutDegree(last))
	}
}

func TestResolveSpellingVariants(t *testing.T) {
	s := mustDefault(t)
	cases := map[string]string{
		"hoà giải": "hòa giải", // tone moved to the second vowel
		"hòa man":  "hoà man",  // and back
		"lí luận":  "lý luận",  // i/y swap
		"sỹ quan":  "sĩ quan",  // listed sĩ/sỹ pair
		"lí do":    "lí do",    // both spellings are headwords: exact wins
	}
	for in, want := range cases {
		if got, ok := s.Resolve(in); !ok || got != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if got, ok := s.Resolve("xyz abc"); ok {
		t.Errorf("Resolve(non-word) = %q, want miss", got)
	}
}

func TestResolveOnSmallCorpus(t *testing.T) {
	s, err := Parse("#@source_sha256 abc\n\nsĩ hai\tdanh từ|nghĩa một\nlý do\t|lý lẽ\nlí lẽ\t\nlý lẽ\t\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, ok := s.Resolve("sỹ hai"); !ok || got != "sĩ hai" {
		t.Errorf("Resolve(sỹ hai) = %q %v, want sĩ hai", got, ok)
	}
	if got, ok := s.Resolve("lí do"); !ok || got != "lý do" {
		t.Errorf("Resolve(lí do) = %q %v, want lý do", got, ok)
	}
	// Both "lí lẽ" and "lý lẽ" are headwords: an exact match wins.
	if got, _ := s.Resolve("lí lẽ"); got != "lí lẽ" {
		t.Errorf("Resolve(lí lẽ) = %q, want exact match", got)
	}
	if first, _ := s.FirstSyllable("sĩ hai"); first != "sĩ" {
		t.Errorf("FirstSyllable = %q", first)
	}
	if m := s.Meanings("sĩ hai"); len(m) != 1 || m[0].POS != "danh từ" || m[0].Gloss != "nghĩa một" {
		t.Errorf("Meanings = %+v", m)
	}
	if m := s.Meanings("lý do"); len(m) != 1 || m[0].POS != "" {
		t.Errorf("Meanings with empty pos = %+v", m)
	}
}

func TestResolveRejectsAmbiguousVariant(t *testing.T) {
	// "hoá lý" has variants "hóa lý", "hoá lí" and "hóa lí"; with two of them
	// real words, the lookup must not guess.
	s, err := Parse("hóa lý\t\nhoá lí\t\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, ok := s.Resolve("hóa lí"); ok {
		t.Fatalf("ambiguous Resolve = %q, want miss", got)
	}
}

func TestParseRejectsBadHeadwords(t *testing.T) {
	for _, bad := range []string{"một\t\n", "Hoa Hồng\t\n", "a  b\t\n", "a b\t\na b\t\n", "# only header\n"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", strings.TrimSpace(bad))
		}
	}
}

func TestMeaningsCapped(t *testing.T) {
	line := "ab cd" + strings.Repeat("\tpos|gloss", 8) + "\n"
	s, err := Parse(line)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := len(s.Meanings("ab cd")); got != maxSenses {
		t.Fatalf("Meanings len = %d, want %d", got, maxSenses)
	}
}

func TestVariantRules(t *testing.T) {
	if v, ok := toneShiftVariant("hòa"); !ok || v != "hoà" {
		t.Errorf("toneShiftVariant(hòa) = %q %v", v, ok)
	}
	if _, ok := toneShiftVariant("hoàn"); ok {
		t.Error("closed syllable shifted")
	}
	if _, ok := toneShiftVariant("quý"); ok {
		t.Error("qu onset shifted")
	}
	if v, ok := iyVariant("lý"); !ok || v != "lí" {
		t.Errorf("iyVariant(lý) = %q %v", v, ok)
	}
	if _, ok := iyVariant("gì"); ok {
		t.Error("gi digraph swapped")
	}
}
