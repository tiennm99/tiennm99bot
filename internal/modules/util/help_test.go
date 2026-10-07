package util_test

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// helpTestNoop is a stand-in handler used only to satisfy the registry's
// non-nil-handler validator.
func helpTestNoop(_ context.Context, _ *bot.Bot, _ *models.Update) error { return nil }

// fakeFactory builds a module that exposes the supplied commands. Used to
// drive RenderHelp without touching the real util/misc factories (avoids a
// dependency back into the package under test).
func fakeFactory(name string, cmds []modules.Command) modules.Factory {
	return func(_ modules.Deps) modules.Module {
		return modules.Module{Name: name, Commands: cmds}
	}
}

func TestRenderHelp_GroupsByModuleAndSkipsNonPublic(t *testing.T) {
	cmd := func(name string, vis modules.Visibility, desc string) modules.Command {
		return modules.Command{Name: name, Visibility: vis, Description: desc, Handler: helpTestNoop}
	}
	factories := map[string]modules.Factory{
		"alpha": fakeFactory("alpha", []modules.Command{
			cmd("a_pub", modules.VisibilityPublic, "alpha public"),
			cmd("a_prot", modules.VisibilityProtected, "alpha protected"),
			cmd("a_priv", modules.VisibilityPrivate, "alpha private — must not appear"),
			cmd("a_unlisted", modules.VisibilityUnlisted, "alpha unlisted — must not appear"),
		}),
		"beta": fakeFactory("beta", []modules.Command{
			cmd("b_pub", modules.VisibilityPublic, "beta <i>desc</i>"),
			cmd("b_amp", modules.VisibilityPublic, `Tom & "Jerry"`),
		}),
	}
	reg, err := modules.Build([]string{"alpha", "beta"}, factories, storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	out := util.RenderHelp(reg)

	for _, want := range []string{
		"<b>alpha</b>",
		"<b>beta</b>",
		"/a_pub. alpha public.",
		// HTML in user descriptions must be escaped.
		"beta &lt;i&gt;desc&lt;/i&gt;",
		// Locks html.EscapeString contract: & → &amp;, " → &#34;.
		"Tom &amp; &#34;Jerry&#34;",
		// Support footer always present.
		"github.com/tiennm99/tiennm99bot",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---output---\n%s", want, out)
		}
	}
	if strings.Contains(out, "a_priv") {
		t.Errorf("output leaked private command\n---output---\n%s", out)
	}
	if strings.Contains(out, "a_unlisted") {
		t.Errorf("output leaked unlisted command\n---output---\n%s", out)
	}
	if strings.Contains(out, "a_prot") {
		t.Errorf("output leaked protected command\n---output---\n%s", out)
	}
	if strings.Contains(out, "<pre>/a_pub</pre>") || strings.Contains(out, "Eg:") {
		t.Errorf("commands without parameters should not render examples\n---output---\n%s", out)
	}
}

func TestRenderHelp_ShowsParametersWithoutExample(t *testing.T) {
	command := modules.Command{
		Name:        "buy",
		Visibility:  modules.VisibilityPublic,
		Description: "Buy & hold",
		Parameters:  "<quantity> <ticker>",
		Handler:     helpTestNoop,
	}
	reg, err := modules.Build(
		[]string{"stock"},
		map[string]modules.Factory{"stock": fakeFactory("stock", []modules.Command{command})},
		storage.NewMemoryProvider(),
		modules.BuildOptions{},
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	out := util.RenderHelp(reg)
	if !strings.Contains(out, "/buy &lt;quantity&gt; &lt;ticker&gt;. Buy &amp; hold.") {
		t.Fatalf("help missing formatted invocation and summary:\n%s", out)
	}
	if strings.Contains(out, "Eg:") || strings.Contains(out, "<code>") || strings.Contains(out, "<pre>") {
		t.Fatalf("help should not render examples:\n%s", out)
	}
}

func TestRenderHelp_ModuleOrderMatchesEnvOrder(t *testing.T) {
	cmd := func(name string) modules.Command {
		return modules.Command{Name: name, Visibility: modules.VisibilityPublic, Description: name, Handler: helpTestNoop}
	}
	factories := map[string]modules.Factory{
		"first":  fakeFactory("first", []modules.Command{cmd("f1")}),
		"second": fakeFactory("second", []modules.Command{cmd("s1")}),
	}

	// MODULES order: second,first → expect "second" section before "first".
	reg, err := modules.Build([]string{"second", "first"}, factories, storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	out := util.RenderHelp(reg)
	iSecond := strings.Index(out, "<b>second</b>")
	iFirst := strings.Index(out, "<b>first</b>")
	if iSecond < 0 || iFirst < 0 {
		t.Fatalf("missing sections; output:\n%s", out)
	}
	if iSecond >= iFirst {
		t.Errorf("expected 'second' before 'first'; got second=%d first=%d\n%s", iSecond, iFirst, out)
	}
}

func TestRenderHelp_OmitsModulesWithNoVisibleCommands(t *testing.T) {
	cmd := func(name string, vis modules.Visibility) modules.Command {
		return modules.Command{Name: name, Visibility: vis, Description: name, Handler: helpTestNoop}
	}
	factories := map[string]modules.Factory{
		"shadow": fakeFactory("shadow", []modules.Command{
			cmd("hidden_private", modules.VisibilityPrivate),
			cmd("hidden_protected", modules.VisibilityProtected),
		}),
		"visible": fakeFactory("visible", []modules.Command{
			cmd("seen", modules.VisibilityPublic),
		}),
	}
	reg, err := modules.Build([]string{"shadow", "visible"}, factories, storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	out := util.RenderHelp(reg)
	if strings.Contains(out, "<b>shadow</b>") {
		t.Errorf("module with only non-public commands should not render a section\n%s", out)
	}
	if !strings.Contains(out, "<b>visible</b>") {
		t.Errorf("visible module section missing\n%s", out)
	}
}

func TestRenderHelp_NilRegistryReturnsFooterOnly(t *testing.T) {
	out := util.RenderHelp(nil)
	if !strings.Contains(out, "no commands registered") {
		t.Errorf("nil registry should render placeholder; got:\n%s", out)
	}
	if !strings.Contains(out, "github.com/tiennm99/tiennm99bot") {
		t.Errorf("footer missing; got:\n%s", out)
	}
}

func TestRenderHelpMessages_SplitsBetweenModulesUnderTelegramLimit(t *testing.T) {
	factories := map[string]modules.Factory{}
	var order []string
	for i := range 12 {
		name := "mod" + string(rune('a'+i))
		var cmds []modules.Command
		for j := range 10 {
			cmds = append(cmds, modules.Command{
				Name:        name + "_" + string(rune('a'+j)),
				Visibility:  modules.VisibilityPublic,
				Description: strings.Repeat("long description ", 2),
				Handler:     helpTestNoop,
			})
		}
		factories[name] = fakeFactory(name, cmds)
		order = append(order, name)
	}
	reg, err := modules.Build(order, factories, storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	messages := util.RenderHelpMessages(reg)
	if len(messages) < 2 {
		t.Fatalf("messages = %d, want the help split into several", len(messages))
	}
	for i, m := range messages {
		if n := utf8.RuneCountInString(m); n > 4096 {
			t.Errorf("message %d is %d runes, over 4096", i+1, n)
		}
		if !strings.HasPrefix(m, "<b>") {
			t.Errorf("message %d does not start at a module section: %q", i+1, m[:min(len(m), 40)])
		}
		if hasFooter := strings.Contains(m, "starring the repo"); hasFooter != (i == len(messages)-1) {
			t.Errorf("message %d footer present = %v", i+1, hasFooter)
		}
	}
	if got := strings.Join(messages, "\n\n"); got != util.RenderHelp(reg) {
		t.Error("joined messages differ from RenderHelp")
	}
}

func TestRenderHelpMessages_SmallHelpIsOneMessage(t *testing.T) {
	if got := util.RenderHelpMessages(nil); len(got) != 1 {
		t.Fatalf("messages = %d, want 1", len(got))
	}
}
