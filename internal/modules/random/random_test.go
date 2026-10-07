package random

import (
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

func TestNew_RegistersRandomGroup(t *testing.T) {
	mod := New(modules.Deps{Store: storage.NewMemoryProvider().Collection("random")})

	want := map[string]modules.Visibility{
		"random":       modules.VisibilityPublic,
		"wheelofnames": modules.VisibilityPublic,
		"gacha":        modules.VisibilityPublic,
		"genshin":      modules.VisibilityUnlisted,
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
