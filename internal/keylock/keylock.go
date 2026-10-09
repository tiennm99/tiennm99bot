// Package keylock serialises compound operations that target the same key
// (typically a chat / user / subject identifier) across goroutines.
//
// Why a separate package: several modules need a per-subject mutex to turn
// the store's single-op atomicity into safe Get→mutate→Put. Telegram updates
// are handled one at a time, but crons fire on scheduler goroutines alongside
// them, so without explicit per-subject serialisation a cron and a handler
// writing the same subject could race and drop a write.
//
// Trade-off: the underlying sync.Map grows unboundedly with distinct keys
// (~32 B each). At the current bot scale, that is acceptable; add eviction if
// production cardinality starts growing materially.
package keylock

import "sync"

// Map gives each string key its own mutex, lazily created. Zero value is
// usable; do not copy after first use (sync.Map is non-copyable).
type Map struct {
	m sync.Map // key: string → val: *sync.Mutex
}

// Acquire locks the per-key mutex and returns its Unlock as a func so the
// caller can `defer m.Acquire(key)()` at the top of a critical section.
//
// Distinct keys never block each other; same-key callers run one at a time.
// Like sync.Mutex, it does not guarantee FIFO order among waiters.
func (m *Map) Acquire(key string) func() {
	v, _ := m.m.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Len counts the keys that have a mutex, which is the map's memory
// footprint: keys are never freed, so callers keep their key set bounded.
func (m *Map) Len() int {
	n := 0
	m.m.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}
