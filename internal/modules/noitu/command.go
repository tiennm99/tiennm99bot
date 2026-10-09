package noitu

import (
	"context"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

const (
	msgChannel      = "Không thể chơi nối từ trong kênh. Hãy dùng /noitu trong nhóm hoặc chat riêng với bot."
	msgSendGameFail = "Không gửi được trò chơi nối từ. Thử lại sau nhé."
)

// handleCommand sends the BotFather game. No reply markup: Telegram then adds
// the Play button itself, which is the button the game requires first.
// The command works even when the game is disabled; Play explains why.
func (s *service) handleCommand(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg.Chat.Type == models.ChatTypeChannel {
		return chathelper.Reply(ctx, b, msg, msgChannel)
	}
	_, err := b.SendGame(ctx, &bot.SendGameParams{
		ChatID:          msg.Chat.ID,
		MessageThreadID: msg.MessageThreadID,
		GameShorName:    ShortName, // the library's field name is misspelled
	})
	if err != nil {
		_ = chathelper.Reply(ctx, b, msg, msgSendGameFail)
		return err
	}
	return nil
}
