package stock

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

type stockEventProviderFunc func(context.Context, string, time.Time, time.Time) ([]SSIStockEvent, error)

func (f stockEventProviderFunc) FetchStockEvents(ctx context.Context, symbol string, after, through time.Time) ([]SSIStockEvent, error) {
	return f(ctx, symbol, after, through)
}

func TestStockEventsCommandRegistration(t *testing.T) {
	mod := New(modDepsForTest())
	for _, command := range mod.Commands {
		if command.Name != "stock_events" {
			continue
		}
		if command.Parameters != "<ticker> [days]" || command.Description != "Show SSI corporate actions for a VN stock" {
			t.Fatalf("stock_events metadata = params %q description %q", command.Parameters, command.Description)
		}
		return
	}
	t.Fatal("stock_events command is not registered")
}

func TestHandleStockEventsDefaultWindowAndSenderless(t *testing.T) {
	now := time.Date(2026, 7, 23, 15, 30, 0, 0, saigonLocation)
	called := false
	s := &state{
		nowFn: func() time.Time { return now },
		events: stockEventProviderFunc(func(_ context.Context, symbol string, after, through time.Time) ([]SSIStockEvent, error) {
			called = true
			if symbol != "TCB" || !through.Equal(now) || !after.Equal(now.Add(-30*24*time.Hour)) {
				t.Errorf("provider args = %q, %v, %v", symbol, after, through)
			}
			return []SSIStockEvent{{
				CorID: "meeting-1", Symbol: "TCB", EventListCode: "AGM", EventName: "Annual meeting",
				EventTitle: "Shareholder meeting", PublicDate: "23/07/2026 14:30:00", SourceURL: "https://ssi.example/event",
			}}, nil
		}),
	}
	rb := testutil.NewRecordingBot(t)
	if err := s.handleStockEvents(context.Background(), rb.Bot, testutil.NewChannelMessage(-100, "/stock_events tcb")); err != nil {
		t.Fatalf("handleStockEvents: %v", err)
	}
	if !called {
		t.Fatal("provider was not called")
	}
	rb.AssertSentText(t, "TCB SSI event · AGM")
	rb.AssertSentText(t, "Name: Annual meeting")
	rb.AssertSentText(t, "SSI event: meeting-1")
}

func TestHandleStockEventsExplicitWindowAndValidation(t *testing.T) {
	now := time.Date(2026, 7, 23, 15, 30, 0, 0, saigonLocation)
	s := &state{
		nowFn: func() time.Time { return now },
		events: stockEventProviderFunc(func(_ context.Context, _ string, after, through time.Time) ([]SSIStockEvent, error) {
			if !after.Equal(now.Add(-7*24*time.Hour)) || !through.Equal(now) {
				t.Fatalf("window = %v..%v", after, through)
			}
			return nil, nil
		}),
	}
	rb := testutil.NewRecordingBot(t)
	if err := s.handleStockEvents(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_events TCB 7")); err != nil {
		t.Fatal(err)
	}
	rb.AssertSentText(t, "No stock events found for TCB in the last 7 days.")

	for _, tc := range []struct {
		command string
		want    string
	}{
		{"/stock_events", "Usage: /stock_events <ticker> [days]"},
		{"/stock_events TCB 7 extra", "Usage: /stock_events <ticker> [days]"},
		{"/stock_events TCB 0", "Days must be a whole number from 1 to 90."},
		{"/stock_events TCB 91", "Days must be a whole number from 1 to 90."},
		{"/stock_events TCB 1.5", "Days must be a whole number from 1 to 90."},
		{"/stock_events $$$", "Unknown stock ticker \"$$$\"."},
	} {
		rb.Reset()
		if err := s.handleStockEvents(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, tc.command)); err != nil {
			t.Fatalf("%q: %v", tc.command, err)
		}
		rb.AssertSentText(t, tc.want)
	}
}

func TestHandleStockEventsProviderError(t *testing.T) {
	s := &state{
		events: stockEventProviderFunc(func(context.Context, string, time.Time, time.Time) ([]SSIStockEvent, error) {
			return nil, errors.New("upstream unavailable")
		}),
	}
	rb := testutil.NewRecordingBot(t)
	if err := s.handleStockEvents(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_events TCB")); err != nil {
		t.Fatal(err)
	}
	rb.AssertSentText(t, "Could not fetch stock events for TCB. Try again later.")
}

func TestStockEventFormattingAndChunking(t *testing.T) {
	unicodeTitle := strings.Repeat("ổ", stockEventTitleLimit+50)
	block := formatStockEvent(SSIStockEvent{
		CorID: "event-1", Symbol: "TCB", EventListCode: "ISS", EventName: "Issue",
		EventTitle: unicodeTitle, EventDescription: "Raw API description", Value: "1500.25", Ratio: "0.125",
		PublicDate: "20/07/2026 10:00:00", ExrightDate: "malformed-but-displayed", RecordDate: "22/07/2026",
		IssueDate: "30/07/2026", SourceURL: "https://ssi.example/event-1",
	})
	if !strings.Contains(block, "Title: "+strings.Repeat("ổ", stockEventTitleLimit-1)+"…") {
		t.Fatal("title was not truncated rune-safely")
	}
	for _, expected := range []string{
		"Description: Raw API description", "Value: 1500.25", "Ratio: 0.125",
		"Published: 20/07/2026 10:00:00", "Ex-right: malformed-but-displayed", "Record: 22/07/2026",
		"Issue/payment: 30/07/2026", "Source: https://ssi.example/event-1",
	} {
		if !strings.Contains(block, expected) {
			t.Errorf("block missing %q: %q", expected, block)
		}
	}

	blocks := []string{strings.Repeat("a", 2500), strings.Repeat("b", 2500), "final-event"}
	replies := chunkStockEventReplies("TCB", blocks)
	if len(replies) != 2 {
		t.Fatalf("reply count = %d, want 2", len(replies))
	}
	for i, reply := range replies {
		if utf8.RuneCountInString(reply) >= stockEventsReplyLimit {
			t.Errorf("reply %d length = %d", i, utf8.RuneCountInString(reply))
		}
	}
	if !strings.Contains(replies[0], "(1/2)") || !strings.Contains(replies[1], "(2/2)") || !strings.HasSuffix(replies[1], "final-event") {
		t.Fatalf("chunk ordering/headings = %#v", replies)
	}
}
