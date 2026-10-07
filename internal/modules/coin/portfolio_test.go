package coin

import (
	"context"
	"errors"
	"math"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

func TestLoadPortfolioFirstTimeUser(t *testing.T) {
	p, err := LoadPortfolio(context.Background(), newCoinStore(), 42, 123)
	if err != nil || p.USD != 0 || len(p.Assets) != 0 || p.Meta.CreatedAt != 123 {
		t.Fatalf("portfolio=%+v err=%v", p, err)
	}
}

func TestPortfolioDropsLegacyDividendCursorOnBSONRoundTrip(t *testing.T) {
	legacy, err := bson.Marshal(bson.M{
		"usd": 100.0,
		"assets": bson.M{"BTC": bson.M{
			"quantity":          0.5,
			"base":              25_000.0,
			"dividendCheckedAt": int64(123),
		}},
		"meta": bson.M{"invested": 100.0, "createdAt": int64(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	var portfolio Portfolio
	if err := bson.Unmarshal(legacy, &portfolio); err != nil {
		t.Fatal(err)
	}
	if err := portfolio.Validate(); err != nil {
		t.Fatalf("legacy cursor prevented load: %v", err)
	}

	current, err := bson.Marshal(portfolio)
	if err != nil {
		t.Fatal(err)
	}
	position := bson.Raw(current).Lookup("assets").Document().Lookup("BTC").Document()
	if _, err := position.LookupErr("dividendCheckedAt"); err == nil {
		t.Fatal("legacy dividend cursor survived current BSON encoding")
	}
}

func TestCoinBuySellMath(t *testing.T) {
	p := NewPortfolio(1)
	p.AddUSD(1000)
	p.Meta.Invested = 1000
	if ok, balance := p.DeductUSD(250); !ok || balance != 750 {
		t.Fatalf("balance=%v ok=%v", balance, ok)
	}
	if err := p.BuyTicker("BTC", 0.1, 250); err != nil {
		t.Fatal(err)
	}
	if err := p.BuyTicker("BTC", 0.05, 150); err != nil {
		t.Fatal(err)
	}
	remaining, soldBase, ok, err := p.SellTicker("BTC", 0.06)
	if err != nil || !ok || math.Abs(remaining-0.09) > 1e-12 || math.Abs(soldBase-160) > 1e-9 {
		t.Fatalf("remaining=%v soldBase=%v ok=%v err=%v", remaining, soldBase, ok, err)
	}
}

func TestCoinFullSellRemovesTicker(t *testing.T) {
	p := NewPortfolio(1)
	_ = p.BuyTicker("BTC", 0.3, 12_000)
	_, soldBase, ok, err := p.SellTicker("BTC", 0.3)
	if err != nil || !ok || math.Abs(soldBase-12_000) > 1e-9 || len(p.Assets) != 0 {
		t.Fatalf("portfolio=%+v soldBase=%v ok=%v err=%v", p, soldBase, ok, err)
	}
}

type conflictOnceStore struct {
	Store
	conflicted bool
}

func (s *conflictOnceStore) PutVersioned(ctx context.Context, key string, expectedVersion int64, val Portfolio) error {
	if !s.conflicted {
		s.conflicted = true
		competing := NewPortfolio(1)
		competing.AddUSD(10)
		if err := s.Put(ctx, key, competing); err != nil {
			return err
		}
		return storage.ErrConflict
	}
	return s.Store.PutVersioned(ctx, key, expectedVersion, val)
}

func TestUpdatePortfolioRetriesAfterWriteConflict(t *testing.T) {
	store := &conflictOnceStore{Store: newCoinStore()}
	got, err := UpdatePortfolio(context.Background(), store, 7, 1, func(p *Portfolio) error {
		p.AddUSD(5)
		return nil
	})
	if err != nil || got.USD != 15 {
		t.Fatalf("USD=%v err=%v", got.USD, err)
	}
}

func TestUpdatePortfolioMutateErrorDoesNotPersist(t *testing.T) {
	ctx := context.Background()
	store := newCoinStore()
	_, err := UpdatePortfolio(ctx, store, 7, 1, func(*Portfolio) error { return errInsufficientUSD })
	if !errors.Is(err, errInsufficientUSD) {
		t.Fatalf("err=%v", err)
	}
	if _, _, err := store.Get(ctx, "user:7"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get err=%v", err)
	}
}
