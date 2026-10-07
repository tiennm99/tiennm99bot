package stock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ssiQueryDefaultURL is the SSI iBoard stock-query endpoint base.
const ssiQueryDefaultURL = "https://iboard-query.ssi.com.vn"

// ssiErrorBodyLimit keeps upstream diagnostics useful without dumping large responses.
const ssiErrorBodyLimit = 512

type ssiSingleResponse struct {
	Data ssiStockQuote `json:"data"`
}

type ssiQuoteDetailResponse struct {
	Data ssiStockQuoteDetail `json:"data"`
}

type ssiMultipleResponse struct {
	Data []ssiStockQuote `json:"data"`
}

type ssiStockQuote struct {
	StockSymbol  string  `json:"stockSymbol"`
	MatchedPrice float64 `json:"matchedPrice"`
}

type ssiStockQuoteDetail struct {
	StockSymbol      string   `json:"stockSymbol"`
	CompanyNameVi    string   `json:"companyNameVi"`
	CompanyNameEn    string   `json:"companyNameEn"`
	Exchange         string   `json:"exchange"`
	RefPrice         float64  `json:"refPrice"`
	OpenPrice        float64  `json:"openPrice"`
	Highest          float64  `json:"highest"`
	Lowest           float64  `json:"lowest"`
	MatchedPrice     float64  `json:"matchedPrice"`
	NMTotalTradedQty *float64 `json:"nmTotalTradedQty"`
}

func (c *PriceClient) baseURL() string {
	if c.URL != "" {
		return strings.TrimRight(c.URL, "/")
	}
	return ssiQueryDefaultURL
}

func (c *PriceClient) fetchSSIPrice(ctx context.Context, ticker string) (float64, error) {
	if ticker == "" {
		return 0, errors.New("stock: ticker is empty")
	}
	full := c.baseURL() + "/stock/" + url.PathEscape(ticker)
	req, err := newSSIRequest(ctx, http.MethodGet, full, nil)
	if err != nil {
		return 0, err
	}

	var body ssiSingleResponse
	if err := c.doSSIJSON(req, &body); err != nil {
		return 0, err
	}
	price := body.Data.MatchedPrice
	if price <= 0 {
		return 0, fmt.Errorf("%w: SSI quote has no matchedPrice for %s", ErrNoPrice, strings.ToUpper(ticker))
	}
	return price, nil
}

// fetchSSIQuote returns the detailed SSI quote using exactly one single-stock
// request. It intentionally bypasses FetchPrice and its KBS/VCI fallbacks.
func (c *PriceClient) fetchSSIQuote(ctx context.Context, ticker string) (ssiStockQuoteDetail, error) {
	if ticker == "" {
		return ssiStockQuoteDetail{}, errors.New("stock: ticker is empty")
	}
	full := c.baseURL() + "/stock/" + url.PathEscape(ticker)
	req, err := newSSIRequest(ctx, http.MethodGet, full, nil)
	if err != nil {
		return ssiStockQuoteDetail{}, err
	}

	// A redirect would turn one command into multiple outbound requests. Clone
	// the client shallowly so only this detail request disables redirects.
	client := *c.httpClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	var body ssiQuoteDetailResponse
	if err := doSSIJSON(&client, req, &body); err != nil {
		return ssiStockQuoteDetail{}, err
	}
	if body.Data.MatchedPrice <= 0 {
		return ssiStockQuoteDetail{}, fmt.Errorf("%w: SSI quote has no matchedPrice for %s", ErrNoPrice, strings.ToUpper(ticker))
	}
	return body.Data, nil
}

func (c *PriceClient) fetchSSIPrices(ctx context.Context, tickers []string) (map[string]float64, error) {
	if len(tickers) == 0 {
		return map[string]float64{}, nil
	}
	form := url.Values{}
	requested := normalizeTickers(tickers)
	for _, ticker := range requested {
		form.Add("stocks", ticker)
	}
	if len(form) == 0 {
		return nil, errors.New("stock: no tickers to fetch")
	}

	req, err := newSSIRequest(ctx, http.MethodPost, c.baseURL()+"/stock/multiple", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var body ssiMultipleResponse
	if err := c.doSSIJSON(req, &body); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(body.Data))
	for _, quote := range body.Data {
		symbol := strings.ToUpper(strings.TrimSpace(quote.StockSymbol))
		if symbol == "" || quote.MatchedPrice <= 0 {
			continue
		}
		out[symbol] = quote.MatchedPrice
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: SSI batch returned no usable quotes for %s (data_len=%d)", ErrNoPrice, strings.Join(requested, ","), len(body.Data))
	}
	return out, nil
}

func newSSIRequest(ctx context.Context, method, full string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, full, body)
	if err != nil {
		return nil, fmt.Errorf("stock: build SSI request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://iboard.ssi.com.vn")
	req.Header.Set("Referer", "https://iboard.ssi.com.vn/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (tiennm99bot)")
	return req, nil
}

func (c *PriceClient) doSSIJSON(req *http.Request, dst any) error {
	return doSSIJSON(c.httpClient(), req, dst)
}

func doSSIJSON(client *http.Client, req *http.Request, dst any) error {
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("stock: SSI request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, ssiErrorBodyLimit))
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: SSI status %d body %q", ErrNoPrice, resp.StatusCode, strings.TrimSpace(string(snippet)))
		}
		return fmt.Errorf("stock: SSI status %d body %q", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("stock: SSI decode: %w", err)
	}
	return nil
}
