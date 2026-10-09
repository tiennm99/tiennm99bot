package wordlist

import (
	"slices"
	"strings"
	"testing"
)

func TestLoad_EmbeddedDictIsValid(t *testing.T) {
	words, set := Load()
	if len(words) < 14000 || len(words) > 15000 {
		t.Errorf("word count = %d, want ~14855", len(words))
	}
	if len(words) != len(set) {
		t.Errorf("words/set length mismatch: %d vs %d (duplicates?)", len(words), len(set))
	}
}

// The answer list must stay a sorted, duplicate-free subset of the guess
// dictionary, or a daily answer could be a word nobody may guess.
func TestAnswers_SortedUniqueSubsetOfDictionary(t *testing.T) {
	answers := Answers()
	if len(answers) < 2000 || len(answers) > 2600 {
		t.Fatalf("answer count = %d, want 2000-2600", len(answers))
	}
	if !slices.IsSorted(answers) {
		t.Error("answers.txt is not sorted")
	}
	if len(slices.Compact(slices.Clone(answers))) != len(answers) {
		t.Error("answers.txt has duplicates")
	}
	_, set := Load()
	for _, a := range answers {
		if _, ok := set[a]; !ok {
			t.Errorf("answer %q is not in words.txt", a)
		}
	}
}

func marks(rs []LetterScore) string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Result[:1]
	}
	return strings.Join(out, "")
}

func TestCompare_DuplicateLetters(t *testing.T) {
	cases := []struct{ guess, target, want string }{
		{"crane", "crane", "ccccc"},
		{"slate", "shale", "cpcwc"},
		{"babes", "abbey", "ppccw"},
		{"aahed", "abide", "cwwpp"},
		{"ebbed", "lever", "pwwcw"},
		{"aabbb", "aaaaa", "ccwww"},
		{"speed", "abide", "wwpwp"},
	}
	for _, c := range cases {
		if got := marks(Compare(c.guess, c.target)); got != c.want {
			t.Errorf("Compare(%q, %q) = %s, want %s", c.guess, c.target, got, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	dict := map[string]struct{}{"crane": {}}
	for in, want := range map[string]Result{
		"":        {Reason: ReasonEmpty},
		"cat":     {Word: "cat", Reason: ReasonLength},
		"there":   {Word: "there", Reason: ReasonUnknown},
		" CRANE!": {OK: true, Word: "crane"},
	} {
		if got := Validate(dict, in); got != want {
			t.Errorf("Validate(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestEmojiRow(t *testing.T) {
	if got := EmojiRow(Compare("slate", "shale")); got != "🟩🟨🟩⬜🟩" {
		t.Errorf("EmojiRow = %s", got)
	}
}
