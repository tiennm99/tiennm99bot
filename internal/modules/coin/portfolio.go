package coin

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// coinDustEpsilon is the tolerance below which balances and quantities are
// treated as zero, absorbing float rounding left over by trades.
const coinDustEpsilon = 1e-9

// portfolioUpdateAttempts bounds optimistic-write retries in UpdatePortfolio.
const portfolioUpdateAttempts = 5

// CollectionName is the module name and store collection for coin portfolios.
const CollectionName = "coin"

// Store is the coin module's typed portfolio store.
type Store = storage.DocStore[Portfolio]

// AssetPosition is one held coin: Quantity in coin units and Base, the USD
// cost basis still attributed to that quantity.
type AssetPosition struct {
	Quantity float64 `json:"quantity" bson:"quantity"`
	Base     float64 `json:"base" bson:"base"`
}

// Portfolio is one user's coin account, keyed by upper-case ticker in Assets.
type Portfolio struct {
	USD    float64                  `json:"usd" bson:"usd"`
	Assets map[string]AssetPosition `json:"assets" bson:"assets"`
	Meta   PortfolioMeta            `json:"meta" bson:"meta"`
}

// PortfolioMeta holds account-level totals: Invested is the sum of all USD
// top-ups and CreatedAt is ms since epoch.
type PortfolioMeta struct {
	Invested  float64 `json:"invested" bson:"invested"`
	CreatedAt int64   `json:"createdAt" bson:"createdAt"`
}

func NewPortfolio(now int64) Portfolio {
	return Portfolio{Assets: map[string]AssetPosition{}, Meta: PortfolioMeta{CreatedAt: now}}
}

func portfolioKey(userID int64) string { return "user:" + strconv.FormatInt(userID, 10) }

func LoadPortfolio(ctx context.Context, store Store, userID int64, now int64) (Portfolio, error) {
	p, _, err := loadPortfolioForUpdate(ctx, store, portfolioKey(userID), now)
	if err != nil {
		return Portfolio{}, fmt.Errorf("coin: load portfolio %d: %w", userID, err)
	}
	return p, nil
}

func SavePortfolio(ctx context.Context, store Store, userID int64, p Portfolio) error {
	p.normalize()
	if err := p.Validate(); err != nil {
		return fmt.Errorf("coin: save portfolio %d: %w", userID, err)
	}
	if err := store.Put(ctx, portfolioKey(userID), p); err != nil {
		return fmt.Errorf("coin: save portfolio %d: %w", userID, err)
	}
	return nil
}

// UpdatePortfolio loads the user's portfolio, applies mutate, validates it, and
// writes it back with a versioned put, retrying on write conflicts. An error
// returned by mutate aborts without saving and is passed through unwrapped.
func UpdatePortfolio(ctx context.Context, store Store, userID int64, now int64, mutate func(*Portfolio) error) (Portfolio, error) {
	key := portfolioKey(userID)
	for attempt := 0; attempt < portfolioUpdateAttempts; attempt++ {
		p, version, err := loadPortfolioForUpdate(ctx, store, key, now)
		if err != nil {
			return Portfolio{}, fmt.Errorf("coin: load portfolio %d: %w", userID, err)
		}
		if err := mutate(&p); err != nil {
			return p, err
		}
		p.normalize()
		if err := p.Validate(); err != nil {
			return Portfolio{}, err
		}
		if err := store.PutVersioned(ctx, key, version, p); err == nil {
			return p, nil
		} else if !errors.Is(err, storage.ErrConflict) {
			return Portfolio{}, fmt.Errorf("coin: save portfolio %d: %w", userID, err)
		}
	}
	return Portfolio{}, fmt.Errorf("coin: save portfolio %d: %w", userID, storage.ErrConflict)
}

func loadPortfolioForUpdate(ctx context.Context, store Store, key string, now int64) (Portfolio, int64, error) {
	p, version, err := store.Get(ctx, key)
	switch {
	case err == nil:
		if p.Assets == nil {
			p.Assets = map[string]AssetPosition{}
		}
		if p.Meta.CreatedAt == 0 {
			p.Meta.CreatedAt = now
		}
		if err := p.Validate(); err != nil {
			return Portfolio{}, 0, err
		}
		return p, version, nil
	case errors.Is(err, storage.ErrNotFound):
		return NewPortfolio(now), 0, nil
	default:
		return Portfolio{}, 0, err
	}
}

// Validate rejects a negative or non-finite USD balance and any position whose
// key is not a canonical ticker or whose quantity or basis is not positive.
func (p Portfolio) Validate() error {
	if math.IsNaN(p.USD) || math.IsInf(p.USD, 0) || p.USD < 0 {
		return fmt.Errorf("coin: invalid USD balance")
	}
	for symbol, position := range p.Assets {
		coin, err := ResolveCoinSymbol(symbol)
		if err != nil || coin.Symbol != symbol || !isPositiveFinite(position.Quantity) ||
			!isPositiveFinite(position.Base) {
			return fmt.Errorf("coin: %s has invalid position", symbol)
		}
	}
	return nil
}

func (p *Portfolio) AddUSD(amount float64) {
	p.USD += amount
	p.normalize()
}

// DeductUSD withdraws amount when the balance covers it within dust
// tolerance. It reports whether it did, plus the resulting (or unchanged)
// balance.
func (p *Portfolio) DeductUSD(amount float64) (ok bool, balance float64) {
	p.normalize()
	if p.USD+coinDustEpsilon < amount {
		return false, p.USD
	}
	p.USD = normalizeAmount(p.USD - amount)
	return true, p.USD
}

func (p *Portfolio) BuyTicker(symbol string, quantity, base float64) error {
	if !isPositiveFinite(quantity) || !isPositiveFinite(base) {
		return fmt.Errorf("coin: invalid purchase position")
	}
	if p.Assets == nil {
		p.Assets = map[string]AssetPosition{}
	}
	position := p.Assets[symbol]
	position.Quantity += quantity
	position.Base += base
	if !isPositiveFinite(position.Quantity) || !isPositiveFinite(position.Base) {
		return fmt.Errorf("coin: position overflows")
	}
	p.Assets[symbol] = position
	return nil
}

// SellTicker removes quantity of symbol and the proportional share of its cost
// basis, returned as soldBase. Selling the whole position (within dust
// tolerance) deletes it. ok is false when the position cannot cover quantity.
func (p *Portfolio) SellTicker(symbol string, quantity float64) (remaining, soldBase float64, ok bool, err error) {
	position, exists := p.Assets[symbol]
	if !exists || !isPositiveFinite(quantity) || position.Quantity+coinDustEpsilon < quantity {
		return position.Quantity, 0, false, nil
	}
	remaining = normalizeAmount(position.Quantity - quantity)
	if remaining == 0 {
		delete(p.Assets, symbol)
		return 0, position.Base, true, nil
	}
	soldBase = position.Base * (quantity / position.Quantity)
	position.Quantity = remaining
	position.Base -= soldBase
	if !isPositiveFinite(soldBase) || !isPositiveFinite(position.Base) {
		return 0, 0, false, fmt.Errorf("coin: invalid remaining cost basis")
	}
	p.Assets[symbol] = position
	return remaining, soldBase, true, nil
}

func (p *Portfolio) normalize() {
	p.USD = normalizeAmount(p.USD)
	p.Meta.Invested = normalizeAmount(p.Meta.Invested)
}

func normalizeAmount(n float64) float64 {
	if math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) < coinDustEpsilon {
		return 0
	}
	return n
}
