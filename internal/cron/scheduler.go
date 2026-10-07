// Package cron runs module crons in-process for the self-hosted long-lived
// container. It reads each registered cron's Schedule field and fires its
// handler on time, in UTC.
package cron

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
)

// cronTimeout caps a single in-process cron fire so a slow handler cannot
// wedge the scheduler. 60s is the budget; long crons must offload and return
// fast.
const cronTimeout = 60 * time.Second

// Run starts an in-process scheduler that fires every registered cron with a
// non-empty Schedule. Each fire is wrapped with a timeout, panic recovery, and
// structured logging — modules.DispatchScheduled provides none of these (it is
// only a registry lookup + handler call), so the wrapping lives here.
//
// A malformed schedule string is fatal (returned as an error) so a config typo
// fails fast at startup rather than silently never firing. The returned stop
// function halts the scheduler and waits for any in-flight job to finish; it is
// safe to call exactly once (e.g. via defer).
func Run(ctx context.Context, reg *modules.Registry) (stop func(), err error) {
	c := cron.New(cron.WithLocation(time.UTC))

	registered := 0
	for _, cr := range reg.Crons() {
		if cr.Schedule == "" {
			log.Warn("cron has empty schedule; it will never fire", "name", cr.Name)
			continue
		}
		name := cr.Name // capture per iteration
		if _, err := c.AddFunc(cr.Schedule, func() { fire(ctx, name, reg) }); err != nil {
			return func() {}, fmt.Errorf("cron %q has invalid schedule %q: %w", cr.Name, cr.Schedule, err)
		}
		registered++
	}

	c.Start()
	log.Info("cron scheduler started", "registered", registered)

	stop = func() {
		// c.Stop() returns a context that is done once running jobs complete.
		stopCtx := c.Stop()
		<-stopCtx.Done()
	}
	return stop, nil
}

// fire dispatches one scheduled cron with its own timeout and panic recovery so
// a slow or panicking handler never wedges or kills the scheduler.
func fire(ctx context.Context, name string, reg *modules.Registry) {
	runCtx, cancel := context.WithTimeout(ctx, cronTimeout)
	defer cancel()

	log.Info("cron triggered", "source", "scheduler", "name", name)
	defer func() {
		if rec := recover(); rec != nil {
			log.Error("cron handler panic",
				"source", "scheduler",
				"name", name,
				"panic", rec,
				"stack", string(debug.Stack()))
		}
	}()
	if err := modules.DispatchScheduled(runCtx, name, reg); err != nil {
		log.Error("cron failed", "source", "scheduler", "name", name, "err", err)
	}
}
