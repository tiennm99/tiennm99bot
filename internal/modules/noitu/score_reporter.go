package noitu

import (
	"context"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/telegram"
)

// scoreReporter records a finished game's score on the game message.
type scoreReporter interface {
	Report(ctx context.Context, c claims, score int) error
}

// botReporter reports through the Bot API. A chat message goes through the
// library; an inline message needs the raw call, because the library types
// inline_message_id as an int and cannot decode Telegram's `true` result.
type botReporter struct {
	b *bot.Bot
}

func (r botReporter) Report(ctx context.Context, c claims, score int) error {
	if c.InlineID != "" {
		return telegram.SetInlineGameScore(ctx, r.b.Token(), c.InlineID, c.UserID, score)
	}
	_, err := r.b.SetGameScore(ctx, &bot.SetGameScoreParams{
		UserID:    c.UserID,
		Score:     score,
		ChatID:    c.ChatID,
		MessageID: c.MessageID,
	})
	return err
}

// announcer posts a finished room game's result into the card's chat.
type announcer interface {
	Announce(ctx context.Context, card claims, text string) error
}

// Announce replies to the card in its chat and forum topic, as plain text so
// player names need no escaping.
func (r botReporter) Announce(ctx context.Context, card claims, text string) error {
	_, err := r.b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          card.ChatID,
		MessageThreadID: card.ThreadID,
		Text:            text,
		ReplyParameters: &models.ReplyParameters{MessageID: card.MessageID, AllowSendingWithoutReply: true},
	})
	return err
}

// scoreNotModified reports Telegram's answer to a score that does not beat the
// player's best. With force=false that is the expected outcome, not a failure.
func scoreNotModified(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BOT_SCORE_NOT_MODIFIED")
}
