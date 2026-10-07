package coin

import (
	"context"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

func TestHandleBuyAcceptsCoinFirstOrder(t *testing.T) {
	ctx := context.Background()
	s := newTestState(map[string]CoinPrice{"BTC": {USD: 50000, Source: "Binance"}}, nil)
	rb := testutil.NewRecordingBot(t)
	_ = s.handleTopup(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_topup 1000"))
	rb.Reset()

	if err := s.handleBuy(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_buy BTC 10")); err != nil {
		t.Fatalf("handleBuy: %v", err)
	}

	rb.AssertSentText(t, "Bought 0.0002 BTC")
	p, _ := LoadPortfolio(ctx, s.store, 7, 999)
	if p.USD != 990 || p.Assets["BTC"].Quantity != 0.0002 {
		t.Fatalf("after coin-first buy = %+v", p)
	}
}

func TestHandleSellAcceptsCoinFirstOrder(t *testing.T) {
	ctx := context.Background()
	s := newTestState(map[string]CoinPrice{"BTC": {USD: 50000, Source: "Binance"}}, nil)
	rb := testutil.NewRecordingBot(t)
	_ = s.handleTopup(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_topup 1000"))
	_ = s.handleBuy(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_buy 500 BTC"))
	rb.Reset()

	if err := s.handleSell(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/coin_sell BTC 500")); err != nil {
		t.Fatalf("handleSell: %v", err)
	}

	rb.AssertSentText(t, "Sold 0.01 BTC")
	p, _ := LoadPortfolio(ctx, s.store, 7, 999)
	if p.USD != 1000 || len(p.Assets) != 0 {
		t.Fatalf("after coin-first sell = %+v", p)
	}
}
