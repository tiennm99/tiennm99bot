package noitu

import (
	"context"
	"net/url"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
)

const (
	msgDisabled     = "Trò chơi nối từ chưa được cấu hình trên máy chủ này."
	msgNoGameTarget = "Không xác định được tin nhắn trò chơi. Hãy gửi /noitu để chơi."
)

// handlePlay answers a Play press with the game URL carrying a signed token,
// or with an alert when the game cannot open. Every path answers the query,
// so the client never keeps spinning.
func (s *service) handlePlay(ctx context.Context, b *bot.Bot, update *models.Update) error {
	q := update.CallbackQuery
	if !s.enabled() {
		return answerAlert(ctx, b, q.ID, msgDisabled)
	}
	c, form := playClaims(q)
	if form == "" {
		return answerAlert(ctx, b, q.ID, msgNoGameTarget)
	}
	c.Expiry = s.cfg.now().Add(tokenTTL).Unix()
	token, err := signToken(s.cfg.key, c)
	if err != nil {
		_ = answerAlert(ctx, b, q.ID, msgDisabled)
		return err
	}
	// The form tells whether ?game= shares arrive as inline messages; the IDs
	// themselves are never logged.
	log.Debug("noitu play", "address", form)
	_, err = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: q.ID,
		URL:             s.cfg.baseURL + routePrefix + "?t=" + url.QueryEscape(token),
	})
	return err
}

// playClaims addresses the game message: the message itself when the bot can
// see it, the inaccessible message's chat and id otherwise, and the inline
// message id for a game sent via the bot. form is "" when none is present.
func playClaims(q *models.CallbackQuery) (claims, string) {
	c := claims{UserID: q.From.ID, Name: truncateName(q.From.FirstName)}
	switch {
	case q.Message.Message != nil:
		c.ChatID, c.MessageID = q.Message.Message.Chat.ID, q.Message.Message.ID
		return c, "message"
	case q.Message.InaccessibleMessage != nil:
		c.ChatID, c.MessageID = q.Message.InaccessibleMessage.Chat.ID, q.Message.InaccessibleMessage.MessageID
		return c, "inaccessible_message"
	case q.InlineMessageID != "":
		c.InlineID = q.InlineMessageID
		return c, "inline_message"
	}
	return c, ""
}

func answerAlert(ctx context.Context, b *bot.Bot, id, text string) error {
	_, err := b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: id, Text: text, ShowAlert: true})
	return err
}
