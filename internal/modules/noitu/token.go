package noitu

import (
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
)

// tokenTTL bounds how long a Play link stays usable.
const tokenTTL = 6 * time.Hour

// maxNameRunes caps the first name carried in a token.
const maxNameRunes = 64

var (
	errBadToken     = htmlgame.ErrBadToken
	errTokenExpired = htmlgame.ErrTokenExpired
)

// claims is what a game token asserts: who pressed Play, on which game
// message, until when. Exactly one address is set: ChatID+MessageID for a
// message the bot sent, or InlineID for one sent via the bot.
//
// PvP marks a Play press on a card /noitu registered: the page then joins
// the card's room instead of playing the bot. Only a chat message can be a
// card, and ThreadID is its forum topic, where the room posts its results.
type claims struct {
	UserID    int64  `json:"u"`
	Name      string `json:"n,omitempty"`
	ChatID    int64  `json:"c,omitempty"`
	MessageID int    `json:"m,omitempty"`
	InlineID  string `json:"i,omitempty"`
	PvP       bool   `json:"p,omitempty"`
	ThreadID  int    `json:"t,omitempty"`
	Expiry    int64  `json:"e"`
}

// Valid and ExpiresAt let htmlgame.Verify check a decoded token.
func (c claims) Valid() bool { return c.valid() }

// ExpiresAt is the unix second the token stops working.
func (c claims) ExpiresAt() int64 { return c.Expiry }

func (c claims) valid() bool {
	if c.UserID <= 0 || c.Expiry <= 0 {
		return false
	}
	chat := c.ChatID != 0 && c.MessageID != 0
	inline := c.InlineID != ""
	if c.PvP && !chat {
		return false
	}
	return chat != inline
}

// ownerKey identifies one player on one game message; a new start replaces
// the live session with the same key.
func (c claims) ownerKey() string {
	if c.InlineID != "" {
		return strconv.FormatInt(c.UserID, 10) + "|i|" + c.InlineID
	}
	return strconv.FormatInt(c.UserID, 10) + "|c|" + strconv.FormatInt(c.ChatID, 10) + "|" + strconv.Itoa(c.MessageID)
}

// truncateName keeps a first name within maxNameRunes.
func truncateName(name string) string {
	if utf8.RuneCountInString(name) <= maxNameRunes {
		return name
	}
	return string([]rune(name)[:maxNameRunes])
}

// signToken encodes claims as base64url(json) "." base64url(HMAC-SHA256).
func signToken(key []byte, c claims) (string, error) { return htmlgame.Sign(key, c) }

// verifyToken checks the signature first, then the claims and the expiry.
func verifyToken(key []byte, token string, now time.Time) (claims, error) {
	var c claims
	if err := htmlgame.Verify(key, token, now, &c); err != nil {
		return claims{}, err
	}
	return c, nil
}
