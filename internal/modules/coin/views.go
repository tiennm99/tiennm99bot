package coin

import (
	"context"
	"sort"
	"strconv"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

func (s *state) handleStats(ctx context.Context, b *bot.Bot, update *models.Update) error {
	userID, ok := senderInfo(update)
	if !ok {
		return chathelper.Reply(ctx, b, update.Message, "Cannot identify user - /coin_portfolio needs a sender.")
	}
	p, err := LoadPortfolio(ctx, s.store, userID, s.now().UnixMilli())
	if err != nil {
		log.Error("coin_load_portfolio", "user", userID, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not load coin portfolio. Try again later.")
	}
	var positions [][]string
	totalValue := p.USD
	totalBasis := 0.0
	missingPrice := false

	// Fetch sequentially (not concurrently) so the price client's keep-alive
	// connection pool is reused across coins rather than opening N simultaneous
	// TLS handshakes. The reply-reserved sub-context bounds the whole loop so the
	// final reply keeps its budget; a slow or failed provider degrades that row
	// to "N/A" and the summary to partial totals instead of failing the reply.
	fetchCtx, cancel := chathelper.FetchContext(ctx)
	defer cancel()
	for _, symbol := range sortedAssetSymbols(p.Assets) {
		held := p.Assets[symbol].Quantity
		basis := p.Assets[symbol].Base
		average := basis / held
		if coin, err := ResolveCoinSymbol(symbol); err == nil {
			if price, err := s.prices.FetchUSD(fetchCtx, coin); err == nil && isPositiveFinite(price.USD) {
				value := held * price.USD
				if !isPositiveFinite(value) || !isPositiveFinite(average) {
					missingPrice = true
					positions = append(positions, []string{symbol, FormatCoinQty(held), "N/A", "N/A", "N/A", "N/A", "N/A"})
					continue
				}
				totalValue += value
				totalBasis += basis
				pnlAmount, pnlPercentage := formatPortfolioPositionPnLUSD(value, basis)
				positions = append(positions, []string{symbol, FormatCoinQty(held), formatCompactUSD(average), formatCompactUSD(price.USD), formatCompactUSD(value), pnlAmount, pnlPercentage})
			} else {
				log.Error("coin_fetch_price", "symbol", symbol, "err", err)
				missingPrice = true
				positions = append(positions, []string{symbol, FormatCoinQty(held), formatCompactUSD(average), "N/A", "N/A", "N/A", "N/A"})
			}
		} else {
			missingPrice = true
			positions = append(positions, []string{symbol, FormatCoinQty(held), formatCompactUSD(average), "N/A", "N/A", "N/A", "N/A"})
		}
	}
	var summary [][]string
	if missingPrice {
		summary = [][]string{
			{"USD", FormatUSD(p.USD)},
			{"Priced value (partial)", FormatUSD(totalValue)},
			{"Unrealized P&L (priced)", FormatPnLUSD(totalValue-p.USD, totalBasis)},
			{"Invested", FormatUSD(p.Meta.Invested)},
			{"Account P&L", "Unavailable"},
		}
	} else {
		summary = [][]string{
			{"USD", FormatUSD(p.USD)},
			{"Total value", FormatUSD(totalValue)},
			{"Invested", FormatUSD(p.Meta.Invested)},
			{"Unrealized P&L", FormatPnLUSD(totalValue-p.USD, totalBasis)},
			{"Account P&L", FormatPnLUSD(totalValue, p.Meta.Invested)},
		}
	}
	return chathelper.ReplyHTML(ctx, b, update.Message, portfolioTableReply("Coin Portfolio", positions, summary))
}

func sortedAssetSymbols(assets map[string]AssetPosition) []string {
	symbols := make([]string, 0, len(assets))
	for symbol, position := range assets {
		if position.Quantity > 0 {
			symbols = append(symbols, symbol)
		}
	}
	sort.Strings(symbols)
	return symbols
}

// portfolioReplyLimit keeps the rendered reply under Telegram's 4096-character
// message limit with room to spare.
const portfolioReplyLimit = 4000

// portfolioTableReply renders the position and summary tables, dropping
// positions from the end (and noting how many) until the reply fits
// portfolioReplyLimit.
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
