package htmlgame

import (
	"sync"
	"time"
)

// Limiter allows at most Per requests per key within any Window (a sliding
// window). It is in-memory: a restart forgets it, which only resets the
// window. Keys with no request in the last Window are pruned as it runs.
type Limiter struct {
	Per    int
	Window time.Duration

	mu     sync.Mutex
	hits   map[int64][]time.Time
	pruned time.Time
}

// Allow records a request for key at now and reports whether it is within
// the limit. A refused request is not recorded.
func (l *Limiter) Allow(key int64, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[int64][]time.Time{}
	}
	cutoff := now.Add(-l.Window)
	if now.Sub(l.pruned) >= l.Window {
		for k, ts := range l.hits {
			if len(ts) == 0 || !ts[len(ts)-1].After(cutoff) {
				delete(l.hits, k)
			}
		}
		l.pruned = now
	}
	recent := l.hits[key]
	i := 0
	for i < len(recent) && !recent[i].After(cutoff) {
		i++
	}
	recent = recent[i:]
	if len(recent) >= l.Per {
		l.hits[key] = recent
		return false
	}
	l.hits[key] = append(recent, now)
	return true
}
