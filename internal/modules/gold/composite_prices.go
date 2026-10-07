package gold

import (
	"context"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// sjcPriceFetcher is the subset of VNAppMobClient needed by the composite
// fetcher. It is implemented by *VNAppMobClient and by test stubs.
type sjcPriceFetcher interface {
	FetchSJCPrice(ctx context.Context) (buy, sell float64, err error)
}

// compositePriceFetcher uses VNAppMob SJC prices only. If VNAppMob fails, the
// error is surfaced to the user; there is no fallback source.
type compositePriceFetcher struct {
	vnappmob sjcPriceFetcher
}

// NewCompositePriceFetcher builds the production price fetcher using the coded
// VNAppMob endpoint.
func NewCompositePriceFetcher(coll storage.Collection) priceFetcher {
	return &compositePriceFetcher{
		vnappmob: NewVNAppMobClientFromEnv(coll),
	}
}

// FetchLuongPrice returns the SJC mid price. It errors when VNAppMob is
// unavailable so callers can show a failure message.
func (f *compositePriceFetcher) FetchLuongPrice(ctx context.Context) (float64, error) {
	buy, sell, err := f.FetchLuongPrices(ctx)
	if err != nil {
		return 0, err
	}
	return (buy + sell) / 2, nil
}

// FetchLuongPrices returns SJC buy/sell VND/lượng from VNAppMob.
func (f *compositePriceFetcher) FetchLuongPrices(ctx context.Context) (float64, float64, error) {
	return f.vnappmob.FetchSJCPrice(ctx)
}

// FetchPrice returns a GoldPrice with SJC buy/sell data and Source
// "vnappmob-sjc". It errors when VNAppMob is unavailable.
func (f *compositePriceFetcher) FetchPrice(ctx context.Context) (GoldPrice, error) {
	buy, sell, err := f.vnappmob.FetchSJCPrice(ctx)
	if err != nil {
		return GoldPrice{}, err
	}
	mid := (buy + sell) / 2
	return GoldPrice{
		VNDPerLuong: mid,
		Source:      "vnappmob-sjc",
		SJC:         &SJCPrice{Buy: buy, Sell: sell},
	}, nil
}
