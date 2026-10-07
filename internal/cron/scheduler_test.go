package cron

import (
	"context"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// buildReg builds a registry with a single module exposing one cron whose
// handler runs onFire. schedule is the cron expression under test.
func buildReg(t *testing.T, schedule string, onFire func()) *modules.Registry {
	t.Helper()
	factories := map[string]modules.Factory{
		"ticker": func(_ modules.Deps) modules.Module {
			return modules.Module{
				Name: "ticker",
				Crons: []modules.Cron{{
					Name:     "tick",
					Schedule: schedule,
					Handler: func(_ context.Context, _ modules.Deps) error {
						onFire()
						return nil
					},
				}},
			}
		},
	}
	reg, err := modules.Build([]string{"ticker"}, factories, storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("modules.Build: %v", err)
	}
	return reg
}

func TestRun_FiresHandlerOnSchedule(t *testing.T) {
	fired := make(chan struct{}, 1)
	reg := buildReg(t, "@every 1s", func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	})

	stop, err := Run(context.Background(), reg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer stop()

	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("cron handler did not fire within 5s")
	}
}

func TestRun_BadScheduleIsFatal(t *testing.T) {
	reg := buildReg(t, "not-a-cron-expression", func() {})
	if _, err := Run(context.Background(), reg); err == nil {
		t.Fatal("Run with invalid schedule: got nil error, want a parse error")
	}
}

func TestRun_StopHaltsScheduler(t *testing.T) {
	reg := buildReg(t, "@every 1s", func() {})
	stop, err := Run(context.Background(), reg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop() did not return within 5s")
	}
}

// TestRun_RecoversHandlerPanic asserts a panicking cron handler does not crash
// the scheduler; the next tick still fires.
func TestRun_RecoversHandlerPanic(t *testing.T) {
	calls := make(chan struct{}, 4)
	reg := buildReg(t, "@every 1s", func() {
		calls <- struct{}{}
		panic("boom")
	})
	stop, err := Run(context.Background(), reg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer stop()

	// Two separate fires prove the scheduler survived the first panic.
	for i := 0; i < 2; i++ {
		select {
		case <-calls:
		case <-time.After(5 * time.Second):
			t.Fatalf("expected fire #%d after panic recovery", i+1)
		}
	}
}
