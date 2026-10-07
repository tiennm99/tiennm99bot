// Package monkeyd exports a monkeydd.com novel as a PDF and sends it back as a
// Telegram document (/monkeyd_crawl), and reports a novel's genre tags as a
// copyable hashtag line (/monkeyd_tags). The crawling and rendering live in the
// monkeyd-crawler submodule (third_party/monkeyd-crawler); this module is the
// Telegram surface around it: argument validation, one-at-a-time scheduling,
// and delivery.
package monkeyd

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules/monkeyd/export"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

// commandName is the PDF export command; tagsCommandName is the other one.
const commandName = "monkeyd_crawl"

// parameters is the display syntax shared by the command menu, /help, and the
// usage text below, so the three cannot drift apart.
const parameters = "<url> [font_size]"

// usage is shown when the command arrives without usable arguments. The bounds
// and default are read from their definitions rather than restated, so the
// message stays true if either changes.
var usage = fmt.Sprintf(
	"Usage: /%s %s\nfont_size is in points, %g to %g (default %g).",
	commandName, parameters, minFontSize, maxFontSize, export.DefaultFontSize)

// New is the module Factory. The module keeps no persistent state — an export
// is a one-shot job — so deps.Store is unused.
func New(_ modules.Deps) modules.Module {
	return newModule(newRunner(), fetchTags)
}

// newModule builds the module around a given runner and tag fetcher, which is
// how tests supply a stubbed exporter, a synchronous launch, and tags without
// network access.
func newModule(r *runner, fetch tagsFetcher) modules.Module {
	return modules.Module{
		Commands: []modules.Command{
			{
				Name:       commandName,
				Visibility: modules.VisibilityPublic,
				// Public despite being expensive: one invocation makes
				// hundreds of outbound requests over several minutes. The
				// host allowlist and the single in-flight export are what
				// bound the cost, so both are load-bearing here.
				Description: "Export a " + AllowedHostsHint + " novel as a PDF",
				Parameters:  parameters,
				Handler:     r.handle,
			},
			tagsCommand(fetch),
		},
	}
}

// runner serialises exports. The crawler spaces its own requests out per run,
// so two concurrent crawls would double the request rate against the site —
// and a novel is minutes of work, which makes queueing pointless. One at a
// time, globally, with a clear reply to anyone who asks meanwhile.
type runner struct {
	mu      sync.Mutex
	running bool
	current string // novel URL of the in-flight export, for the busy reply

	// exporter is the crawl-and-render step. It is a field so tests can
	// exercise scheduling and delivery without network access.
	exporter func(context.Context, export.Request) (*export.Result, error)

	// launch runs an export job. Production detaches it onto its own
	// goroutine; tests substitute a synchronous run for determinism.
	launch func(job func())
}

func newRunner() *runner {
	return &runner{
		exporter: export.Export,
		launch: func(job func()) {
			// Detached from the handler context on purpose: handlers run one
			// at a time (the bot is built WithNotAsyncHandlers), so crawling
			// inline would block every other command for minutes. The job
			// owns its own timeout and recovers its own panics.
			go job() //nolint:gosec // G118: intentional; see runner.export
		},
	}
}

// begin claims the single export slot, reporting the in-flight URL when it is
// already taken.
func (r *runner) begin(novelURL string) (ok bool, inFlight string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return false, r.current
	}
	r.running = true
	r.current = novelURL
	return true, ""
}

func (r *runner) end() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running = false
	r.current = ""
}

func (r *runner) handle(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}

	arg := chathelper.ArgAfterCommand(msg.Text)
	if arg == "" {
		return chathelper.Reply(ctx, b, msg, usage)
	}

	args, err := parseCrawlArgs(arg)
	if err != nil {
		return chathelper.Reply(ctx, b, msg, fmt.Sprintf("%s.\n%s", capitalize(err.Error()), usage))
	}
	novelURL := args.NovelURL

	ok, inFlight := r.begin(novelURL)
	if !ok {
		return chathelper.Reply(ctx, b, msg,
			"Already exporting "+inFlight+". Try again once it finishes.")
	}

	// Reply before starting so the user knows the wait is expected. If the
	// reply cannot be delivered, drop the slot rather than crawling for a chat
	// that will never hear the result.
	if err := chathelper.Reply(ctx, b, msg,
		"Exporting "+novelURL+" — this takes a few minutes. I will send the PDF here when it is ready."); err != nil {
		r.end()
		return err
	}

	r.launch(func() { r.export(b, msg, args) })
	return nil
}

// capitalize upper-cases the first letter so a lower-case error string reads as
// a sentence in a Telegram reply.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
