package coin

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/keylock"
	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

type priceFetcher interface {
	FetchUSD(ctx context.Context, coin CoinSymbol) (CoinPrice, error)
}

type state struct {
	store  Store
	prices priceFetcher
	locks  keylock.Map
	nowFn  func() time.Time
}

func newState(store Store) *state {
	return &state{store: store, prices: NewPriceClient()}
}

func (s *state) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now()
}

func senderInfo(update *models.Update) (userID int64, ok bool) {
	msg := update.Message
	if msg == nil || msg.From == nil || msg.From.ID == 0 {
		return 0, false
	}
	return msg.From.ID, true
}

func argsAfterCommand(text string) []string {
	parts := strings.Fields(text)
	if len(parts) <= 1 {
		return nil
	}
	return parts[1:]
}

func parsePositiveFinite(s string) (float64, bool) {
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || !isPositiveFinite(n) {
		return 0, false
	}
	return n, true
}

func isPositiveFinite(n float64) bool {
	return n > 0 && !math.IsNaN(n) && !math.IsInf(n, 0)
}

func isSafeUSD(n float64) bool {
	return isPositiveFinite(n) && n <= float64(math.MaxInt64)
}

func (s *state) replyPriceError(ctx context.Context, b *bot.Bot, update *models.Update, err error) error {
	if errors.Is(err, ErrUnsupportedCoin) {
		return chathelper.Reply(ctx, b, update.Message, "Invalid coin ticker. Use 1-20 letters/numbers, with at least one letter.")
	}
	if errors.Is(err, ErrNoCoinPrice) {
		return chathelper.Reply(ctx, b, update.Message, "No coin price available.")
	}
	log.Error("coin_fetch_price", "err", err)
	return chathelper.Reply(ctx, b, update.Message, "Could not fetch coin price. Try again later.")
}
