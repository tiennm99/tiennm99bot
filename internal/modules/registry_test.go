package modules

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

func noopCmd(name string) Command {
	return Command{
		Name:        name,
		Visibility:  VisibilityPublic,
		Description: "test " + name,
		Handler:     func(_ context.Context, _ *bot.Bot, _ *models.Update) error { return nil },
	}
}

func noopCron(name string) Cron {
	return Cron{
		Schedule: "@every 24h",
		Name:     name,
		Handler:  func(_ context.Context, _ Deps) error { return nil },
	}
}

func factory(name string, cmds []Command, crons []Cron) Factory {
	return func(_ Deps) Module {
		return Module{Name: name, Commands: cmds, Crons: crons}
	}
}

func newProvider() storage.Provider { return storage.NewMemoryProvider() }

func TestBuild_EmptyModulesBootsCleanly(t *testing.T) {
	reg, err := Build(nil, map[string]Factory{}, newProvider(), BuildOptions{})
	if err != nil {
		t.Fatalf("Build empty: %v", err)
	}
	if len(reg.AllCommands) != 0 {
		t.Errorf("expected 0 commands, got %d", len(reg.AllCommands))
	}
}

func TestBuild_EmptyModulesLoadsAllRegistered(t *testing.T) {
	// Empty/unset MODULES is the documented "load every module" contract.
	factories := map[string]Factory{
		"alpha": factory("alpha", []Command{noopCmd("a1")}, nil),
		"beta":  factory("beta", []Command{noopCmd("b1")}, []Cron{noopCron("daily")}),
	}
	reg, err := Build(nil, factories, newProvider(), BuildOptions{})
	if err != nil {
		t.Fatalf("Build nil enabled: %v", err)
	}
	if len(reg.Modules) != len(factories) {
		t.Fatalf("expected all %d modules loaded, got %d", len(factories), len(reg.Modules))
	}
	if _, ok := reg.AllCommands["a1"]; !ok {
		t.Error("missing command a1 from auto-loaded alpha")
	}
	if _, ok := reg.Cron("daily"); !ok {
		t.Error("missing cron daily from auto-loaded beta")
	}
}

func TestBuild_LoadsRequestedModules(t *testing.T) {
	factories := map[string]Factory{
		"alpha": factory("alpha", []Command{noopCmd("a1")}, nil),
		"beta":  factory("beta", []Command{noopCmd("b1")}, []Cron{noopCron("daily")}),
	}
	reg, err := Build([]string{"alpha", "beta"}, factories, newProvider(), BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(reg.Modules) != 2 {
		t.Errorf("expected 2 modules, got %d", len(reg.Modules))
	}
	if _, ok := reg.AllCommands["a1"]; !ok {
		t.Error("missing command a1")
	}
	if _, ok := reg.Cron("daily"); !ok {
		t.Error("missing cron daily")
	}
}

func TestBuild_SkipsModulesNotInEnv(t *testing.T) {
	factories := map[string]Factory{
		"alpha": factory("alpha", []Command{noopCmd("a1")}, nil),
		"beta":  factory("beta", []Command{noopCmd("b1")}, nil),
	}
	reg, err := Build([]string{"alpha"}, factories, newProvider(), BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := reg.AllCommands["b1"]; ok {
		t.Error("beta should not have been loaded")
	}
}

func TestBuild_RejectsUnknownModule(t *testing.T) {
	_, err := Build([]string{"ghost"}, map[string]Factory{}, newProvider(), BuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("expected error mentioning ghost, got %v", err)
	}
}

func TestBuild_DetectsCommandConflict(t *testing.T) {
	factories := map[string]Factory{
		"alpha": factory("alpha", []Command{noopCmd("ping")}, nil),
		"beta":  factory("beta", []Command{noopCmd("ping")}, nil),
	}
	_, err := Build([]string{"alpha", "beta"}, factories, newProvider(), BuildOptions{})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if !strings.Contains(err.Error(), "command conflict") {
		t.Errorf("error should mention conflict, got %v", err)
	}
}

func TestBuild_DetectsCronConflict(t *testing.T) {
	factories := map[string]Factory{
		"alpha": factory("alpha", nil, []Cron{noopCron("daily")}),
		"beta":  factory("beta", nil, []Cron{noopCron("daily")}),
	}
	_, err := Build([]string{"alpha", "beta"}, factories, newProvider(), BuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "cron conflict") {
		t.Errorf("expected cron conflict, got %v", err)
	}
}

func TestBuild_DetectsOverlappingCallbackPrefixes(t *testing.T) {
	callback := func(prefix string) Callback {
		return Callback{Prefix: prefix, Visibility: VisibilityPublic, Handler: func(_ context.Context, _ *bot.Bot, _ *models.Update) error { return nil }}
	}
	factories := map[string]Factory{
		"alpha": func(_ Deps) Module { return Module{Callbacks: []Callback{callback("stock:")}} },
		"beta":  func(_ Deps) Module { return Module{Callbacks: []Callback{callback("stock:div:")}} },
	}
	_, err := Build([]string{"alpha", "beta"}, factories, newProvider(), BuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "callback prefix conflict") {
		t.Fatalf("expected callback prefix conflict, got %v", err)
	}
}

func TestBuild_RejectsInvalidCallback(t *testing.T) {
	factories := map[string]Factory{
		"alpha": func(_ Deps) Module { return Module{Callbacks: []Callback{{Prefix: "", Handler: nil}}} },
	}
	_, err := Build([]string{"alpha"}, factories, newProvider(), BuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "callback") {
		t.Fatalf("expected callback validation error, got %v", err)
	}
}

func TestBuild_RequiresProvider(t *testing.T) {
	_, err := Build(nil, map[string]Factory{}, nil, BuildOptions{})
	if err == nil {
		t.Error("expected error when the storage provider is nil")
	}
}

func TestBuild_ValidationErrorsMentionModule(t *testing.T) {
	bad := Command{Name: "BAD-NAME", Visibility: VisibilityPublic, Description: "x", Handler: noopCmd("x").Handler}
	factories := map[string]Factory{
		"alpha": factory("alpha", []Command{bad}, nil),
	}
	_, err := Build([]string{"alpha"}, factories, newProvider(), BuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "alpha") {
		t.Errorf("expected error mentioning module 'alpha', got %v", err)
	}
}

func TestDispatchScheduled_RunsHandler(t *testing.T) {
	called := false
	factories := map[string]Factory{
		"alpha": factory("alpha", nil, []Cron{{
			Name: "tick",
			Handler: func(_ context.Context, _ Deps) error {
				called = true
				return nil
			},
		}}),
	}
	reg, err := Build([]string{"alpha"}, factories, newProvider(), BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := DispatchScheduled(context.Background(), "tick", reg); err != nil {
		t.Fatalf("DispatchScheduled: %v", err)
	}
	if !called {
		t.Error("cron handler not invoked")
	}
}

func TestDispatchScheduled_UnknownReturnsErrCronNotFound(t *testing.T) {
	reg, err := Build(nil, map[string]Factory{}, newProvider(), BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	err = DispatchScheduled(context.Background(), "missing", reg)
	if !errors.Is(err, ErrCronNotFound) {
		t.Errorf("expected ErrCronNotFound, got %v", err)
	}
}

func TestDispatchScheduled_PassesScopedDeps(t *testing.T) {
	ctx := context.Background()
	provider := storage.NewMemoryProvider()

	factories := map[string]Factory{
		"alpha": func(d Deps) Module {
			return Module{Crons: []Cron{{
				Name: "tick_a",
				Handler: func(ctx context.Context, deps Deps) error {
					return storage.Typed[string](deps.Store).Put(ctx, "last", "A")
				},
			}}}
		},
		"beta": func(d Deps) Module {
			return Module{Crons: []Cron{{
				Name: "tick_b",
				Handler: func(ctx context.Context, deps Deps) error {
					return storage.Typed[string](deps.Store).Put(ctx, "last", "B")
				},
			}}}
		},
	}
	reg, err := Build([]string{"alpha", "beta"}, factories, provider, BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := DispatchScheduled(ctx, "tick_a", reg); err != nil {
		t.Fatalf("tick_a: %v", err)
	}
	if err := DispatchScheduled(ctx, "tick_b", reg); err != nil {
		t.Fatalf("tick_b: %v", err)
	}

	// Each module's per-collection store holds its own "last" key separately.
	gotA, _, err := storage.Typed[string](provider.Collection("alpha")).Get(ctx, "last")
	if err != nil || gotA != "A" {
		t.Errorf("alpha/last = %q (err=%v), want A", gotA, err)
	}
	gotB, _, err := storage.Typed[string](provider.Collection("beta")).Get(ctx, "last")
	if err != nil || gotB != "B" {
		t.Errorf("beta/last = %q (err=%v), want B", gotB, err)
	}
}

func TestBuild_RejectsInvalidModuleName(t *testing.T) {
	// `-` is intentionally allowed so modules can carry hyphenated names. `:`
	// must stay rejected — the module alphabet mirrors storage's
	// collection-name alphabet, and loosening one without the other would
	// hand a module an always-failing store.
	for _, name := range []string{"BadName", "a:b", "", "with space", "with.dot", "with/slash"} {
		t.Run(name, func(t *testing.T) {
			_, err := Build([]string{name}, map[string]Factory{}, newProvider(), BuildOptions{})
			if err == nil {
				t.Errorf("name %q: expected error", name)
			}
		})
	}
}

func TestBuild_RejectsFactoryNameMismatch(t *testing.T) {
	// A factory that hardcodes its own name disagreeing with the registry key
	// is a programming bug — surface it instead of silently overwriting.
	factories := map[string]Factory{
		"alpha": func(_ Deps) Module {
			return Module{Name: "imposter", Commands: []Command{noopCmd("a1")}}
		},
	}
	_, err := Build([]string{"alpha"}, factories, newProvider(), BuildOptions{})
	if err == nil {
		t.Fatal("expected error for factory Name mismatch")
	}
	if !strings.Contains(err.Error(), "imposter") {
		t.Errorf("error should mention mismatched name: %v", err)
	}
}

func TestBuild_AllowsFactoryWithBlankName(t *testing.T) {
	// Factory leaves Name blank; registry fills it from the key. Common,
	// non-buggy pattern.
	factories := map[string]Factory{
		"alpha": func(_ Deps) Module {
			return Module{Commands: []Command{noopCmd("a1")}}
		},
	}
	reg, err := Build([]string{"alpha"}, factories, newProvider(), BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if reg.Modules[0].Name != "alpha" {
		t.Errorf("blank-name factory: registered Name = %q, want 'alpha'", reg.Modules[0].Name)
	}
}

func TestBuild_AcceptsHyphenatedModuleName(t *testing.T) {
	factories := map[string]Factory{
		"demo-mod": factory("demo-mod", []Command{noopCmd("demo_cmd")}, nil),
	}
	reg, err := Build([]string{"demo-mod"}, factories, newProvider(), BuildOptions{})
	if err != nil {
		t.Fatalf("hyphenated name should be allowed: %v", err)
	}
	if len(reg.Modules) != 1 || reg.Modules[0].Name != "demo-mod" {
		t.Errorf("module not registered correctly: %+v", reg.Modules)
	}
}

func TestBuild_RejectsDuplicateModuleInEnv(t *testing.T) {
	factories := map[string]Factory{
		"alpha": factory("alpha", []Command{noopCmd("a1")}, nil),
	}
	_, err := Build([]string{"alpha", "alpha"}, factories, newProvider(), BuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate-module error, got %v", err)
	}
}

func TestBuild_PerModuleStoreIsolation(t *testing.T) {
	ctx := context.Background()
	provider := storage.NewMemoryProvider()

	// Each module writes a value to the same key; with one collection per
	// module they must not collide.
	captured := map[string]Deps{}
	factories := map[string]Factory{
		"alpha": func(d Deps) Module {
			captured["alpha"] = d
			return Module{Commands: []Command{noopCmd("a")}}
		},
		"beta": func(d Deps) Module {
			captured["beta"] = d
			return Module{Commands: []Command{noopCmd("b")}}
		},
	}
	if _, err := Build([]string{"alpha", "beta"}, factories, provider, BuildOptions{}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	alpha := storage.Typed[string](captured["alpha"].Store)
	beta := storage.Typed[string](captured["beta"].Store)
	if err := alpha.Put(ctx, "score", "1"); err != nil {
		t.Fatal(err)
	}
	if err := beta.Put(ctx, "score", "2"); err != nil {
		t.Fatal(err)
	}

	if got, _, _ := alpha.Get(ctx, "score"); got != "1" {
		t.Errorf("alpha/score = %q, want 1", got)
	}
	if got, _, _ := beta.Get(ctx, "score"); got != "2" {
		t.Errorf("beta/score = %q, want 2", got)
	}
}

// Fallback and inline are single-slot. Two modules claiming either is a
// configuration bug the registry catches, rather than leaving the winner to
// map iteration order.
func TestBuild_DetectsFallbackConflict(t *testing.T) {
	withFallback := func(name string) Factory {
		return func(_ Deps) Module {
			return Module{Name: name, Fallback: &CommandFallback{
				Visibility: VisibilityPublic,
				Handler: func(_ context.Context, _ *bot.Bot, _ string, _ *models.Update) error {
					return nil
				},
			}}
		}
	}
	factories := map[string]Factory{"alpha": withFallback("alpha"), "beta": withFallback("beta")}
	_, err := Build([]string{"alpha", "beta"}, factories, newProvider(), BuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "fallback conflict") {
		t.Errorf("expected fallback conflict, got %v", err)
	}
}

func TestBuild_DetectsInlineConflict(t *testing.T) {
	withInline := func(name string) Factory {
		return func(_ Deps) Module {
			return Module{Name: name, Inline: &InlineQuery{
				Visibility: VisibilityPublic,
				Handler: func(_ context.Context, _ *bot.Bot, _ *models.Update) error {
					return nil
				},
			}}
		}
	}
	factories := map[string]Factory{"alpha": withInline("alpha"), "beta": withInline("beta")}
	_, err := Build([]string{"alpha", "beta"}, factories, newProvider(), BuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "inline conflict") {
		t.Errorf("expected inline conflict, got %v", err)
	}
}

// A declared slot with no handler would panic at dispatch; reject it at build.
func TestBuild_RejectsHandlerlessFallbackAndInline(t *testing.T) {
	cases := map[string]Factory{
		"fallback has no handler": func(_ Deps) Module {
			return Module{Fallback: &CommandFallback{Visibility: VisibilityPublic}}
		},
		"inline has no handler": func(_ Deps) Module {
			return Module{Inline: &InlineQuery{Visibility: VisibilityPublic}}
		},
	}
	for want, f := range cases {
		t.Run(want, func(t *testing.T) {
			_, err := Build([]string{"alpha"}, map[string]Factory{"alpha": f}, newProvider(), BuildOptions{})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("expected %q, got %v", want, err)
			}
		})
	}
}

func TestBuild_DetectsGameConflict(t *testing.T) {
	game := Game{ShortName: "noitu", Visibility: VisibilityPublic, Handler: okHandler}
	factories := map[string]Factory{
		"alpha": func(_ Deps) Module { return Module{Games: []Game{game}} },
		"beta":  func(_ Deps) Module { return Module{Games: []Game{game}} },
	}
	_, err := Build([]string{"alpha", "beta"}, factories, newProvider(), BuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "game conflict") {
		t.Fatalf("expected game conflict, got %v", err)
	}
}

func TestBuild_RejectsInvalidGame(t *testing.T) {
	factories := map[string]Factory{
		"alpha": func(_ Deps) Module {
			return Module{Games: []Game{{ShortName: "no-itu", Visibility: VisibilityPublic, Handler: okHandler}}}
		},
	}
	_, err := Build([]string{"alpha"}, factories, newProvider(), BuildOptions{})
	if err == nil || !strings.Contains(err.Error(), `module "alpha"`) {
		t.Fatalf("expected game validation error naming the module, got %v", err)
	}
}

func TestBuild_IndexesHTTPRoutesSorted(t *testing.T) {
	h := http.NotFoundHandler()
	factories := map[string]Factory{
		"zeta":  func(_ Deps) Module { return Module{HTTP: []Route{{Pattern: "/games/zeta/", Handler: h}}} },
		"alpha": func(_ Deps) Module { return Module{HTTP: []Route{{Pattern: "/games/alpha/", Handler: h}}} },
	}
	reg, err := Build([]string{"zeta", "alpha"}, factories, newProvider(), BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	routes := reg.HTTPRoutes()
	if len(routes) != 2 || routes[0].Pattern != "/games/alpha/" || routes[1].Pattern != "/games/zeta/" {
		t.Fatalf("HTTPRoutes = %+v, want alpha then zeta", routes)
	}
}

func TestBuild_RejectsRouteOutsideModuleSubtree(t *testing.T) {
	h := http.NotFoundHandler()
	for _, pattern := range []string{"/", "/games/other/", "/games/alpha", "GET /games/alpha/", "example.com/games/alpha/", ""} {
		factories := map[string]Factory{
			"alpha": func(_ Deps) Module { return Module{HTTP: []Route{{Pattern: pattern, Handler: h}}} },
		}
		if _, err := Build([]string{"alpha"}, factories, newProvider(), BuildOptions{}); err == nil {
			t.Errorf("pattern %q: expected validation error", pattern)
		}
	}
}

func TestBuild_RejectsDuplicateRoute(t *testing.T) {
	h := http.NotFoundHandler()
	factories := map[string]Factory{
		"alpha": func(_ Deps) Module {
			return Module{HTTP: []Route{{Pattern: "/games/alpha/", Handler: h}, {Pattern: "/games/alpha/", Handler: h}}}
		},
	}
	_, err := Build([]string{"alpha"}, factories, newProvider(), BuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "route conflict") {
		t.Fatalf("expected route conflict, got %v", err)
	}
}
