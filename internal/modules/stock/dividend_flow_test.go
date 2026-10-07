package stock

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

type dividendProviderCall struct {
	symbol         string
	after, through time.Time
}

type fakeDividendProvider struct {
	mu     sync.Mutex
	events []DividendEvent
	err    error
	calls  []dividendProviderCall
}

func (f *fakeDividendProvider) FetchDividendEvents(_ context.Context, symbol string, after, through time.Time) ([]DividendEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, dividendProviderCall{symbol: symbol, after: after, through: through})
	return append([]DividendEvent(nil), f.events...), f.err
}

func (f *fakeDividendProvider) snapshotCalls() []dividendProviderCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]dividendProviderCall(nil), f.calls...)
}

func newDividendFlowState(t *testing.T, events []DividendEvent) (*state, Store, PendingDividendStore, *testutil.RecordingBot, time.Time, *fakeDividendProvider) {
	t.Helper()
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, saigonLocation)
	priceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"stockSymbol":"TCB","matchedPrice":30000}]}`))
	}))
	t.Cleanup(priceServer.Close)

	provider := storage.NewMemoryProvider()
	collection := provider.Collection(CollectionName)
	store := storage.Typed[Portfolio](collection)
	pending := storage.Typed[PendingDividendAction](collection)
	fake := &fakeDividendProvider{events: events}
	s := &state{
		store: store, pending: pending,
		prices:    &PriceClient{HTTP: priceServer.Client(), URL: priceServer.URL},
		dividends: fake, nowFn: func() time.Time { return now },
	}
	return s, store, pending, testutil.NewRecordingBot(t), now, fake
}

func seedDividendFlowPortfolio(t *testing.T, store Store, now time.Time, quantity int64) {
	t.Helper()
	p := NewPortfolio(now.Add(-48 * time.Hour).UnixMilli())
	p.VND = 100_000
	p.Meta.Invested = 3_000_000
	if err := p.BuyTicker("TCB", quantity, float64(quantity)*30_000, now.Add(-48*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := SavePortfolio(context.Background(), store, 7, p); err != nil {
		t.Fatal(err)
	}
}

func cashDividendEvent(now, recordDate time.Time) DividendEvent {
	return DividendEvent{
		ProviderID: "2612974", Symbol: "TCB", Kind: DividendKindCash,
		PublishedAt: now.Add(-24 * time.Hour), RecordDate: recordDate,
		ExDate: now.AddDate(0, 0, 1), PaymentDate: now.AddDate(0, 0, 10),
		VNDPerShare: 1500, Title: "Cash dividend",
	}
}

func TestStockPortfolioNoEventsUsesExactRecentWindow(t *testing.T) {
	s, store, _, rb, now, provider := newDividendFlowState(t, nil)
	seedDividendFlowPortfolio(t, store, now, 100)

	if err := s.handleStats(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_portfolio")); err != nil {
		t.Fatal(err)
	}
	if calls := rb.Sent(); len(calls) != 1 || !strings.Contains(calls[0].Text(), "Stock Portfolio") {
		t.Fatalf("messages = %#v", calls)
	}
	providerCalls := provider.snapshotCalls()
	if len(providerCalls) != 1 || !providerCalls[0].after.Equal(now.Add(-30*24*time.Hour)) || !providerCalls[0].through.Equal(now) {
		t.Fatalf("provider calls = %+v", providerCalls)
	}
}

func TestFutureDividendNotifiesOnEveryPortfolioWithoutButton(t *testing.T) {
	event := cashDividendEvent(time.Date(2026, 6, 25, 12, 0, 0, 0, saigonLocation), time.Date(2026, 6, 27, 0, 0, 0, 0, saigonLocation))
	s, store, pending, rb, now, _ := newDividendFlowState(t, []DividendEvent{event})
	seedDividendFlowPortfolio(t, store, now, 100)

	for range 2 {
		if err := s.handleStats(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_portfolio")); err != nil {
			t.Fatal(err)
		}
	}
	calls := rb.Sent()
	if len(calls) != 4 || !strings.Contains(calls[1].Text(), "Upcoming dividend event") || !strings.Contains(calls[3].Text(), "Upcoming dividend event") || calls[1].Form["reply_markup"] != "" || calls[3].Form["reply_markup"] != "" {
		t.Fatalf("messages = %#v", calls)
	}
	keys, err := pending.List(context.Background(), pendingDividendPrefix)
	if err != nil || len(keys) != 0 {
		t.Fatalf("pending = %v, err=%v", keys, err)
	}
	p, _ := LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	record := p.Dividends["TCB"][event.ProviderID]
	if record.Processed || record.VNDPerShare != 1500 {
		t.Fatalf("stored event = %+v", record)
	}
}

func TestMissingRecordDateRemainsInformational(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, saigonLocation)
	event := cashDividendEvent(now, time.Time{})
	s, store, pending, rb, _, _ := newDividendFlowState(t, []DividendEvent{event})
	seedDividendFlowPortfolio(t, store, now, 100)

	if err := s.handleStats(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_portfolio")); err != nil {
		t.Fatal(err)
	}
	calls := rb.Sent()
	if len(calls) != 2 || !strings.Contains(calls[1].Text(), "awaiting SSI update") || calls[1].Form["reply_markup"] != "" {
		t.Fatalf("messages = %#v", calls)
	}
	keys, err := pending.List(context.Background(), pendingDividendPrefix)
	if err != nil || len(keys) != 0 {
		t.Fatalf("pending = %v, err=%v", keys, err)
	}
}

func TestMissingRecordDateIsRefetchedFromOriginalPublicationDay(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, saigonLocation)
	oldPublished := now.AddDate(0, 0, -45)
	event := cashDividendEvent(now, startOfSaigonDay(now))
	event.PublishedAt = oldPublished
	s, store, pending, rb, _, provider := newDividendFlowState(t, []DividendEvent{event})
	seedDividendFlowPortfolio(t, store, now, 100)
	p, _ := LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	p.Dividends["TCB"] = map[string]DividendRecord{
		event.ProviderID: {
			Kind: DividendKindCash, PublishedAt: oldPublished.UnixMilli(),
			VNDPerShare: 1000,
		},
	}
	if err := SavePortfolio(context.Background(), store, 7, p); err != nil {
		t.Fatal(err)
	}

	if err := s.handleStats(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_portfolio")); err != nil {
		t.Fatal(err)
	}
	providerCalls := provider.snapshotCalls()
	foundHistoricalWindow := false
	for _, call := range providerCalls {
		if startOfSaigonDay(call.after).Equal(startOfSaigonDay(oldPublished)) {
			foundHistoricalWindow = true
			break
		}
	}
	if len(providerCalls) != 2 || !foundHistoricalWindow {
		t.Fatalf("provider calls = %+v", providerCalls)
	}
	keys, err := pending.List(context.Background(), pendingDividendPrefix)
	if err != nil || len(keys) != 1 {
		t.Fatalf("pending = %v, err=%v", keys, err)
	}
	p, _ = LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	if p.Dividends["TCB"][event.ProviderID].RecordDate == 0 || p.Dividends["TCB"][event.ProviderID].VNDPerShare != 1500 {
		t.Fatalf("refreshed event = %+v", p.Dividends["TCB"][event.ProviderID])
	}
}

func TestExpiredIncompleteEventIsNotRestoredByHistoricalRefresh(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, saigonLocation)
	oldPublished := now.AddDate(0, 0, -90)
	event := cashDividendEvent(now, time.Time{})
	event.PublishedAt = oldPublished
	s, store, _, rb, _, _ := newDividendFlowState(t, []DividendEvent{event})
	seedDividendFlowPortfolio(t, store, now, 100)
	p, _ := LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	p.Dividends["TCB"] = map[string]DividendRecord{
		event.ProviderID: {Kind: DividendKindCash, PublishedAt: oldPublished.UnixMilli(), VNDPerShare: 1500},
	}
	if err := SavePortfolio(context.Background(), store, 7, p); err != nil {
		t.Fatal(err)
	}

	if err := s.handleStats(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_portfolio")); err != nil {
		t.Fatal(err)
	}
	p, _ = LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	if _, exists := p.Dividends["TCB"]; exists {
		t.Fatalf("expired event was restored: %+v", p.Dividends)
	}
}

func TestRecordDateCreatesSeparateActionableMessages(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, saigonLocation)
	cash := cashDividendEvent(now, startOfSaigonDay(now))
	shares := DividendEvent{
		ProviderID: "2612975", Symbol: "TCB", Kind: DividendKindShares,
		PublishedAt: now.Add(-12 * time.Hour), RecordDate: startOfSaigonDay(now),
		OwnedShares: 100, NewShares: 10, Title: "Share dividend",
	}
	s, store, pending, rb, _, _ := newDividendFlowState(t, []DividendEvent{cash, shares})
	seedDividendFlowPortfolio(t, store, now, 139)

	for range 2 {
		if err := s.handleStats(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_portfolio")); err != nil {
			t.Fatal(err)
		}
	}
	suggestions, buttonRemovals := 0, 0
	for _, call := range rb.Sent() {
		if call.Method == "sendMessage" && strings.Contains(call.Text(), "Dividend ready to apply") {
			suggestions++
		}
		if call.Method == "editMessageReplyMarkup" {
			buttonRemovals++
		}
	}
	if suggestions != 4 || buttonRemovals != 2 {
		t.Fatalf("suggestions=%d buttonRemovals=%d calls=%#v", suggestions, buttonRemovals, rb.Sent())
	}
	keys, err := pending.List(context.Background(), pendingDividendPrefix)
	if err != nil || len(keys) != 2 {
		t.Fatalf("pending = %v, err=%v", keys, err)
	}
}

func TestFailedFutureNoticeKeepsStoredHistoryForRetry(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, saigonLocation)
	event := cashDividendEvent(now, now.AddDate(0, 0, 2))
	s, store, _, rb, _, _ := newDividendFlowState(t, []DividendEvent{event})
	seedDividendFlowPortfolio(t, store, now, 100)
	snapshot, _ := LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	rb.FailMethod("sendMessage", http.StatusInternalServerError, "")
	_ = s.notifyDividendEvents(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_portfolio").Message, 7, snapshot, now)
	p, _ := LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	if p.Dividends["TCB"][event.ProviderID].VNDPerShare != 1500 {
		t.Fatal("failed future notice removed stored history")
	}
}

func TestDividendCallbackUsesStoredEventAndCurrentHoldingOnce(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, saigonLocation)
	event := cashDividendEvent(now, startOfSaigonDay(now))
	s, store, pending, rb, _, _ := newDividendFlowState(t, []DividendEvent{event})
	seedDividendFlowPortfolio(t, store, now, 100)
	for range 2 {
		if err := s.handleStats(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_portfolio")); err != nil {
			t.Fatal(err)
		}
	}

	// Re-suggesting the same event replaces the pending action instead of
	// stacking a second one.
	keys, err := pending.List(context.Background(), pendingDividendPrefix)
	if err != nil || len(keys) != 1 || keys[0] != pendingDividendKey(7, event.ProviderID) {
		t.Fatalf("pending keys = %v, err=%v", keys, err)
	}
	data := "7:" + event.ProviderID
	action, _, _ := pending.Get(context.Background(), keys[0])
	if action.ProviderEventID != event.ProviderID || action.Symbol != "TCB" {
		t.Fatalf("pending action = %+v", action)
	}
	if err := s.handleDividendCallback(context.Background(), rb.Bot, dividendCallbackUpdate(8, 7, action.MessageID, data)); err != nil {
		t.Fatal(err)
	}
	p, _ := LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	if p.VND != 100_000 {
		t.Fatalf("unauthorized click changed balance: %v", p.VND)
	}
	// The superseded first suggestion message no longer applies anything.
	if err := s.handleDividendCallback(context.Background(), rb.Bot, dividendCallbackUpdate(7, 7, action.MessageID-2, data)); err != nil {
		t.Fatal(err)
	}
	p, _ = LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	if p.VND != 100_000 {
		t.Fatalf("superseded button changed balance: %v", p.VND)
	}
	if err := s.handleDividendCallback(context.Background(), rb.Bot, dividendCallbackUpdate(7, 7, action.MessageID, data)); err != nil {
		t.Fatal(err)
	}
	p, _ = LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	if p.VND != 250_000 || p.Assets["TCB"].Base != 2_850_000 || !p.Dividends["TCB"][event.ProviderID].Processed {
		t.Fatalf("processed portfolio = %+v", p)
	}
	// Applying the dividend removes its only pending key.
	keys, err = pending.List(context.Background(), pendingDividendPrefix)
	if err != nil || len(keys) != 0 {
		t.Fatalf("pending keys after apply = %v, err=%v", keys, err)
	}
	if err := s.handleDividendCallback(context.Background(), rb.Bot, dividendCallbackUpdate(7, 7, action.MessageID, data)); err != nil {
		t.Fatal(err)
	}
	p, _ = LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	if p.VND != 250_000 {
		t.Fatalf("repeated press credited twice: %v", p.VND)
	}
}

func TestStoredDueEventStillNotifiesWhenSSIOmitsIt(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, saigonLocation)
	s, store, pending, rb, _, _ := newDividendFlowState(t, nil)
	seedDividendFlowPortfolio(t, store, now, 100)
	p, _ := LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	p.Dividends["TCB"] = map[string]DividendRecord{
		"2612974": {
			Kind: DividendKindCash, PublishedAt: now.AddDate(0, 0, -45).UnixMilli(),
			RecordDate: startOfSaigonDay(now).UnixMilli(), VNDPerShare: 1500,
		},
	}
	if err := SavePortfolio(context.Background(), store, 7, p); err != nil {
		t.Fatal(err)
	}

	if err := s.handleStats(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_portfolio")); err != nil {
		t.Fatal(err)
	}
	if calls := rb.Sent(); len(calls) != 2 || !strings.Contains(calls[1].Text(), "Dividend ready to apply") {
		t.Fatalf("messages = %#v", calls)
	}
	keys, err := pending.List(context.Background(), pendingDividendPrefix)
	if err != nil || len(keys) != 1 {
		t.Fatalf("pending = %v, err=%v", keys, err)
	}
}

func TestStoredFutureEventStillNotifiesWhenSSIOmitsIt(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, saigonLocation)
	s, store, pending, rb, _, _ := newDividendFlowState(t, nil)
	seedDividendFlowPortfolio(t, store, now, 100)
	p, _ := LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	p.Dividends["TCB"] = map[string]DividendRecord{
		"2612974": {
			Kind: DividendKindCash, PublishedAt: now.Add(-24 * time.Hour).UnixMilli(),
			RecordDate: now.AddDate(0, 0, 5).UnixMilli(), VNDPerShare: 1500,
		},
	}
	if err := SavePortfolio(context.Background(), store, 7, p); err != nil {
		t.Fatal(err)
	}

	if err := s.handleStats(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_portfolio")); err != nil {
		t.Fatal(err)
	}
	if calls := rb.Sent(); len(calls) != 2 || !strings.Contains(calls[1].Text(), "Upcoming dividend event") {
		t.Fatalf("messages = %#v", calls)
	}
	keys, err := pending.List(context.Background(), pendingDividendPrefix)
	if err != nil || len(keys) != 0 {
		t.Fatalf("pending = %v, err=%v", keys, err)
	}
}

func TestDividendCallbackAppliesShareEventFromStoredHistory(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, saigonLocation)
	s, store, pending, rb, _, _ := newDividendFlowState(t, nil)
	seedDividendFlowPortfolio(t, store, now, 139)
	p, _ := LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	p.Dividends["TCB"] = map[string]DividendRecord{
		"2612975": {
			Kind: DividendKindShares, PublishedAt: now.Add(-24 * time.Hour).UnixMilli(),
			RecordDate: startOfSaigonDay(now).UnixMilli(), OwnedShares: 100, NewShares: 10,
		},
	}
	if err := SavePortfolio(context.Background(), store, 7, p); err != nil {
		t.Fatal(err)
	}
	action := PendingDividendAction{
		OwnerUserID: 7, ChatID: 7, MessageID: 1, ProviderEventID: "2612975", Symbol: "TCB",
		PositionOpenedAt: p.Assets["TCB"].OpenedAt, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli(),
	}
	if err := pending.Put(context.Background(), pendingDividendKey(7, "2612975"), action); err != nil {
		t.Fatal(err)
	}
	if err := s.handleDividendCallback(context.Background(), rb.Bot, dividendCallbackUpdate(7, 7, 1, "7:2612975")); err != nil {
		t.Fatal(err)
	}
	p, _ = LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	if p.Assets["TCB"].Quantity != 152 || p.Assets["TCB"].Base != 139*30_000 || !p.Dividends["TCB"]["2612975"].Processed {
		t.Fatalf("share dividend result = %+v", p)
	}
}

func TestExpiredDividendActionDoesNotApply(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, saigonLocation)
	s, store, pending, rb, _, _ := newDividendFlowState(t, nil)
	seedDividendFlowPortfolio(t, store, now, 100)
	p, _ := LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	p.Dividends["TCB"] = map[string]DividendRecord{
		"2612974": {Kind: DividendKindCash, PublishedAt: now.Add(-24 * time.Hour).UnixMilli(), RecordDate: startOfSaigonDay(now).UnixMilli(), VNDPerShare: 1500},
	}
	if err := SavePortfolio(context.Background(), store, 7, p); err != nil {
		t.Fatal(err)
	}
	action := PendingDividendAction{
		OwnerUserID: 7, ChatID: 7, MessageID: 1, ProviderEventID: "2612974", Symbol: "TCB",
		PositionOpenedAt: p.Assets["TCB"].OpenedAt, CreatedAt: now.Add(-2 * time.Hour).UnixMilli(), ExpiresAt: now.Add(-time.Hour).UnixMilli(),
	}
	key := pendingDividendKey(7, "2612974")
	if err := pending.Put(context.Background(), key, action); err != nil {
		t.Fatal(err)
	}
	if err := s.handleDividendCallback(context.Background(), rb.Bot, dividendCallbackUpdate(7, 7, 1, "7:2612974")); err != nil {
		t.Fatal(err)
	}
	p, _ = LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	if p.VND != 100_000 || p.Dividends["TCB"]["2612974"].Processed {
		t.Fatalf("expired action changed portfolio: %+v", p)
	}
	if _, _, err := pending.Get(context.Background(), key); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expired action was not deleted: %v", err)
	}
}

func TestConcurrentPortfolioRequestsKeepSinglePendingAction(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, saigonLocation)
	s, store, pending, rb, _, _ := newDividendFlowState(t, nil)
	seedDividendFlowPortfolio(t, store, now, 100)
	p, _ := LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	p.Dividends["TCB"] = map[string]DividendRecord{
		"2612974": {Kind: DividendKindCash, PublishedAt: now.Add(-24 * time.Hour).UnixMilli(), RecordDate: startOfSaigonDay(now).UnixMilli(), VNDPerShare: 1500},
	}
	if err := SavePortfolio(context.Background(), store, 7, p); err != nil {
		t.Fatal(err)
	}
	msg := testutil.NewPrivateMessage(7, "/stock_portfolio").Message
	ref := dividendRef{symbol: "TCB", eventID: "2612974"}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.sendDividendSuggestion(context.Background(), rb.Bot, msg, 7, p.Assets["TCB"].OpenedAt, ref)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	keys, err := pending.List(context.Background(), pendingDividendPrefix)
	if err != nil || len(keys) != 1 || keys[0] != pendingDividendKey(7, "2612974") {
		t.Fatalf("pending=%v err=%v", keys, err)
	}
	suggestions, buttonRemovals := 0, 0
	for _, call := range rb.Sent() {
		switch call.Method {
		case "sendMessage":
			suggestions++
		case "editMessageReplyMarkup":
			buttonRemovals++
		}
	}
	if suggestions != 2 || buttonRemovals != 1 {
		t.Fatalf("suggestions=%d buttonRemovals=%d calls=%#v", suggestions, buttonRemovals, rb.Sent())
	}
	action, _, _ := pending.Get(context.Background(), keys[0])
	if action.MessageID != 2 {
		t.Fatalf("surviving action should bind the newest message: %+v", action)
	}
}

func TestDividendCallbackRejectsPositionOpenedAfterRecordDate(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, saigonLocation)
	s, store, pending, rb, _, _ := newDividendFlowState(t, nil)
	seedDividendFlowPortfolio(t, store, now, 100)
	p, _ := LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	recordDate := startOfSaigonDay(now.AddDate(0, 0, -1))
	position := p.Assets["TCB"]
	position.OpenedAt = now.UnixMilli()
	p.Assets["TCB"] = position
	p.Dividends["TCB"] = map[string]DividendRecord{
		"2612974": {Kind: DividendKindCash, PublishedAt: now.AddDate(0, 0, -2).UnixMilli(), RecordDate: recordDate.UnixMilli(), VNDPerShare: 1500},
	}
	if err := SavePortfolio(context.Background(), store, 7, p); err != nil {
		t.Fatal(err)
	}
	action := PendingDividendAction{
		OwnerUserID: 7, ChatID: 7, MessageID: 1, ProviderEventID: "2612974", Symbol: "TCB",
		PositionOpenedAt: position.OpenedAt, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli(),
	}
	key := pendingDividendKey(7, "2612974")
	if err := pending.Put(context.Background(), key, action); err != nil {
		t.Fatal(err)
	}
	if err := s.handleDividendCallback(context.Background(), rb.Bot, dividendCallbackUpdate(7, 7, 1, "7:2612974")); err != nil {
		t.Fatal(err)
	}
	p, _ = LoadPortfolio(context.Background(), store, 7, now.UnixMilli())
	if p.VND != 100_000 || p.Dividends["TCB"]["2612974"].Processed {
		t.Fatalf("ineligible position applied event: %+v", p)
	}
}

func TestProviderFailureKeepsPortfolioAvailable(t *testing.T) {
	s, store, _, rb, now, provider := newDividendFlowState(t, nil)
	provider.err = errors.New("SSI unavailable")
	seedDividendFlowPortfolio(t, store, now, 100)
	if err := s.handleStats(context.Background(), rb.Bot, testutil.NewPrivateMessage(7, "/stock_portfolio")); err != nil {
		t.Fatal(err)
	}
	calls := rb.Sent()
	if len(calls) != 2 || !strings.Contains(calls[0].Text(), "Stock Portfolio") || !strings.Contains(calls[1].Text(), "could not be checked") {
		t.Fatalf("messages = %#v", calls)
	}
}

// dividendCallbackUpdate simulates a button press. data is the callback-data
// suffix after the "stock_div:" prefix, i.e. "<ownerUserID>:<eventID>".
func dividendCallbackUpdate(userID, chatID int64, messageID int, data string) *models.Update {
	return &models.Update{CallbackQuery: &models.CallbackQuery{
		ID: "query", From: models.User{ID: userID}, Data: dividendCallbackPrefix + data,
		Message: models.MaybeInaccessibleMessage{Type: models.MaybeInaccessibleMessageTypeMessage, Message: &models.Message{
			ID: messageID, Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		}},
	}}
}
