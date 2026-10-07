package stock

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

const (
	stockInfoCompanyLimit  = 240
	stockInfoExchangeLimit = 40
	stockInfoReplyLimit    = 3990
)

func (s *state) handleStockInfo(ctx context.Context, b *bot.Bot, update *models.Update) error {
	args := argsAfterCommand(update.Message.Text)
	if len(args) != 1 {
		return chathelper.Reply(ctx, b, update.Message, "Usage: /stock_info <ticker>")
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
	quote, err := s.prices.fetchSSIQuote(fetchCtx, symbol)
	if err != nil {
		if errors.Is(err, ErrNoPrice) {
			return chathelper.Reply(ctx, b, update.Message, "No stock information available for "+symbol+".")
		}
		log.Error("stock_fetch_info", "ticker", symbol, "err", err)
		return chathelper.Reply(ctx, b, update.Message, "Could not fetch stock information for "+symbol+". Try again later.")
	}
	return chathelper.Reply(ctx, b, update.Message, formatStockInfo(symbol, quote))
}

func formatStockInfo(symbol string, quote ssiStockQuoteDetail) string {
	title := symbol
	if company := stockInfoCompanyName(quote); company != "" {
		title += " — " + company
	}

	reply := strings.Join([]string{
		title,
		"Exchange: " + stockInfoExchange(quote.Exchange),
		"Current: " + stockInfoPrice(quote.MatchedPrice),
		"Since open: " + stockInfoChange(quote.MatchedPrice, quote.OpenPrice),
		"Vs reference: " + stockInfoChange(quote.MatchedPrice, quote.RefPrice),
		"Open: " + stockInfoPrice(quote.OpenPrice),
		"High: " + stockInfoPrice(quote.Highest),
		"Low: " + stockInfoPrice(quote.Lowest),
		"Volume: " + stockInfoVolume(quote.NMTotalTradedQty),
	}, "\n")
	return truncateRunes(reply, stockInfoReplyLimit)
}

func stockInfoCompanyName(quote ssiStockQuoteDetail) string {
	if name := strings.TrimSpace(quote.CompanyNameVi); name != "" {
		return truncateRunes(name, stockInfoCompanyLimit)
	}
	return truncateRunes(strings.TrimSpace(quote.CompanyNameEn), stockInfoCompanyLimit)
}

func stockInfoExchange(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "N/A"
	}
	return truncateRunes(value, stockInfoExchangeLimit)
}

func stockInfoPrice(value float64) string {
	if !isPositiveFiniteCost(value) {
		return "N/A"
	}
	return FormatVND(value)
}

func stockInfoVolume(value *float64) string {
	if value == nil || *value < 0 || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return "N/A"
	}
	return formatVNDNumber(*value)
}

func stockInfoChange(current, baseline float64) string {
	if !isPositiveFiniteCost(current) || !isPositiveFiniteCost(baseline) {
		return "N/A"
	}
	diff := current - baseline
	percentage := diff / baseline * 100
	if math.IsNaN(diff) || math.IsInf(diff, 0) || math.IsNaN(percentage) || math.IsInf(percentage, 0) {
		return "N/A"
	}
	if diff == 0 {
		return "0 VND (0.00%)"
	}

	amount := FormatVND(diff)
	percentageText := strconv.FormatFloat(percentage, 'f', 2, 64) + "%"
	if diff > 0 {
		amount = "+" + amount
		percentageText = "+" + percentageText
	}
	return amount + " (" + percentageText + ")"
}
