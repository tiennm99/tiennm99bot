package htmlgame

import (
	"context"
	"strings"

	"github.com/go-telegram/bot"

	"github.com/tiennm99/tiennm99bot/internal/telegram"
)

// Address names one game message: ChatID+MessageID for a message the bot
// sent into a chat, or InlineID for one sent via the bot.
type Address struct {
	ChatID    int64
	MessageID int
	InlineID  string
}

// ReportScore records score for userID on the game message with
// setGameScore (force=false, so Telegram keeps each player's best). A chat
// message goes through the library; an inline message needs the raw call,
// because the library types inline_message_id as an int and cannot decode
// Telegram's `true` result.
func ReportScore(ctx context.Context, b *bot.Bot, a Address, userID int64, score int) error {
	if a.InlineID != "" {
		return telegram.SetInlineGameScore(ctx, b.Token(), a.InlineID, userID, score)
	}
	_, err := b.SetGameScore(ctx, &bot.SetGameScoreParams{
		UserID:    userID,
		Score:     score,
		ChatID:    a.ChatID,
		MessageID: a.MessageID,
	})
	return err
}

// ScoreNotModified reports Telegram's answer to a score that does not beat
// the player's best. With force=false that is the expected outcome, not a
// failure.
func ScoreNotModified(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BOT_SCORE_NOT_MODIFIED")
}
