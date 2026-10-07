package stock

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

func TestModuleRegistersExpectedCommands(t *testing.T) {
	mod := New(modDepsForTest())
	got := map[string]bool{}
	for _, cmd := range mod.Commands {
		got[cmd.Name] = true
	}
	for _, name := range []string{
		"stock_price",
		"stock_info",
		"stock_events",
		"stock_topup",
		"stock_buy",
		"stock_sell",
		"stock_cash_dividend",
		"stock_share_dividend",
		"stock_portfolio",
	} {
		if !got[name] {
			t.Fatalf("missing command %s", name)
		}
	}
}

func TestHandlePrice(t *testing.T) {
	priceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/stock/TCB" {
			t.Errorf("path = %q, want /stock/TCB", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"matchedPrice":30000}}`))
	}))
	t.Cleanup(priceSrv.Close)

	s := &state{
		store:  newStockStore(),
		prices: &PriceClient{HTTP: priceSrv.Client(), URL: priceSrv.URL},
		nowFn:  func() time.Time { return time.UnixMilli(123) },
	}
	rb := testutil.NewRecordingBot(t)
	if err := s.handlePrice(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_price tcb")); err != nil {
		t.Fatalf("handlePrice: %v", err)
	}

	if got := rb.LastSent().Text(); !strings.Contains(got, "TCB price: 30.000 VND") {
		t.Fatalf("price reply = %q", got)
	}
}

func TestHandlePriceUsage(t *testing.T) {
	s := &state{prices: &PriceClient{}}
	rb := testutil.NewRecordingBot(t)
	if err := s.handlePrice(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_price")); err != nil {
		t.Fatalf("handlePrice: %v", err)
	}
	rb.AssertSentText(t, "Usage: /stock_price <ticker>")
}

func TestHandleBuyAndPartialSellTracksCostBasisAndRealizedPnL(t *testing.T) {
	ctx := context.Background()
	price := 30_000.0
	priceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{"matchedPrice":%v}}`, price)
	}))
	t.Cleanup(priceSrv.Close)
	s := &state{
		store:  newStockStore(),
		prices: &PriceClient{HTTP: priceSrv.Client(), URL: priceSrv.URL},
		nowFn:  func() time.Time { return time.UnixMilli(123) },
	}
	rb := testutil.NewRecordingBot(t)
	if err := s.handleTopup(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/stock_topup 10000000")); err != nil {
		t.Fatal(err)
	}
	if err := s.handleBuy(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/stock_buy 100 TCB")); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPortfolio(ctx, s.store, 7, 999)
	if err != nil || p.Assets["TCB"].Base != 3_000_000 {
		t.Fatalf("after buy=%+v err=%v", p, err)
	}
	price = 36_000
	rb.Reset()
	if err := s.handleSell(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/stock_sell 40 TCB")); err != nil {
		t.Fatal(err)
	}
	rb.AssertSentText(t, "Realized P&L: +240.000 VND (+20.00%)")
	p, err = LoadPortfolio(ctx, s.store, 7, 999)
	if err != nil || p.Assets["TCB"].Quantity != 60 || p.Assets["TCB"].Base != 1_800_000 {
		t.Fatalf("after sell=%+v err=%v", p, err)
	}
}

func TestMutableHandlersRejectExtraArgs(t *testing.T) {
	ctx := context.Background()
	s := &state{
		store:  newStockStore(),
		prices: &PriceClient{},
		nowFn:  func() time.Time { return time.UnixMilli(123) },
	}
	cases := []struct {
		name string
		text string
		run  func(context.Context, *testutil.RecordingBot, *models.Update) error
		want string
	}{
		{
			name: "topup",
			text: "/stock_topup 1000000 extra",
			run: func(ctx context.Context, rb *testutil.RecordingBot, upd *models.Update) error {
				return s.handleTopup(ctx, rb.Bot, upd)
			},
			want: "Usage: /stock_topup <vnd_amount>",
		},
		{
			name: "buy",
			text: "/stock_buy 100 TCB extra",
			run: func(ctx context.Context, rb *testutil.RecordingBot, upd *models.Update) error {
				return s.handleBuy(ctx, rb.Bot, upd)
			},
			want: "Usage: /stock_buy <quantity> <ticker>",
		},
		{
			name: "sell",
			text: "/stock_sell 100 TCB extra",
			run: func(ctx context.Context, rb *testutil.RecordingBot, upd *models.Update) error {
				return s.handleSell(ctx, rb.Bot, upd)
			},
			want: "Usage: /stock_sell <quantity> <ticker>",
		},
		{
			name: "cash dividend",
			text: "/stock_cash_dividend 1500 TCB extra",
			run: func(ctx context.Context, rb *testutil.RecordingBot, upd *models.Update) error {
				return s.handleCashDividend(ctx, rb.Bot, upd)
			},
			want: "Usage: /stock_cash_dividend <vnd_per_share> <ticker>",
		},
		{
			name: "share dividend",
			text: "/stock_share_dividend 100:10 TCB extra",
			run: func(ctx context.Context, rb *testutil.RecordingBot, upd *models.Update) error {
				return s.handleShareDividend(ctx, rb.Bot, upd)
			},
			want: "Usage: /stock_share_dividend <ratio(owned:new)> <ticker>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rb := testutil.NewRecordingBot(t)
			if err := tc.run(ctx, rb, testutil.NewPrivateMessage(7, tc.text)); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			rb.AssertSentText(t, tc.want)
		})
	}

	p, err := LoadPortfolio(ctx, s.store, 7, 999)
	if err != nil {
		t.Fatalf("LoadPortfolio: %v", err)
	}
	if p.VND != 0 || len(p.Assets) != 0 {
		t.Fatalf("invalid commands mutated portfolio: %+v", p)
	}
}

func TestDividendHandlersRejectInvalidNumbers(t *testing.T) {
	ctx := context.Background()
	s := &state{
		store:  newStockStore(),
		prices: &PriceClient{},
		nowFn:  func() time.Time { return time.UnixMilli(123) },
	}

	rb := testutil.NewRecordingBot(t)
	if err := s.handleTopup(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/stock_topup NaN")); err != nil {
		t.Fatalf("topup: %v", err)
	}
	rb.AssertSentText(t, "positive finite")

	rb.Reset()
	if err := s.handleCashDividend(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/stock_cash_dividend 1.5 TCB")); err != nil {
		t.Fatalf("cash dividend: %v", err)
	}
	rb.AssertSentText(t, "positive whole number")

	rb.Reset()
	if err := s.handleShareDividend(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/stock_share_dividend 4.0:1 TCB")); err != nil {
		t.Fatalf("share dividend: %v", err)
	}
	rb.AssertSentText(t, "owned:new")
}

type countingPortfolioStore struct {
	Store
	puts   int
	putErr error
}

func (s *countingPortfolioStore) Put(ctx context.Context, id string, p Portfolio) error {
	s.puts++
	if s.putErr != nil {
		return s.putErr
	}
	return s.Store.Put(ctx, id, p)
}

func seedStockPortfolio(t *testing.T, store Store, userID int64, held int64, balance float64) {
	t.Helper()
	p := NewPortfolio(123)
	p.Assets["TCB"] = AssetPosition{Quantity: held, Base: float64(held) * 30_000, OpenedAt: 100}
	p.VND = balance
	if err := SavePortfolio(context.Background(), store, userID, p); err != nil {
		t.Fatalf("seed portfolio: %v", err)
	}
}

func TestHandleCashDividendAllowsRepeatedManualAdjustments(t *testing.T) {
	ctx := context.Background()
	store := newStockStore()
	seedStockPortfolio(t, store, 7, 139, 1000)
	s := &state{store: store, nowFn: func() time.Time { return time.UnixMilli(123) }}
	rb := testutil.NewRecordingBot(t)

	for i := 0; i < 2; i++ {
		if err := s.handleCashDividend(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/stock_cash_dividend 1500 TCB")); err != nil {
			t.Fatalf("handleCashDividend call %d: %v", i+1, err)
		}
	}
	p, err := LoadPortfolio(ctx, store, 7, 999)
	if err != nil {
		t.Fatalf("load portfolio: %v", err)
	}
	if got, want := p.VND, float64(418000); got != want {
		t.Fatalf("balance = %v, want %v", got, want)
	}
	// Each payout (1.500 × 139 = 208.500) is a return of capital: the basis
	// drops from 4.170.000 through 3.961.500 to 3.753.000.
	if p.Assets["TCB"].Base != 3_753_000 || p.Assets["TCB"].OpenedAt != 100 {
		t.Fatalf("cash dividend position: %+v", p.Assets["TCB"])
	}
	rb.AssertSentText(t, "Cost basis: 3.961.500 → 3.753.000 VND")
}

func TestHandleCashDividendRejectsInexactBalanceSum(t *testing.T) {
	ctx := context.Background()
	base := newStockStore()
	seedStockPortfolio(t, base, 7, 1, 1)
	store := &countingPortfolioStore{Store: base}
	s := &state{store: store, nowFn: func() time.Time { return time.UnixMilli(123) }}
	rb := testutil.NewRecordingBot(t)

	if err := s.handleCashDividend(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/stock_cash_dividend 9007199254740992 TCB")); err != nil {
		t.Fatalf("handleCashDividend: %v", err)
	}
	if store.puts != 0 {
		t.Fatalf("store writes = %d, want 0", store.puts)
	}
	p, _ := LoadPortfolio(ctx, base, 7, 999)
	if p.Assets["TCB"].Quantity != 1 || p.VND != 1 {
		t.Fatalf("portfolio changed: %+v", p)
	}
	rb.AssertSentText(t, "Dividend amount is too large.")
}

func TestHandleShareDividendPreservesRatioAndFloors(t *testing.T) {
	ctx := context.Background()
	store := newStockStore()
	seedStockPortfolio(t, store, 7, 139, 0)
	s := &state{store: store, nowFn: func() time.Time { return time.UnixMilli(123) }}
	rb := testutil.NewRecordingBot(t)

	if err := s.handleShareDividend(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/stock_share_dividend 100:10 TCB")); err != nil {
		t.Fatalf("handleShareDividend: %v", err)
	}
	p, _ := LoadPortfolio(ctx, store, 7, 999)
	if got, want := p.Assets["TCB"].Quantity, int64(152); got != want {
		t.Fatalf("holding = %d, want %d", got, want)
	}
	if p.Assets["TCB"].Base != 139*30_000 || p.Assets["TCB"].OpenedAt != 100 {
		t.Fatalf("share dividend position: %+v", p.Assets["TCB"])
	}
	rb.AssertSentText(t, "Share dividend (100:10): +13 TCB")
}

func TestHandleShareDividendFormatsExactLargeQuantities(t *testing.T) {
	ctx := context.Background()
	store := newStockStore()
	seedStockPortfolio(t, store, 7, 9_007_199_254_740_993, 0)
	s := &state{store: store, nowFn: func() time.Time { return time.UnixMilli(123) }}
	rb := testutil.NewRecordingBot(t)

	if err := s.handleShareDividend(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/stock_share_dividend 1:1 TCB")); err != nil {
		t.Fatalf("handleShareDividend: %v", err)
	}
	rb.AssertSentText(t, "Share dividend (1:1): +9.007.199.254.740.993 TCB")
	rb.AssertSentText(t, "Holding: 9.007.199.254.740.993 → 18.014.398.509.481.986")
}

func TestHandleShareDividendRejectsZeroEntitlement(t *testing.T) {
	ctx := context.Background()
	base := newStockStore()
	seedStockPortfolio(t, base, 7, 9, 0)
	store := &countingPortfolioStore{Store: base}
	s := &state{store: store, nowFn: func() time.Time { return time.UnixMilli(123) }}
	rb := testutil.NewRecordingBot(t)

	if err := s.handleShareDividend(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/stock_share_dividend 100:10 TCB")); err != nil {
		t.Fatalf("handleShareDividend: %v", err)
	}
	if store.puts != 0 {
		t.Fatalf("store writes = %d, want 0", store.puts)
	}
	p, _ := LoadPortfolio(ctx, base, 7, 999)
	if p.Assets["TCB"].Quantity != 9 {
		t.Fatalf("holding changed to %d", p.Assets["TCB"].Quantity)
	}
	rb.AssertSentText(t, "Minimum holding: 10")
}

func TestHandleShareDividendFormatsExactLargeMinimum(t *testing.T) {
	ctx := context.Background()
	store := newStockStore()
	seedStockPortfolio(t, store, 7, 1, 0)
	s := &state{store: store, nowFn: func() time.Time { return time.UnixMilli(123) }}
	rb := testutil.NewRecordingBot(t)

	if err := s.handleShareDividend(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/stock_share_dividend 9007199254740993:1 TCB")); err != nil {
		t.Fatalf("handleShareDividend: %v", err)
	}
	rb.AssertSentText(t, "Minimum holding: 9.007.199.254.740.993.")
}

func modDepsForTest() modules.Deps {
	return modules.Deps{Store: storage.NewMemoryProvider().Collection("stock")}
}
