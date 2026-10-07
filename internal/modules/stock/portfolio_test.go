package stock

import (
	"context"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

func newStockStore() Store {
	return storage.Typed[Portfolio](storage.NewMemoryProvider().Collection(CollectionName))
}

func TestLoadPortfolioFirstTimeUser(t *testing.T) {
	p, err := LoadPortfolio(context.Background(), newStockStore(), 42, 1234567890)
	if err != nil {
		t.Fatal(err)
	}
	if p.VND != 0 || p.Assets == nil || p.Dividends == nil || p.Meta.CreatedAt != 1234567890 {
		t.Fatalf("portfolio=%+v", p)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newStockStore()
	p := NewPortfolio(1)
	p.AddVND(5_000_000)
	if err := p.BuyTicker("TCB", 100, 3_000_000, 10); err != nil {
		t.Fatal(err)
	}
	p.Meta.Invested = 5_000_000
	p.Dividends["TCB"] = map[string]DividendRecord{
		"2612974": {Kind: DividendKindCash, PublishedAt: 2, RecordDate: 3, VNDPerShare: 1500},
	}
	if err := SavePortfolio(ctx, store, 42, p); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPortfolio(ctx, store, 42, 999)
	if err != nil {
		t.Fatal(err)
	}
	position := got.Assets["TCB"]
	if got.VND != 5_000_000 || position.Quantity != 100 || position.Base != 3_000_000 || position.OpenedAt != 10 || got.Meta.CreatedAt != 1 || got.Dividends["TCB"]["2612974"].VNDPerShare != 1500 {
		t.Fatalf("portfolio=%+v", got)
	}
}

func TestBuyPreservesOpenedAtAndSellUsesWeightedBasis(t *testing.T) {
	p := NewPortfolio(1)
	if err := p.BuyTicker("TCB", 100, 2_000_000, 10); err != nil {
		t.Fatal(err)
	}
	if err := p.BuyTicker("TCB", 50, 1_500_000, 20); err != nil {
		t.Fatal(err)
	}
	position := p.Assets["TCB"]
	if position.OpenedAt != 10 {
		t.Fatalf("additional buy changed openedAt to %d", position.OpenedAt)
	}
	remaining, soldBase, ok, err := p.SellTicker("TCB", 60)
	if err != nil || !ok || remaining != 90 || soldBase != 1_400_000 {
		t.Fatalf("remaining=%d soldBase=%v ok=%v err=%v", remaining, soldBase, ok, err)
	}
	position = p.Assets["TCB"]
	if position.Base != 2_100_000 || position.OpenedAt != 10 {
		t.Fatalf("position=%+v", position)
	}
}

func TestCashDividendReducesBaseAndKeepsLifecycle(t *testing.T) {
	p := NewPortfolio(1)
	_ = p.BuyTicker("TCB", 100, 3_000_000, 10)
	if err := p.ApplyCashDividend("TCB", 150_000, 650_000, 30); err != nil {
		t.Fatal(err)
	}
	position := p.Assets["TCB"]
	if position.Quantity != 100 || position.Base != 2_850_000 || position.OpenedAt != 10 || p.VND != 650_000 {
		t.Fatalf("portfolio=%+v", p)
	}
}

func TestCashDividendFloorsBaseAtZeroAndSellStillWorks(t *testing.T) {
	p := NewPortfolio(1)
	_ = p.BuyTicker("TCB", 100, 100_000, 10)
	if err := p.ApplyCashDividend("TCB", 150_000, 150_000, 30); err != nil {
		t.Fatal(err)
	}
	if position := p.Assets["TCB"]; position.Base != 0 || position.Quantity != 100 {
		t.Fatalf("position=%+v", position)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("zero-base position failed validation: %v", err)
	}
	remaining, soldBase, ok, err := p.SellTicker("TCB", 40)
	if err != nil || !ok || remaining != 60 || soldBase != 0 {
		t.Fatalf("remaining=%d soldBase=%v ok=%v err=%v", remaining, soldBase, ok, err)
	}
}

func TestShareDividendDoesNotChangeLifecycleOrBase(t *testing.T) {
	p := NewPortfolio(1)
	_ = p.BuyTicker("TCB", 100, 3_000_000, 10)
	if err := p.ApplyShareDividend("TCB", 110, 30); err != nil {
		t.Fatal(err)
	}
	position := p.Assets["TCB"]
	if position.Quantity != 110 || position.Base != 3_000_000 || position.OpenedAt != 10 || p.VND != 0 {
		t.Fatalf("portfolio=%+v", p)
	}
}

func TestFullSaleKeepsDividendHistorySeparate(t *testing.T) {
	p := NewPortfolio(1)
	if err := p.BuyTicker("TCB", 100, 3_000_000, 10); err != nil {
		t.Fatal(err)
	}
	p.Dividends["TCB"] = map[string]DividendRecord{
		"2612974": {Kind: DividendKindCash, PublishedAt: 2, VNDPerShare: 1500, Processed: true},
	}
	if _, _, ok, err := p.SellTicker("TCB", 100); err != nil || !ok {
		t.Fatalf("full sale: ok=%v err=%v", ok, err)
	}
	if _, ok := p.Assets["TCB"]; ok {
		t.Fatal("full sale retained active asset")
	}
	if !p.Dividends["TCB"]["2612974"].Processed {
		t.Fatal("full sale removed dividend history")
	}
}
