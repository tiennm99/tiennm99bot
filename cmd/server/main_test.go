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

// noitu is in the catalog and, with GAME_BASE_URL unset, registers its commands
// and game but no public HTTP route.
func TestFactoriesRegistersNoituDisabledByDefault(t *testing.T) {
	t.Setenv("GAME_BASE_URL", "")
	t.Setenv("NOITU_GAME_SECRET", "")
	reg, err := modules.Build([]string{"noitu"}, factories(), storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build noitu: %v", err)
	}
	for _, name := range []string{"noitu", "noitubot", "noitutop"} {
		if _, ok := reg.AllCommands[name]; !ok {
			t.Fatalf("missing command %s", name)
		}
	}
	if _, ok := reg.AllCommands["noitupvp"]; ok {
		t.Fatal("renamed command noitupvp is still registered")
	}
	if routes := serverRoutes(reg); len(routes) != 0 {
		t.Fatalf("disabled game exposes routes: %+v", routes)
	}
}

// wordledaily is in the catalog and, with GAME_BASE_URL unset, registers its
// commands and game but no public HTTP route and no daily push.
func TestFactoriesRegistersWordleDailyDisabledByDefault(t *testing.T) {
	t.Setenv("GAME_BASE_URL", "")
	t.Setenv("WORDLEDAILY_GAME_SECRET", "")
	reg, err := modules.Build([]string{"wordledaily"}, factories(), storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build wordledaily: %v", err)
	}
	for _, name := range []string{"wordledaily", "wordledaily_subscribe", "wordledaily_unsubscribe"} {
		if _, ok := reg.AllCommands[name]; !ok {
			t.Fatalf("missing command %s", name)
		}
	}
	if routes := serverRoutes(reg); len(routes) != 0 {
		t.Fatalf("disabled game exposes routes: %+v", routes)
	}
	for _, m := range reg.Modules {
		if len(m.Crons) != 0 {
			t.Fatalf("disabled game registers crons: %+v", m.Crons)
		}
	}
}

// loldle is in the catalog and, with GAME_BASE_URL unset, registers its
// unlimited and daily commands and its game, but no route and no cron.
func TestFactoriesRegistersLoldleDisabledByDefault(t *testing.T) {
	t.Setenv("GAME_BASE_URL", "")
	t.Setenv("LOLDLE_GAME_SECRET", "")
	reg, err := modules.Build([]string{"loldle"}, factories(), storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build loldle: %v", err)
	}
	for _, name := range []string{"loldle", "loldle_giveup", "loldle_stats", "loldle_setmax", "loldledaily", "loldledaily_subscribe", "loldledaily_unsubscribe"} {
		if _, ok := reg.AllCommands[name]; !ok {
			t.Fatalf("missing command %s", name)
		}
	}
	if routes := serverRoutes(reg); len(routes) != 0 {
		t.Fatalf("disabled game exposes routes: %+v", routes)
	}
	for _, m := range reg.Modules {
		if len(m.Crons) != 0 {
			t.Fatalf("disabled game registers crons: %+v", m.Crons)
		}
	}
}

// The in-chat wordle module was folded into wordledaily: a MODULES value
// naming it loads wordledaily, once, with every /wordle command.
func TestNormalizeModules_MapsRetiredWordleToWordleDaily(t *testing.T) {
	for in, want := range map[string]string{
		"wordle":                  "wordledaily",
		"util,wordle,loldle":      "util wordledaily loldle",
		"wordle,wordledaily,misc": "wordledaily misc",
		"wordledaily,wordle":      "wordledaily",
	} {
		if got := strings.Join(normalizeModules(splitCSV(in)), " "); got != want {
			t.Errorf("normalizeModules(%q) = %q, want %q", in, got, want)
		}
	}
	if normalizeModules(nil) != nil {
		t.Fatal("empty MODULES must stay empty: it loads every module")
	}
	t.Setenv("GAME_BASE_URL", "")
	reg, err := modules.Build(normalizeModules([]string{"wordle"}), factories(), storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, name := range []string{"wordle", "wordle_new", "wordle_giveup", "wordle_stats", "wordledaily"} {
		if _, ok := reg.AllCommands[name]; !ok {
			t.Fatalf("missing command %s", name)
		}
	}
	if _, ok := factories()["wordle"]; ok {
		t.Fatal("the retired wordle module is still in the catalog")
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
