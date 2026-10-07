package metrics

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	logger "github.com/tiennm99/tiennm99bot/internal/log"
)

// captureLogger swaps the package-level logger for one writing to buf and
// returns a restore func. Tests must defer the restore.
func captureLogger(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	prev := logger.Default()
	buf := &bytes.Buffer{}
	logger.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return buf, func() { logger.SetDefault(prev) }
}

func TestRegistry_IncCommand_AccumulatesCounts(t *testing.T) {
	r := New()
	r.IncCommand("wordle")
	r.IncCommand("wordle")
	r.IncCommand("loldle")

	cmds, _ := r.snapshot()
	if cmds["wordle"] != 2 {
		t.Errorf("wordle = %d, want 2", cmds["wordle"])
	}
	if cmds["loldle"] != 1 {
		t.Errorf("loldle = %d, want 1", cmds["loldle"])
	}
}

func TestRegistry_Snapshot_ResetsCounters(t *testing.T) {
	r := New()
	r.IncCommand("wordle")
	r.snapshot() // first snapshot drains
	cmds, _ := r.snapshot()
	if len(cmds) != 0 {
		t.Errorf("after drain, snapshot = %v, want empty", cmds)
	}
}

func TestRegistry_Flush_EmitsMetricsLine(t *testing.T) {
	buf, restore := captureLogger(t)
	defer restore()

	r := New()
	r.IncCommand("wordle")
	r.IncError("handler-error")
	r.Flush()

	output := buf.String()
	if !strings.Contains(output, `"msg":"metrics"`) {
		t.Errorf("flush output missing msg=metrics: %s", output)
	}
	if !strings.Contains(output, `"wordle":1`) {
		t.Errorf("flush output missing wordle counter: %s", output)
	}
	if !strings.Contains(output, `"handler-error":1`) {
		t.Errorf("flush output missing error counter: %s", output)
	}
	if strings.Contains(output, `"ai":`) {
		t.Errorf("flush output contains removed AI metrics field: %s", output)
	}
}

func TestRegistry_Flush_EmptyIsSilent(t *testing.T) {
	buf, restore := captureLogger(t)
	defer restore()
	r := New()
	r.Flush()
	if buf.Len() != 0 {
		t.Errorf("empty flush should produce no output; got %q", buf.String())
	}
}

// Steady-state increments should not race under -race. Hammer with
// goroutines and verify the total adds up.
func TestRegistry_ConcurrentInc(t *testing.T) {
	r := New()
	const goroutines = 16
	const itersEach = 1000
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < itersEach; j++ {
				r.IncCommand("hot")
			}
		}()
	}
	wg.Wait()
	cmds, _ := r.snapshot()
	if got := cmds["hot"]; got != goroutines*itersEach {
		t.Errorf("hot = %d, want %d", got, goroutines*itersEach)
	}
}

func TestPackageDefault_RoundTrip(t *testing.T) {
	// The package-level default is shared global state. Snapshot first to
	// clear any leakage from earlier tests in the same binary.
	Default.snapshot()

	IncCommand("ping")
	IncError("kv-fail")

	cmds, errs := Default.snapshot()
	if cmds["ping"] != 1 || errs["kv-fail"] != 1 {
		t.Errorf("default registry: cmds=%v errs=%v", cmds, errs)
	}
}

// Sanity check that the metrics line is valid JSON, not just a prefix.
func TestRegistry_Flush_OutputIsValidJSON(t *testing.T) {
	buf, restore := captureLogger(t)
	defer restore()
	r := New()
	r.IncCommand("wordle")
	r.Flush()

	// One JSON line per slog record.
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("not JSON: %q (%v)", line, err)
		}
	}
}
