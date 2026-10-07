// Package stock is a paper-trading module for Vietnamese stocks. Each Telegram
// user gets a virtual VND account: /stock_topup adds cash, /stock_buy and
// /stock_sell trade at the current market price, and /stock_portfolio shows
// positions with P&L. Prices come from KBS, then VCI, then SSI iBoard (see
// PriceClient); /stock_info and /stock_events read SSI directly.
//
// Each portfolio is one document keyed "user:<id>" in the module's collection
// (MongoDB or in-memory). It also retains recent SSI dividend events:
// /stock_portfolio syncs them for held tickers and offers each one that is due
// behind an inline "Apply dividend" button, while /stock_cash_dividend and
// /stock_share_dividend record a dividend manually.
package stock

import (
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// New is the stock module Factory. It registers nine public commands plus the
// callback handler behind the "Apply dividend" button.
func New(deps modules.Deps) modules.Module {
	s := newState(
		storage.Typed[Portfolio](deps.Store),
		storage.Typed[PendingDividendAction](deps.Store),
	)
	return modules.Module{
		Callbacks: []modules.Callback{{Prefix: dividendCallbackPrefix, Visibility: modules.VisibilityPublic, Handler: s.handleDividendCallback}},
		Commands: []modules.Command{
			{
				Name:        "stock_events",
				Visibility:  modules.VisibilityPublic,
				Description: "Show SSI corporate actions for a VN stock",
				Parameters:  "<ticker> [days]",
				Handler:     s.handleStockEvents,
			},
			{
				Name:        "stock_price",
				Visibility:  modules.VisibilityPublic,
				Description: "Show current VN stock price",
				Parameters:  "<ticker>",
				Handler:     s.handlePrice,
			},
			{
				Name:        "stock_info",
				Visibility:  modules.VisibilityPublic,
				Description: "Show detailed SSI quote for a VN stock",
				Parameters:  "<ticker>",
				Handler:     s.handleStockInfo,
			},
			{
				Name:        "stock_topup",
				Visibility:  modules.VisibilityPublic,
				Description: "Top up VND to your stock account",
				Parameters:  "<vnd_amount>",
				Handler:     s.handleTopup,
			},
			{
				Name:        "stock_buy",
				Visibility:  modules.VisibilityPublic,
				Description: "Buy VN stock at market price",
				Parameters:  "<quantity> <ticker>",
				Handler:     s.handleBuy,
			},
			{
				Name:        "stock_sell",
				Visibility:  modules.VisibilityPublic,
				Description: "Sell VN stock back to VND",
				Parameters:  "<quantity> <ticker>",
				Handler:     s.handleSell,
			},
			{
				Name:        "stock_cash_dividend",
				Visibility:  modules.VisibilityPublic,
				Description: "Record cash dividend",
				Parameters:  "<vnd_per_share> <ticker>",
				Handler:     s.handleCashDividend,
			},
			{
				Name:        "stock_share_dividend",
				Visibility:  modules.VisibilityPublic,
				Description: "Record share dividend",
				Parameters:  "<ratio(owned:new)> <ticker>",
				Handler:     s.handleShareDividend,
			},
			{
				Name:        "stock_portfolio",
				Visibility:  modules.VisibilityPublic,
				Description: "Show stock portfolio with P&L",
				Handler:     s.handleStats,
			},
		},
	}
}
