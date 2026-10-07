package stock

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestPriceClient(t *testing.T, handler http.HandlerFunc) (*PriceClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &PriceClient{
		HTTP: &http.Client{Timeout: 2 * time.Second},
		URL:  srv.URL,
	}, srv
}

func TestPriceClient_HappyPath(t *testing.T) {
	c, _ := newTestPriceClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/stock/TCB" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Origin"); got != "https://iboard.ssi.com.vn" {
			t.Errorf("origin = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"stockSymbol":"TCB","matchedPrice":24500}}`))
	})
	got, err := c.FetchPrice(context.Background(), "TCB")
	if err != nil {
		t.Fatalf("FetchPrice: %v", err)
	}
	if got != 24500 {
		t.Errorf("price: got %v, want 24500", got)
	}
}

func TestPriceClient_FetchSSIQuoteOneRequest(t *testing.T) {
	requests := 0
	c, _ := newTestPriceClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/stock/TCB" {
			t.Errorf("path = %q, want /stock/TCB", r.URL.Path)
		}
		for header, want := range map[string]string{
			"Accept":  "application/json",
			"Origin":  "https://iboard.ssi.com.vn",
			"Referer": "https://iboard.ssi.com.vn/",
		} {
			if got := r.Header.Get(header); got != want {
				t.Errorf("%s = %q, want %q", header, got, want)
			}
		}
		if got := r.Header.Get("User-Agent"); !strings.Contains(got, "tiennm99bot") {
			t.Errorf("User-Agent = %q, want tiennm99bot", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{
			"stockSymbol":"TCB",
			"companyNameVi":"Ngân hàng TMCP Kỹ Thương Việt Nam",
			"companyNameEn":"Vietnam Technological and Commercial Joint Stock Bank",
			"exchange":"HOSE",
			"refPrice":30000,
			"openPrice":29500,
			"highest":31000,
			"lowest":29200,
			"matchedPrice":30500,
			"nmTotalTradedQty":1234567
		}}`))
	})

	got, err := c.fetchSSIQuote(context.Background(), "TCB")
	if err != nil {
		t.Fatalf("fetchSSIQuote: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if got.StockSymbol != "TCB" ||
		got.CompanyNameVi != "Ngân hàng TMCP Kỹ Thương Việt Nam" ||
		got.CompanyNameEn != "Vietnam Technological and Commercial Joint Stock Bank" ||
		got.Exchange != "HOSE" ||
		got.RefPrice != 30000 ||
		got.OpenPrice != 29500 ||
		got.Highest != 31000 ||
		got.Lowest != 29200 ||
		got.MatchedPrice != 30500 ||
		got.NMTotalTradedQty == nil ||
		*got.NMTotalTradedQty != 1234567 {
		t.Fatalf("quote = %+v", got)
	}
}

func TestPriceClient_LegacyQuotesIgnoreDetailSchemaDrift(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		c, _ := newTestPriceClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"data":{
				"stockSymbol":"TCB",
				"matchedPrice":24500,
				"companyNameVi":{"unexpected":"object"},
				"companyNameEn":123,
				"exchange":false,
				"refPrice":"bad",
				"openPrice":null,
				"highest":[],
				"lowest":{},
				"nmTotalTradedQty":"unknown"
			}}`))
		})
		got, err := c.FetchPrice(context.Background(), "TCB")
		if err != nil {
			t.Fatalf("FetchPrice: %v", err)
		}
		if got != 24500 {
			t.Fatalf("price = %v, want 24500", got)
		}
	})

	t.Run("batch", func(t *testing.T) {
		c, _ := newTestPriceClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"data":[{
				"stockSymbol":"TCB",
				"matchedPrice":24500,
				"companyNameVi":123,
				"exchange":{"unexpected":"object"},
				"openPrice":"bad",
				"nmTotalTradedQty":null
			}]}`))
		})
		got, err := c.FetchPrices(context.Background(), []string{"TCB"})
		if err != nil {
			t.Fatalf("FetchPrices: %v", err)
		}
		if got["TCB"] != 24500 {
			t.Fatalf("prices = %+v, want TCB=24500", got)
		}
	})
}

func TestPriceClient_FetchSSIQuoteStopsRedirectWithoutMutatingLegacyClient(t *testing.T) {
	requests := 0
	c, srv := newTestPriceClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/stock/TCB" {
			http.Redirect(w, r, "/quote", http.StatusFound)
			return
		}
		if r.URL.Path != "/quote" {
			t.Errorf("path = %q, want /quote", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":{"stockSymbol":"TCB","matchedPrice":24500}}`))
	})

	_, err := c.fetchSSIQuote(context.Background(), "TCB")
	if err == nil || !strings.Contains(err.Error(), "SSI status 302") {
		t.Fatalf("fetchSSIQuote error = %v, want SSI status 302", err)
	}
	if requests != 1 {
		t.Fatalf("detail requests = %d, want 1", requests)
	}

	requests = 0
	got, err := c.FetchPrice(context.Background(), "TCB")
	if err != nil {
		t.Fatalf("legacy FetchPrice through redirect: %v", err)
	}
	if got != 24500 || requests != 2 {
		t.Fatalf("legacy price = %v, requests = %d; want 24500 and 2", got, requests)
	}
	if c.HTTP.CheckRedirect != nil {
		t.Fatalf("shared client CheckRedirect was mutated after request to %s", srv.URL)
	}
}

func TestPriceClient_FetchSSIQuoteFailureUsesOneRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want error
	}{
		{name: "no price", body: `{"data":{"stockSymbol":"TCB","matchedPrice":0}}`, want: ErrNoPrice},
		{name: "malformed", body: `{"data":`, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			c, _ := newTestPriceClient(t, func(w http.ResponseWriter, _ *http.Request) {
				requests++
				_, _ = w.Write([]byte(tc.body))
			})

			_, err := c.fetchSSIQuote(context.Background(), "TCB")
			if err == nil {
				t.Fatal("fetchSSIQuote: expected error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if tc.want == nil && !strings.Contains(err.Error(), "SSI decode") {
				t.Fatalf("error = %v, want SSI decode error", err)
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}
		})
	}
}

func TestPriceClient_BatchHappyPath(t *testing.T) {
	c, _ := newTestPriceClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/stock/multiple" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Errorf("content-type = %q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		stocks := r.Form["stocks"]
		if strings.Join(stocks, ",") != "TCB,FPT" {
			t.Errorf("stocks = %v, want [TCB FPT]", stocks)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"stockSymbol":"TCB","matchedPrice":24500},{"stockSymbol":"FPT","matchedPrice":120000}]}`))
	})
	got, err := c.FetchPrices(context.Background(), []string{"TCB", "FPT"})
	if err != nil {
		t.Fatalf("FetchPrices: %v", err)
	}
	if got["TCB"] != 24500 || got["FPT"] != 120000 {
		t.Errorf("prices = %+v", got)
	}
}

func TestPriceClient_FallsBackToKBSWhenSSIDirectBlocked(t *testing.T) {
	ssiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<title>Security Check - SSI</title>"))
	}))
	t.Cleanup(ssiSrv.Close)

	kbsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/" {
			t.Errorf("path = %q, want /", r.URL.Path)
		}
		var body kbsRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode KBS request: %v", err)
		}
		if body.Code != "TCB" {
			t.Errorf("KBS code = %q, want TCB", body.Code)
		}
		if got := r.Header.Get("User-Agent"); !strings.Contains(got, "Chrome/120") {
			t.Errorf("KBS user-agent = %q, want browser-like", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"SB":"TCB","CP":33400}]`))
	}))
	t.Cleanup(kbsSrv.Close)

	c := &PriceClient{HTTP: ssiSrv.Client(), URL: ssiSrv.URL, KBSURL: kbsSrv.URL}
	got, err := c.FetchPrice(context.Background(), "TCB")
	if err != nil {
		t.Fatalf("FetchPrice: %v", err)
	}
	if got != 33400 {
		t.Errorf("price: got %v, want 33400", got)
	}
}

func TestPriceClient_BatchFallsBackToKBSWhenSSIDirectBlocked(t *testing.T) {
	ssiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<title>Security Check - SSI</title>"))
	}))
	t.Cleanup(ssiSrv.Close)

	var gotCodes []string
	kbsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body kbsRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode KBS request: %v", err)
		}
		gotCodes = append(gotCodes, body.Code)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"SB":"TCB","CP":33400},{"SB":"FPT","CP":71000}]`))
	}))
	t.Cleanup(kbsSrv.Close)

	c := &PriceClient{HTTP: ssiSrv.Client(), URL: ssiSrv.URL, KBSURL: kbsSrv.URL}
	got, err := c.FetchPrices(context.Background(), []string{"TCB", "FPT"})
	if err != nil {
		t.Fatalf("FetchPrices: %v", err)
	}
	if got["TCB"] != 33400 || got["FPT"] != 71000 {
		t.Errorf("prices = %+v", got)
	}
	if strings.Join(gotCodes, ",") != "TCB,FPT" {
		t.Errorf("KBS codes = %v, want one batch TCB,FPT", gotCodes)
	}
}

func TestPriceClient_BatchFallsBackToVCIWhenKBSBlocked(t *testing.T) {
	ssiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<title>Security Check - SSI</title>"))
	}))
	t.Cleanup(ssiSrv.Close)

	kbsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(kbsSrv.Close)

	var gotSymbols []string
	vciSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body vciRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode VCI request: %v", err)
		}
		gotSymbols = append(gotSymbols, body.Symbols...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"listingInfo":{"symbol":"TCB"},"matchPrice":{"matchPrice":33400}},
			{"listingInfo":{"symbol":"FPT"},"matchPrice":{"matchPrice":71000}}
		]`))
	}))
	t.Cleanup(vciSrv.Close)

	c := &PriceClient{HTTP: ssiSrv.Client(), URL: ssiSrv.URL, KBSURL: kbsSrv.URL, VCIURL: vciSrv.URL}
	got, err := c.FetchPrices(context.Background(), []string{"TCB", "FPT"})
	if err != nil {
		t.Fatalf("FetchPrices: %v", err)
	}
	if got["TCB"] != 33400 || got["FPT"] != 71000 {
		t.Errorf("prices = %+v", got)
	}
	if strings.Join(gotSymbols, ",") != "TCB,FPT" {
		t.Errorf("VCI symbols = %v, want TCB,FPT", gotSymbols)
	}
}

func TestPriceClient_BatchOmitsInvalidQuotes(t *testing.T) {
	c, _ := newTestPriceClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"stockSymbol":"TCB","matchedPrice":24500},{"stockSymbol":"BAD","matchedPrice":0},{"stockSymbol":"NEG","matchedPrice":-1}]}`))
	})
	got, err := c.FetchPrices(context.Background(), []string{"TCB", "BAD", "NEG"})
	if err != nil {
		t.Fatalf("FetchPrices: %v", err)
	}
	if len(got) != 1 || got["TCB"] != 24500 {
		t.Errorf("prices = %+v, want only TCB", got)
	}
}

func TestPriceClient_NoData_ReturnsErrNoPrice(t *testing.T) {
	c, _ := newTestPriceClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":null}`))
	})
	_, err := c.FetchPrice(context.Background(), "NOPE")
	if !errors.Is(err, ErrNoPrice) {
		t.Errorf("got %v, want ErrNoPrice", err)
	}
}

func TestPriceClient_BatchNoUsableData_ReturnsErrNoPrice(t *testing.T) {
	c, _ := newTestPriceClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"stockSymbol":"NOPE","matchedPrice":0}]}`))
	})
	_, err := c.FetchPrices(context.Background(), []string{"NOPE"})
	if !errors.Is(err, ErrNoPrice) {
		t.Errorf("got %v, want ErrNoPrice", err)
	}
	if !strings.Contains(err.Error(), "SSI batch returned no usable quotes for NOPE (data_len=1)") {
		t.Errorf("error = %q, want batch diagnostic", err.Error())
	}
}

func TestPriceClient_4xx_ReturnsErrNoPrice(t *testing.T) {
	c, _ := newTestPriceClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("blocked by upstream"))
	})
	_, err := c.FetchPrice(context.Background(), "BADTICKER")
	if !errors.Is(err, ErrNoPrice) {
		t.Errorf("got %v, want ErrNoPrice", err)
	}
	if !strings.Contains(err.Error(), "SSI status 404 body") || !strings.Contains(err.Error(), "blocked by upstream") {
		t.Errorf("error = %q, want status/body diagnostic", err.Error())
	}
}

func TestPriceClient_NegativeMatchedPrice_ReturnsErrNoPrice(t *testing.T) {
	c, _ := newTestPriceClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"stockSymbol":"WEIRD","matchedPrice":-1}}`))
	})
	_, err := c.FetchPrice(context.Background(), "WEIRD")
	if !errors.Is(err, ErrNoPrice) {
		t.Errorf("got %v, want ErrNoPrice", err)
	}
}

func TestPriceClient_EmptyTicker(t *testing.T) {
	c := &PriceClient{}
	_, err := c.FetchPrice(context.Background(), "")
	if err == nil {
		t.Error("empty ticker: expected error, got nil")
	}
}
