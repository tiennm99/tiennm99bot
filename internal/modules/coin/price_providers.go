package coin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var errProviderRateLimited = errors.New("coin: provider rate limited")

// BinanceProvider quotes the coin's USDT pair, then its USD pair. URL
// overrides the public market-data endpoint for tests.
type BinanceProvider struct {
	HTTP *http.Client
	URL  string
}

// CoinbaseProvider reads the coin's USD rate from Coinbase exchange rates.
type CoinbaseProvider struct {
	HTTP *http.Client
	URL  string
}

// CoinGeckoProvider looks up the coin by its known CoinGecko ID or, failing
// that, by the lower-case ticker and then the best market-cap match from
// CoinGecko search.
type CoinGeckoProvider struct {
	HTTP      *http.Client
	URL       string
	SearchURL string
}

type binanceResponse struct {
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
}

type coinbaseResponse struct {
	Data struct {
		Currency string            `json:"currency"`
		Rates    map[string]string `json:"rates"`
	} `json:"data"`
}

type coinGeckoQuote struct {
	USD float64 `json:"usd"`
}

type coinGeckoSearchResponse struct {
	Coins []coinGeckoSearchCoin `json:"coins"`
}

type coinGeckoSearchCoin struct {
	ID            string `json:"id"`
	Symbol        string `json:"symbol"`
	MarketCapRank *int   `json:"market_cap_rank"`
}

// FetchUSD stops at the first rate-limit response instead of trying the USD
// pair, so the caller can move on to the next provider.
func (p *BinanceProvider) FetchUSD(ctx context.Context, coin CoinSymbol) (CoinPrice, error) {
	for _, quote := range []string{"USDT", "USD"} {
		price, err := p.fetchPair(ctx, coin.Symbol, quote)
		if err == nil {
			return price, nil
		}
		if errors.Is(err, errProviderRateLimited) {
			return CoinPrice{}, err
		}
	}
	return CoinPrice{}, ErrNoCoinPrice
}

func (p *BinanceProvider) fetchPair(ctx context.Context, symbol, quote string) (CoinPrice, error) {
	endpoint := p.baseURL()
	if err := validateEndpoint(endpoint); err != nil {
		return CoinPrice{}, err
	}
	q := url.Values{}
	q.Set("symbol", symbol+quote)
	resp, err := getJSON(ctx, p.HTTP, endpoint+"?"+q.Encode())
	if err != nil {
		return CoinPrice{}, fmt.Errorf("coin: Binance request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusTeapot {
		return CoinPrice{}, errProviderRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CoinPrice{}, ErrNoCoinPrice
	}
	var body binanceResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return CoinPrice{}, fmt.Errorf("coin: Binance decode: %w", err)
	}
	price, err := parsePositivePrice(body.Price)
	if err != nil {
		return CoinPrice{}, ErrNoCoinPrice
	}
	return CoinPrice{Symbol: symbol, USD: price, Source: "Binance"}, nil
}

func (p *BinanceProvider) baseURL() string {
	if strings.TrimSpace(p.URL) != "" {
		return strings.TrimSpace(p.URL)
	}
	return binanceDefaultURL
}

func (p *CoinbaseProvider) FetchUSD(ctx context.Context, coin CoinSymbol) (CoinPrice, error) {
	endpoint := p.baseURL()
	if err := validateEndpoint(endpoint); err != nil {
		return CoinPrice{}, err
	}
	q := url.Values{}
	q.Set("currency", coin.Symbol)
	resp, err := getJSON(ctx, p.HTTP, endpoint+"?"+q.Encode())
	if err != nil {
		return CoinPrice{}, fmt.Errorf("coin: Coinbase request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CoinPrice{}, ErrNoCoinPrice
	}
	var body coinbaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return CoinPrice{}, fmt.Errorf("coin: Coinbase decode: %w", err)
	}
	price, err := parsePositivePrice(body.Data.Rates["USD"])
	if err != nil {
		return CoinPrice{}, ErrNoCoinPrice
	}
	return CoinPrice{Symbol: coin.Symbol, USD: price, Source: "Coinbase"}, nil
}

func (p *CoinbaseProvider) baseURL() string {
	if strings.TrimSpace(p.URL) != "" {
		return strings.TrimSpace(p.URL)
	}
	return coinbaseDefaultURL
}

func (p *CoinGeckoProvider) FetchUSD(ctx context.Context, coin CoinSymbol) (CoinPrice, error) {
	if coin.CoinGeckoID != "" {
		return p.fetchID(ctx, coin, coin.CoinGeckoID)
	}
	price, err := p.fetchID(ctx, coin, strings.ToLower(coin.Symbol))
	if err == nil {
		return price, nil
	}
	if !errors.Is(err, ErrNoCoinPrice) {
		return CoinPrice{}, err
	}
	id, err := p.searchID(ctx, coin.Symbol)
	if err != nil {
		return CoinPrice{}, err
	}
	return p.fetchID(ctx, coin, id)
}

func (p *CoinGeckoProvider) fetchID(ctx context.Context, coin CoinSymbol, id string) (CoinPrice, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return CoinPrice{}, ErrNoCoinPrice
	}
	endpoint := p.baseURL()
	if err := validateEndpoint(endpoint); err != nil {
		return CoinPrice{}, err
	}
	q := url.Values{}
	q.Set("ids", id)
	q.Set("vs_currencies", "usd")
	q.Set("include_last_updated_at", "true")
	resp, err := getJSON(ctx, p.HTTP, endpoint+"?"+q.Encode())
	if err != nil {
		return CoinPrice{}, fmt.Errorf("coin: CoinGecko request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CoinPrice{}, ErrNoCoinPrice
	}
	var body map[string]coinGeckoQuote
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return CoinPrice{}, fmt.Errorf("coin: CoinGecko decode: %w", err)
	}
	quote := body[id]
	if quote.USD <= 0 {
		return CoinPrice{}, ErrNoCoinPrice
	}
	return CoinPrice{Symbol: coin.Symbol, USD: quote.USD, Source: "CoinGecko"}, nil
}

func (p *CoinGeckoProvider) searchID(ctx context.Context, symbol string) (string, error) {
	endpoint := p.searchURL()
	if err := validateEndpoint(endpoint); err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("query", symbol)
	resp, err := getJSON(ctx, p.HTTP, endpoint+"?"+q.Encode())
	if err != nil {
		return "", fmt.Errorf("coin: CoinGecko search request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", ErrNoCoinPrice
	}
	var body coinGeckoSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("coin: CoinGecko search decode: %w", err)
	}
	best := coinGeckoSearchCoin{}
	for _, candidate := range body.Coins {
		if candidate.ID == "" || !strings.EqualFold(candidate.Symbol, symbol) {
			continue
		}
		if best.ID == "" || betterCoinGeckoSearchMatch(candidate, best) {
			best = candidate
		}
	}
	if best.ID == "" {
		return "", ErrNoCoinPrice
	}
	return best.ID, nil
}

func betterCoinGeckoSearchMatch(candidate, current coinGeckoSearchCoin) bool {
	if candidate.MarketCapRank == nil {
		return false
	}
	if current.MarketCapRank == nil {
		return true
	}
	return *candidate.MarketCapRank < *current.MarketCapRank
}

func (p *CoinGeckoProvider) baseURL() string {
	if strings.TrimSpace(p.URL) != "" {
		return strings.TrimSpace(p.URL)
	}
	return coinGeckoDefaultURL
}

func (p *CoinGeckoProvider) searchURL() string {
	if strings.TrimSpace(p.SearchURL) != "" {
		return strings.TrimSpace(p.SearchURL)
	}
	return coinGeckoSearchURL
}

func getJSON(ctx context.Context, client *http.Client, endpoint string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (tiennm99bot)")
	if client == nil {
		client = &http.Client{Timeout: coinHTTPTimeout}
	}
	return client.Do(req)
}

func parsePositivePrice(raw string) (float64, error) {
	price, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || !isPositiveFinite(price) {
		return 0, ErrNoCoinPrice
	}
	return price, nil
}
