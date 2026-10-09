package wordledaily

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
)

// tokenTTL bounds how long a Play link stays usable. The token is not tied
// to a puzzle: the page always plays the puzzle current at request time.
const tokenTTL = 6 * time.Hour

// maxNameRunes caps the display name carried in a token.
const maxNameRunes = 64

// claims is what a game token asserts: who pressed Play, on which card,
// until when. Exactly one address is set: ChatID+MessageID for a card the
// bot sent into a chat (ThreadID is its forum topic), or InlineID for one
// sent via the bot.
type claims struct {
	UserID    int64  `json:"u"`
	Name      string `json:"n,omitempty"`
	ChatID    int64  `json:"c,omitempty"`
	MessageID int    `json:"m,omitempty"`
	InlineID  string `json:"i,omitempty"`
	ThreadID  int    `json:"t,omitempty"`
	Expiry    int64  `json:"e"`
}

// Valid requires a player, an expiry and exactly one card address.
func (c claims) Valid() bool {
	if c.UserID <= 0 || c.Expiry <= 0 {
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
func (c claims) ExpiresAt() int64 { return c.Expiry }

func (s *service) signToken(c claims) (string, error) { return htmlgame.Sign(s.tokenKey, c) }

func (s *service) verifyToken(token string, now time.Time) (claims, error) {
	var c claims
	if err := htmlgame.Verify(s.tokenKey, token, now, &c); err != nil {
		return claims{}, err
	}
	return c, nil
}

// displayName joins a Telegram first and last name within maxNameRunes.
func displayName(first, last string) string {
	name := strings.TrimSpace(strings.TrimSpace(first) + " " + strings.TrimSpace(last))
	name = strings.Join(strings.Fields(name), " ")
	if utf8.RuneCountInString(name) > maxNameRunes {
		name = string([]rune(name)[:maxNameRunes])
	}
	return name
}
