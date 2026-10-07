package coin

import (
	"context"
	"errors"
	"strconv"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

var (
	errInsufficientUSD  = errors.New("coin: insufficient USD")
	errInsufficientCoin = errors.New("coin: insufficient coin")
)

func (s *state) handlePrice(ctx context.Context, b *bot.Bot, update *models.Update) error {
	args := argsAfterCommand(update.Message.Text)
	if len(args) != 1 {
		return chathelper.Reply(ctx, b, update.Message, "Usage: /coin_price <coin>\nEg: /coin_price BTC")
	}
	coin, err := ResolveCoinSymbol(args[0])
	if err != nil {
		return s.replyPriceError(ctx, b, update, err)
	}
	price, err := s.prices.FetchUSD(ctx, coin)
	if err != nil {
		return s.replyPriceError(ctx, b, update, err)
	}
	return chathelper.Reply(ctx, b, update.Message,
		coin.Symbol+" price: "+FormatUSD(price.USD)+" ("+price.Source+")")
}

func (s *state) handleTopup(ctx context.Context, b *bot.Bot, update *models.Update) error {
	userID, ok := senderInfo(update)
	if !ok {
		return chathelper.Reply(ctx, b, update.Message,
			"Cannot identify user - coin only works in private/group chats with a sender.")
	}
	args := argsAfterCommand(update.Message.Text)
	if len(args) != 1 {
		return chathelper.Reply(ctx, b, update.Message, "Usage: /coin_topup <usd_amount>\nEg: /coin_topup 1000")
	}
	amount, ok := parsePositiveFinite(args[0])
	if !ok || !isSafeUSD(amount) {
		return chathelper.Reply(ctx, b, update.Message, "Amount must be a positive finite USD number within the supported range.")
	}
	defer s.locks.Acquire(strconv.FormatInt(userID, 10))()
	now := s.now().UnixMilli()
	p, err := UpdatePortfolio(ctx, s.store, userID, now, func(p *Portfolio) error {
		p.AddUSD(amount)
		p.Meta.Invested += amount
		return nil
	})
	if err != nil {
		log.Error("coin_save_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not save coin portfolio. Try again later.")
	}
	return chathelper.Reply(ctx, b, update.Message,
		"Topped up "+FormatUSD(amount)+".\nBalance: "+FormatUSD(p.USD))
}

func (s *state) handleBuy(ctx context.Context, b *bot.Bot, update *models.Update) error {
	userID, ok := senderInfo(update)
	if !ok {
		return chathelper.Reply(ctx, b, update.Message,
			"Cannot identify user - coin only works in private/group chats with a sender.")
	}
	args := argsAfterCommand(update.Message.Text)
	if len(args) != 2 {
		return chathelper.Reply(ctx, b, update.Message, "Usage: /coin_buy <coin> <usd_to_spend>\nSpend USD to buy coin.\nAlternative: /coin_buy <usd_to_spend> <coin>\nEg: /coin_buy BTC 10")
	}
	parsed, err := parseCoinValueArgs(args, isSafeUSD, errInvalidUSDAmount)
	if errors.Is(err, errInvalidUSDAmount) {
		return chathelper.Reply(ctx, b, update.Message, "USD amount must be a positive finite number within the supported range.")
	}
	if err != nil {
		return s.replyPriceError(ctx, b, update, err)
	}
	coin := parsed.coin
	amount := parsed.value
	price, err := s.prices.FetchUSD(ctx, coin)
	if err != nil {
		return s.replyPriceError(ctx, b, update, err)
	}
	if !isPositiveFinite(price.USD) {
		return s.replyPriceError(ctx, b, update, ErrNoCoinPrice)
	}
	qty := amount / price.USD
	if !isPositiveFinite(qty) || qty < coinDustEpsilon {
		return chathelper.Reply(ctx, b, update.Message, "USD amount is too small to convert to a tradeable coin quantity at the current price.")
	}
	defer s.locks.Acquire(strconv.FormatInt(userID, 10))()
	var insufficientBalance *float64
	now := s.now().UnixMilli()
	p, err := UpdatePortfolio(ctx, s.store, userID, now, func(p *Portfolio) error {
		ok, balance := p.DeductUSD(amount)
		if !ok {
			insufficientBalance = &balance
			return errInsufficientUSD
		}
		return p.BuyTicker(coin.Symbol, qty, amount)
	})
	if errors.Is(err, errInsufficientUSD) && insufficientBalance != nil {
		return chathelper.Reply(ctx, b, update.Message,
			"Insufficient USD. Need "+FormatUSD(amount)+", have "+FormatUSD(*insufficientBalance)+".")
	}
	if err != nil {
		log.Error("coin_save_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not save coin portfolio. Try again later.")
	}
	return chathelper.Reply(ctx, b, update.Message,
		"Bought "+FormatCoinQty(qty)+" "+coin.Symbol+" @ "+FormatUSD(price.USD)+" ("+price.Source+")"+
			"\nSpent: "+FormatUSD(amount)+"\nRemaining: "+FormatUSD(p.USD))
}

func (s *state) handleSell(ctx context.Context, b *bot.Bot, update *models.Update) error {
	userID, ok := senderInfo(update)
	if !ok {
		return chathelper.Reply(ctx, b, update.Message,
			"Cannot identify user - coin only works in private/group chats with a sender.")
	}
	args := argsAfterCommand(update.Message.Text)
	if len(args) != 2 {
		return chathelper.Reply(ctx, b, update.Message, "Usage: /coin_sell <coin> <usd_to_receive>\nSell enough coin to receive USD.\nAlternative: /coin_sell <usd_to_receive> <coin>\nEg: /coin_sell BTC 10")
	}
	parsed, err := parseCoinValueArgs(args, isSafeUSD, errInvalidUSDAmount)
	if errors.Is(err, errInvalidUSDAmount) {
		return chathelper.Reply(ctx, b, update.Message, "USD amount must be a positive finite number within the supported range.")
	}
	if err != nil {
		return s.replyPriceError(ctx, b, update, err)
	}
	coin := parsed.coin
	amount := parsed.value
	price, err := s.prices.FetchUSD(ctx, coin)
	if err != nil {
		return s.replyPriceError(ctx, b, update, err)
	}
	if !isPositiveFinite(price.USD) {
		return s.replyPriceError(ctx, b, update, ErrNoCoinPrice)
	}
	qty := amount / price.USD
	if !isPositiveFinite(qty) || qty < coinDustEpsilon {
		return chathelper.Reply(ctx, b, update.Message, "USD amount is too small to convert to a tradeable coin quantity at the current price.")
	}
	defer s.locks.Acquire(strconv.FormatInt(userID, 10))()
	var insufficientHeldQty float64
	var insufficientHeld bool
	var soldBasis float64
	p, err := UpdatePortfolio(ctx, s.store, userID, s.now().UnixMilli(), func(p *Portfolio) error {
		heldBefore := p.Assets[coin.Symbol].Quantity
		_, removedBase, ok, sellErr := p.SellTicker(coin.Symbol, qty)
		if !ok {
			insufficientHeldQty = heldBefore
			insufficientHeld = true
			return errInsufficientCoin
		}
		if sellErr != nil {
			return sellErr
		}
		soldBasis = removedBase
		p.AddUSD(amount)
		return nil
	})
	if errors.Is(err, errInsufficientCoin) && insufficientHeld {
		return chathelper.Reply(ctx, b, update.Message, formatInsufficientSellMessage(coin, amount, insufficientHeldQty, price.USD))
	}
	if err != nil {
		log.Error("coin_save_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not save coin portfolio. Try again later.")
	}
	return chathelper.Reply(ctx, b, update.Message,
		"Sold "+FormatCoinQty(qty)+" "+coin.Symbol+" @ "+FormatUSD(price.USD)+" ("+price.Source+")"+
			"\nReceived: "+FormatUSD(amount)+"\nRealized P&L: "+FormatPnLUSD(amount, soldBasis)+
			"\nRemaining: "+FormatUSD(p.USD))
}

func formatInsufficientSellMessage(coin CoinSymbol, requestedUSD, heldQty, priceUSD float64) string {
	heldQty = normalizeAmount(heldQty)
	if heldQty == 0 {
		return "No " + coin.Symbol + " available to sell.\nTry /coin_buy " + coin.Symbol + " <usd_to_spend> first."
	}
	availableUSD := heldQty * priceUSD
	return "Not enough " + coin.Symbol + " to sell " + FormatUSD(requestedUSD) + ".\n" +
		"Available to sell: " + FormatUSD(availableUSD) + " (" + FormatCoinQty(heldQty) + " " + coin.Symbol + " @ " + FormatUSD(priceUSD) + ").\n" +
		"Try " + FormatUSD(availableUSD) + " or less."
}
