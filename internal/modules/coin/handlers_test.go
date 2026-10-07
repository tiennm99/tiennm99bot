package coin

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

type fakePriceFetcher struct {
	prices map[string]CoinPrice
	err    error
}

func (f fakePriceFetcher) FetchUSD(_ context.Context, coin CoinSymbol) (CoinPrice, error) {
	if f.err != nil {
		return CoinPrice{}, f.err
	}
	price, ok := f.prices[coin.Symbol]
	if !ok {
		return CoinPrice{}, ErrNoCoinPrice
	}
	if price.Symbol == "" {
		price.Symbol = coin.Symbol
	}
	return price, nil
}

// newCoinStore returns a fresh in-memory typed portfolio store for tests.
func newCoinStore() Store {
	return storage.Typed[Portfolio](storage.NewMemoryProvider().Collection("coin"))
}

func newTestState(prices map[string]CoinPrice, err error) *state {
	return &state{
		store:  newCoinStore(),
		prices: fakePriceFetcher{prices: prices, err: err},
		nowFn:  func() time.Time { return time.UnixMilli(123) },
	}
}

func TestParsePositiveFinite(t *testing.T) {
	bad := []string{"", "0", "-1", "NaN", "Inf", "+Inf", "-Inf", "1e9999"}
	for _, in := range bad {
		if got, ok := parsePositiveFinite(in); ok {
			t.Fatalf("parsePositiveFinite(%q) = %v, true; want false", in, got)
		}
	}
	if got, ok := parsePositiveFinite("0.5"); !ok || got != 0.5 {
		t.Fatalf("parsePositiveFinite valid = %v, %v", got, ok)
	}
}

func TestResolveCoinSymbol(t *testing.T) {
	coin, err := ResolveCoinSymbol("btc")
	if err != nil || coin.Symbol != "BTC" || coin.CoinGeckoID != "bitcoin" {
		t.Fatalf("ResolveCoinSymbol btc = %+v, %v", coin, err)
	}
	coin, err = ResolveCoinSymbol("ena")
	if err != nil || coin.Symbol != "ENA" || coin.CoinGeckoID != "" {
		t.Fatalf("ResolveCoinSymbol ena = %+v, %v", coin, err)
	}
	for _, input := range []string{"", "123", "BAD-COIN", strings.Repeat("A", maxCoinSymbolLength+1)} {
		if _, err := ResolveCoinSymbol(input); !errors.Is(err, ErrUnsupportedCoin) {
			t.Fatalf("ResolveCoinSymbol(%q) got %v, want ErrUnsupportedCoin", input, err)
		}
	}
}

func TestHandlePriceAllowsUnlistedTicker(t *testing.T) {
	s := newTestState(map[string]CoinPrice{"ENA": {USD: 0.08, Source: "Binance"}}, nil)
	rb := testutil.NewRecordingBot(t)
	if err := s.handlePrice(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/coin_price ena")); err != nil {
		t.Fatalf("handlePrice: %v", err)
	}
	rb.AssertSentText(t, "ENA price: $0.08 (Binance)")
}

func TestHandlePriceReturnsNoPriceForUnlistedTickerWhenProvidersFail(t *testing.T) {
	s := newTestState(nil, nil)
	rb := testutil.NewRecordingBot(t)
	if err := s.handlePrice(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/coin_price nope")); err != nil {
		t.Fatalf("handlePrice: %v", err)
	}
	rb.AssertSentText(t, "No coin price available")
}

func TestHandlePriceRejectsMalformedCoinTicker(t *testing.T) {
	s := newTestState(nil, nil)
	rb := testutil.NewRecordingBot(t)
	if err := s.handlePrice(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/coin_price bad-coin")); err != nil {
		t.Fatalf("handlePrice: %v", err)
	}
	if _, err := ResolveCoinSymbol("bad-coin"); !errors.Is(err, ErrUnsupportedCoin) {
		t.Fatalf("got %v, want ErrUnsupportedCoin", err)
	}
	rb.AssertSentText(t, "Invalid coin ticker")
}

func TestModuleRegistersExpectedCommands(t *testing.T) {
	mod := New(modDepsForTest())
	got := map[string]bool{}
	for _, cmd := range mod.Commands {
		got[cmd.Name] = true
	}
	for _, name := range []string{"coin_price", "coin_topup", "coin_buy", "coin_sell", "coin_portfolio"} {
		if !got[name] {
			t.Fatalf("missing command %s", name)
		}
	}
}

func TestHandlePrice(t *testing.T) {
	s := newTestState(map[string]CoinPrice{"BTC": {USD: 67000, Source: "Binance"}}, nil)
	rb := testutil.NewRecordingBot(t)
	if err := s.handlePrice(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/coin_price btc")); err != nil {
		t.Fatalf("handlePrice: %v", err)
	}
	rb.AssertSentText(t, "BTC price: $67,000.00 (Binance)")
}

func TestHandleTopup(t *testing.T) {
	ctx := context.Background()
	s := newTestState(nil, nil)
	rb := testutil.NewRecordingBot(t)
	if err := s.handleTopup(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_topup 1000")); err != nil {
		t.Fatalf("handleTopup: %v", err)
	}
	rb.AssertSentText(t, "Topped up $1,000.00")
	p, err := LoadPortfolio(ctx, s.store, 7, 999)
	if err != nil {
		t.Fatalf("LoadPortfolio: %v", err)
	}
	if p.USD != 1000 || p.Meta.Invested != 1000 {
		t.Fatalf("portfolio = %+v", p)
	}
}

func TestHandleBuyAndSell(t *testing.T) {
	ctx := context.Background()
	s := newTestState(map[string]CoinPrice{"BTC": {USD: 50000, Source: "Binance"}}, nil)
	rb := testutil.NewRecordingBot(t)
	_ = s.handleTopup(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_topup 1000"))
	rb.Reset()
	if err := s.handleBuy(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_buy 500 BTC")); err != nil {
		t.Fatalf("handleBuy: %v", err)
	}
	rb.AssertSentText(t, "Bought 0.01 BTC")
	p, _ := LoadPortfolio(ctx, s.store, 7, 999)
	if p.USD != 500 || p.Assets["BTC"].Quantity != 0.01 {
		t.Fatalf("after buy = %+v", p)
	}
	if p.Assets["BTC"].Base != 500 {
		t.Fatalf("buy asset = %+v", p.Assets["BTC"])
	}
	s.prices.(fakePriceFetcher).prices["BTC"] = CoinPrice{USD: 60_000, Source: "Binance"}
	rb.Reset()
	if err := s.handleSell(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_sell 600 BTC")); err != nil {
		t.Fatalf("handleSell: %v", err)
	}
	rb.AssertSentText(t, "Sold 0.01 BTC")
	rb.AssertSentText(t, "Realized P&L: +$100.00 (+20.00%)")
	p, _ = LoadPortfolio(ctx, s.store, 7, 999)
	if p.USD != 1100 || len(p.Assets) != 0 {
		t.Fatalf("after sell = %+v", p)
	}
}

func TestHandleBuyInsufficientUSD(t *testing.T) {
	s := newTestState(map[string]CoinPrice{"ETH": {USD: 3000, Source: "Coinbase"}}, nil)
	rb := testutil.NewRecordingBot(t)
	if err := s.handleBuy(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/coin_buy 10 ETH")); err != nil {
		t.Fatalf("handleBuy: %v", err)
	}
	rb.AssertSentText(t, "Insufficient USD")
}

func TestHandleSellInsufficientCoin(t *testing.T) {
	s := newTestState(map[string]CoinPrice{"ETH": {USD: 3000, Source: "Coinbase"}}, nil)
	rb := testutil.NewRecordingBot(t)
	if err := s.handleSell(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/coin_sell 10 ETH")); err != nil {
		t.Fatalf("handleSell: %v", err)
	}
	text := rb.LastSent().Text()
	for _, want := range []string{"No ETH available to sell.", "Try /coin_buy ETH <usd_to_spend> first."} {
		if !strings.Contains(text, want) {
			t.Fatalf("zero-holdings sell message missing %q in %q", want, text)
		}
	}
	for _, unwanted := range []string{"$0.00", "@"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("zero-holdings sell message included %q in %q", unwanted, text)
		}
	}
}

func TestHandleSellRejectsInvalidUSDAmount(t *testing.T) {
	s := newTestState(map[string]CoinPrice{"BTC": {USD: 50000, Source: "Binance"}}, nil)
	rb := testutil.NewRecordingBot(t)
	if err := s.handleSell(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/coin_sell BTC -5")); err != nil {
		t.Fatalf("handleSell: %v", err)
	}
	rb.AssertSentText(t, "USD amount must be a positive finite number within the supported range.")
}

func TestHandleSellRejectsAmountTooSmall(t *testing.T) {
	s := newTestState(map[string]CoinPrice{"BTC": {USD: 1e300, Source: "Test"}}, nil)
	rb := testutil.NewRecordingBot(t)
	if err := s.handleSell(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/coin_sell BTC 1e-300")); err != nil {
		t.Fatalf("handleSell: %v", err)
	}
	rb.AssertSentText(t, "too small")
}

func TestHandleSellInsufficientCoinWithHoldings(t *testing.T) {
	ctx := context.Background()
	s := newTestState(map[string]CoinPrice{"BTC": {USD: 50000, Source: "Binance"}}, nil)
	rb := testutil.NewRecordingBot(t)
	_ = s.handleTopup(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_topup 1000"))
	_ = s.handleBuy(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_buy 500 BTC"))
	rb.Reset()

	if err := s.handleSell(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_sell 600 BTC")); err != nil {
		t.Fatalf("handleSell: %v", err)
	}
	text := rb.LastSent().Text()
	for _, want := range []string{
		"Not enough BTC to sell $600.00.",
		"Available to sell: $500.00 (0.01 BTC @ $50,000.00).",
		"Try $500.00 or less.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("insufficient sell message missing %q in %q", want, text)
		}
	}
	if strings.Contains(text, "have") {
		t.Fatalf("insufficient sell message used ambiguous have wording: %q", text)
	}

	p, _ := LoadPortfolio(ctx, s.store, 7, 999)
	if p.USD != 500 || p.Assets["BTC"].Quantity != 0.01 {
		t.Fatalf("portfolio mutated on failed sell = %+v", p)
	}
}

func TestHandleSellRejectsDustQuantity(t *testing.T) {
	ctx := context.Background()
	s := newTestState(map[string]CoinPrice{"BTC": {USD: 1e9, Source: "Test"}}, nil)
	rb := testutil.NewRecordingBot(t)
	_ = s.handleTopup(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_topup 1000"))
	_ = s.handleBuy(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_buy 1 BTC"))
	rb.Reset()

	if err := s.handleSell(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_sell 0.5 BTC")); err != nil {
		t.Fatalf("handleSell: %v", err)
	}
	rb.AssertSentText(t, "too small")

	p, _ := LoadPortfolio(ctx, s.store, 7, 999)
	if p.USD != 999 || len(p.Assets) != 1 {
		t.Fatalf("portfolio mutated on dust sell = %+v", p)
	}
}

func TestHandleBuyRejectsDustQuantity(t *testing.T) {
	ctx := context.Background()
	s := newTestState(map[string]CoinPrice{"BTC": {USD: 1e9, Source: "Test"}}, nil)
	rb := testutil.NewRecordingBot(t)
	_ = s.handleTopup(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_topup 1000"))
	rb.Reset()

	if err := s.handleBuy(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_buy 0.5 BTC")); err != nil {
		t.Fatalf("handleBuy: %v", err)
	}
	rb.AssertSentText(t, "too small")

	p, _ := LoadPortfolio(ctx, s.store, 7, 999)
	if p.USD != 1000 || len(p.Assets) != 0 {
		t.Fatalf("portfolio mutated on dust buy = %+v", p)
	}
}

func TestHandleSellRejectsZeroPrice(t *testing.T) {
	ctx := context.Background()
	s := newTestState(map[string]CoinPrice{"BTC": {USD: 0, Source: "Test"}}, nil)
	rb := testutil.NewRecordingBot(t)
	if err := s.handleSell(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_sell 10 BTC")); err != nil {
		t.Fatalf("handleSell: %v", err)
	}
	rb.AssertSentText(t, "No coin price available")
}

func TestHandleBuyRejectsZeroPrice(t *testing.T) {
	ctx := context.Background()
	s := newTestState(map[string]CoinPrice{"BTC": {USD: 0, Source: "Test"}}, nil)
	rb := testutil.NewRecordingBot(t)
	if err := s.handleBuy(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_buy 10 BTC")); err != nil {
		t.Fatalf("handleBuy: %v", err)
	}
	rb.AssertSentText(t, "No coin price available")
}

func TestPriceErrorDoesNotMutatePortfolio(t *testing.T) {
	ctx := context.Background()
	s := newTestState(nil, errors.New("upstream down"))
	rb := testutil.NewRecordingBot(t)
	if err := s.handleBuy(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_buy 10 BTC")); err != nil {
		t.Fatalf("handleBuy: %v", err)
	}
	rb.AssertSentText(t, "Could not fetch coin price")
	p, err := LoadPortfolio(ctx, s.store, 7, 999)
	if err != nil {
		t.Fatalf("LoadPortfolio: %v", err)
	}
	if p.USD != 0 || len(p.Assets) != 0 {
		t.Fatalf("unexpected mutation = %+v", p)
	}
}

func TestStatsWithAndWithoutPrice(t *testing.T) {
	ctx := context.Background()
	s := newTestState(map[string]CoinPrice{"BTC": {USD: 50000, Source: "Binance"}}, nil)
	rb := testutil.NewRecordingBot(t)
	_ = s.handleTopup(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_topup 1000"))
	_ = s.handleBuy(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_buy 500 BTC"))
	rb.Reset()
	if err := s.handleStats(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_portfolio")); err != nil {
		t.Fatalf("handleStats: %v", err)
	}
	text := rb.LastSent().Text()
	for _, want := range []string{"Coin Portfolio", "<pre>", "Sym", "BTC", "0.01", "50k", "500.00", "+0.00", "+0.00%", "P&amp;L"} {
		if !strings.Contains(text, want) {
			t.Fatalf("stats missing %q in %q", want, text)
		}
	}
	s.prices = fakePriceFetcher{err: ErrNoCoinPrice}
	rb.Reset()
	if err := s.handleStats(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_portfolio")); err != nil {
		t.Fatalf("handleStats no price: %v", err)
	}
	rb.AssertSentText(t, "N/A")
	rb.AssertSentText(t, "50k")
	if strings.Contains(rb.LastSent().Text(), "Account P&L: +") || strings.Contains(rb.LastSent().Text(), "Account P&L: -") {
		t.Fatalf("partial prices must not show numeric account P&L: %q", rb.LastSent().Text())
	}
}

func TestStatsCompactsOnlyPositionMonetaryCells(t *testing.T) {
	ctx := context.Background()
	s := newTestState(map[string]CoinPrice{"BTC": {USD: 1_250, Source: "test"}}, nil)
	p := NewPortfolio(1)
	p.USD = 1_234
	p.Meta.Invested = 2_001_234
	p.Assets["BTC"] = AssetPosition{Quantity: 2_000, Base: 2_000_000}
	if err := SavePortfolio(ctx, s.store, 7, p); err != nil {
		t.Fatal(err)
	}

	rb := testutil.NewRecordingBot(t)
	if err := s.handleStats(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_portfolio")); err != nil {
		t.Fatal(err)
	}
	text := rb.LastSent().Text()
	for _, want := range []string{
		"BTC",
		"1k",
		"1.25k",
		"2.5M",
		"+500k",
		"+25.00%",
		"$1,234.00",
		"$2,501,234.00",
		"+$500,000.00 (+25.00%)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("portfolio missing %q in:\n%s", want, text)
		}
	}
}

func TestStatsTreatsOverflowedValuationAsUnavailable(t *testing.T) {
	ctx := context.Background()
	s := newTestState(map[string]CoinPrice{"BTC": {USD: math.MaxFloat64, Source: "test"}}, nil)
	p := NewPortfolio(1)
	p.Assets["BTC"] = AssetPosition{Quantity: 2, Base: 1}
	if err := SavePortfolio(ctx, s.store, 7, p); err != nil {
		t.Fatal(err)
	}
	rb := testutil.NewRecordingBot(t)
	if err := s.handleStats(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_portfolio")); err != nil {
		t.Fatal(err)
	}
	text := rb.LastSent().Text()
	if !strings.Contains(text, "N/A") || !strings.Contains(text, "Account P&amp;L") || !strings.Contains(text, "Unavailable") {
		t.Fatalf("overflowed valuation was presented as complete: %q", text)
	}
}

func modDepsForTest() modules.Deps {
	return modules.Deps{Store: storage.NewMemoryProvider().Collection("coin")}
}
