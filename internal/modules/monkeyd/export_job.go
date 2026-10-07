package monkeyd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules/monkeyd/export"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

const (
	// crawlTimeout bounds one export. A long novel is roughly one request per
	// 400ms plus retries, so even a thousand chapters fits well inside this;
	// the ceiling exists to release the single export slot if the site stalls.
	crawlTimeout = 30 * time.Minute

	// uploadTimeout is separate from crawlTimeout so a slow crawl cannot eat
	// the budget needed to actually deliver the finished PDF.
	uploadTimeout = 5 * time.Minute

	// statusTimeout bounds the short progress and failure replies.
	statusTimeout = 30 * time.Second

	// maxDocumentBytes is the Bot API's upload ceiling for sendDocument.
	// Checked before opening the upload so an oversized book fails with an
	// explanation instead of a Telegram API error.
	maxDocumentBytes = 50 << 20
)

// cacheDirName is the shared page cache under the system temp directory.
// Keeping it outside the per-run directory is what makes a repeat export of the
// same novel cost no requests. It is not pruned; a container restart clears it.
const cacheDirName = "tiennm99bot-monkeyd-cache"

// export runs one crawl to completion and delivers the PDF. It is called on its
// own goroutine, detached from the Telegram handler context.
func (r *runner) export(b *bot.Bot, msg *models.Message, args crawlArgs) {
	novelURL := args.NovelURL
	defer r.end()
	// A panic here would reach no handler recover and would take the whole
	// process down with it, so contain it.
	defer func() {
		if p := recover(); p != nil {
			log.Error("monkeyd export panicked",
				"command", commandName, "url", novelURL, "panic", p, "stack", string(debug.Stack()))
			r.reportFailure(b, msg, "The export crashed. Nothing was sent.")
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), crawlTimeout)
	defer cancel()

	// The PDF is a temporary artefact: it is uploaded and then dropped.
	outDir, err := os.MkdirTemp("", "monkeyd-export-*")
	if err != nil {
		log.Error("monkeyd temp dir failed", "command", commandName, "err", err)
		r.reportFailure(b, msg, "Could not create a working directory for the export.")
		return
	}
	defer func() {
		if err := os.RemoveAll(outDir); err != nil {
			log.Warn("monkeyd temp cleanup failed", "command", commandName, "dir", outDir, "err", err)
		}
	}()

	result, err := r.exporter(ctx, export.Request{
		NovelURL: novelURL,
		OutDir:   outDir,
		// Zero means the caller gave no font size, which Export reads as
		// "use the default" — exactly the intent.
		FontSize: args.FontSize,
		CacheDir: filepath.Join(os.TempDir(), cacheDirName),
		// Per-chapter progress is one line per chapter — useful when
		// diagnosing a stuck export, too noisy for the default level.
		Log: func(format string, args ...any) {
			log.Debug("monkeyd crawl: "+fmt.Sprintf(format, args...), "command", commandName, "url", novelURL)
		},
	})
	if err != nil {
		log.Error("monkeyd export failed", "command", commandName, "url", novelURL, "err", err)
		r.reportFailure(b, msg, "Export failed: "+err.Error())
		return
	}

	log.Info("monkeyd export done", "command", commandName, "url", novelURL,
		"title", result.Title, "chapters", result.Chapters, "words", result.Words)

	if err := sendPDF(b, msg, result, effectiveFontSize(args.FontSize)); err != nil {
		log.Error("monkeyd delivery failed", "command", commandName, "url", novelURL, "err", err)
		r.reportFailure(b, msg, "The novel was exported but the PDF could not be sent: "+err.Error())
	}
}

// effectiveFontSize resolves what the renderer actually used, so the caption
// can report a real number rather than "default".
func effectiveFontSize(requested float64) float64 {
	if requested == 0 {
		return export.DefaultFontSize
	}
	return requested
}

// sendPDF uploads the finished book as a Telegram document.
func sendPDF(b *bot.Bot, msg *models.Message, result *export.Result, fontSize float64) error {
	info, err := os.Stat(result.Path)
	if err != nil {
		return fmt.Errorf("stat pdf: %w", err)
	}
	if info.Size() > maxDocumentBytes {
		return fmt.Errorf("the PDF is %.1f MB, above Telegram's %d MB limit for bots",
			float64(info.Size())/(1<<20), maxDocumentBytes>>20)
	}

	file, err := os.Open(result.Path)
	if err != nil {
		return fmt.Errorf("open pdf: %w", err)
	}
	defer func() { _ = file.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()

	// MessageThreadID keeps the document in the forum topic the command came
	// from, matching chathelper.Reply's behaviour.
	_, err = b.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID:          msg.Chat.ID,
		MessageThreadID: msg.MessageThreadID,
		Document: &models.InputFileUpload{
			Filename: filepath.Base(result.Path),
			Data:     file,
		},
		Caption: caption(result, fontSize),
	})
	return err
}

// captionLimit is the Bot API's maximum caption length in characters.
const captionLimit = 1024

// caption describes the book under the document. Plain text, so a title
// containing markup characters needs no escaping.
func caption(result *export.Result, fontSize float64) string {
	text := fmt.Sprintf("%s\n%s page (%.0f x %.0f mm), %gpt\n%s",
		result.Summary(), result.Page.Name, result.Page.W, result.Page.H, fontSize, result.SourceURL)
	if runes := []rune(text); len(runes) > captionLimit {
		text = string(runes[:captionLimit])
	}
	return text
}

// reportFailure tells the chat the export did not produce a document. Delivery
// failures here are only logged — there is nothing further to fall back to.
func (r *runner) reportFailure(b *bot.Bot, msg *models.Message, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	if err := chathelper.Reply(ctx, b, msg, text); err != nil {
		log.Error("monkeyd failure reply failed", "command", commandName, "err", err)
	}
}
