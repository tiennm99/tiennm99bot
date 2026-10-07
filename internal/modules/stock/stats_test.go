package stock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

func TestHandleStats_UsesSSIBatchPrices(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)

	requests := 0
	var gotStocks []string
	priceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/stock/multiple" {
			t.Errorf("path = %q, want /stock/multiple", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotStocks = append([]string(nil), r.PostForm["stocks"]...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"stockSymbol":"MWG","matchedPrice":70000},{"stockSymbol":"TCB","matchedPrice":30000},{"stockSymbol":"FPT","matchedPrice":120000}]}`))
	}))
	t.Cleanup(priceSrv.Close)

	store := newStockStore()
	p := NewPortfolio(now.UnixMilli())
	p.VND = 2335000
	p.Meta.Invested = 1000000000
	_ = p.BuyTicker("MWG", 1800, 108_000_000, 1)
	_ = p.BuyTicker("TCB", 4200, 105_000_000, 1)
	_ = p.BuyTicker("FPT", 2300, 230_000_000, 1)
	if err := SavePortfolio(ctx, store, 7, p); err != nil {
		t.Fatalf("SavePortfolio: %v", err)
	}

	s := &state{
		store:  store,
		prices: &PriceClient{HTTP: priceSrv.Client(), URL: priceSrv.URL},
		nowFn:  func() time.Time { return now },
	}
	rb := testutil.NewRecordingBot(t)
	if err := s.handleStats(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/stock_portfolio")); err != nil {
		t.Fatalf("handleStats: %v", err)
	}

	if requests != 1 {
		t.Fatalf("requests = %d, want one SSI batch request", requests)
	}
	sort.Strings(gotStocks)
	if wantStocks := []string{"FPT", "MWG", "TCB"}; !reflect.DeepEqual(gotStocks, wantStocks) {
		t.Fatalf("stocks form values = %#v, want %#v", gotStocks, wantStocks)
	}

	text := rb.LastSent().Text()
	for _, want := range []string{
		"Stock Portfolio",
		"<pre>",
		"Sym",
		"MWG",
		"60",
		"70",
		"126M",
		"+18M",
		"+16.67%",
		"Cash",
		"Total value",
		"530.335.000",
		"P&amp;L",
		"+85.000.000 (+19.19%)",
		"Account P&amp;L",
		"-469.665.000 (-46.97%)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("stats missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "N/A") {
		t.Fatalf("stats rendered missing prices:\n%s", text)
	}
	if strings.Contains(text, "VND") {
		t.Fatalf("stock portfolio should use implicit VND:\n%s", text)
	}
}

func TestHandleStats_UnavailablePriceKeepsCompactAverage(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	priceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(priceSrv.Close)

	store := newStockStore()
	p := NewPortfolio(now.UnixMilli())
	p.VND = 1_000_000
	p.Meta.Invested = 3_535_000
	if err := p.BuyTicker("TCB", 100, 2_535_000, 1); err != nil {
		t.Fatal(err)
	}
	if err := SavePortfolio(ctx, store, 7, p); err != nil {
		t.Fatal(err)
	}

	s := &state{
		store:  store,
		prices: &PriceClient{HTTP: priceSrv.Client(), URL: priceSrv.URL},
		nowFn:  func() time.Time { return now },
	}
	rb := testutil.NewRecordingBot(t)
	if err := s.handleStats(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/stock_portfolio")); err != nil {
		t.Fatal(err)
	}

	text := rb.LastSent().Text()
	for _, want := range []string{"TCB", "25,35", "N/A", "Priced value (partial)", "Account P&amp;L", "Unavailable"} {
		if !strings.Contains(text, want) {
			t.Fatalf("portfolio missing %q in:\n%s", want, text)
		}
	}
	if got := strings.Count(text, "N/A"); got != 4 {
		t.Fatalf("missing-price position has %d N/A cells, want 4:\n%s", got, text)
	}
}

func TestHandleStats_OverflowedValuationKeepsMonetaryCellsUnavailable(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	priceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"stockSymbol":"FPT","matchedPrice":1.7976931348623157e308}]}`))
	}))
	t.Cleanup(priceSrv.Close)

	store := newStockStore()
	p := NewPortfolio(now.UnixMilli())
	p.Meta.Invested = 2_000
	if err := p.BuyTicker("FPT", 2, 2_000, 1); err != nil {
		t.Fatal(err)
	}
	if err := SavePortfolio(ctx, store, 7, p); err != nil {
		t.Fatal(err)
	}

	s := &state{
		store:  store,
		prices: &PriceClient{HTTP: priceSrv.Client(), URL: priceSrv.URL},
		nowFn:  func() time.Time { return now },
	}
	rb := testutil.NewRecordingBot(t)
	if err := s.handleStats(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/stock_portfolio")); err != nil {
		t.Fatal(err)
	}

	text := rb.LastSent().Text()
	for _, want := range []string{"FPT", "Account P&amp;L", "Unavailable"} {
		if !strings.Contains(text, want) {
			t.Fatalf("overflow portfolio missing %q in:\n%s", want, text)
		}
	}
	if got := strings.Count(text, "N/A"); got != 5 {
		t.Fatalf("overflowed position has %d N/A monetary cells, want 5:\n%s", got, text)
	}
	if strings.Contains(text, "1k") {
		t.Fatalf("overflowed position exposed its average instead of N/A:\n%s", text)
	}
}

func TestStockPortfolioReplyStaysWithinTelegramBudget(t *testing.T) {
	positions := make([]string, 200)
	for i := range positions {
		positions[i] = strings.Repeat("position-data-", 20)
	}
	rows := make([][]string, len(positions))
	for index, position := range positions {
		rows[index] = []string{position}
	}
	reply := portfolioTableReply("header", rows, [][]string{{"summary", "value"}})
	if len(reply) > portfolioReplyLimit {
		t.Fatalf("reply length = %d, limit = %d", len(reply), portfolioReplyLimit)
	}
	if !strings.Contains(reply, "omitted") || !strings.Contains(reply, "summary") {
		t.Fatalf("bounded reply lost omission marker or summary: %q", reply)
	}
}
