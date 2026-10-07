package lol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tiennm99/miti99bot/internal/storage"
)

// setTestToken puts a dummy PandaScore token in the environment so client
// calls pass the token presence check. httptest servers ignore it.
func setTestToken(t *testing.T) {
	t.Helper()
	t.Setenv(tokenEnv, "test-token")
}

// newCacheStore returns a fresh typed cache store backed by an in-memory
// collection — the per-test equivalent of what the factory wires in production.
func newCacheStore() CacheStore {
	return storage.Typed[cacheRecord](storage.NewMemoryProvider().Collection("lol"))
}

// mkServer spins an httptest.Server returning the supplied JSON body for
// every page request. callCount counts upstream hits so cache tests can
// assert "1 fetch, then no more".
func mkServer(t *testing.T, body string) (*httptest.Server, *int32) {
	t.Helper()
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &count
}

// sampleBody is a PandaScore /lol/matches response: a top-level array.
// Second entry carries a status outside the mapped vocabulary and must be
// dropped.
const sampleBody = `[
  {
    "id": 1001,
    "begin_at": "2026-05-09T05:00:00Z",
    "status": "not_started",
    "number_of_games": 3,
    "winner_id": null,
    "league": {"name": "LCK", "slug": "league-of-legends-lck-champions-korea", "image_url": ""},
    "tournament": {"name": "Regular Season"},
    "opponents": [
      {"opponent": {"id": 1, "acronym": "T1", "name": "T1", "image_url": ""}},
      {"opponent": {"id": 2, "acronym": "GEN", "name": "Gen.G", "image_url": ""}}
    ],
    "results": [{"team_id": 1, "score": 0}, {"team_id": 2, "score": 0}]
  },
  {
    "id": 1002,
    "begin_at": "2026-05-09T08:00:00Z",
    "status": "canceled",
    "number_of_games": 3,
    "winner_id": null,
    "league": {"name": "LCK", "slug": "league-of-legends-lck-champions-korea", "image_url": ""},
    "tournament": {"name": "Regular Season"},
    "opponents": [],
    "results": []
  }
]`

func TestGetEventsWithFallback_FirstHitFetchesUpstreamAndCaches(t *testing.T) {
	setTestToken(t)
	srv, count := mkServer(t, sampleBody)
	c := &Client{HTTP: srv.Client(), URL: srv.URL}
	cache := newCacheStore()
	from := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	events, err := c.GetEventsWithFallback(context.Background(), cache, from, to)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("events = %d, want 1 (canceled dropped)", len(events))
	}
	if events[0].League.Slug != "lck" {
		t.Errorf("event slug = %q, want lck (canonicalized)", events[0].League.Slug)
	}
	if got := events[0].Match.Teams; len(got) != 2 || got[0].Code != "T1" {
		t.Errorf("teams not mapped from opponents: %+v", got)
	}
	if atomic.LoadInt32(count) != 1 {
		t.Errorf("upstream calls = %d, want 1", *count)
	}
	cached, _, err := cache.Get(context.Background(), cacheKey(from, to))
	if err != nil {
		t.Fatalf("load cached record: %v", err)
	}
	if cached.Ts <= 0 {
		t.Fatalf("cached ts = %d, want positive", cached.Ts)
	}
}

func TestGetEventsWithFallback_AlwaysFetchesWhenUpstreamAvailable(t *testing.T) {
	setTestToken(t)
	srv, count := mkServer(t, sampleBody)
	c := &Client{HTTP: srv.Client(), URL: srv.URL}
	cache := newCacheStore()
	from := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	if _, err := c.GetEventsWithFallback(context.Background(), cache, from, to); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetEventsWithFallback(context.Background(), cache, from, to); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(count); got != 2 {
		t.Errorf("upstream calls = %d, want 2 (live-first should refetch)", got)
	}
}

func TestGetEventsWithFallback_StaleFallback(t *testing.T) {
	setTestToken(t)
	// Prime the typed cache store with a stale-but-still-fresh-enough record.
	cache := newCacheStore()
	from := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	staleEvents := []ScheduleEvent{
		{StartTime: "2026-05-09T05:00:00Z", League: League{Slug: "lck", Name: "LCK"}},
	}
	// 10 minutes ago — well inside the 60-minute stale window.
	staleTs := time.Now().UTC().Add(-10 * time.Minute).UnixMilli()
	if err := cache.Put(context.Background(), cacheKey(from, to), cacheRecord{Ts: staleTs, Events: staleEvents}); err != nil {
		t.Fatal(err)
	}

	// Upstream errors — server returns 500.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"down"}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), URL: srv.URL}

	got, err := c.GetEventsWithFallback(context.Background(), cache, from, to)
	if err != nil {
		t.Fatalf("stale fallback should succeed: %v", err)
	}
	if len(got) != 1 || got[0].League.Slug != "lck" {
		t.Errorf("stale fallback returned wrong events: %+v", got)
	}
}

func TestGetEventsWithFallback_HardFailureWhenNoCache(t *testing.T) {
	setTestToken(t)
	cache := newCacheStore()
	from := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), URL: srv.URL}

	_, err := c.GetEventsWithFallback(context.Background(), cache, from, to)
	if err == nil {
		t.Errorf("expected error when upstream fails AND no cache")
	}
}

func TestGetEventsLive_DoesNotWriteCache(t *testing.T) {
	setTestToken(t)
	srv, _ := mkServer(t, sampleBody)
	c := &Client{HTTP: srv.Client(), URL: srv.URL}
	cache := newCacheStore()
	from := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	if _, err := c.GetEventsLive(context.Background(), from, to); err != nil {
		t.Fatalf("live fetch: %v", err)
	}
	if _, _, err := cache.Get(context.Background(), cacheKey(from, to)); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("live fetch cache entry err = %v, want ErrNotFound", err)
	}
}

func TestFetchEventsPage_TokenMissing(t *testing.T) {
	t.Setenv(tokenEnv, "  ") // whitespace-only counts as missing
	srv, count := mkServer(t, sampleBody)
	c := &Client{HTTP: srv.Client(), URL: srv.URL}
	from := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	_, _, err := c.fetchEventsPage(context.Background(), from, from.Add(24*time.Hour), 1)
	if err == nil || !strings.Contains(err.Error(), tokenEnv) {
		t.Errorf("missing token should error mentioning %s; got %v", tokenEnv, err)
	}
	if atomic.LoadInt32(count) != 0 {
		t.Errorf("upstream must not be called without a token; calls = %d", *count)
	}
}

// TestFetchEventsPage_RequestShape asserts the wire contract: Bearer auth
// header (never token-in-URL) plus range/sort/per_page/page params.
func TestFetchEventsPage_RequestShape(t *testing.T) {
	setTestToken(t)
	var gotAuth, gotRange, gotSort, gotPerPage, gotPage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		q := r.URL.Query()
		gotRange = q.Get("range[begin_at]")
		gotSort = q.Get("sort")
		gotPerPage = q.Get("per_page")
		gotPage = q.Get("page")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), URL: srv.URL}

	from := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	if _, _, err := c.fetchEventsPage(context.Background(), from, to, 2); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want Bearer test-token", gotAuth)
	}
	if want := "2026-05-09T00:00:00Z,2026-05-10T00:00:00Z"; gotRange != want {
		t.Errorf("range[begin_at] = %q, want %q", gotRange, want)
	}
	if gotSort != "begin_at" || gotPerPage != "100" || gotPage != "2" {
		t.Errorf("sort/per_page/page = %q/%q/%q, want begin_at/100/2", gotSort, gotPerPage, gotPage)
	}
}

func TestFetchEventsPage_NonJSONErrors(t *testing.T) {
	setTestToken(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), URL: srv.URL}
	from := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	_, _, err := c.fetchEventsPage(context.Background(), from, from.Add(24*time.Hour), 1)
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Errorf("non-JSON should produce decode error; got %v", err)
	}
}

// TestToScheduleEvent_ScoreAndWinnerMapping guards the results join: results
// arrive keyed by team_id in arbitrary order relative to opponents, and the
// winner flag must translate into win/loss outcomes only when finished.
func TestToScheduleEvent_ScoreAndWinnerMapping(t *testing.T) {
	winner := int64(2)
	body := fmt.Sprintf(`{
	  "id": 42,
	  "begin_at": "2026-05-09T05:00:00Z",
	  "status": "finished",
	  "number_of_games": 5,
	  "winner_id": %d,
	  "league": {"name": "LPL", "slug": "league-of-legends-lpl-china", "image_url": ""},
	  "tournament": {"name": "Playoffs"},
	  "opponents": [
	    {"opponent": {"id": 1, "acronym": "JDG", "name": "JD Gaming", "image_url": ""}},
	    {"opponent": {"id": 2, "acronym": "BLG", "name": "Bilibili Gaming", "image_url": ""}}
	  ],
	  "results": [{"team_id": 2, "score": 3}, {"team_id": 1, "score": 1}]
	}`, winner)
	var m psMatch
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	e, ok := m.toScheduleEvent()
	if !ok {
		t.Fatal("finished match must map")
	}
	if e.State != "completed" || e.BlockName != "Playoffs" || e.League.Slug != "lpl" {
		t.Errorf("state/block/slug = %q/%q/%q", e.State, e.BlockName, e.League.Slug)
	}
	if e.Match.Strategy.Count != 5 {
		t.Errorf("Bo = %d, want 5", e.Match.Strategy.Count)
	}
	jdg, blg := e.Match.Teams[0], e.Match.Teams[1]
	// Results were listed BLG-first; the join by team_id must still credit
	// JDG with 1 and BLG with 3.
	if jdg.Result.GameWins != 1 || jdg.Result.Outcome != "loss" {
		t.Errorf("JDG result = %+v, want 1/loss", jdg.Result)
	}
	if blg.Result.GameWins != 3 || blg.Result.Outcome != "win" {
		t.Errorf("BLG result = %+v, want 3/win", blg.Result)
	}
}

// TestToScheduleEvent_FinishedWithoutWinnerStaysPending: finished but no
// winner_id → no outcome declared → formatters render "score pending".
func TestToScheduleEvent_FinishedWithoutWinnerStaysPending(t *testing.T) {
	m := psMatch{Status: "finished", BeginAt: "2026-05-09T05:00:00Z"}
	m.Opponents = make([]struct {
		Opponent struct {
			ID       int64  `json:"id"`
			Acronym  string `json:"acronym"`
			Name     string `json:"name"`
			ImageURL string `json:"image_url"`
		} `json:"opponent"`
	}, 2)
	m.Opponents[0].Opponent.ID = 1
	m.Opponents[1].Opponent.ID = 2
	e, ok := m.toScheduleEvent()
	if !ok {
		t.Fatal("finished match must map")
	}
	if scoreIsPublished(e.Match.Teams[0], e.Match.Teams[1]) {
		t.Errorf("no winner_id must leave score unpublished")
	}
}

// TestFetchEventsInRange_WalksFullPages guards pagination: a full raw page
// (100 items) must trigger a fetch of the next page, driven by the RAW count
// — status-dropped matches must not end the walk early.
func TestFetchEventsInRange_WalksFullPages(t *testing.T) {
	setTestToken(t)
	// Page 1: 100 raw matches (2 canceled), page 2: 1 match.
	page1 := make([]map[string]any, 0, pageSize)
	for i := 0; i < pageSize; i++ {
		status := "not_started"
		if i < 2 {
			status = "canceled"
		}
		page1 = append(page1, map[string]any{
			"id": 2000 + i, "begin_at": "2026-05-18T05:00:00Z", "status": status,
			"number_of_games": 1,
			"league":          map[string]any{"name": "LCK", "slug": "league-of-legends-lck-champions-korea"},
			"tournament":      map[string]any{"name": "Regular Season"},
			"opponents":       []any{}, "results": []any{},
		})
	}
	page1JSON, _ := json.Marshal(page1)
	page2JSON := `[{"id": 3000, "begin_at": "2026-05-19T05:00:00Z", "status": "not_started",
		"number_of_games": 1,
		"league": {"name": "LCK", "slug": "league-of-legends-lck-champions-korea"},
		"tournament": {"name": "Regular Season"}, "opponents": [], "results": []}]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write(page1JSON)
		case "2":
			_, _ = w.Write([]byte(page2JSON))
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), URL: srv.URL}
	from := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	to := from.Add(7 * 24 * time.Hour)

	events, err := c.fetchEventsInRange(context.Background(), from, to, 0)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	// 100 raw - 2 canceled + 1 on page 2.
	if len(events) != 99 {
		t.Errorf("events = %d, want 99", len(events))
	}
}

// TestFetchEventsInRange_FiltersToExactWindow guards the client-side
// [from, to) filter: PandaScore's range upper bound is inclusive, so an
// event starting exactly at `to` comes back and must be dropped locally.
func TestFetchEventsInRange_FiltersToExactWindow(t *testing.T) {
	setTestToken(t)
	body := `[
	  {"id": 1, "begin_at": "2026-05-18T05:00:00Z", "status": "not_started", "number_of_games": 3,
	   "league": {"name": "LCK", "slug": "league-of-legends-lck-champions-korea"},
	   "tournament": {"name": "Regular Season"},
	   "opponents": [{"opponent": {"id": 1, "acronym": "GEN", "name": "Gen.G"}}], "results": []},
	  {"id": 2, "begin_at": "2026-05-19T00:00:00Z", "status": "not_started", "number_of_games": 3,
	   "league": {"name": "LCK", "slug": "league-of-legends-lck-champions-korea"},
	   "tournament": {"name": "Regular Season"},
	   "opponents": [{"opponent": {"id": 2, "acronym": "T1", "name": "T1"}}], "results": []}
	]`
	srv, _ := mkServer(t, body)
	c := &Client{HTTP: srv.Client(), URL: srv.URL}

	from := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC) // second event is exactly `to`
	events, err := c.fetchEventsInRange(context.Background(), from, to, 0)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(events) != 1 || events[0].Match.Teams[0].Code != "GEN" {
		t.Errorf("window filter wrong, got %v", eventDays(events))
	}
}

func eventDays(events []ScheduleEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.StartTime)
	}
	return out
}

// truncate is internal but worth a smoke test — log payloads use it.
func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate short = %q, want unchanged", got)
	}
	got := truncate("a long enough string", 5)
	if got != "a lon..." {
		t.Errorf("truncate = %q, want 'a lon...'", got)
	}
}

func TestClientUserAgent(t *testing.T) {
	if got := (&Client{}).userAgent(); got != "miti99bot/0.1" {
		t.Errorf("no username: got %q", got)
	}
	if got := (&Client{BotUsername: "examplebot"}).userAgent(); got != "miti99bot/0.1 (https://t.me/examplebot)" {
		t.Errorf("with username: got %q", got)
	}
}
