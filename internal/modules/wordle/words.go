package wordle

import "github.com/tiennm99/tiennm99bot/internal/modules/wordle/wordlist"

// loadWords parses the embedded dictionary into a slice plus a membership
// set; a malformed line panics at startup. See wordlist.Load.
func loadWords() ([]string, map[string]struct{}) { return wordlist.Load() }
