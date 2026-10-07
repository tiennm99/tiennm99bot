package gold

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
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

type priceFetcher interface {
	FetchLuongPrice(ctx context.Context) (float64, error)
	FetchLuongPrices(ctx context.Context) (buy, sell float64, err error)
	FetchPrice(ctx context.Context) (GoldPrice, error)
}

type state struct {
	store  PortfolioStore
	prices priceFetcher
	locks  keylock.Map
	nowFn  func() time.Time
}

func newState(coll storage.Collection) *state {
	return &state{
		store:  storage.Typed[Portfolio](coll),
		prices: NewCompositePriceFetcher(coll),
	}
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

func isSafeVND(n float64) bool {
	return isPositiveFinite(n) && n <= float64(math.MaxInt64)
}

func (s *state) replyPriceError(ctx context.Context, b *bot.Bot, update *models.Update, err error) error {
	if errors.Is(err, ErrNoGoldPrice) {
		return chathelper.Reply(ctx, b, update.Message, "No gold price available.")
	}
	log.Error("gold_fetch_price", "err", err)
	return chathelper.Reply(ctx, b, update.Message, "Could not fetch gold price. Try again later.")
}
