package loldle

import (
	"context"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
)

const (
	msgDisabled     = "LoLdle isn't configured on this server."
	msgNoGameTarget = "Couldn't find the game message. Send /loldle or /loldledaily to play."
	msgCardLookup   = "Couldn't open the game right now. Try again later."
)

// handlePlay answers a Play press with the game URL carrying a signed token.
// A /loldle card opens the player's unlimited round; every other card,
// /loldledaily, the 07:00 push, an inline share or an expired record, opens
// today's daily champion.
func (s *service) handlePlay(ctx context.Context, b *bot.Bot, update *models.Update) error {
	return guessgame.HandlePlay(ctx, b, update.CallbackQuery, guessgame.PlayParams{
		Enabled:       s.enabled(),
		Cards:         s.cards,
		Now:           s.cfg.now(),
		TokenKey:      s.tokenKey,
		PageURL:       s.cfg.baseURL + routePrefix,
		MsgDisabled:   msgDisabled,
		MsgNoTarget:   msgNoGameTarget,
		MsgCardLookup: msgCardLookup,
		LogName:       ShortName,
	})
}

func (s *service) signToken(c guessgame.Claims) (string, error) {
	return guessgame.SignToken(s.tokenKey, c)
}

func (s *service) verifyToken(token string, now time.Time) (guessgame.Claims, error) {
	return guessgame.VerifyToken(s.tokenKey, token, now)
}
