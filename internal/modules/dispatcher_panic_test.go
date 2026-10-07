package modules_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/metrics"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// The bot dispatches updates inline on a single polling goroutine
// (bot.WithNotAsyncHandlers), so an unrecovered handler panic takes the whole
// process down for every user — not just the caller who triggered it. These
// tests pin the barrier that stops that: the panic is contained, logged with a
// stack, and counted, and the process (here, the test binary) survives.

// syncBuffer is a log sink safe to read while another goroutine writes.
//
// slog is concurrency-safe, but the *sink* it writes into is the test's
// responsibility — and one of these tests deliberately provokes a panic on a
// detached goroutine, which then logs while the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs redirects the default logger for the duration of fn and returns
// everything written. Mirrors TestLogCommand's idiom.
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	buf := &syncBuffer{}
	prev := log.Default()
	log.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer log.SetDefault(prev)
	fn()
	return buf.String()
}

// waitForLog polls buf until it contains needle, so a test never races a
// detached goroutine with a fixed sleep.
func waitForLog(t *testing.T, buf *syncBuffer, needle string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), needle) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; log was %q", needle, buf.String())
}

// installPanicking builds a registry with one command and one callback that
// both panic, and installs it on a recording bot.
func installPanicking(t *testing.T) *testutil.RecordingBot {
	t.Helper()
	rb := testutil.NewRecordingBot(t)
	reg, err := modules.Build([]string{"boom"}, map[string]modules.Factory{
		"boom": func(modules.Deps) modules.Module {
			return modules.Module{
				Commands: []modules.Command{{
					Name:        "boom",
					Visibility:  modules.VisibilityPublic,
					Description: "panics on purpose",
					Handler: func(context.Context, *bot.Bot, *models.Update) error {
						panic("command exploded")
					},
				}},
				Callbacks: []modules.Callback{{
					Prefix:     "boom:",
					Visibility: modules.VisibilityPublic,
					Handler: func(context.Context, *bot.Bot, *models.Update) error {
						panic("callback exploded")
					},
				}},
			}
		},
	}, storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	modules.Install(rb.Bot, reg, modules.Auth{})
	return rb
}

func TestInstall_CommandPanicIsContained(t *testing.T) {
	rb := installPanicking(t)

	// The assertion is that this line returns at all: without the barrier the
	// panic unwinds through ProcessUpdate and kills the test binary.
	logs := captureLogs(t, func() {
		rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(42, "/boom"))
		metrics.Flush()
	})

	if !strings.Contains(logs, "command panic") {
		t.Errorf("panic not logged; got %q", logs)
	}
	if !strings.Contains(logs, "command exploded") {
		t.Errorf("panic value not logged; got %q", logs)
	}
	if !strings.Contains(logs, "stack") {
		t.Errorf("stack not logged; got %q", logs)
	}
	// metrics.Flush renders the error counters into its log line, which is the
	// only view of them from outside the metrics package.
	if !strings.Contains(logs, "handler-panic") {
		t.Errorf("handler-panic metric not incremented; got %q", logs)
	}
}

func TestInstall_CallbackPanicIsContainedAndAnswered(t *testing.T) {
	rb := installPanicking(t)

	update := &models.Update{CallbackQuery: &models.CallbackQuery{
		ID:   "cbq-1",
		From: models.User{ID: 42},
		Data: "boom:go",
	}}

	logs := captureLogs(t, func() {
		rb.Bot.ProcessUpdate(context.Background(), update)
	})

	if !strings.Contains(logs, "callback panic") {
		t.Errorf("panic not logged; got %q", logs)
	}

	// A panicking callback must still answer the query, or the caller's client
	// spins until it times out.
	var answered bool
	for _, call := range rb.Sent() {
		if call.Method == "answerCallbackQuery" && call.Form["callback_query_id"] == "cbq-1" {
			answered = true
		}
	}
	if !answered {
		t.Errorf("callback query not answered after panic; sent %+v", rb.Sent())
	}
}

// The stats module registers a CommandHook, which runs on a detached goroutine
// outside the handler's barrier. A panic there has nothing above it on that
// goroutine's stack, so it terminates the process regardless of how well the
// handler itself is protected.
func TestInstall_CommandHookPanicIsContained(t *testing.T) {
	rb := testutil.NewRecordingBot(t)
	reg, err := modules.Build([]string{"hooky"}, map[string]modules.Factory{
		"hooky": func(modules.Deps) modules.Module {
			return modules.Module{
				Commands: []modules.Command{{
					Name:        "hooky",
					Visibility:  modules.VisibilityPublic,
					Description: "fine itself; its hook is not",
					Handler: func(context.Context, *bot.Bot, *models.Update) error {
						return nil
					},
				}},
				CommandHook: func(context.Context, string, *models.Update) {
					panic("hook exploded")
				},
			}
		},
	}, storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	modules.Install(rb.Bot, reg, modules.Auth{})

	buf := &syncBuffer{}
	prev := log.Default()
	log.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer log.SetDefault(prev)

	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(42, "/hooky"))

	// The assertion is that the test binary is still alive to run it: without
	// the barrier, the hook's panic has nothing above it on its own goroutine.
	waitForLog(t, buf, "command hook panic")
}
