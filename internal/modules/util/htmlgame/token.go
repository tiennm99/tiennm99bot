// Package htmlgame holds the plumbing shared by the bot's Telegram HTML5
// games: the public base URL, signed Play-link tokens, the HTTP security
// headers and JSON helpers of a game's API, embedded page assets, score
// reporting with setGameScore, and a per-user request limiter.
//
// Each game keeps its own claims type, token key label, messages and routes;
// this package only owns the parts that must behave the same everywhere.
package htmlgame

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// MaxTokenBytes bounds a token before any work is spent on it.
const MaxTokenBytes = 1024

var (
	// ErrBadToken is a token that is malformed, forged or names no valid
	// game message.
	ErrBadToken = errors.New("htmlgame: bad token")
	// ErrTokenExpired is a correctly signed token past its expiry.
	ErrTokenExpired = errors.New("htmlgame: token expired")
)

// Claims is what a game decodes a token into. Valid checks the game's own
// rules (for example exactly one message address); ExpiresAt is the unix
// second the token stops working.
type Claims interface {
	Valid() bool
	ExpiresAt() int64
}

// DeriveKey is HMAC-SHA256(key=root, label). Each game and purpose uses its
// own label, so a key derived for one never verifies for another. An empty
// root yields nil.
func DeriveKey(root []byte, label string) []byte {
	if len(root) == 0 {
		return nil
	}
	mac := hmac.New(sha256.New, root)
	mac.Write([]byte(label))
	return mac.Sum(nil)
}

// Sign encodes payload as base64url(json) "." base64url(HMAC-SHA256).
func Sign(key []byte, payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + base64.RawURLEncoding.EncodeToString(tokenMAC(key, body)), nil
}

func tokenMAC(key []byte, body string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(body))
	return mac.Sum(nil)
}

// Verify checks the signature first, then decodes into dst (a pointer) and
// checks the claims and the expiry. It returns ErrBadToken or
// ErrTokenExpired.
func Verify(key []byte, token string, now time.Time, dst Claims) error {
	if len(key) == 0 || len(token) > MaxTokenBytes {
		return ErrBadToken
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return ErrBadToken
	}
	gotMAC, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(gotMAC, tokenMAC(key, body)) {
		return ErrBadToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return ErrBadToken
	}
	if err := json.Unmarshal(payload, dst); err != nil || !dst.Valid() {
		return ErrBadToken
	}
	if now.Unix() >= dst.ExpiresAt() {
		return ErrTokenExpired
	}
	return nil
}
