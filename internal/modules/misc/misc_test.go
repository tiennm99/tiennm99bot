package misc

import (
	"context"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// Store behaviour is tested directly here; Telegram command dispatch is covered
// in handlers_test.go with a recording bot.

// newMiscStore returns a fresh in-memory typed lastPing store for tests.
func newMiscStore() storage.DocStore[lastPing] {
	return storage.Typed[lastPing](storage.NewMemoryProvider().Collection("misc"))
}

func TestNew_RegistersExpectedCommands(t *testing.T) {
	deps := modules.Deps{Store: storage.NewMemoryProvider().Collection("misc")}
	mod := New(deps)

	want := map[string]modules.Visibility{
		"ping":              modules.VisibilityPublic,
		"ping_stats":        modules.VisibilityProtected,
		"ff":                modules.VisibilityProtected,
		"xlt1":              modules.VisibilityPublic,
		"giaxang":           modules.VisibilityPublic,
		"the_answer":        modules.VisibilityPrivate,
		"trongtruonghop":    modules.VisibilityPublic,
		"tth":               modules.VisibilityPublic,
		"trongtruonghopvng": modules.VisibilityPublic,
		"tthvng":            modules.VisibilityPublic,
	}
	if len(mod.Commands) != len(want) {
		t.Fatalf("commands count = %d, want %d", len(mod.Commands), len(want))
	}
	for _, c := range mod.Commands {
		v, ok := want[c.Name]
		if !ok {
			t.Errorf("unexpected command %q", c.Name)
			continue
		}
		if c.Visibility != v {
			t.Errorf("command %q visibility = %d, want %d", c.Name, c.Visibility, v)
		}
		if c.Handler == nil {
			t.Errorf("command %q has nil handler", c.Name)
		}
	}
}

func TestPing_WritesLastPingStore(t *testing.T) {
	ctx := context.Background()
	store := newMiscStore()

	// Drive the store side directly: lock the wire format as a ms-epoch number
	// (not an RFC3339 string), matching every other timestamp in the bot's store.
	if err := store.Put(ctx, lastPingKey, lastPing{At: 1700000000000}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, _, err := store.Get(ctx, lastPingKey)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.At != 1700000000000 {
		t.Errorf("ms-epoch round-trip: At = %d, want 1700000000000", got.At)
	}
}

func TestPingStats_MissingKeyReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	store := newMiscStore()

	_, _, err := store.Get(ctx, lastPingKey)
	if err != storage.ErrNotFound {
		t.Errorf("Get missing = %v, want ErrNotFound", err)
	}
}
