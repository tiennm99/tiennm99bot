package stock

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// Store is the typed view of the module collection that holds portfolios.
type Store = storage.DocStore[Portfolio]

// CollectionName is the storage collection shared by portfolios and pending
// dividend actions; the two are told apart by key prefix.
const CollectionName = "stock"

// AssetPosition keeps the complete persisted state for one open stock ticker.
// Base is total remaining VND cost, not average price. Cash dividends return
// part of that cost, so Base can reach zero on a still-open position.
type AssetPosition struct {
	Quantity int64   `json:"quantity" bson:"quantity"`
	Base     float64 `json:"base" bson:"base"`
	OpenedAt int64   `json:"openedAt,omitempty" bson:"openedAt,omitempty"`
}

// DividendRecord is one normalized SSI event in a user's retained history.
// The raw SSI event ID is the containing map key.
type DividendRecord struct {
	Kind        DividendKind `json:"kind" bson:"kind"`
	PublishedAt int64        `json:"publishedAt" bson:"publishedAt"`
	ExDate      int64        `json:"exDate,omitempty" bson:"exDate,omitempty"`
	RecordDate  int64        `json:"recordDate,omitempty" bson:"recordDate,omitempty"`
	PaymentDate int64        `json:"paymentDate,omitempty" bson:"paymentDate,omitempty"`

	VNDPerShare int64 `json:"vndPerShare,omitempty" bson:"vndPerShare,omitempty"`
	OwnedShares int64 `json:"ownedShares,omitempty" bson:"ownedShares,omitempty"`
	NewShares   int64 `json:"newShares,omitempty" bson:"newShares,omitempty"`

	Title     string `json:"title,omitempty" bson:"title,omitempty"`
	SourceURL string `json:"sourceUrl,omitempty" bson:"sourceUrl,omitempty"`

	Processed bool `json:"processed" bson:"processed"`
}

// Portfolio is one user's persisted paper account. Assets holds only open
// positions, and Dividends maps ticker to SSI event ID to retained history,
// which outlives a closed position until it ages out.
type Portfolio struct {
	VND       float64                              `json:"vnd" bson:"vnd"`
	Assets    map[string]AssetPosition             `json:"assets" bson:"assets"`
	Dividends map[string]map[string]DividendRecord `json:"dividends,omitempty" bson:"dividends,omitempty"`
	Meta      PortfolioMeta                        `json:"meta" bson:"meta"`
}

// PortfolioMeta tracks account-level totals. Invested is the sum of all
// top-ups and is the baseline for the account-wide P&L.
type PortfolioMeta struct {
	Invested  float64 `json:"invested" bson:"invested"`
	CreatedAt int64   `json:"createdAt" bson:"createdAt"`
}

// NewPortfolio returns an empty portfolio created at now (Unix milliseconds).
func NewPortfolio(now int64) Portfolio {
	return Portfolio{
		Assets:    map[string]AssetPosition{},
		Dividends: map[string]map[string]DividendRecord{},
		Meta:      PortfolioMeta{CreatedAt: now},
	}
}

func portfolioKey(userID int64) string {
	return "user:" + strconv.FormatInt(userID, 10)
}

// LoadPortfolio reads userID's portfolio, returning a fresh one when none is
// stored. Nil maps and a missing CreatedAt are filled in before validation, so
// callers can mutate the result directly.
func LoadPortfolio(ctx context.Context, store Store, userID int64, now int64) (Portfolio, error) {
	p, _, err := store.Get(ctx, portfolioKey(userID))
	switch {
	case err == nil:
		if p.Assets == nil {
			p.Assets = map[string]AssetPosition{}
		}
		if p.Dividends == nil {
			p.Dividends = map[string]map[string]DividendRecord{}
		}
		if p.Meta.CreatedAt == 0 {
			p.Meta.CreatedAt = now
		}
		if err := p.Validate(); err != nil {
			return Portfolio{}, err
		}
		return p, nil
	case errors.Is(err, storage.ErrNotFound):
		return NewPortfolio(now), nil
	default:
		return Portfolio{}, fmt.Errorf("stock: load portfolio %d: %w", userID, err)
	}
}

// SavePortfolio validates p and overwrites userID's stored portfolio. The write
// is unversioned; handlers serialize read-modify-write cycles with the per-user
// lock instead.
func SavePortfolio(ctx context.Context, store Store, userID int64, p Portfolio) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("stock: save portfolio %d: %w", userID, err)
	}
	if err := store.Put(ctx, portfolioKey(userID), p); err != nil {
		return fmt.Errorf("stock: save portfolio %d: %w", userID, err)
	}
	return nil
}

// Validate rejects a portfolio that must never be persisted: a negative or
// non-finite balance, non-canonical tickers, empty or corrupt positions, and
// malformed dividend records.
func (p Portfolio) Validate() error {
	if math.IsNaN(p.VND) || math.IsInf(p.VND, 0) || p.VND < 0 {
		return fmt.Errorf("stock: invalid VND balance")
	}
	for symbol, position := range p.Assets {
		canonical, err := normalizeStockSymbol(symbol)
		if err != nil || canonical != symbol {
			return fmt.Errorf("stock: invalid ticker %q", symbol)
		}
		if position.Quantity <= 0 || !isNonNegativeFiniteCost(position.Base) || position.OpenedAt < 0 {
			return fmt.Errorf("stock: %s has invalid position", symbol)
		}
	}
	for symbol, events := range p.Dividends {
		canonical, err := normalizeStockSymbol(symbol)
		if err != nil || canonical != symbol || events == nil {
			return fmt.Errorf("stock: invalid dividend ticker %q", symbol)
		}
		for eventID, event := range events {
			if !ssiProviderIDPattern.MatchString(eventID) || !event.valid() {
				return fmt.Errorf("stock: invalid dividend event %q for %s", eventID, symbol)
			}
		}
	}
	return nil
}

func (p *Portfolio) AddVND(amount float64) {
	p.VND += amount
}

// DeductVND debits amount when the balance covers it. On failure the balance
// is left unchanged and returned so the caller can report the shortfall.
func (p *Portfolio) DeductVND(amount float64) (ok bool, balance float64) {
	if p.VND < amount {
		return false, p.VND
	}
	p.VND -= amount
	return true, p.VND
}

// BuyTicker adds quantity and basis. OpenedAt identifies the current position
// lifecycle and is reset only after a full exit.
func (p *Portfolio) BuyTicker(symbol string, quantity int64, base float64, now int64) error {
	if quantity <= 0 || !isPositiveFiniteCost(base) || now <= 0 {
		return fmt.Errorf("stock: invalid purchase position")
	}
	if p.Assets == nil {
		p.Assets = map[string]AssetPosition{}
	}
	position := p.Assets[symbol]
	isOpening := position.Quantity == 0
	if position.Quantity > math.MaxInt64-quantity {
		return fmt.Errorf("stock: quantity overflows")
	}
	position.Quantity += quantity
	position.Base += base
	if !isPositiveFiniteCost(position.Base) {
		return fmt.Errorf("stock: cost basis overflows")
	}
	if isOpening {
		position.OpenedAt = now
	}
	p.Assets[symbol] = position
	return nil
}

// SellTicker removes proportional weighted-average basis. A full exit removes
// the active position but retained dividend history remains separate.
func (p *Portfolio) SellTicker(symbol string, quantity int64) (remaining int64, soldBase float64, ok bool, err error) {
	position, exists := p.Assets[symbol]
	if !exists || position.Quantity < quantity || quantity <= 0 {
		return position.Quantity, 0, false, nil
	}
	if quantity == position.Quantity {
		delete(p.Assets, symbol)
		return 0, position.Base, true, nil
	}
	soldBase = position.Base * (float64(quantity) / float64(position.Quantity))
	position.Quantity -= quantity
	position.Base -= soldBase
	if !isNonNegativeFiniteCost(soldBase) || !isNonNegativeFiniteCost(position.Base) {
		return 0, 0, false, fmt.Errorf("stock: invalid remaining cost basis")
	}
	p.Assets[symbol] = position
	return position.Quantity, soldBase, true, nil
}

// ApplyCashDividend credits the payout balance and treats the payout as a
// return of capital: the position's remaining cost basis drops by the same
// amount, floored at zero, so the ticker's unrealized P&L reflects dividends
// already received. Any payout beyond the remaining basis still lands in the
// balance; it just cannot push the basis negative.
func (p *Portfolio) ApplyCashDividend(symbol string, total int64, balance float64, now int64) error {
	position, ok := p.Assets[symbol]
	if !ok || position.Quantity <= 0 {
		return fmt.Errorf("stock: ticker position not found")
	}
	if total <= 0 || !isNonNegativeFiniteCost(position.Base) || now <= 0 {
		return fmt.Errorf("stock: invalid dividend position")
	}
	position.Base = math.Max(0, position.Base-float64(total))
	p.Assets[symbol] = position
	p.VND = balance
	return nil
}

// ApplyShareDividend grows the holding to quantity. The cost basis is
// unchanged: the same spent money now covers more shares, which lowers the
// derived average price.
func (p *Portfolio) ApplyShareDividend(symbol string, quantity int64, now int64) error {
	position, ok := p.Assets[symbol]
	if !ok || position.Quantity <= 0 {
		return fmt.Errorf("stock: ticker position not found")
	}
	if quantity < position.Quantity || !isNonNegativeFiniteCost(position.Base) || now <= 0 {
		return fmt.Errorf("stock: invalid dividend position")
	}
	position.Quantity = quantity
	p.Assets[symbol] = position
	return nil
}

func isPositiveFiniteCost(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func isNonNegativeFiniteCost(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
