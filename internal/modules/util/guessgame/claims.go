package guessgame

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
)

// TokenTTL bounds how long a Play link stays usable. The token is not tied
// to a puzzle or a round: the page always plays the one current at request
// time.
const TokenTTL = 6 * time.Hour

// MaxNameRunes caps the display name carried in a token.
const MaxNameRunes = 64

// Card modes a token carries. The empty mode is daily, so every token
// signed before unlimited mode existed still opens the daily puzzle.
const (
	ModeDaily     = ""
	ModeUnlimited = "u"
)

// Claims is what a game token asserts: who pressed Play, on which card, in
// which mode, until when. Exactly one address is set: ChatID+MessageID for
// a card the bot sent into a chat (ThreadID is its forum topic), or
// InlineID for one sent via the bot.
type Claims struct {
	UserID    int64  `json:"u"`
	Name      string `json:"n,omitempty"`
	ChatID    int64  `json:"c,omitempty"`
	MessageID int    `json:"m,omitempty"`
	InlineID  string `json:"i,omitempty"`
	ThreadID  int    `json:"t,omitempty"`
	Expiry    int64  `json:"e"`
	Mode      string `json:"md,omitempty"`
}

// Valid requires a player, an expiry, a known mode and exactly one card
// address.
func (c Claims) Valid() bool {
	if c.UserID <= 0 || c.Expiry <= 0 || (c.Mode != ModeDaily && c.Mode != ModeUnlimited) {
		return false
	}
	chat := c.ChatID != 0 && c.MessageID != 0
	inline := c.InlineID != ""
	if c.ThreadID != 0 && !chat {
		return false
	}
	return chat != inline
}

// ExpiresAt is the unix second the token stops working.
func (c Claims) ExpiresAt() int64 { return c.Expiry }

// Unlimited reports whether the token opens the player's unlimited round.
func (c Claims) Unlimited() bool { return c.Mode == ModeUnlimited }

// SignToken signs c with key.
func SignToken(key []byte, c Claims) (string, error) { return htmlgame.Sign(key, c) }

// VerifyToken checks token's signature and expiry under key.
func VerifyToken(key []byte, token string, now time.Time) (Claims, error) {
	var c Claims
	if err := htmlgame.Verify(key, token, now, &c); err != nil {
		return Claims{}, err
	}
	return c, nil
}

// DisplayName joins a Telegram first and last name within MaxNameRunes.
func DisplayName(first, last string) string {
	name := strings.TrimSpace(strings.TrimSpace(first) + " " + strings.TrimSpace(last))
	name = strings.Join(strings.Fields(name), " ")
	if utf8.RuneCountInString(name) > MaxNameRunes {
		name = string([]rune(name)[:MaxNameRunes])
	}
	return name
}

// PlayClaims addresses the card: the message itself when the bot can see
// it (with its forum topic), the inaccessible message's chat and id
// otherwise, and the inline message id for a card sent via the bot. form is
// "" when none is present.
func PlayClaims(q *models.CallbackQuery) (Claims, string) {
	c := Claims{UserID: q.From.ID, Name: DisplayName(q.From.FirstName, q.From.LastName)}
	switch {
	case q.Message.Message != nil:
		m := q.Message.Message
		c.ChatID, c.MessageID = m.Chat.ID, m.ID
		if m.IsTopicMessage {
			c.ThreadID = m.MessageThreadID
		}
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

// AnswerAlert answers a callback query with an alert.
func AnswerAlert(ctx context.Context, b *bot.Bot, id, text string) error {
	_, err := b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: id, Text: text, ShowAlert: true})
	return err
}
