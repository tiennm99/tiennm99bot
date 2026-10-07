// Package deploynotify sends a Telegram DM to the bot owner on every startup,
// announcing the running version (commit SHA). Mirrors the store-scraper-bot
// behaviour: fire once per boot, no dedup. The SHA comes from the SOURCE_COMMIT
// runtime env (Coolify injects it); when unknown the caller passes "unknown"
// rather than silencing the notice.
//
// Wiring: cmd/server/main.go calls Run after the webhook is cleared. Run is
// fire-and-forget — it never returns an error and never panics.
package deploynotify

import (
	"context"
	"fmt"
	"time"

	"github.com/go-telegram/bot"

	"github.com/tiennm99/tiennm99bot/internal/log"
)

// defaultTimeout caps the whole Run path (Telegram send) so a misbehaving
// network can never block process init for long.
const defaultTimeout = 3 * time.Second

// Config bundles the runtime dependencies. Sender is a seam for tests; when
// nil, Run falls back to bot.SendMessage.
type Config struct {
	Bot     *bot.Bot
	OwnerID int64
	GitSHA  string
	Timeout time.Duration
	// Sender is the indirection used by tests. Production wiring leaves it
	// nil and Run uses cfg.Bot.SendMessage.
	Sender func(ctx context.Context, chatID int64, text string) error
}

// Run sends the startup DM. Fire-and-forget — never returns an error and never
// panics. Sends on every call (no dedup): one DM per process start.
func Run(ctx context.Context, cfg Config) {
	if reason := skipReason(cfg); reason != "" {
		log.Info("deploynotify skip", "reason", reason)
		return
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := sendMessage(ctx, cfg, renderMessage(cfg.GitSHA)); err != nil {
		log.Warn("deploynotify telegram send failed", "err", err, "owner", cfg.OwnerID)
		return
	}
	log.Info("deploynotify sent", "sha", cfg.GitSHA, "owner", cfg.OwnerID)
}

// skipReason returns a non-empty short string when Run should no-op without
// touching Telegram. Empty string ⇒ proceed. Note an empty/unknown SHA is NOT
// a skip reason — the owner still gets the startup notice.
func skipReason(cfg Config) string {
	switch {
	case cfg.OwnerID == 0:
		return "no OWNER_ID configured"
	case cfg.Bot == nil && cfg.Sender == nil:
		return "no bot or sender configured"
	}
	return ""
}

// renderMessage builds the DM text. Keep the format stable so the owner can
// search their Telegram history by SHA.
func renderMessage(sha string) string {
	return fmt.Sprintf("🚀 tiennm99bot deployed: %s", sha)
}

// sendMessage routes through Config.Sender when set (tests); otherwise it
// calls bot.SendMessage directly. Plain text — no parse_mode — so the SHA
// renders verbatim even if it ever contained Markdown-special characters.
func sendMessage(ctx context.Context, cfg Config, text string) error {
	if cfg.Sender != nil {
		return cfg.Sender(ctx, cfg.OwnerID, text)
	}
	_, err := cfg.Bot.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: cfg.OwnerID,
		Text:   text,
	})
	return err
}
