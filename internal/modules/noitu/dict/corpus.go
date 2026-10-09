package dict

import (
	_ "embed"
	"sync"
)

// corpus is tiennm99/noitu's data/dictionary.txt, embedded unmodified. It is
// CC BY-SA 4.0 data; see data/LICENSE and data/ATTRIBUTION.md.
//
//go:embed data/dictionary.txt
var corpus string

var (
	defaultOnce  sync.Once
	defaultStore *Store
	errDefault   error
)

// Default parses the embedded corpus on first use and returns the shared
// Store. Parsing takes tens of milliseconds and a few MB of heap, so it only
// runs when the game is enabled.
func Default() (*Store, error) {
	defaultOnce.Do(func() {
		defaultStore, errDefault = Parse(corpus)
	})
	return defaultStore, errDefault
}
