package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

func TestResolveCommitSHA(t *testing.T) {
	prev := gitSHA
	defer func() { gitSHA = prev }()

	// Coolify runtime env wins over the baked fallback.
	gitSHA = "baked"
	if got := resolveCommitSHA("  runtime-sha "); got != "runtime-sha" {
		t.Errorf("env present: got %q, want trimmed runtime-sha", got)
	}

	// Empty env falls back to the VCS revision embedded by go build.
	if got := resolveCommitSHA(""); got != "baked" {
		t.Errorf("env empty: got %q, want baked fallback", got)
	}

	// Neither source set → "unknown" so the owner still gets the startup DM.
	gitSHA = ""
	if got := resolveCommitSHA("   "); got != "unknown" {
		t.Errorf("both empty: got %q, want unknown", got)
	}
}

func TestShortCommitSHA(t *testing.T) {
	if got := shortCommitSHA(" 0123456789abcdef "); got != "0123456" {
		t.Errorf("full revision: got %q, want %q", got, "0123456")
	}
	if got := shortCommitSHA("abc123"); got != "abc123" {
		t.Errorf("short revision: got %q, want %q", got, "abc123")
	}
}

func TestComposeDoesNotOverrideSourceCommit(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "compose.yml"))
	if err != nil {
		t.Fatalf("read compose.yml: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "SOURCE_COMMIT:") || strings.HasPrefix(trimmed, "- SOURCE_COMMIT") {
			t.Fatalf("compose.yml must not declare SOURCE_COMMIT; Coolify supplies it at runtime and an explicit Compose value can override it with empty")
		}
	}
}

func TestFactoriesIncludesExpectedModules(t *testing.T) {
	catalog := factories()
	for _, name := range []string{"gold", "coin"} {
		if catalog[name] == nil {
			t.Fatalf("factories missing %s", name)
		}
	}
	reg, err := modules.Build([]string{"gold", "coin"}, catalog, storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build selected modules: %v", err)
	}
	for _, name := range []string{
		"gold_price", "gold_topup", "gold_buy", "gold_sell", "gold_portfolio",
		"coin_price", "coin_topup", "coin_buy", "coin_sell", "coin_portfolio",
	} {
		if _, ok := reg.AllCommands[name]; !ok {
			t.Fatalf("missing command %s", name)
		}
	}
}

func TestFactoriesRegistersBlacklistCommands(t *testing.T) {
	catalog := factories()
	if catalog["blacklist"] == nil {
		t.Fatal("factories missing blacklist")
	}
	reg, err := modules.Build([]string{"blacklist"}, catalog, storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build blacklist: %v", err)
	}
	for _, name := range []string{
		"blacklist", "blacklist_add", "blacklist_del", "blacklist_rules",
		"blacklist_check", "whitelist_add", "whitelist_del", "whitelist_rnd",
	} {
		if _, ok := reg.AllCommands[name]; !ok {
			t.Fatalf("missing command %s", name)
		}
	}
	if got := len(reg.AllCommands); got != 8 {
		t.Fatalf("blacklist registered %d commands, want 8", got)
	}
}

// An empty MODULES loads every module, so a command name that collides with an
// existing module surfaces here as a test failure rather than as a startup
// crash on deploy.
func TestFactoriesBuildWholeCatalog(t *testing.T) {
	if _, err := modules.Build(nil, factories(), storage.NewMemoryProvider(), modules.BuildOptions{}); err != nil {
		t.Fatalf("Build whole catalog: %v", err)
	}
}
