package gold

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// goldDustEpsilon is the tolerance below which balances and quantities are
// treated as zero, absorbing float rounding left over by trades.
const goldDustEpsilon = 1e-9

// portfolioUpdateAttempts bounds optimistic-write retries in UpdatePortfolio.
const portfolioUpdateAttempts = 5

// PortfolioStore is the gold module's typed portfolio store.
type PortfolioStore = storage.DocStore[Portfolio]

// Portfolio is one user's gold account: a VND cash balance and gold held in
// lượng.
type Portfolio struct {
	VND   float64       `json:"vnd" bson:"vnd"`
	Luong float64       `json:"luong" bson:"luong"`
	Meta  PortfolioMeta `json:"meta" bson:"meta"`
}

// PortfolioMeta holds account-level totals: Invested is the sum of all VND
// top-ups and CreatedAt is ms since epoch.
type PortfolioMeta struct {
	Invested  float64 `json:"invested" bson:"invested"`
	CreatedAt int64   `json:"createdAt" bson:"createdAt"`
}

func NewPortfolio(now int64) Portfolio {
	return Portfolio{Meta: PortfolioMeta{CreatedAt: now}}
}

func portfolioKey(userID int64) string {
	return "user:" + strconv.FormatInt(userID, 10)
}

func LoadPortfolio(ctx context.Context, store PortfolioStore, userID int64, now int64) (Portfolio, error) {
	p, _, err := loadPortfolioForUpdate(ctx, store, portfolioKey(userID), now)
	if err != nil {
		return Portfolio{}, fmt.Errorf("gold: load portfolio %d: %w", userID, err)
	}
	return p, nil
}

func SavePortfolio(ctx context.Context, store PortfolioStore, userID int64, p Portfolio) error {
	p.normalize()
	if err := store.Put(ctx, portfolioKey(userID), p); err != nil {
		return fmt.Errorf("gold: save portfolio %d: %w", userID, err)
	}
	return nil
}

// UpdatePortfolio loads the user's portfolio, applies mutate, and writes it
// back with a versioned put, retrying on write conflicts. An error returned by
// mutate aborts without saving and is passed through unwrapped.
func UpdatePortfolio(ctx context.Context, store PortfolioStore, userID int64, now int64, mutate func(*Portfolio) error) (Portfolio, error) {
	key := portfolioKey(userID)
	for attempt := 0; attempt < portfolioUpdateAttempts; attempt++ {
		p, version, err := loadPortfolioForUpdate(ctx, store, key, now)
		if err != nil {
			return Portfolio{}, fmt.Errorf("gold: load portfolio %d: %w", userID, err)
		}
		if err := mutate(&p); err != nil {
			return p, err
		}
		p.normalize()
		if err := store.PutVersioned(ctx, key, version, p); err == nil {
			return p, nil
		} else if !errors.Is(err, storage.ErrConflict) {
			return Portfolio{}, fmt.Errorf("gold: save portfolio %d: %w", userID, err)
		}
	}
	return Portfolio{}, fmt.Errorf("gold: save portfolio %d: %w", userID, storage.ErrConflict)
}

func loadPortfolioForUpdate(ctx context.Context, store PortfolioStore, key string, now int64) (Portfolio, int64, error) {
	p, version, err := store.Get(ctx, key)
	switch {
	case err == nil:
		p.normalize()
		if p.Meta.CreatedAt == 0 {
			p.Meta.CreatedAt = now
		}
		return p, version, nil
	case errors.Is(err, storage.ErrNotFound):
		return NewPortfolio(now), 0, nil
	default:
		return Portfolio{}, 0, err
	}
}

func (p *Portfolio) AddVND(amount float64) {
	p.VND += amount
	p.normalize()
}

func (p *Portfolio) DeductVND(amount float64) (ok bool, balance float64) {
	p.normalize()
	balance = p.VND
	if balance+goldDustEpsilon < amount {
		return false, balance
	}
	p.VND = balance - amount
	p.normalize()
	return true, p.VND
}

func (p *Portfolio) AddLuong(amount float64) {
	p.Luong += amount
	p.normalize()
}

func (p *Portfolio) DeductLuong(amount float64) (ok bool, held float64) {
	p.normalize()
	held = p.Luong
	if held+goldDustEpsilon < amount {
		return false, held
	}
	p.Luong = held - amount
	p.normalize()
	return true, p.Luong
}

func (p *Portfolio) normalize() {
	p.VND = normalizeAmount(p.VND)
	p.Luong = normalizeAmount(p.Luong)
	p.Meta.Invested = normalizeAmount(p.Meta.Invested)
}

func normalizeAmount(n float64) float64 {
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 0
	}
	if math.Abs(n) < goldDustEpsilon {
		return 0
	}
	return n
}
