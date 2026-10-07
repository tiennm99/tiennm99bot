package gold

import (
	"context"
	"strings"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

func TestHandlersRejectExtraArgs(t *testing.T) {
	ctx := context.Background()
	s := newTestState(2_000_000, nil)
	rb := testutil.NewRecordingBot(t)
	cases := []struct {
		name string
		text string
		want string
	}{
		{name: "topup currency", text: "/gold_topup 100 USD", want: "Usage: /gold_topup <vnd_amount>"},
		{name: "buy unit", text: "/gold_buy 1 oz", want: "Usage: /gold_buy <luong>"},
		{name: "sell symbol", text: "/gold_sell 1 SJC", want: "Usage: /gold_sell <luong>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rb.Reset()
			update := testutil.NewPrivateMessage(7, tc.text)
			var err error
			switch {
			case strings.HasPrefix(tc.text, "/gold_topup"):
				err = s.handleTopup(ctx, rb.Bot, update)
			case strings.HasPrefix(tc.text, "/gold_buy"):
				err = s.handleBuy(ctx, rb.Bot, update)
			case strings.HasPrefix(tc.text, "/gold_sell"):
				err = s.handleSell(ctx, rb.Bot, update)
			}
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			rb.AssertSentText(t, tc.want)
		})
	}
}

func TestHandlersRejectTooLargeValues(t *testing.T) {
	ctx := context.Background()
	s := newTestState(1e308, nil)
	rb := testutil.NewRecordingBot(t)
	if err := s.handleTopup(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/gold_topup 1e308")); err != nil {
		t.Fatalf("topup: %v", err)
	}
	rb.AssertSentText(t, "supported range")
	rb.Reset()
	if err := s.handleBuy(ctx, rb.Bot, testutil.NewPrivateMessage(7, "/gold_buy 2")); err != nil {
		t.Fatalf("buy: %v", err)
	}
	rb.AssertSentText(t, "Trade value is too large")
}
