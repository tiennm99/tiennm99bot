package gold

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

var (
	errInsufficientVND  = errors.New("gold: insufficient VND")
	errInsufficientGold = errors.New("gold: insufficient gold")
)

func (s *state) handlePrice(ctx context.Context, b *bot.Bot, update *models.Update) error {
	args := argsAfterCommand(update.Message.Text)
	if len(args) != 0 {
		return chathelper.Reply(ctx, b, update.Message, "Usage: /gold_price")
	}
	// Fetch under a reply-reserved sub-context (VNAppMob may refresh its API
	// key and retry, costing several round trips); reply on the original ctx so
	// delivery keeps its budget headroom.
	fetchCtx, cancel := chathelper.FetchContext(ctx)
	defer cancel()
	p, err := s.prices.FetchPrice(fetchCtx)
	if err != nil {
		return s.replyPriceError(ctx, b, update, err)
	}
	lines := goldPriceLines(p)
	return chathelper.Reply(ctx, b, update.Message, strings.Join(lines, "\n"))
}

func goldPriceLines(p GoldPrice) []string {
	return []string{
		"SJC gold price",
		"SJC buy (you sell): " + FormatVND(p.SJC.Buy) + "/luong",
		"SJC sell (you buy): " + FormatVND(p.SJC.Sell) + "/luong",
	}
}

func (s *state) handleTopup(ctx context.Context, b *bot.Bot, update *models.Update) error {
	userID, ok := senderInfo(update)
	if !ok {
		return chathelper.Reply(ctx, b, update.Message,
			"Cannot identify user - gold only works in private/group chats with a sender.")
	}
	args := argsAfterCommand(update.Message.Text)
	if len(args) != 1 {
		return chathelper.Reply(ctx, b, update.Message, "Usage: /gold_topup <vnd_amount>\nEg: /gold_topup 5000000")
	}
	amount, ok := parsePositiveFinite(args[0])
	if !ok || !isSafeVND(amount) {
		return chathelper.Reply(ctx, b, update.Message, "Amount must be a positive finite number within the supported range.")
	}

	defer s.locks.Acquire(strconv.FormatInt(userID, 10))()
	p, err := UpdatePortfolio(ctx, s.store, userID, s.now().UnixMilli(), func(p *Portfolio) error {
		p.AddVND(amount)
		p.Meta.Invested += amount
		return nil
	})
	if err != nil {
		log.Error("gold_save_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not save gold portfolio. Try again later.")
	}
	return chathelper.Reply(ctx, b, update.Message,
		"Topped up "+FormatVND(amount)+".\nBalance: "+FormatVND(p.VND))
}

func (s *state) handleBuy(ctx context.Context, b *bot.Bot, update *models.Update) error {
	userID, ok := senderInfo(update)
	if !ok {
		return chathelper.Reply(ctx, b, update.Message,
			"Cannot identify user - gold only works in private/group chats with a sender.")
	}
	args := argsAfterCommand(update.Message.Text)
	if len(args) != 1 {
		return chathelper.Reply(ctx, b, update.Message, "Usage: /gold_buy <luong>\nEg: /gold_buy 1")
	}
	qty, ok := parsePositiveFinite(args[0])
	if !ok {
		return chathelper.Reply(ctx, b, update.Message, "Luong must be a positive finite number.")
	}
	_, sellPrice, err := s.prices.FetchLuongPrices(ctx)
	if err != nil {
		return s.replyPriceError(ctx, b, update, err)
	}
	cost := qty * sellPrice
	if !isSafeVND(cost) {
		return chathelper.Reply(ctx, b, update.Message, "Trade value is too large.")
	}

	defer s.locks.Acquire(strconv.FormatInt(userID, 10))()
	var insufficientBalance *float64
	p, err := UpdatePortfolio(ctx, s.store, userID, s.now().UnixMilli(), func(p *Portfolio) error {
		ok, balance := p.DeductVND(cost)
		if !ok {
			insufficientBalance = &balance
			return errInsufficientVND
		}
		p.AddLuong(qty)
		return nil
	})
	if errors.Is(err, errInsufficientVND) && insufficientBalance != nil {
		return chathelper.Reply(ctx, b, update.Message,
			"Insufficient VND. Need "+FormatVND(cost)+", have "+FormatVND(*insufficientBalance)+".")
	}
	if err != nil {
		log.Error("gold_save_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not save gold portfolio. Try again later.")
	}
	return chathelper.Reply(ctx, b, update.Message,
		"Bought "+FormatLuong(qty)+" luong gold @ "+FormatVND(sellPrice)+"/luong (SJC sell)\nCost: "+FormatVND(cost)+
			"\nRemaining: "+FormatVND(p.VND))
}

func (s *state) handleSell(ctx context.Context, b *bot.Bot, update *models.Update) error {
	userID, ok := senderInfo(update)
	if !ok {
		return chathelper.Reply(ctx, b, update.Message,
			"Cannot identify user - gold only works in private/group chats with a sender.")
	}
	args := argsAfterCommand(update.Message.Text)
	if len(args) != 1 {
		return chathelper.Reply(ctx, b, update.Message, "Usage: /gold_sell <luong>\nEg: /gold_sell 0.5")
	}
	qty, ok := parsePositiveFinite(args[0])
	if !ok {
		return chathelper.Reply(ctx, b, update.Message, "Luong must be a positive finite number.")
	}
	buyPrice, _, err := s.prices.FetchLuongPrices(ctx)
	if err != nil {
		return s.replyPriceError(ctx, b, update, err)
	}

	defer s.locks.Acquire(strconv.FormatInt(userID, 10))()
	revenue := qty * buyPrice
	if !isSafeVND(revenue) {
		return chathelper.Reply(ctx, b, update.Message, "Trade value is too large.")
	}
	var insufficientHeld *float64
	p, err := UpdatePortfolio(ctx, s.store, userID, s.now().UnixMilli(), func(p *Portfolio) error {
		ok, held := p.DeductLuong(qty)
		if !ok {
			insufficientHeld = &held
			return errInsufficientGold
		}
		p.AddVND(revenue)
		return nil
	})
	if errors.Is(err, errInsufficientGold) && insufficientHeld != nil {
		return chathelper.Reply(ctx, b, update.Message,
			"Insufficient gold. You have: "+FormatLuong(*insufficientHeld)+" luong")
	}
	if err != nil {
		log.Error("gold_save_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not save gold portfolio. Try again later.")
	}
	return chathelper.Reply(ctx, b, update.Message,
		"Sold "+FormatLuong(qty)+" luong gold @ "+FormatVND(buyPrice)+"/luong (SJC buy)\nRevenue: "+FormatVND(revenue)+
			"\nRemaining: "+FormatVND(p.VND))
}

func (s *state) handleStats(ctx context.Context, b *bot.Bot, update *models.Update) error {
	userID, ok := senderInfo(update)
	if !ok {
		return chathelper.Reply(ctx, b, update.Message,
			"Cannot identify user - /gold_portfolio needs a sender.")
	}
	p, err := LoadPortfolio(ctx, s.store, userID, s.now().UnixMilli())
	if err != nil {
		log.Error("gold_load_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not load gold portfolio. Try again later.")
	}

	lines := []string{"Gold Account Summary\n", "VND: " + FormatVND(p.VND), "Gold: " + FormatLuong(p.Luong) + " luong"}
	totalValue := p.VND
	// Fetch under a reply-reserved sub-context; reply on the original ctx.
	fetchCtx, cancel := chathelper.FetchContext(ctx)
	defer cancel()
	if buyPrice, sellPrice, err := s.prices.FetchLuongPrices(fetchCtx); err == nil {
		goldValue := p.Luong * buyPrice
		totalValue += goldValue
		lines = append(lines, "SJC buy (you sell): "+FormatVND(buyPrice)+"/luong")
		lines = append(lines, "SJC sell (you buy): "+FormatVND(sellPrice)+"/luong")
		lines = append(lines, "Gold value (at SJC buy): "+FormatVND(goldValue))
		lines = append(lines, "Total value: "+FormatVND(totalValue))
		lines = append(lines, "Invested: "+FormatVND(p.Meta.Invested))
		lines = append(lines, "P&L: "+FormatPnL(totalValue, p.Meta.Invested))
	} else {
		lines = append(lines, "Price: no price")
		lines = append(lines, "Total value: "+FormatVND(totalValue)+" + gold holdings")
	}
	return chathelper.Reply(ctx, b, update.Message, strings.Join(lines, "\n"))
}
