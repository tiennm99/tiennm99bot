// Package gold is an SJC gold paper-trading module: users top up a virtual VND
// balance and buy or sell gold by the lượng at live SJC quotes from VNAppMob,
// buying at the SJC sell price and selling at the SJC buy price.
package gold

import (
	"github.com/tiennm99/tiennm99bot/internal/modules"
)

// New is the gold paper-trading module factory. It keeps its portfolio state
// separate from the stock and coin modules.
func New(deps modules.Deps) modules.Module {
	s := newState(deps.Store)
	return modules.Module{
		Commands: []modules.Command{
			{
				Name:        "gold_price",
				Visibility:  modules.VisibilityPublic,
				Description: "Show current SJC gold buy/sell price",
				Handler:     s.handlePrice,
			},
			{
				Name:        "gold_topup",
				Visibility:  modules.VisibilityPublic,
				Description: "Top up VND to your gold account",
				Parameters:  "<vnd_amount>",
				Handler:     s.handleTopup,
			},
			{
				Name:        "gold_buy",
				Visibility:  modules.VisibilityPublic,
				Description: "Buy gold at SJC sell price",
				Parameters:  "<luong>",
				Handler:     s.handleBuy,
			},
			{
				Name:        "gold_sell",
				Visibility:  modules.VisibilityPublic,
				Description: "Sell gold at SJC buy price",
				Parameters:  "<luong>",
				Handler:     s.handleSell,
			},
			{
				Name:        "gold_portfolio",
				Visibility:  modules.VisibilityPublic,
				Description: "Show gold portfolio with P&L",
				Handler:     s.handleStats,
			},
		},
	}
}
