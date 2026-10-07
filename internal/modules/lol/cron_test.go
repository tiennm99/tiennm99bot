package lol

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/subscription"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// fakeSender records every SendMessage call. errOn returns a transient
// failure for the configured chat IDs; terminalErrOn returns a chat-wide
// permanent-failure message string; topicTerminalErrOn returns a
// topic-only permanent-failure string. All others succeed.
type fakeSender struct {
	mu                 sync.Mutex
	calls              []bot.SendMessageParams
	errOn              map[int64]bool
	terminalErrOn      map[int64]bool
	topicTerminalErrOn map[int64]bool
}

func (f *fakeSender) SendMessage(_ context.Context, p *bot.SendMessageParams) (*models.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, *p)
	id, ok := p.ChatID.(int64)
	if ok && f.terminalErrOn[id] {
		// String shape that classifyTerminal matches; verifies the marker
		// list works against a realistic Telegram error message.
		return nil, errors.New("Forbidden: bot was blocked by the user")
	}
	if ok && f.topicTerminalErrOn[id] {
		return nil, errors.New("Bad Request: have no rights to send a message")
	}
	if ok && f.errOn[id] {
		return nil, errors.New("fakeSender: induced failure for chat " + chatIDString(id))
	}
	return &models.Message{}, nil
}

func chatIDString(id int64) string {
	return time.Unix(id, 0).Format("00") // arbitrary stringification; only used in error msg
}

// fixedNow returns a deterministic clock for the cron tests. Picked to land
// inside one ICT day cleanly so cache key + filter logic are stable.
func fixedNow() time.Time {
	// 2026-05-10 12:00 ICT == 05:00 UTC
	return time.Date(2026, 5, 10, 5, 0, 0, 0, time.UTC)
}

// newTestStore builds a fresh set of typed stores over a shared in-memory
// collection, matching what the factory wires in production.
func newTestStore(t *testing.T) (subscription.Store, PushDateStore, CacheStore) {
	t.Helper()
	col := storage.NewMemoryProvider().Collection("lol")
	return storage.Typed[subscription.Doc](col),
		storage.Typed[subscription.DayDoc](col),
		storage.Typed[cacheRecord](col)
}

// seedFreshCache writes a cacheRecord with `now` as timestamp so
// GetEventsWithFallback can serve it when the test upstream fails.
func seedFreshCache(t *testing.T, cache CacheStore, events []ScheduleEvent) {
	t.Helper()
	from := ictDayStartOf(fixedNow())
	to := addDays(from, 1)
	rec := cacheRecord{
		Ts:     time.Now().UTC().UnixMilli(),
		Events: events,
	}
	if err := cache.Put(context.Background(), cacheKey(from, to), rec); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
}

func newTestState(t *testing.T) *state {
	t.Helper()
	subs, pd, cache := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(upstream.Close)
	return &state{
		subscribers: subs,
		pushDate:    pd,
		cache:       cache,
		client:      &Client{HTTP: upstream.Client(), URL: upstream.URL},
		nowFn:       fixedNow,
	}
}

func TestRunDailyPush_NoSubscribers(t *testing.T) {
	s := newTestState(t)
	seedFreshCache(t, s.cache, nil)

	sender := &fakeSender{}
	if err := runDailyPush(context.Background(), s, sender); err != nil {
		t.Fatalf("runDailyPush: %v", err)
	}
	if len(sender.calls) != 0 {
		t.Errorf("expected 0 sends, got %d", len(sender.calls))
	}
}

func TestRunDailyPush_SendsEmptyScheduleSilently(t *testing.T) {
	s := newTestState(t)
	seedFreshCache(t, s.cache, nil) // empty schedule still produces a "no matches" message

	chatIDs := []int64{100, 200, 300}
	for _, id := range chatIDs {
		if _, err := subscription.Add(context.Background(), s.subscribers, id, 0); err != nil {
			t.Fatalf("addSubscriber %d: %v", id, err)
		}
	}

	sender := &fakeSender{}
	if err := runDailyPush(context.Background(), s, sender); err != nil {
		t.Fatalf("runDailyPush: %v", err)
	}
	if len(sender.calls) != len(chatIDs) {
		t.Fatalf("expected %d sends, got %d", len(chatIDs), len(sender.calls))
	}
	for i, call := range sender.calls {
		gotID, ok := call.ChatID.(int64)
		if !ok {
			t.Errorf("send %d: ChatID not int64: %T", i, call.ChatID)
			continue
		}
		if gotID != chatIDs[i] {
			t.Errorf("send %d: chat got %d, want %d", i, gotID, chatIDs[i])
		}
		if call.ParseMode != models.ParseModeHTML {
			t.Errorf("send %d: parse mode got %v, want HTML", i, call.ParseMode)
		}
		if call.Text == "" {
			t.Errorf("send %d: empty text", i)
		}
		if call.MessageThreadID != 0 {
			t.Errorf("send %d: thread got %d, want 0 (no topic)", i, call.MessageThreadID)
		}
		if !call.DisableNotification {
			t.Errorf("send %d: DisableNotification got false, want true for empty schedule", i)
		}
	}
}

func TestRunDailyPush_SendsMatchScheduleWithNotification(t *testing.T) {
	s := newTestState(t)
	seedFreshCache(t, s.cache, []ScheduleEvent{{
		StartTime: "2026-05-10T06:00:00Z",
		State:     "unstarted",
		League:    League{Name: "LCK", Slug: "lck"},
		Match: Match{
			Teams:    []Team{{Code: "T1"}, {Code: "GEN"}},
			Strategy: Strategy{Count: 3},
		},
	}})

	if _, err := subscription.Add(context.Background(), s.subscribers, 100, 0); err != nil {
		t.Fatalf("addSubscriber: %v", err)
	}

	sender := &fakeSender{}
	if err := runDailyPush(context.Background(), s, sender); err != nil {
		t.Fatalf("runDailyPush: %v", err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("expected 1 send, got %d", len(sender.calls))
	}
	if sender.calls[0].DisableNotification {
		t.Fatalf("DisableNotification got true, want false when today's match list is non-empty")
	}
}

// TestRunDailyPush_ForwardsMessageThreadID locks in the forum-topic fix:
// subscribers stored with a non-zero ThreadID receive the daily push in that
// topic, not in General.
func TestRunDailyPush_ForwardsMessageThreadID(t *testing.T) {
	s := newTestState(t)
	seedFreshCache(t, s.cache, nil)

	subs := []subscription.Subscriber{
		{ChatID: 100, ThreadID: 0},
		{ChatID: 100, ThreadID: 7},
		{ChatID: 200, ThreadID: 42},
	}
	for _, sub := range subs {
		if _, err := subscription.Add(context.Background(), s.subscribers, sub.ChatID, sub.ThreadID); err != nil {
			t.Fatal(err)
		}
	}

	sender := &fakeSender{}
	if err := runDailyPush(context.Background(), s, sender); err != nil {
		t.Fatalf("runDailyPush: %v", err)
	}
	if len(sender.calls) != len(subs) {
		t.Fatalf("expected %d sends, got %d", len(subs), len(sender.calls))
	}
	for i, call := range sender.calls {
		gotID, _ := call.ChatID.(int64)
		if gotID != subs[i].ChatID || call.MessageThreadID != subs[i].ThreadID {
			t.Errorf("send %d: got (chat=%d, thread=%d), want (chat=%d, thread=%d)",
				i, gotID, call.MessageThreadID, subs[i].ChatID, subs[i].ThreadID)
		}
	}
}

// TestRunDailyPush_IdempotentPerICTDay locks in the double-fire guard: invoking
// the handler twice on the same ICT schedule day sends each subscriber exactly
// one digest. Defends against rolling-deploy overlap and operator
// misconfiguration (all double-fire windows the daily push must survive).
func TestRunDailyPush_IdempotentPerICTDay(t *testing.T) {
	s := newTestState(t)
	seedFreshCache(t, s.cache, nil)

	chatIDs := []int64{100, 200, 300}
	for _, id := range chatIDs {
		if _, err := subscription.Add(context.Background(), s.subscribers, id, 0); err != nil {
			t.Fatalf("addSubscriber %d: %v", id, err)
		}
	}

	sender := &fakeSender{}
	for i := 0; i < 2; i++ {
		if err := runDailyPush(context.Background(), s, sender); err != nil {
			t.Fatalf("runDailyPush call %d: %v", i+1, err)
		}
	}
	if len(sender.calls) != len(chatIDs) {
		t.Errorf("two same-ICT-day pushes sent %d messages, want %d (one per subscriber)",
			len(sender.calls), len(chatIDs))
	}
}

func TestRunDailyPush_ClaimsICTDayAtMidnight(t *testing.T) {
	s := newTestState(t)
	s.nowFn = func() time.Time {
		return time.Date(2026, 5, 9, 17, 0, 0, 0, time.UTC) // 2026-05-10 00:00 ICT
	}
	seedFreshCache(t, s.cache, nil)
	if _, err := subscription.Add(context.Background(), s.subscribers, 100, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.pushDate.Put(context.Background(), lastPushDateKey, subscription.DayDoc{Date: "2026-05-09"}); err != nil {
		t.Fatal(err)
	}

	sender := &fakeSender{}
	if err := runDailyPush(context.Background(), s, sender); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("calls = %d, want 1 midnight ICT push", len(sender.calls))
	}
	doc, _, err := s.pushDate.Get(context.Background(), lastPushDateKey)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Date != "2026-05-10" {
		t.Fatalf("last push date = %q, want ICT day 2026-05-10", doc.Date)
	}
}

func TestRunDailyPush_PartialFailureContinues(t *testing.T) {
	s := newTestState(t)
	seedFreshCache(t, s.cache, nil)

	chatIDs := []int64{100, 200, 300}
	for _, id := range chatIDs {
		if _, err := subscription.Add(context.Background(), s.subscribers, id, 0); err != nil {
			t.Fatalf("addSubscriber %d: %v", id, err)
		}
	}

	sender := &fakeSender{errOn: map[int64]bool{200: true}}
	if err := runDailyPush(context.Background(), s, sender); err != nil {
		t.Fatalf("runDailyPush: %v (should swallow per-chat failures)", err)
	}
	if len(sender.calls) != 3 {
		t.Errorf("expected 3 attempts (failure does not abort batch), got %d", len(sender.calls))
	}
}

// TestRunDailyPush_PrunesDeadSubscribers locks in the auto-cleanup of chats
// that have permanently blocked the bot. Recoverable (transient) errors
// MUST NOT trigger removal — only terminal Telegram errors do. A chat-wide
// terminal error removes every topic subscription for that chat.
func TestRunDailyPush_PrunesDeadSubscribers(t *testing.T) {
	s := newTestState(t)
	seedFreshCache(t, s.cache, nil)

	// Chat 400 has two topic subs; both should be pruned when the chat
	// returns a chat-wide terminal error.
	seedSubs := []subscription.Subscriber{
		{ChatID: 100, ThreadID: 0},
		{ChatID: 200, ThreadID: 0},
		{ChatID: 300, ThreadID: 0},
		{ChatID: 400, ThreadID: 0},
		{ChatID: 400, ThreadID: 9},
	}
	for _, sub := range seedSubs {
		if _, err := subscription.Add(context.Background(), s.subscribers, sub.ChatID, sub.ThreadID); err != nil {
			t.Fatalf("addSubscriber %v: %v", sub, err)
		}
	}

	sender := &fakeSender{
		errOn:         map[int64]bool{200: true}, // transient → keep
		terminalErrOn: map[int64]bool{400: true}, // chat-wide → wipe all 400 entries
	}
	if err := runDailyPush(context.Background(), s, sender); err != nil {
		t.Fatalf("runDailyPush: %v", err)
	}

	remaining, err := subscription.List(context.Background(), s.subscribers)
	if err != nil {
		t.Fatalf("listSubscribers: %v", err)
	}
	want := []subscription.Subscriber{{ChatID: 100}, {ChatID: 200}, {ChatID: 300}}
	if len(remaining) != len(want) {
		t.Fatalf("subscribers after prune: got %v, want %v", remaining, want)
	}
	for i, s := range want {
		if remaining[i] != s {
			t.Errorf("subscriber[%d]: got %v, want %v", i, remaining[i], s)
		}
	}
}

// TestRunDailyPush_TopicOnlyTerminalPrunesOneTopic verifies that a
// "have no rights to send" failure removes only the failing
// (ChatID, ThreadID) entry — sister topics in the same chat stay subscribed.
func TestRunDailyPush_TopicOnlyTerminalPrunesOneTopic(t *testing.T) {
	s := newTestState(t)
	seedFreshCache(t, s.cache, nil)

	seedSubs := []subscription.Subscriber{
		{ChatID: 500, ThreadID: 0},
		{ChatID: 500, ThreadID: 11},
		{ChatID: 500, ThreadID: 22},
	}
	for _, sub := range seedSubs {
		if _, err := subscription.Add(context.Background(), s.subscribers, sub.ChatID, sub.ThreadID); err != nil {
			t.Fatal(err)
		}
	}

	// Every send to chat 500 returns the topic-only terminal — but only the
	// matching (ChatID, ThreadID) should be pruned per call. With the marker
	// applying to all three sends we expect all three entries to drop.
	sender := &fakeSender{topicTerminalErrOn: map[int64]bool{500: true}}
	if err := runDailyPush(context.Background(), s, sender); err != nil {
		t.Fatalf("runDailyPush: %v", err)
	}

	remaining, _ := subscription.List(context.Background(), s.subscribers)
	if len(remaining) != 0 {
		t.Errorf("expected all topic-only entries pruned, got %v", remaining)
	}
}

// TestRunDailyPush_TopicOnlyTerminalKeepsOtherTopics: only one topic in a
// chat goes bad; the other topics in the same chat must stay.
func TestRunDailyPush_TopicOnlyTerminalKeepsOtherTopics(t *testing.T) {
	s := newTestState(t)
	seedFreshCache(t, s.cache, nil)

	// Two distinct chats, each with multiple topic subs. Only chat 600 hits
	// the topic-terminal error; chat 700 sends cleanly.
	seedSubs := []subscription.Subscriber{
		{ChatID: 600, ThreadID: 1},
		{ChatID: 600, ThreadID: 2},
		{ChatID: 700, ThreadID: 3},
	}
	for _, sub := range seedSubs {
		if _, err := subscription.Add(context.Background(), s.subscribers, sub.ChatID, sub.ThreadID); err != nil {
			t.Fatal(err)
		}
	}

	// Custom sender: chat 600 always fails topic-terminal, chat 700 succeeds.
	sender := &fakeSender{topicTerminalErrOn: map[int64]bool{600: true}}
	if err := runDailyPush(context.Background(), s, sender); err != nil {
		t.Fatalf("runDailyPush: %v", err)
	}

	remaining, _ := subscription.List(context.Background(), s.subscribers)
	if len(remaining) != 1 || remaining[0] != (subscription.Subscriber{ChatID: 700, ThreadID: 3}) {
		t.Errorf("after topic-terminal prune of chat 600: got %v, want [{700 3}]", remaining)
	}
}

func TestDailyPushHandler_NilBot_ReturnsError(t *testing.T) {
	s := newTestState(t)
	deps := modules.Deps{Store: storage.NewMemoryProvider().Collection("lol")}
	err := s.dailyPushHandler(context.Background(), deps)
	if err == nil {
		t.Fatal("expected error when deps.Bot is nil, got nil")
	}
}

func TestDailyPushCron_Registration(t *testing.T) {
	s := newTestState(t)
	c := s.dailyPushCron()
	if c.Name != dailyPushCronName {
		t.Errorf("Name: got %q, want %q", c.Name, dailyPushCronName)
	}
	if c.Schedule != dailyPushSchedule {
		t.Errorf("Schedule: got %q, want %q", c.Schedule, dailyPushSchedule)
	}
	if c.Handler == nil {
		t.Error("Handler is nil")
	}
}
