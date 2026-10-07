package coin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// blockingPriceFetcher simulates an upstream that never responds: it blocks
// until the fetch context is cancelled, then returns its error. This is the
// exact failure that made /coin_portfolio (and /stock_portfolio) time out — the fetch
// must not be allowed to consume the budget the reply needs.
type blockingPriceFetcher struct{}

func (blockingPriceFetcher) FetchUSD(ctx context.Context, _ CoinSymbol) (CoinPrice, error) {
	<-ctx.Done()
	return CoinPrice{}, ctx.Err()
}

// TestHandleStatsDeliversReplyWhenUpstreamHangs proves the reply-reserve fix:
// even when the price upstream hangs for the entire fetch budget, handleStats
// still delivers a summary (with "N/A" price cells) on the original
// context instead of failing the whole reply with "context deadline exceeded".
func TestHandleStatsDeliversReplyWhenUpstreamHangs(t *testing.T) {
	// Seed a holding using a fast fetcher, then swap in the hanging upstream.
	s := newTestState(map[string]CoinPrice{"BTC": {USD: 100}}, nil)
	rb := testutil.NewRecordingBot(t)
	setupCtx := context.Background()
	if err := s.handleTopup(setupCtx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_topup 1000")); err != nil {
		t.Fatalf("topup: %v", err)
	}
	if err := s.handleBuy(setupCtx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_buy 500 BTC")); err != nil {
		t.Fatalf("buy: %v", err)
	}

	s.prices = blockingPriceFetcher{}
	rb.Reset()

	// 4s parent deadline → ~1s fetch budget (replyReserve is 3s), leaving the
	// reply ample headroom.
	statsCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	start := time.Now()
	if err := s.handleStats(statsCtx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_portfolio")); err != nil {
		t.Fatalf("handleStats returned error (reply not delivered): %v", err)
	}
	elapsed := time.Since(start)

	if statsCtx.Err() != nil {
		t.Fatalf("parent context expired before reply (no budget reserved): %v", statsCtx.Err())
	}
	if elapsed >= 3*time.Second {
		t.Fatalf("handleStats took %v — fetch was not bounded below the reply reserve", elapsed)
	}
	sent := rb.LastSent().Text()
	if !strings.Contains(sent, "Coin Portfolio") || !strings.Contains(sent, "N/A") || !strings.Contains(sent, "<pre>") {
		t.Fatalf("reply missing summary / degraded line; got:\n%s", sent)
	}
}

func TestCoinPortfolioReplyStaysWithinTelegramBudget(t *testing.T) {
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
