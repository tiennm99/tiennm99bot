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
	msgNoGameTarget = "Không xác định được tin nhắn trò chơi. Hãy gửi /noitubot để chơi với bot, hoặc /noitu trong nhóm để chơi cùng nhau."
)

// handlePlay answers a Play press with the game URL carrying a signed token,
// or with an alert when the game cannot open. A press on a /noitu card
// marks the token PvP, so the page joins the card's room. Every path answers
// the query, so the client never keeps spinning.
func (s *service) handlePlay(ctx context.Context, b *bot.Bot, update *models.Update) error {
	q := update.CallbackQuery
	if !s.enabled() {
		return answerAlert(ctx, b, q.ID, msgDisabled)
	}
	c, form := playClaims(q)
	if form == "" {
		return answerAlert(ctx, b, q.ID, msgNoGameTarget)
	}
	// Cards live only in groups, whose chat IDs are negative; a private chat
	// never needs the lookup, so a storage fault cannot block its solo game.
	if c.InlineID == "" && c.ChatID < 0 {
		card, ok, err := s.lookupCard(ctx, c.ChatID, c.MessageID)
		if err != nil {
			_ = answerAlert(ctx, b, q.ID, msgCardLookup)
			return err
		}
		c.PvP, c.ThreadID = ok, card.ThreadID
	}
	c.Expiry = s.cfg.now().Add(tokenTTL).Unix()
	token, err := signToken(s.cfg.key, c)
	if err != nil {
		_ = answerAlert(ctx, b, q.ID, msgDisabled)
		return err
	}
	// The form tells whether ?game= shares arrive as inline messages; the IDs
	// themselves are never logged.
	log.Debug("noitu play", "address", form, "pvp", c.PvP)
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
