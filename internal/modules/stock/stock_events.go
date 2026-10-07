package stock

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

const (
	stockEventsDefaultDays = 30
	stockEventsMaxDays     = 90
	stockEventsReplyLimit  = 4000
	stockEventTitleLimit   = 500
)

// SSIStockEventProvider returns SSI corporate-action fields for display. It is
// separate from DividendEventProvider and does not imply portfolio semantics.
type SSIStockEventProvider interface {
	FetchStockEvents(ctx context.Context, symbol string, after, through time.Time) ([]SSIStockEvent, error)
}

// SSIStockEvent preserves SSI's raw corporate-action fields. cursorAt is used
// only for range filtering and deterministic ordering; it is not displayed in
// place of the source strings.
type SSIStockEvent struct {
	CorID            string
	Symbol           string
	EventListCode    string
	EventName        string
	EventTitle       string
	EventDescription string
	PublicDate       string
	ExrightDate      string
	RecordDate       string
	IssueDate        string
	Value            string
	Ratio            string
	SourceURL        string

	cursorAt time.Time
}

func (s *state) handleStockEvents(ctx context.Context, b *bot.Bot, update *models.Update) error {
	args := argsAfterCommand(update.Message.Text)
	if len(args) < 1 || len(args) > 2 {
		return chathelper.Reply(ctx, b, update.Message, "Usage: /stock_events <ticker> [days]")
	}

	days := stockEventsDefaultDays
	if len(args) == 2 {
		parsed, err := strconv.Atoi(args[1])
		if err != nil || parsed < 1 || parsed > stockEventsMaxDays {
			return chathelper.Reply(ctx, b, update.Message, "Days must be a whole number from 1 to 90.")
		}
		days = parsed
	}

	symbol, err := normalizeStockSymbol(args[0])
	if err != nil {
		if errors.Is(err, ErrUnknownTicker) {
			return chathelper.Reply(ctx, b, update.Message, "Unknown stock ticker \""+strings.ToUpper(args[0])+"\".")
		}
		return chathelper.Reply(ctx, b, update.Message, "Could not parse that ticker. Try again later.")
	}

	through := s.now()
	after := through.Add(-time.Duration(days) * 24 * time.Hour)
	fetchCtx, cancel := chathelper.FetchContext(ctx)
	defer cancel()
	events, err := s.events.FetchStockEvents(fetchCtx, symbol, after, through)
	if err != nil {
		log.Error("stock_fetch_events", "ticker", symbol, "days", days, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not fetch stock events for "+symbol+". Try again later.")
	}
	if len(events) == 0 {
		return chathelper.Reply(ctx, b, update.Message, fmt.Sprintf("No stock events found for %s in the last %d days.", symbol, days))
	}

	blocks := make([]string, 0, len(events))
	for _, event := range events {
		blocks = append(blocks, formatStockEvent(event))
	}
	for _, reply := range chunkStockEventReplies(symbol, blocks) {
		if err := chathelper.Reply(ctx, b, update.Message, reply); err != nil {
			return err
		}
	}
	return nil
}

func formatStockEvent(event SSIStockEvent) string {
	code := truncateRunes(strings.TrimSpace(event.EventListCode), 80)
	if code == "" {
		code = "Corporate action"
	}
	lines := []string{event.Symbol + " SSI event · " + code}
	if name := truncateRunes(strings.TrimSpace(event.EventName), 240); name != "" {
		lines = append(lines, "Name: "+name)
	}
	if title := truncateRunes(strings.TrimSpace(event.EventTitle), stockEventTitleLimit); title != "" {
		lines = append(lines, "Title: "+title)
	}
	if description := truncateRunes(strings.TrimSpace(event.EventDescription), 700); description != "" {
		lines = append(lines, "Description: "+description)
	}
	if value := truncateRunes(strings.TrimSpace(event.Value), 160); value != "" {
		lines = append(lines, "Value: "+value)
	}
	if ratio := truncateRunes(strings.TrimSpace(event.Ratio), 160); ratio != "" {
		lines = append(lines, "Ratio: "+ratio)
	}
	for _, field := range []struct {
		label string
		value string
	}{
		{"Published", event.PublicDate},
		{"Ex-right", event.ExrightDate},
		{"Record", event.RecordDate},
		{"Issue/payment", event.IssueDate},
	} {
		if value := truncateRunes(strings.TrimSpace(field.value), 100); value != "" {
			lines = append(lines, field.label+": "+value)
		}
	}
	lines = append(lines, "SSI event: "+event.CorID)
	if source := strings.TrimSpace(event.SourceURL); source != "" {
		lines = append(lines, "Source: "+source)
	}
	return strings.Join(lines, "\n")
}

func chunkStockEventReplies(symbol string, blocks []string) []string {
	// Reserve enough space for the heading and part numbering so every final
	// reply remains below Telegram's 4000-character safety margin.
	const bodyLimit = stockEventsReplyLimit - 80
	chunks := make([]string, 0, 1)
	current := ""
	for _, block := range blocks {
		if utf8.RuneCountInString(block) > bodyLimit {
			block = truncateRunes(block, bodyLimit-16) + "\n…(truncated)"
		}
		candidate := block
		if current != "" {
			candidate = current + "\n\n" + block
		}
		if utf8.RuneCountInString(candidate) > bodyLimit && current != "" {
			chunks = append(chunks, current)
			current = block
		} else {
			current = candidate
		}
	}
	if current != "" {
		chunks = append(chunks, current)
	}

	replies := make([]string, len(chunks))
	for i, chunk := range chunks {
		heading := symbol + " events"
		if len(chunks) > 1 {
			heading += fmt.Sprintf(" (%d/%d)", i+1, len(chunks))
		}
		replies[i] = heading + "\n\n" + chunk
	}
	return replies
}
