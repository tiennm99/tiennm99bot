package stock

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

func newStockInfoTestState(t *testing.T, ssiHandler http.HandlerFunc) (*state, *int, *int) {
	t.Helper()

	ssiRequests := 0
	ssiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ssiRequests++
		ssiHandler(w, r)
	}))
	t.Cleanup(ssiServer.Close)

	fallbackRequests := 0
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		fallbackRequests++
	}))
	t.Cleanup(fallbackServer.Close)

	s := &state{prices: &PriceClient{
		HTTP:   ssiServer.Client(),
		URL:    ssiServer.URL,
		KBSURL: fallbackServer.URL,
		VCIURL: fallbackServer.URL,
	}}
	return s, &ssiRequests, &fallbackRequests
}

func TestStockInfoCommandRegistration(t *testing.T) {
	mod := New(modDepsForTest())
	for _, command := range mod.Commands {
		if command.Name != "stock_info" {
			continue
		}
		if command.Parameters != "<ticker>" || command.Description != "Show detailed SSI quote for a VN stock" {
			t.Fatalf("stock_info metadata = params %q description %q", command.Parameters, command.Description)
		}
		return
	}
	t.Fatal("stock_info command is not registered")
}

func TestHandleStockInfoOneSSIRequestAndSenderless(t *testing.T) {
	s, ssiRequests, fallbackRequests := newStockInfoTestState(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/stock/TCB" {
			t.Errorf("request = %s %s, want GET /stock/TCB", r.Method, r.URL.Path)
		}
		if r.Header.Get("Origin") != "https://iboard.ssi.com.vn" {
			t.Errorf("Origin = %q", r.Header.Get("Origin"))
		}
		_, _ = w.Write([]byte(`{"data":{
			"stockSymbol":"TCB",
			"companyNameVi":"Ngân hàng Kỹ Thương",
			"companyNameEn":"Techcombank",
			"exchange":"HOSE",
			"refPrice":30000,
			"openPrice":29000,
			"highest":31500,
			"lowest":28500,
			"matchedPrice":30500,
			"nmTotalTradedQty":1234567
		}}`))
	})
	rb := testutil.NewRecordingBot(t)
	if err := s.handleStockInfo(context.Background(), rb.Bot, testutil.NewChannelMessage(-100, "/stock_info tcb")); err != nil {
		t.Fatalf("handleStockInfo: %v", err)
	}
	if *ssiRequests != 1 {
		t.Fatalf("SSI requests = %d, want 1", *ssiRequests)
	}
	if *fallbackRequests != 0 {
		t.Fatalf("fallback requests = %d, want 0", *fallbackRequests)
	}
	want := strings.Join([]string{
		"TCB — Ngân hàng Kỹ Thương",
		"Exchange: HOSE",
		"Current: 30.500 VND",
		"Since open: +1.500 VND (+5.17%)",
		"Vs reference: +500 VND (+1.67%)",
		"Open: 29.000 VND",
		"High: 31.500 VND",
		"Low: 28.500 VND",
		"Volume: 1.234.567",
	}, "\n")
	if got := rb.LastSent().Text(); got != want {
		t.Fatalf("reply:\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatStockInfoNegativeChanges(t *testing.T) {
	volume := 100.0
	got := formatStockInfo("FPT", ssiStockQuoteDetail{
		CompanyNameVi:    "FPT",
		Exchange:         "HOSE",
		RefPrice:         125000,
		OpenPrice:        124000,
		Highest:          126000,
		Lowest:           120000,
		MatchedPrice:     121000,
		NMTotalTradedQty: &volume,
	})
	if !strings.Contains(got, "Since open: -3.000 VND (-2.42%)") {
		t.Errorf("missing negative since-open change: %q", got)
	}
	if !strings.Contains(got, "Vs reference: -4.000 VND (-3.20%)") {
		t.Errorf("missing negative reference change: %q", got)
	}
}

func TestFormatStockInfoOptionalFieldsAndCompanyFallback(t *testing.T) {
	quote := ssiStockQuoteDetail{
		CompanyNameEn: "Techcombank",
		MatchedPrice:  30000,
	}
	got := formatStockInfo("TCB", quote)
	want := strings.Join([]string{
		"TCB — Techcombank",
		"Exchange: N/A",
		"Current: 30.000 VND",
		"Since open: N/A",
		"Vs reference: N/A",
		"Open: N/A",
		"High: N/A",
		"Low: N/A",
		"Volume: N/A",
	}, "\n")
	if got != want {
		t.Fatalf("reply:\n%q\nwant:\n%q", got, want)
	}

	quote.CompanyNameEn = ""
	if got := formatStockInfo("TCB", quote); !strings.HasPrefix(got, "TCB\n") {
		t.Fatalf("missing-company title = %q, want ticker only", got)
	}
}

func TestFormatStockInfoChangeBoundaries(t *testing.T) {
	if got := stockInfoChange(30000, 30000); got != "0 VND (0.00%)" {
		t.Fatalf("zero change = %q, want neutral zero", got)
	}
	if got := stockInfoChange(math.MaxFloat64, 1); got != "N/A" {
		t.Fatalf("overflowing percentage = %q, want N/A", got)
	}
}

func TestFormatStockInfoVolumeBoundaries(t *testing.T) {
	zero := 0.0
	negative := -1.0
	infinite := math.Inf(1)
	for _, tc := range []struct {
		name  string
		value *float64
		want  string
	}{
		{name: "missing", value: nil, want: "N/A"},
		{name: "zero", value: &zero, want: "0"},
		{name: "negative", value: &negative, want: "N/A"},
		{name: "nonfinite", value: &infinite, want: "N/A"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stockInfoVolume(tc.value); got != tc.want {
				t.Fatalf("stockInfoVolume = %q, want %q", got, tc.want)
			}
		})
	}
	if got := formatStockInfo("TCB", ssiStockQuoteDetail{MatchedPrice: 30000, NMTotalTradedQty: &zero}); !strings.Contains(got, "Volume: 0") {
		t.Fatalf("explicit-zero reply = %q, want Volume: 0", got)
	}
}

func TestHandleStockInfoBoundsOversizedUpstreamText(t *testing.T) {
	s, ssiRequests, fallbackRequests := newStockInfoTestState(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"stockSymbol":   "TCB",
			"companyNameVi": strings.Repeat("ổ", 10000),
			"exchange":      strings.Repeat("X", 10000),
			"matchedPrice":  30000,
		}})
	})
	rb := testutil.NewRecordingBot(t)
	if err := s.handleStockInfo(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_info TCB")); err != nil {
		t.Fatalf("handleStockInfo: %v", err)
	}
	reply := rb.LastSent().Text()
	if got := utf8.RuneCountInString(reply); got >= 4000 {
		t.Fatalf("reply rune count = %d, want below 4000", got)
	}
	lines := strings.Split(reply, "\n")
	if got := utf8.RuneCountInString(lines[0]); got > utf8.RuneCountInString("TCB — ")+stockInfoCompanyLimit {
		t.Fatalf("title rune count = %d, company limit not applied", got)
	}
	if got := utf8.RuneCountInString(strings.TrimPrefix(lines[1], "Exchange: ")); got > stockInfoExchangeLimit {
		t.Fatalf("exchange rune count = %d, limit not applied", got)
	}
	if *ssiRequests != 1 || *fallbackRequests != 0 {
		t.Fatalf("requests: SSI=%d fallback=%d", *ssiRequests, *fallbackRequests)
	}
}

func TestHandleStockInfoValidationDoesNotRequestSSI(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	t.Cleanup(server.Close)
	s := &state{prices: &PriceClient{HTTP: server.Client(), URL: server.URL}}
	rb := testutil.NewRecordingBot(t)

	for _, tc := range []struct {
		command string
		want    string
	}{
		{"/stock_info", "Usage: /stock_info <ticker>"},
		{"/stock_info TCB extra", "Usage: /stock_info <ticker>"},
		{"/stock_info $$$", "Unknown stock ticker \"$$$\"."},
	} {
		rb.Reset()
		if err := s.handleStockInfo(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, tc.command)); err != nil {
			t.Fatalf("%q: %v", tc.command, err)
		}
		rb.AssertSentText(t, tc.want)
	}
	if requests != 0 {
		t.Fatalf("SSI requests = %d, want 0", requests)
	}
}

func TestHandleStockInfoFailureUsesOneSSIRequestAndNoFallback(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		location string
		body     string
		want     string
	}{
		{
			name: "no matched price",
			body: `{"data":{"stockSymbol":"TCB","matchedPrice":0}}`,
			want: "No stock information available for TCB.",
		},
		{
			name: "malformed response",
			body: `{"data":`,
			want: "Could not fetch stock information for TCB. Try again later.",
		},
		{
			name:   "upstream error",
			status: http.StatusBadGateway,
			body:   "upstream unavailable",
			want:   "Could not fetch stock information for TCB. Try again later.",
		},
		{
			name:     "redirect",
			status:   http.StatusFound,
			location: "/redirect-target",
			want:     "Could not fetch stock information for TCB. Try again later.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ssiRequests, fallbackRequests := newStockInfoTestState(t, func(w http.ResponseWriter, _ *http.Request) {
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte(tc.body))
			})
			rb := testutil.NewRecordingBot(t)
			if err := s.handleStockInfo(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_info TCB")); err != nil {
				t.Fatalf("handleStockInfo: %v", err)
			}
			rb.AssertSentText(t, tc.want)
			if *ssiRequests != 1 {
				t.Fatalf("SSI requests = %d, want 1", *ssiRequests)
			}
			if *fallbackRequests != 0 {
				t.Fatalf("fallback requests = %d, want 0", *fallbackRequests)
			}
		})
	}
}
