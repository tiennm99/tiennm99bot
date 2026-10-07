package lol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/subscription"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// installSchedule wires the lol module to a recording bot, with a
// custom upstream HTTP server returning bodyJSON for every request. nowMs
// fixes the clock so date-based handlers are deterministic.
// Returns the recording bot and the subscriber store for inspection in tests.
func installSchedule(t *testing.T, bodyJSON string, nowMs int64) (*testutil.RecordingBot, subscription.Store) {
	t.Helper()
	setTestToken(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodyJSON))
	}))
	t.Cleanup(upstream.Close)

	rb := testutil.NewRecordingBot(t)
	col := storage.NewMemoryProvider().Collection("lol")

	s := &state{
		subscribers: storage.Typed[subscription.Doc](col),
		pushDate:    storage.Typed[subscription.DayDoc](col),
		cache:       storage.Typed[cacheRecord](col),
		client:      &Client{HTTP: upstream.Client(), URL: upstream.URL},
		nowFn:       func() time.Time { return time.UnixMilli(nowMs).UTC() },
	}
	mod := modules.Module{
		Name: "lol",
		Commands: []modules.Command{
			{Name: "lol", Visibility: modules.VisibilityPublic, Description: "x", Handler: s.handleSchedule},
			{Name: "lol_tomorrow", Visibility: modules.VisibilityPublic, Description: "x", Handler: s.handleTomorrow},
			{Name: "lol_this_week", Visibility: modules.VisibilityPublic, Description: "x", Handler: s.handleWeek},
			{Name: "lol_next_week", Visibility: modules.VisibilityPublic, Description: "x", Handler: s.handleNextWeek},
			{Name: "lol_subscribe", Visibility: modules.VisibilityPublic, Description: "x", Handler: s.handleSubscribe},
			{Name: "lol_unsubscribe", Visibility: modules.VisibilityPublic, Description: "x", Handler: s.handleUnsubscribe},
		},
	}
	reg := &modules.Registry{
		Modules:     []modules.Module{mod},
		AllCommands: map[string]modules.Command{},
	}
	for _, c := range mod.Commands {
		reg.AllCommands[c.Name] = c
	}
	modules.Install(rb.Bot, reg, modules.Auth{})
	return rb, s.subscribers
}

// 2026-05-09 12:00 UTC = 19:00 ICT (still May 9 ICT day). Used as the fake
// "now" in handler tests.
const fakeNowMs int64 = 1778328000000 // 2026-05-09T12:00:00Z

const todayBody = `[
  {
    "id": 1, "begin_at": "2026-05-09T05:00:00Z", "status": "not_started", "number_of_games": 3,
    "league": {"name": "LCK", "slug": "league-of-legends-lck-champions-korea"},
    "tournament": {"name": "Regular Season"},
    "opponents": [
      {"opponent": {"id": 1, "acronym": "T1", "name": "T1"}},
      {"opponent": {"id": 2, "acronym": "GEN", "name": "Gen.G"}}
    ],
    "results": []
  }
]`

const futureBody = `[
  {
    "id": 2, "begin_at": "2026-05-10T05:00:00Z", "status": "not_started", "number_of_games": 3,
    "league": {"name": "LCK", "slug": "league-of-legends-lck-champions-korea"},
    "tournament": {"name": "Regular Season"},
    "opponents": [
      {"opponent": {"id": 3, "acronym": "DK", "name": "Dplus KIA"}},
      {"opponent": {"id": 4, "acronym": "KT", "name": "KT Rolster"}}
    ],
    "results": []
  },
  {
    "id": 3, "begin_at": "2026-05-12T08:00:00Z", "status": "not_started", "number_of_games": 5,
    "league": {"name": "LPL", "slug": "league-of-legends-lpl-china"},
    "tournament": {"name": "Playoffs"},
    "opponents": [
      {"opponent": {"id": 5, "acronym": "JDG", "name": "JD Gaming"}},
      {"opponent": {"id": 6, "acronym": "BLG", "name": "Bilibili Gaming"}}
    ],
    "results": []
  }
]`

func TestHandleSchedule_DefaultsToToday(t *testing.T) {
	rb, _ := installSchedule(t, todayBody, fakeNowMs)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/lol"))

	got := rb.LastSent()
	if got.Method != "sendMessage" {
		t.Errorf("method = %q, want sendMessage", got.Method)
	}
	if got.Form["parse_mode"] != "HTML" {
		t.Errorf("parse_mode = %q, want HTML", got.Form["parse_mode"])
	}
	for _, want := range []string{"<b>LoL —", "(ICT)", "<b>LCK</b>", "T1 vs GEN"} {
		if !strings.Contains(got.Text(), want) {
			t.Errorf("missing %q in:\n%s", want, got.Text())
		}
	}
}

func TestHandleSchedule_BadDateInput(t *testing.T) {
	rb, _ := installSchedule(t, todayBody, fakeNowMs)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/lol notadate"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "Invalid date") {
		t.Errorf("expected parse error reply; got %q", got)
	}
}

func TestHandleWeek_RendersWeek(t *testing.T) {
	rb, _ := installSchedule(t, todayBody, fakeNowMs)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/lol_this_week"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "→") {
		t.Errorf("week header missing arrow: %q", got)
	}
	// fakeNowMs is Sat 2026-05-09 ICT → calendar week is Mon May 4 → Sun May 10.
	for _, want := range []string{"Mon May 4", "Sun May 10"} {
		if !strings.Contains(got, want) {
			t.Errorf("week header missing %q in:\n%s", want, got)
		}
	}
}

func TestHandleTomorrow_RendersTomorrow(t *testing.T) {
	rb, _ := installSchedule(t, futureBody, fakeNowMs)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/lol_tomorrow"))

	got := rb.LastSent().Text()
	for _, want := range []string{"Sun May 10", "<b>LCK</b>", "DK vs KT"} {
		if !strings.Contains(got, want) {
			t.Errorf("tomorrow reply missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "JDG vs BLG") {
		t.Errorf("tomorrow reply included next-week match:\n%s", got)
	}
}

func TestHandleNextWeek_RendersNextWeek(t *testing.T) {
	rb, _ := installSchedule(t, futureBody, fakeNowMs)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/lol_next_week"))

	got := rb.LastSent().Text()
	for _, want := range []string{"Mon May 11", "Sun May 17", "<b>LPL</b>", "JDG vs BLG"} {
		if !strings.Contains(got, want) {
			t.Errorf("next-week reply missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "DK vs KT") {
		t.Errorf("next-week reply included tomorrow match:\n%s", got)
	}
}

func TestNewRegistersScheduleShortcuts(t *testing.T) {
	mod := New(modules.Deps{Store: storage.NewMemoryProvider().Collection("lol")})
	got := map[string]bool{}
	for _, cmd := range mod.Commands {
		got[cmd.Name] = true
	}
	for _, want := range []string{"lol", "lol_tomorrow", "lol_this_week", "lol_next_week"} {
		if !got[want] {
			t.Errorf("New() missing command %s", want)
		}
	}
}

func TestHandleSubscribe_AddsAndIsIdempotent(t *testing.T) {
	rb, subsStore := installSchedule(t, todayBody, fakeNowMs)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/lol_subscribe"))
	if got := rb.LastSent().Text(); !strings.Contains(got, "Subscribed this chat") || !strings.Contains(got, "daily LoL schedule") {
		t.Errorf("first subscribe should confirm; got %q", got)
	}
	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/lol_subscribe"))
	if got := rb.LastSent().Text(); !strings.Contains(got, "Already subscribed") {
		t.Errorf("duplicate subscribe should report Already; got %q", got)
	}
	ids, _ := subscription.List(context.Background(), subsStore)
	if len(ids) != 1 || ids[0] != (subscription.Subscriber{ChatID: 7}) {
		t.Errorf("subscribers = %v, want [{7 0}]", ids)
	}
}

// TestHandleSubscribe_ForumTopic_CapturesThreadID locks in the forum-topic
// fix at the handler boundary: subscribing from a non-zero MessageThreadID
// records that thread on the Subscriber so the daily push lands in the
// originating topic instead of General.
func TestHandleSubscribe_ForumTopic_CapturesThreadID(t *testing.T) {
	rb, subsStore := installSchedule(t, todayBody, fakeNowMs)
	upd := testutil.NewSupergroupMessage(555, 999, "/lol_subscribe")
	upd.Message.MessageThreadID = 42
	rb.Bot.ProcessUpdate(context.Background(), upd)

	if got := rb.LastSent().Text(); !strings.Contains(got, "Subscribed this topic") {
		t.Errorf("topic subscribe reply = %q", got)
	}
	subs, _ := subscription.List(context.Background(), subsStore)
	want := subscription.Subscriber{ChatID: 555, ThreadID: 42}
	if len(subs) != 1 || subs[0] != want {
		t.Errorf("subscribers = %v, want [%v]", subs, want)
	}
}

// TestHandleUnsubscribe_ForumTopic_RemovesOnlyThatTopic verifies that
// unsubscribing from one topic does not affect sister-topic subscriptions
// in the same chat.
func TestHandleUnsubscribe_ForumTopic_RemovesOnlyThatTopic(t *testing.T) {
	rb, subsStore := installSchedule(t, todayBody, fakeNowMs)

	// Subscribe in two topics of the same supergroup.
	for _, tid := range []int{42, 99} {
		upd := testutil.NewSupergroupMessage(555, 999, "/lol_subscribe")
		upd.Message.MessageThreadID = tid
		rb.Bot.ProcessUpdate(context.Background(), upd)
	}

	// Unsubscribe in topic 42 only.
	upd := testutil.NewSupergroupMessage(555, 999, "/lol_unsubscribe")
	upd.Message.MessageThreadID = 42
	rb.Bot.ProcessUpdate(context.Background(), upd)

	subs, _ := subscription.List(context.Background(), subsStore)
	want := subscription.Subscriber{ChatID: 555, ThreadID: 99}
	if len(subs) != 1 || subs[0] != want {
		t.Errorf("subscribers = %v, want [%v]", subs, want)
	}
}

func TestHandleUnsubscribe(t *testing.T) {
	rb, _ := installSchedule(t, todayBody, fakeNowMs)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/lol_subscribe"))
	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/lol_unsubscribe"))
	if got := rb.LastSent().Text(); got != "Unsubscribed this chat." {
		t.Errorf("unsubscribe reply = %q, want 'Unsubscribed this chat.'", got)
	}
	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/lol_unsubscribe"))
	if got := rb.LastSent().Text(); !strings.Contains(got, "This chat wasn't subscribed") {
		t.Errorf("idempotent unsubscribe reply = %q", got)
	}
}

func TestHandleSchedule_UpstreamFailureGivesFriendlyError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	rb := testutil.NewRecordingBot(t)
	col := storage.NewMemoryProvider().Collection("lol")
	s := &state{
		subscribers: storage.Typed[subscription.Doc](col),
		pushDate:    storage.Typed[subscription.DayDoc](col),
		cache:       storage.Typed[cacheRecord](col),
		client:      &Client{HTTP: upstream.Client(), URL: upstream.URL},
		nowFn:       func() time.Time { return time.UnixMilli(fakeNowMs).UTC() },
	}
	cmd := modules.Command{Name: "lol", Visibility: modules.VisibilityPublic, Description: "x", Handler: s.handleSchedule}
	reg := &modules.Registry{
		Modules:     []modules.Module{{Name: "lol", Commands: []modules.Command{cmd}}},
		AllCommands: map[string]modules.Command{cmd.Name: cmd},
	}
	modules.Install(rb.Bot, reg, modules.Auth{})

	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/lol"))
	if got := rb.LastSent().Text(); !strings.Contains(got, "Could not fetch") {
		t.Errorf("expected friendly fetch-error reply; got %q", got)
	}
}

func TestHandleSchedule_CustomDateDoesNotUseFallbackCache(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	rb := testutil.NewRecordingBot(t)
	col := storage.NewMemoryProvider().Collection("lol")
	cache := storage.Typed[cacheRecord](col)
	s := &state{
		subscribers: storage.Typed[subscription.Doc](col),
		pushDate:    storage.Typed[subscription.DayDoc](col),
		cache:       cache,
		client:      &Client{HTTP: upstream.Client(), URL: upstream.URL},
		nowFn:       func() time.Time { return time.UnixMilli(fakeNowMs).UTC() },
	}
	from := ictDayStartOf(s.now())
	to := addDays(from, 1)
	if err := cache.Put(context.Background(), cacheKey(from, to), cacheRecord{
		Ts: time.Now().UTC().UnixMilli(),
		Events: []ScheduleEvent{{
			StartTime: "2026-05-09T05:00:00Z",
			State:     "unstarted",
			League:    League{Name: "LCK", Slug: "lck"},
			Match: Match{
				Teams:    []Team{{Code: "T1"}, {Code: "GEN"}},
				Strategy: Strategy{Count: 3},
			},
		}},
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	cmd := modules.Command{Name: "lol", Visibility: modules.VisibilityPublic, Description: "x", Handler: s.handleSchedule}
	reg := &modules.Registry{
		Modules:     []modules.Module{{Name: "lol", Commands: []modules.Command{cmd}}},
		AllCommands: map[string]modules.Command{cmd.Name: cmd},
	}
	modules.Install(rb.Bot, reg, modules.Auth{})

	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/lol"))
	if got := rb.LastSent().Text(); !strings.Contains(got, "T1 vs GEN") {
		t.Fatalf("default /lol should use stale fallback; got %q", got)
	}

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/lol 09-05-2026"))
	if got := rb.LastSent().Text(); !strings.Contains(got, "Could not fetch") {
		t.Fatalf("custom-date /lol should not use stale fallback; got %q", got)
	}
}
