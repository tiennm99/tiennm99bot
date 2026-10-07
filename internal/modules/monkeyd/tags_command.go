package monkeyd

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules/monkeyd/crawler"
	"github.com/tiennm99/tiennm99bot/internal/modules/monkeyd/export"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

// tagsCommandName is the command that reports a novel's tags.
const tagsCommandName = "monkeyd_tags"

// tagsParameters is the display syntax shared by the command menu, /help, and
// the usage text.
const tagsParameters = "<url>"

const tagsUsage = "Usage: /" + tagsCommandName + " " + tagsParameters

const (
	// tagsFetchTimeout bounds the tag fetch. Unlike an export this is one
	// request and runs inline, and handlers are dispatched one at a time, so a
	// stalled fetch would hold up every other command until it gives up.
	tagsFetchTimeout = 30 * time.Second

	// tagsRetries is deliberately lower than the export's: the caller is
	// waiting on this reply, so failing early beats a long retry chain.
	tagsRetries = 1
)

// tagsFetcher returns a novel's tags. It is injected through newModule rather
// than called directly so tests can exercise the command without network
// access.
type tagsFetcher func(ctx context.Context, novelURL string) ([]string, error)

func tagsCommand(fetch tagsFetcher) modules.Command {
	return modules.Command{
		Name:        tagsCommandName,
		Visibility:  modules.VisibilityPublic,
		Description: "Show a " + AllowedHostsHint + " novel's tags as hashtags",
		Parameters:  tagsParameters,
		Handler:     tagsHandler(fetch),
	}
}

func tagsHandler(fetch tagsFetcher) modules.CommandHandler {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) error {
		msg := update.Message
		if msg == nil {
			return nil
		}

		arg := chathelper.ArgAfterCommand(msg.Text)
		if arg == "" {
			return chathelper.Reply(ctx, b, msg, tagsUsage)
		}
		fields := strings.Fields(arg)
		if len(fields) > 1 {
			return chathelper.Reply(ctx, b, msg,
				fmt.Sprintf("%s.\n%s", capitalize(errTooManyArgs.Error()), tagsUsage))
		}
		novelURL, err := normalizeNovelURL(fields[0])
		if err != nil {
			return chathelper.Reply(ctx, b, msg,
				fmt.Sprintf("%s.\n%s", capitalize(err.Error()), tagsUsage))
		}

		// Reserve part of the handler's budget for the reply itself, then cap
		// the fetch so it cannot outlive the command.
		fetchCtx, cancel := chathelper.FetchContext(ctx)
		defer cancel()
		fetchCtx, cancelTimeout := context.WithTimeout(fetchCtx, tagsFetchTimeout)
		defer cancelTimeout()

		tags, err := fetch(fetchCtx, novelURL)
		if err != nil {
			log.Error("monkeyd tags fetch failed",
				"command", tagsCommandName, "url", novelURL, "err", err)
			return chathelper.Reply(ctx, b, msg, "Could not read that novel page: "+err.Error())
		}
		if len(tags) == 0 {
			return chathelper.Reply(ctx, b, msg, "That page lists no tags.")
		}

		// Sent as a code block so the line can be copied in one tap.
		return chathelper.ReplyHTML(ctx, b, msg, monospaceBlock(tagsMessage(tags, novelURL)))
	}
}

// monospaceBlock wraps text in Telegram's <pre> block, which renders as a
// copyable code box. The content is escaped, so a tag containing < or & cannot
// break the markup.
func monospaceBlock(text string) string {
	return "<pre>" + html.EscapeString(text) + "</pre>"
}

// fetchTags reads a novel's tags from the site.
//
// It shares the export cache directory, so tags requested for a novel that was
// already exported — or an export following a tags lookup — cost no request.
func fetchTags(ctx context.Context, novelURL string) ([]string, error) {
	c := &crawler.Crawler{
		Client:   crawler.NewClient(export.DefaultDelay, tagsRetries),
		CacheDir: filepath.Join(os.TempDir(), cacheDirName),
	}
	novel, err := c.NovelInfo(ctx, novelURL)
	if err != nil {
		return nil, err
	}
	return novel.Tags, nil
}
