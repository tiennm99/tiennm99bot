package subscription

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

func newTestStore() Store {
	return storage.Typed[Doc](storage.NewMemoryProvider().Collection("sub"))
}

func TestFanoutFunc_SendsPrunesChatWideAndTopicOnly(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	var mu sync.Mutex
	for _, s := range []Subscriber{{ChatID: 1}, {ChatID: -2, ThreadID: 5}, {ChatID: -2, ThreadID: 6}, {ChatID: -3, ThreadID: 7}, {ChatID: -3}, {ChatID: 4}} {
		if _, err := Add(ctx, store, s.ChatID, s.ThreadID); err != nil {
			t.Fatal(err)
		}
	}
	subs, _ := List(ctx, store)
	var got []Subscriber
	res, err := FanoutFunc(ctx, "test", store, &mu, subs, func(_ context.Context, sub Subscriber) error {
		got = append(got, sub)
		switch {
		case sub.ChatID == -2 && sub.ThreadID == 5:
			return errors.New("Forbidden: bot was blocked by the user")
		case sub.ChatID == -3 && sub.ThreadID == 7:
			return errors.New("Bad Request: have no rights to send a message")
		case sub.ChatID == 4:
			return errors.New("Too Many Requests: retry after 3")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 || res.Sent != 3 || res.Failed != 3 || res.Pruned != 3 || res.Throttled {
		t.Fatalf("calls %d, result %+v", len(got), res)
	}
	left, _ := List(ctx, store)
	want := []Subscriber{{ChatID: 1}, {ChatID: -3}, {ChatID: 4}}
	if len(left) != len(want) {
		t.Fatalf("left = %+v, want %+v", left, want)
	}
	for i := range want {
		if left[i] != want[i] {
			t.Fatalf("left = %+v, want %+v", left, want)
		}
	}
}

func TestFanoutFunc_ThrottlesAndStopsOnCancel(t *testing.T) {
	subs := make([]Subscriber, rateLimitThreshold+5)
	for i := range subs {
		subs[i] = Subscriber{ChatID: int64(i + 1)}
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	var mu sync.Mutex
	start := time.Now()
	res, err := FanoutFunc(ctx, "test", newTestStore(), &mu, subs, func(context.Context, Subscriber) error {
		calls++
		if calls == 3 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || !res.Throttled || calls != 3 || res.Sent != 3 {
		t.Fatalf("calls %d, result %+v, err %v", calls, res, err)
	}
	if elapsed := time.Since(start); elapsed < 2*rateLimitDelay {
		t.Fatalf("not throttled: %v", elapsed)
	}
}
