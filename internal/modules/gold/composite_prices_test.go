package gold

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// stubVNAppMobClient implements just enough of the VNAppMob client contract
// for composite fetcher tests.
type stubVNAppMobClient struct {
	buy  float64
	sell float64
	err  error
}

func (s *stubVNAppMobClient) FetchSJCPrice(context.Context) (float64, float64, error) {
	return s.buy, s.sell, s.err
}

func TestCompositeFetcher_UsesVNAppMob(t *testing.T) {
	f := &compositePriceFetcher{
		vnappmob: &stubVNAppMobClient{buy: 90_000_000, sell: 91_000_000},
	}

	p, err := f.FetchPrice(context.Background())
	if err != nil {
		t.Fatalf("FetchPrice: %v", err)
	}
	if p.Source != "vnappmob-sjc" {
		t.Fatalf("source: got %q, want vnappmob-sjc", p.Source)
	}
	if p.SJC == nil || p.SJC.Buy != 90_000_000 || p.SJC.Sell != 91_000_000 {
		t.Fatalf("SJC: got %+v", p.SJC)
	}
	wantMid := 90_500_000.0
	if p.VNDPerLuong != wantMid {
		t.Fatalf("VNDPerLuong: got %v, want %v", p.VNDPerLuong, wantMid)
	}

	mid, err := f.FetchLuongPrice(context.Background())
	if err != nil {
		t.Fatalf("FetchLuongPrice: %v", err)
	}
	if mid != wantMid {
		t.Fatalf("FetchLuongPrice: got %v, want %v", mid, wantMid)
	}

	buy, sell, err := f.FetchLuongPrices(context.Background())
	if err != nil {
		t.Fatalf("FetchLuongPrices: %v", err)
	}
	if buy != 90_000_000 || sell != 91_000_000 {
		t.Fatalf("FetchLuongPrices: got buy=%v sell=%v", buy, sell)
	}
}

func TestCompositeFetcher_ErrorsWhenVNAppMobFails(t *testing.T) {
	wantErr := errors.New("vnappmob down")
	f := &compositePriceFetcher{
		vnappmob: &stubVNAppMobClient{err: wantErr},
	}
	if _, err := f.FetchPrice(context.Background()); err == nil {
		t.Fatal("FetchPrice: expected error")
	}
	if _, err := f.FetchLuongPrice(context.Background()); err == nil {
		t.Fatal("FetchLuongPrice: expected error")
	}
	if _, _, err := f.FetchLuongPrices(context.Background()); err == nil {
		t.Fatal("FetchLuongPrices: expected error")
	}
}

func TestGoldPriceLines(t *testing.T) {
	lines := goldPriceLines(GoldPrice{
		VNDPerLuong: 90_500_000,
		Source:      "vnappmob-sjc",
		SJC:         &SJCPrice{Buy: 90_000_000, Sell: 91_000_000},
	})
	want := []string{"SJC gold price", "SJC buy (you sell):", "SJC sell (you buy):"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %v", len(lines), len(want), lines)
	}
	for i, w := range want {
		if !strings.Contains(lines[i], w) {
			t.Fatalf("line %d missing %q in %v", i, w, lines)
		}
	}
}

func TestNewCompositePriceFetcher(t *testing.T) {
	f, ok := NewCompositePriceFetcher(storage.NewMemoryProvider().Collection("gold")).(*compositePriceFetcher)
	if !ok {
		t.Fatalf("expected *compositePriceFetcher, got %T", f)
	}
	if f.vnappmob == nil {
		t.Fatal("vnappmob client is nil")
	}
}
