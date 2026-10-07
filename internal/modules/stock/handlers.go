package stock

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/keylock"
	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

// state is the per-module runtime. store and pending are typed views of the
// module's own collection. prices is reused across calls so its HTTP
// connection pool survives; locks serializes each user's portfolio
// read-modify-write; nowFn lets tests inject a deterministic clock.
type state struct {
	store     Store
	pending   PendingDividendStore
	prices    *PriceClient
	dividends DividendEventProvider
	events    SSIStockEventProvider
	locks     keylock.Map
	nowFn     func() time.Time
}

func (s *state) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now()
}

// newState builds the default state used by the module factory.
func newState(store Store, pending PendingDividendStore) *state {
	ssi := &SSIDividendProvider{}
	return &state{
		store:     store,
		pending:   pending,
		prices:    &PriceClient{},
		dividends: ssi,
		events:    ssi,
	}
}

// senderInfo extracts the Telegram user ID for state-keying. Channel posts
// and inline queries lack a from-user; we refuse to operate without one
// because state under "user:0" would collide across all such updates.
// Defensive against From.ID == 0 (anonymized senders / future Telegram
// schema drift) for the same reason.
//
// Chat targeting (ID + forum-topic thread) lives on update.Message — pass
// that to chathelper.Reply directly; this helper intentionally returns only
// the per-user state key.
func senderInfo(update *models.Update) (userID int64, ok bool) {
	msg := update.Message
	if msg == nil || msg.From == nil || msg.From.ID == 0 {
		return 0, false
	}
	return msg.From.ID, true
}

// argsAfterCommand splits the command body into whitespace-separated args.
// "/stock_buy 100 TCB" → ["100", "TCB"]; "/stock_topup" → []
func argsAfterCommand(text string) []string {
	parts := strings.Fields(text)
	if len(parts) <= 1 {
		return nil
	}
	return parts[1:]
}

func parsePositiveFinite(raw string) (float64, bool) {
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, false
	}
	return n, true
}

func (s *state) handlePrice(ctx context.Context, b *bot.Bot, update *models.Update) error {
	args := argsAfterCommand(update.Message.Text)
	if len(args) != 1 {
		return chathelper.Reply(ctx, b, update.Message, "Usage: /stock_price <ticker>\nEg: /stock_price TCB")
	}
	symbol, err := normalizeStockSymbol(args[0])
	if err != nil {
		if errors.Is(err, ErrUnknownTicker) {
			return chathelper.Reply(ctx, b, update.Message, "Unknown stock ticker \""+strings.ToUpper(args[0])+"\".")
		}
		return chathelper.Reply(ctx, b, update.Message, "Could not parse that ticker. Try again later.")
	}
	fetchCtx, cancel := chathelper.FetchContext(ctx)
	defer cancel()
	price, err := s.prices.FetchPrice(fetchCtx, symbol)
	if err != nil {
		if errors.Is(err, ErrNoPrice) {
			return chathelper.Reply(ctx, b, update.Message, "No price available for "+symbol+".")
		}
		log.Error("stock_fetch_price", "ticker", symbol, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not fetch price. Try again later.")
	}
	return chathelper.Reply(ctx, b, update.Message, symbol+" price: "+FormatVND(price))
}

func (s *state) handleTopup(ctx context.Context, b *bot.Bot, update *models.Update) error {
	userID, ok := senderInfo(update)
	if !ok {
		return chathelper.Reply(ctx, b, update.Message,
			"Cannot identify user — stock only works in private/group chats with a sender.")
	}
	args := argsAfterCommand(update.Message.Text)
	if len(args) != 1 {
		return chathelper.Reply(ctx, b, update.Message, "Usage: /stock_topup <vnd_amount>\nEg: /stock_topup 5000000")
	}
	amount, ok := parsePositiveFinite(args[0])
	if !ok {
		return chathelper.Reply(ctx, b, update.Message, "Amount must be a positive finite number.")
	}

	defer s.locks.Acquire(strconv.FormatInt(userID, 10))()

	now := s.now().UnixMilli()
	p, err := LoadPortfolio(ctx, s.store, userID, now)
	if err != nil {
		log.Error("stock_load_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not load portfolio. Try again later.")
	}
	p.AddVND(amount)
	p.Meta.Invested += amount
	if err := SavePortfolio(ctx, s.store, userID, p); err != nil {
		log.Error("stock_save_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not save portfolio. Try again later.")
	}
	return chathelper.Reply(ctx, b, update.Message,
		"Topped up "+FormatVND(amount)+".\nBalance: "+FormatVND(p.VND))
}

func (s *state) handleBuy(ctx context.Context, b *bot.Bot, update *models.Update) error {
	userID, ok := senderInfo(update)
	if !ok {
		return chathelper.Reply(ctx, b, update.Message,
			"Cannot identify user — stock only works in private/group chats with a sender.")
	}
	args := argsAfterCommand(update.Message.Text)
	if len(args) != 2 {
		return chathelper.Reply(ctx, b, update.Message, "Usage: /stock_buy <quantity> <ticker>\nEg: /stock_buy 100 TCB")
	}
	qty, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || qty <= 0 {
		return chathelper.Reply(ctx, b, update.Message, "Quantity must be a positive whole number.")
	}

	symbol, err := normalizeStockSymbol(args[1])
	if err != nil {
		if errors.Is(err, ErrUnknownTicker) {
			return chathelper.Reply(ctx, b, update.Message,
				"Unknown stock ticker \""+strings.ToUpper(args[1])+"\".")
		}
		return chathelper.Reply(ctx, b, update.Message, "Could not parse that ticker. Try again later.")
	}

	price, err := s.prices.FetchPrice(ctx, symbol)
	if err != nil {
		if errors.Is(err, ErrNoPrice) {
			return chathelper.Reply(ctx, b, update.Message, "No price available for "+symbol+".")
		}
		log.Error("stock_fetch_price", "ticker", symbol, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not fetch price. Try again later.")
	}
	cost := float64(qty) * price

	defer s.locks.Acquire(strconv.FormatInt(userID, 10))()

	now := s.now().UnixMilli()
	p, err := LoadPortfolio(ctx, s.store, userID, now)
	if err != nil {
		log.Error("stock_load_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not load portfolio. Try again later.")
	}
	ok, balance := p.DeductVND(cost)
	if !ok {
		return chathelper.Reply(ctx, b, update.Message,
			"Insufficient VND. Need "+FormatVND(cost)+", have "+FormatVND(balance)+".")
	}
	if err := p.BuyTicker(symbol, qty, cost, now); err != nil {
		log.Error("stock_add_cost_basis", "user", userID, "ticker", symbol, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not record purchase cost. Try again later.")
	}
	if err := SavePortfolio(ctx, s.store, userID, p); err != nil {
		log.Error("stock_save_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not save portfolio. Try again later.")
	}
	return chathelper.Reply(ctx, b, update.Message,
		"Bought "+FormatStock(float64(qty))+" "+symbol+
			" @ "+FormatVND(price)+"\nCost: "+FormatVND(cost)+
			"\nRemaining: "+FormatVND(p.VND))
}

func (s *state) handleSell(ctx context.Context, b *bot.Bot, update *models.Update) error {
	userID, ok := senderInfo(update)
	if !ok {
		return chathelper.Reply(ctx, b, update.Message,
			"Cannot identify user — stock only works in private/group chats with a sender.")
	}
	args := argsAfterCommand(update.Message.Text)
	if len(args) != 2 {
		return chathelper.Reply(ctx, b, update.Message, "Usage: /stock_sell <quantity> <ticker>\nEg: /stock_sell 100 TCB")
	}
	qty, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || qty <= 0 {
		return chathelper.Reply(ctx, b, update.Message, "Quantity must be a positive whole number.")
	}

	// Normalize + fetch price BEFORE taking the per-user lock. Mirrors handleBuy:
	// keeps the critical section to a fast Get→mutate→Put, and removes any need
	// for a rollback path (no in-memory mutation precedes the network call).
	symbol, err := normalizeStockSymbol(args[1])
	if err != nil {
		if errors.Is(err, ErrUnknownTicker) {
			return chathelper.Reply(ctx, b, update.Message,
				"Unknown stock ticker \""+strings.ToUpper(args[1])+"\".")
		}
		return chathelper.Reply(ctx, b, update.Message, "Could not parse that ticker. Try again later.")
	}
	price, err := s.prices.FetchPrice(ctx, symbol)
	if err != nil {
		if errors.Is(err, ErrNoPrice) {
			return chathelper.Reply(ctx, b, update.Message, "No price available for "+symbol+".")
		}
		log.Error("stock_fetch_price", "ticker", symbol, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not fetch price. Try again later.")
	}

	defer s.locks.Acquire(strconv.FormatInt(userID, 10))()

	p, err := LoadPortfolio(ctx, s.store, userID, s.now().UnixMilli())
	if err != nil {
		log.Error("stock_load_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not load portfolio. Try again later.")
	}
	held, soldBasis, ok, basisErr := p.SellTicker(symbol, qty)
	if !ok {
		return chathelper.Reply(ctx, b, update.Message,
			"Insufficient "+symbol+". You have: "+FormatStock(float64(held)))
	}
	revenue := float64(qty) * price
	if basisErr != nil {
		log.Error("stock_remove_cost_basis", "user", userID, "ticker", symbol, "err", basisErr)
		return chathelper.Reply(ctx, b, update.Message, "Portfolio cost basis is unavailable. Restart the bot or contact the owner.")
	}
	p.AddVND(revenue)
	if err := SavePortfolio(ctx, s.store, userID, p); err != nil {
		log.Error("stock_save_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not save portfolio. Try again later.")
	}
	return chathelper.Reply(ctx, b, update.Message,
		"Sold "+FormatStock(float64(qty))+" "+symbol+
			" @ "+FormatVND(price)+"\nRevenue: "+FormatVND(revenue)+
			"\nRealized P&L: "+FormatPnL(revenue, soldBasis)+
			"\nRemaining: "+FormatVND(p.VND))
}

func (s *state) handleCashDividend(ctx context.Context, b *bot.Bot, update *models.Update) error {
	userID, ok := senderInfo(update)
	if !ok {
		return chathelper.Reply(ctx, b, update.Message,
			"Cannot identify user — stock only works in private/group chats with a sender.")
	}
	args := argsAfterCommand(update.Message.Text)
	if len(args) != 2 {
		return chathelper.Reply(ctx, b, update.Message,
			"Usage: /stock_cash_dividend <vnd_per_share> <ticker>\nEg: /stock_cash_dividend 1500 TCB")
	}
	vndPerShare, ok := parsePositiveWhole(args[0])
	if !ok {
		return chathelper.Reply(ctx, b, update.Message, "VND per share must be a positive whole number.")
	}

	symbol, err := normalizeStockSymbol(args[1])
	if err != nil {
		if errors.Is(err, ErrUnknownTicker) {
			return chathelper.Reply(ctx, b, update.Message,
				"Unknown stock ticker \""+strings.ToUpper(args[1])+"\".")
		}
		return chathelper.Reply(ctx, b, update.Message, "Could not parse that ticker. Try again later.")
	}

	defer s.locks.Acquire(strconv.FormatInt(userID, 10))()

	p, err := LoadPortfolio(ctx, s.store, userID, s.now().UnixMilli())
	if err != nil {
		log.Error("stock_load_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not load portfolio. Try again later.")
	}
	held := p.Assets[symbol].Quantity
	if held <= 0 {
		return chathelper.Reply(ctx, b, update.Message,
			"You don't hold any "+symbol+" to receive a cash dividend.")
	}
	total, err := cashDividendTotal(held, vndPerShare)
	if err != nil {
		return chathelper.Reply(ctx, b, update.Message, "Dividend amount is too large.")
	}
	balance, err := checkedVNDBalance(p.VND, total)
	if err != nil {
		return chathelper.Reply(ctx, b, update.Message, "Dividend amount is too large.")
	}
	baseBefore := p.Assets[symbol].Base
	if err := p.ApplyCashDividend(symbol, total, balance, s.now().UnixMilli()); err != nil {
		return chathelper.Reply(ctx, b, update.Message, "Could not apply this dividend. Try again later.")
	}
	if err := SavePortfolio(ctx, s.store, userID, p); err != nil {
		log.Error("stock_save_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not save portfolio. Try again later.")
	}
	return chathelper.Reply(ctx, b, update.Message,
		"Cash dividend: "+FormatVND(float64(vndPerShare))+" × "+formatShareQuantity(held)+" "+symbol+
			" = "+FormatVND(float64(total))+
			"\nBalance: "+FormatVND(balance)+
			"\nCost basis: "+formatVNDNumber(baseBefore)+" → "+FormatVND(p.Assets[symbol].Base))
}

func (s *state) handleShareDividend(ctx context.Context, b *bot.Bot, update *models.Update) error {
	userID, ok := senderInfo(update)
	if !ok {
		return chathelper.Reply(ctx, b, update.Message,
			"Cannot identify user — stock only works in private/group chats with a sender.")
	}
	args := argsAfterCommand(update.Message.Text)
	if len(args) != 2 {
		return chathelper.Reply(ctx, b, update.Message,
			"Usage: /stock_share_dividend <ratio(owned:new)> <ticker>\nEg: /stock_share_dividend 100:10 TCB")
	}
	ratio, ok := parseShareRatio(args[0])
	if !ok {
		return chathelper.Reply(ctx, b, update.Message, "Share ratio must use positive whole numbers in owned:new form.")
	}

	symbol, err := normalizeStockSymbol(args[1])
	if err != nil {
		if errors.Is(err, ErrUnknownTicker) {
			return chathelper.Reply(ctx, b, update.Message,
				"Unknown stock ticker \""+strings.ToUpper(args[1])+"\".")
		}
		return chathelper.Reply(ctx, b, update.Message, "Could not parse that ticker. Try again later.")
	}

	defer s.locks.Acquire(strconv.FormatInt(userID, 10))()

	p, err := LoadPortfolio(ctx, s.store, userID, s.now().UnixMilli())
	if err != nil {
		log.Error("stock_load_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not load portfolio. Try again later.")
	}
	held := p.Assets[symbol].Quantity
	if held <= 0 {
		return chathelper.Reply(ctx, b, update.Message,
			"You don't hold any "+symbol+" to receive a share dividend.")
	}
	newShares, err := shareDividendEntitlement(held, ratio)
	if err != nil {
		return chathelper.Reply(ctx, b, update.Message, "Share dividend is too large.")
	}
	if newShares == 0 {
		minimum := minimumHoldingForShare(ratio)
		return chathelper.Reply(ctx, b, update.Message,
			"Share dividend "+ratio.raw+" rounds down to 0 for "+formatShareQuantity(held)+" "+symbol+". Minimum holding: "+formatShareQuantity(minimum)+".")
	}
	finalHolding, err := checkedHoldingAfterDividend(held, newShares)
	if err != nil {
		return chathelper.Reply(ctx, b, update.Message, "Share dividend is too large.")
	}
	if err := p.ApplyShareDividend(symbol, finalHolding, s.now().UnixMilli()); err != nil {
		return chathelper.Reply(ctx, b, update.Message, "Could not apply this dividend. Try again later.")
	}
	if err := SavePortfolio(ctx, s.store, userID, p); err != nil {
		log.Error("stock_save_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not save portfolio. Try again later.")
	}
	return chathelper.Reply(ctx, b, update.Message,
		"Share dividend ("+ratio.raw+"): +"+formatShareQuantity(newShares)+" "+symbol+
			"\nHolding: "+formatShareQuantity(held)+" → "+formatShareQuantity(finalHolding))
}

// handleStats serves /stock_portfolio. It renders the portfolio first, then
// synchronizes dividend history for each held ticker. Price and SSI dividend
// fetches never hold the user lock; history merging and each dividend message
// reload the portfolio under it.
func (s *state) handleStats(ctx context.Context, b *bot.Bot, update *models.Update) error {
	userID, ok := senderInfo(update)
	if !ok {
		return chathelper.Reply(ctx, b, update.Message,
			"Cannot identify user — /stock_portfolio needs a sender.")
	}
	checkedThrough := s.now()
	p, err := LoadPortfolio(ctx, s.store, userID, checkedThrough.UnixMilli())
	if err != nil {
		log.Error("stock_load_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not load portfolio. Try again later.")
	}

	var positions [][]string
	totalValue := p.VND
	totalBasis := 0.0
	missingPrice := false

	// Skip zero-quantity positions. SellTicker deletes a fully sold position and
	// Validate rejects them on load, so this is only a defensive guard.
	type held struct {
		symbol string
		qty    int64
	}
	var heldList []held
	for symbol, position := range p.Assets {
		if position.Quantity > 0 {
			heldList = append(heldList, held{symbol, position.Quantity})
		}
	}
	sort.Slice(heldList, func(i, j int) bool { return heldList[i].symbol < heldList[j].symbol })

	if len(heldList) > 0 {
		fetchCtx, cancel := chathelper.FetchContext(ctx)

		symbols := make([]string, 0, len(heldList))
		for _, h := range heldList {
			symbols = append(symbols, h.symbol)
		}
		prices, fetchErr := s.prices.FetchPrices(fetchCtx, symbols)
		cancel()
		if fetchErr != nil {
			log.Error("stock_fetch_prices", "symbols", strings.Join(symbols, ","), "err", fetchErr)
		}

		for _, h := range heldList {
			price := prices[h.symbol]
			basis := p.Assets[h.symbol].Base
			average := basis / float64(h.qty)
			if !isPositiveFiniteCost(price) {
				missingPrice = true
				positions = append(positions, []string{h.symbol, FormatStock(float64(h.qty)), formatThousandVND(average), "N/A", "N/A", "N/A", "N/A"})
				continue
			}
			val := float64(h.qty) * price
			if !isPositiveFiniteCost(val) || !isNonNegativeFiniteCost(average) {
				missingPrice = true
				positions = append(positions, []string{h.symbol, FormatStock(float64(h.qty)), "N/A", "N/A", "N/A", "N/A", "N/A"})
				continue
			}
			totalValue += val
			totalBasis += basis
			pnlAmount, pnlPercentage := formatPortfolioPositionPnL(val, basis)
			positions = append(positions, []string{h.symbol, FormatStock(float64(h.qty)), formatThousandVND(average), formatThousandVND(price), formatCompactVND(val), pnlAmount, pnlPercentage})
		}
	}
	var summary [][]string
	if missingPrice {
		summary = [][]string{
			{"Cash", formatVNDNumber(p.VND)},
			{"Priced value (partial)", formatVNDNumber(totalValue)},
			{"Unrealized P&L (priced)", formatPortfolioPnL(totalValue-p.VND, totalBasis)},
			{"Invested", formatVNDNumber(p.Meta.Invested)},
			{"Account P&L", "Unavailable"},
		}
	} else {
		summary = [][]string{
			{"Cash", formatVNDNumber(p.VND)},
			{"Total value", formatVNDNumber(totalValue)},
			{"Invested", formatVNDNumber(p.Meta.Invested)},
			{"Unrealized P&L", formatPortfolioPnL(totalValue-p.VND, totalBasis)},
			{"Account P&L", formatPortfolioPnL(totalValue, p.Meta.Invested)},
		}
	}
	if err := chathelper.ReplyHTML(ctx, b, update.Message, portfolioTableReply("Stock Portfolio", positions, summary)); err != nil {
		return err
	}
	return s.notifyDividendEvents(ctx, b, update.Message, userID, p, checkedThrough)
}

const portfolioReplyLimit = 4000

func portfolioTableReply(title string, positions, summary [][]string) string {
	omitted := 0
	for {
		rows := append([][]string{}, positions...)
		if omitted > 0 {
			rows = append(rows, []string{"… " + strconv.Itoa(omitted) + " omitted"})
		}
		reply := "<b>" + title + "</b>\n" +
			chathelper.MonospaceTable([]string{"Sym", "Qty", "Avg", "Now", "Val", "P&L", "%"}, rows) + "\n" +
			chathelper.MonospaceTable([]string{"Metric", "Value"}, summary)
		if len(reply) <= portfolioReplyLimit || len(positions) == 0 {
			return reply
		}
		positions = positions[:len(positions)-1]
		omitted++
	}
}
