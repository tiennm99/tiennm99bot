package noitu

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// tokenTTL bounds how long a Play link stays usable.
const tokenTTL = 6 * time.Hour

// maxNameRunes caps the first name carried in a token.
const maxNameRunes = 64

var (
	errBadToken     = errors.New("noitu: bad token")
	errTokenExpired = errors.New("noitu: token expired")
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
func signToken(key []byte, c claims) (string, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + base64.RawURLEncoding.EncodeToString(tokenMAC(key, body)), nil
}

func tokenMAC(key []byte, body string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(body))
	return mac.Sum(nil)
}

// verifyToken checks the signature first, then the claims and the expiry.
func verifyToken(key []byte, token string, now time.Time) (claims, error) {
	if len(key) == 0 || len(token) > 1024 {
		return claims{}, errBadToken
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return claims{}, errBadToken
	}
	gotMAC, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(gotMAC, tokenMAC(key, body)) {
		return claims{}, errBadToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return claims{}, errBadToken
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil || !c.valid() {
		return claims{}, errBadToken
	}
	if now.Unix() >= c.Expiry {
		return claims{}, errTokenExpired
	}
	return c, nil
}
