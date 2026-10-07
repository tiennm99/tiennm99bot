// Package metrics is a tiny in-memory counter store with periodic flush to the
// project's structured logger.
//
// The project intentionally avoids running a metrics sidecar or external
// exporter. Per-instance counters are reset on flush so each log line
// represents a delta that can be aggregated by the hosting log sink.
package metrics

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/log"
)

// DefaultFlushInterval is how often Run flushes counters to the log. 60s
// keeps log volume modest (1 metrics line per minute per active instance)
// while still surfacing minute-scale traffic shifts.
const DefaultFlushInterval = 60 * time.Second

// Registry holds named counters across two categories: command invocations
// and errors. Zero-value Registry is ready to use.
//
// Counters use atomic.Int64 so increments don't lock; the per-name map
// itself is guarded by an RWMutex for the rare add path. Names should be
// short and stable — they become log label values.
type Registry struct {
	mu       sync.RWMutex
	commands map[string]*atomic.Int64
	errors   map[string]*atomic.Int64
}

// New returns an empty Registry. Most callers use the package-level
// Default instead.
func New() *Registry {
	return &Registry{
		commands: map[string]*atomic.Int64{},
		errors:   map[string]*atomic.Int64{},
	}
}

// Default is the package-level registry. Convenience for the common case
// where a process needs exactly one. Tests can construct their own and use
// the methods directly.
var Default = New()

// IncCommand bumps the counter for a command invocation. name is the
// Telegram command without the leading slash.
func (r *Registry) IncCommand(name string) { r.inc(r.commandsMap(), name) }

// IncError bumps the counter for an error category — small, stable kinds
// like "handler-error" or "handler-panic".
func (r *Registry) IncError(kind string) { r.inc(r.errorsMap(), kind) }

func (r *Registry) commandsMap() map[string]*atomic.Int64 { return r.commands }
func (r *Registry) errorsMap() map[string]*atomic.Int64   { return r.errors }

// inc bumps the counter for name in m, allocating on first use. Only a new
// name takes the write lock; steady-state increments share the read lock and
// bump the atomic.
func (r *Registry) inc(m map[string]*atomic.Int64, name string) {
	r.mu.RLock()
	c, ok := m[name]
	r.mu.RUnlock()
	if ok {
		c.Add(1)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := m[name]; ok {
		c.Add(1)
		return
	}
	c = &atomic.Int64{}
	c.Store(1)
	m[name] = c
}

// snapshot copies and resets the counters atomically per category. The
// returned maps are owned by the caller; the registry's internal state is
// reset to zero for the next interval.
func (r *Registry) snapshot() (cmds, errs map[string]int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cmds = drain(r.commands)
	errs = drain(r.errors)
	return
}

// drain swaps out a counter map's values into a plain int64 map and
// resets each atomic to zero. The map keys are kept so subsequent
// increments don't reallocate the entry — only the count is reset.
func drain(m map[string]*atomic.Int64) map[string]int64 {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]int64, len(m))
	for k, v := range m {
		n := v.Swap(0)
		if n != 0 {
			out[k] = n
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Flush emits one structured log line with the current counters and
// resets them. Safe to call from anywhere; tests use it directly.
//
// The log line shape:
//
//	{"msg":"metrics","commands":{"wordle":3,"loldle":1},"errors":{"handler-error":1}}
//
// Keep msg=metrics stable; log-based dashboards filter on it.
// Empty categories appear as null (slog's default for nil maps).
func (r *Registry) Flush() {
	cmds, errs := r.snapshot()
	// Avoid an empty-everything log line — adds noise without signal.
	if cmds == nil && errs == nil {
		return
	}
	// slog renders map[string]int64 as a JSON object; tests assert on
	// per-key substrings rather than full-line equality so non-deterministic
	// hashtable iteration order doesn't make them flaky.
	log.Info("metrics", "commands", cmds, "errors", errs)
}

// Run flushes counters every DefaultFlushInterval until ctx is cancelled,
// then does one final Flush so a SIGTERM shutdown captures the trailing
// window. It blocks until ctx is done, so callers run it in its own
// goroutine.
//
// Idiomatic usage:
//
//	go metrics.Default.Run(rootCtx)
func (r *Registry) Run(ctx context.Context) {
	tick := time.NewTicker(DefaultFlushInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			r.Flush()
			return
		case <-tick.C:
			r.Flush()
		}
	}
}

// IncCommand / IncError on the package-level Default — short import-path-free
// spelling for the common case.
func IncCommand(name string) { Default.IncCommand(name) }
func IncError(kind string)   { Default.IncError(kind) }

// Flush flushes the default registry. Used in graceful-shutdown paths.
func Flush() { Default.Flush() }

// Run starts the default-registry's flush loop. Cancels on ctx done.
func Run(ctx context.Context) { Default.Run(ctx) }
