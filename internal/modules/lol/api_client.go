// Package lol serves LoL esports match schedules via the PandaScore REST
// API plus a daily push to subscribers.
//
// Endpoint: https://api.pandascore.co/lol/matches
// Auth: Bearer token from the LOL_PANDASCORE_TOKEN env var (free tier,
// 1000 requests/hour — orders of magnitude above this module's needs).
// PandaScore replaced the lolesports.com gql persisted-query transport,
// which broke whenever Riot redeployed their frontend; before that, Riot
// retired the original public esports-api.lolesports.com key outright.
//
// One request shape covers past, running, and upcoming matches:
//
//	GET /lol/matches?range[begin_at]=<from>,<to>&sort=begin_at&per_page=100&page=N
//
// The response is a top-level JSON array. The range's upper bound is
// inclusive upstream, so callers rely on the client-side [from, to) filter
// in fetchEventsInRange for exact window semantics.
//
// PandaScore league slugs (league-of-legends-lck-champions-korea, …) are
// canonicalized to the slugs format.go's allowlist and ordering were built
// on (lck, lpl, …) via leagueSlugMap; unmapped leagues pass through and the
// major-league filter drops them naturally.
//
// Cache strategy: live-first fetches with a document-store-backed 60-minute
// stale fallback for current schedule windows (today, tomorrow, this and next
// week, and the daily push). Explicit-date /lol lookups are always live.
package lol

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	apiURL = "https://api.pandascore.co/lol/matches"
	// tokenEnv is the name of the env var holding the token, not a
	// credential itself.
	tokenEnv = "LOL_PANDASCORE_TOKEN" // #nosec G101

	// userAgentProduct leads the User-Agent; Client.userAgent appends the
	// running bot's t.me link when its username is known.
	userAgentProduct = "tiennm99bot/0.1"
	// pageSize is PandaScore's per_page maximum; 100 covers a dense day in
	// one request and a full week in a couple.
	pageSize = 100
	// staleMaxAge: how long to fall back to a cached payload when the
	// upstream call fails outright.
	staleMaxAge = 60 * 60 * time.Second
	// httpTimeout: keep upstream calls bounded so a hung upstream edge
	// can't hold a worker goroutine indefinitely.
	httpTimeout = 8 * time.Second
)

// leagueSlugMap canonicalizes PandaScore league slugs to the slugs the
// formatters were built on (format.go's majorLeagueSlugs / leagueOrder).
// The PandaScore slugs were taken from the live /lol/leagues listing.
// lta-north also maps to lcs: it is the LTA-era NA top flight; only the
// slug is canonicalized, the display name still passes through.
var leagueSlugMap = map[string]string{
	"league-of-legends-lck-champions-korea": "lck",
	"league-of-legends-lpl-china":           "lpl",
	"league-of-legends-lec":                 "lec",
	"league-of-legends-lcs":                 "lcs",
	"league-of-legends-lta-north":           "lcs",
	"league-of-legends-world-championship":  "worlds",
	"league-of-legends-mid-invitational":    "msi",
	"league-of-legends-first-stand":         "first_stand",
	"league-of-legends-esports-world-cup":   "ewc_lol",
	"league-of-legends-lcp":                 "lcp",
}

// canonicalLeagueSlug maps a PandaScore slug to the module's canonical slug,
// passing unknown slugs through unchanged (FilterMajor drops them).
func canonicalLeagueSlug(psSlug string) string {
	if s, ok := leagueSlugMap[psSlug]; ok {
		return s
	}
	return psSlug
}

// Team is one side of a match. bson tags mirror the json names exactly:
// this tree is persisted inside cacheRecord, so the store must read those
// keys back verbatim — not the driver's lowercased default.
type Team struct {
	Name   string      `json:"name,omitempty" bson:"name,omitempty"`
	Code   string      `json:"code,omitempty" bson:"code,omitempty"`
	Image  string      `json:"image,omitempty" bson:"image,omitempty"`
	Result *TeamResult `json:"result,omitempty" bson:"result,omitempty"`
	Record *TeamRecord `json:"record,omitempty" bson:"record,omitempty"`
}

// TeamResult is a team's per-match outcome (named so tests and the bson decoder
// share one type).
type TeamResult struct {
	Outcome  string `json:"outcome,omitempty" bson:"outcome,omitempty"` // "win" or "loss"
	GameWins int    `json:"gameWins,omitempty" bson:"gameWins,omitempty"`
}

// TeamRecord is a team's running series record.
type TeamRecord struct {
	Wins   int `json:"wins,omitempty" bson:"wins,omitempty"`
	Losses int `json:"losses,omitempty" bson:"losses,omitempty"`
}

// League holds the league-section-header info on each event.
type League struct {
	Name  string `json:"name,omitempty" bson:"name,omitempty"`
	Slug  string `json:"slug,omitempty" bson:"slug,omitempty"`
	Image string `json:"image,omitempty" bson:"image,omitempty"`
}

// Strategy is the bestOf descriptor (Bo1, Bo3, Bo5).
type Strategy struct {
	Type  string `json:"type,omitempty" bson:"type,omitempty"`
	Count int    `json:"count,omitempty" bson:"count,omitempty"`
}

// Match is the inner match metadata.
type Match struct {
	ID       string   `json:"id,omitempty" bson:"id,omitempty"`
	Teams    []Team   `json:"teams,omitempty" bson:"teams,omitempty"`
	Strategy Strategy `json:"strategy,omitempty" bson:"strategy,omitempty"`
}

// ScheduleEvent is one upcoming or past match. State is "unstarted",
// "inProgress", or "completed". This struct is the module's stable shape:
// formatters and the bson cache read it, so the upstream response is mapped
// into it rather than leaking the transport shape.
type ScheduleEvent struct {
	StartTime string `json:"startTime" bson:"startTime"`
	State     string `json:"state,omitempty" bson:"state,omitempty"`
	Type      string `json:"type,omitempty" bson:"type,omitempty"`
	BlockName string `json:"blockName,omitempty" bson:"blockName,omitempty"`
	League    League `json:"league,omitempty" bson:"league,omitempty"`
	Match     Match  `json:"match,omitempty" bson:"match,omitempty"`
}

// psMatch is one match as PandaScore returns it (top-level array element).
type psMatch struct {
	ID            int64  `json:"id"`
	BeginAt       string `json:"begin_at"`
	Status        string `json:"status"`
	NumberOfGames int    `json:"number_of_games"`
	WinnerID      *int64 `json:"winner_id"`
	League        struct {
		Name     string `json:"name"`
		Slug     string `json:"slug"`
		ImageURL string `json:"image_url"`
	} `json:"league"`
	Tournament struct {
		Name string `json:"name"`
	} `json:"tournament"`
	Opponents []struct {
		Opponent struct {
			ID       int64  `json:"id"`
			Acronym  string `json:"acronym"`
			Name     string `json:"name"`
			ImageURL string `json:"image_url"`
		} `json:"opponent"`
	} `json:"opponents"`
	Results []struct {
		TeamID int64 `json:"team_id"`
		Score  int   `json:"score"`
	} `json:"results"`
}

// psStateMap translates PandaScore match statuses to the module's states.
// Anything absent (canceled, postponed) is dropped by toScheduleEvent.
var psStateMap = map[string]string{
	"not_started": "unstarted",
	"running":     "inProgress",
	"finished":    "completed",
}

// toScheduleEvent maps a PandaScore match into the stable ScheduleEvent
// shape. ok=false drops the match (unknown status). Results are joined to
// opponents strictly by team_id — upstream order of the two arrays is not
// guaranteed to agree. Outcome is only declared once the match is finished
// and upstream has committed a winner; that keeps scoreIsPublished's
// "score pending" semantics intact for finished-but-unresolved series.
func (m psMatch) toScheduleEvent() (ScheduleEvent, bool) {
	state, ok := psStateMap[m.Status]
	if !ok {
		return ScheduleEvent{}, false
	}
	scoreByTeam := make(map[int64]int, len(m.Results))
	for _, r := range m.Results {
		scoreByTeam[r.TeamID] = r.Score
	}
	teams := make([]Team, 0, len(m.Opponents))
	for _, o := range m.Opponents {
		t := Team{
			Name:  o.Opponent.Name,
			Code:  o.Opponent.Acronym,
			Image: o.Opponent.ImageURL,
		}
		res := &TeamResult{GameWins: scoreByTeam[o.Opponent.ID]}
		if m.Status == "finished" && m.WinnerID != nil {
			if o.Opponent.ID == *m.WinnerID {
				res.Outcome = "win"
			} else {
				res.Outcome = "loss"
			}
		}
		t.Result = res
		teams = append(teams, t)
	}
	return ScheduleEvent{
		StartTime: m.BeginAt,
		State:     state,
		Type:      "match",
		BlockName: m.Tournament.Name,
		League: League{
			Name:  m.League.Name,
			Slug:  canonicalLeagueSlug(m.League.Slug),
			Image: m.League.ImageURL,
		},
		Match: Match{
			ID:       strconv.FormatInt(m.ID, 10),
			Teams:    teams,
			Strategy: Strategy{Type: "bestOf", Count: m.NumberOfGames},
		},
	}, true
}

// cacheRecord is the store value: fetch timestamp + events. The Mongo store
// owns updatedAt separately; LoL uses ts for cache freshness in every backend.
type cacheRecord struct {
	Ts     int64           `json:"ts" bson:"ts"` // ms-since-epoch when fetched
	Events []ScheduleEvent `json:"events" bson:"events"`
}

// CacheStore is the typed store for schedule cache records.
type CacheStore = storage.DocStore[cacheRecord]

// Client is the PandaScore API client. Default zero-value uses a bounded
// default HTTP client; tests inject a custom HTTP client (typically pointing
// at httptest.Server).
type Client struct {
	HTTP        *http.Client
	URL         string // override for tests; empty falls back to apiURL
	BotUsername string // running bot's username for the User-Agent; may be empty
}

// userAgent identifies the caller to PandaScore, linking the running bot when
// its username is known.
func (c *Client) userAgent() string {
	if c.BotUsername == "" {
		return userAgentProduct
	}
	return userAgentProduct + " (https://t.me/" + c.BotUsername + ")"
}

// httpClient returns the client to use, or a sensible default.
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: httpTimeout}
}

func (c *Client) baseURL() string {
	if c.URL != "" {
		return c.URL
	}
	return apiURL
}

// fetchEventsPage retrieves one page of matches overlapping [from, to].
// Returns the mapped events plus the raw upstream item count — pagination
// must be driven by the raw count, since status-dropped matches shrink the
// mapped slice below per_page on otherwise-full pages.
func (c *Client) fetchEventsPage(ctx context.Context, from, to time.Time, page int) ([]ScheduleEvent, int, error) {
	token := strings.TrimSpace(os.Getenv(tokenEnv))
	if token == "" {
		log.Error("lol_token_missing", "env", tokenEnv)
		return nil, 0, fmt.Errorf("lol %s not set", tokenEnv)
	}

	u, err := url.Parse(c.baseURL())
	if err != nil {
		return nil, 0, fmt.Errorf("lol parse url: %w", err)
	}
	q := u.Query()
	q.Set("range[begin_at]", from.UTC().Format(time.RFC3339)+","+to.UTC().Format(time.RFC3339))
	q.Set("sort", "begin_at")
	q.Set("per_page", strconv.Itoa(pageSize))
	q.Set("page", strconv.Itoa(page))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("lol build request: %w", err)
	}
	// Bearer header only — never the token-in-URL variant, so request URLs
	// stay safe to log.
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent())

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("lol do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("lol read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Warn("lol_fetch", "status", resp.StatusCode, "body", truncate(string(body), 500))
		return nil, 0, fmt.Errorf("lol API HTTP %d", resp.StatusCode)
	}
	var matches []psMatch
	if err := json.Unmarshal(body, &matches); err != nil {
		return nil, 0, fmt.Errorf("lol decode: %w", err)
	}
	out := make([]ScheduleEvent, 0, len(matches))
	for _, m := range matches {
		if e, ok := m.toScheduleEvent(); ok {
			out = append(out, e)
		}
	}
	return out, len(matches), nil
}

// fetchEventsInRange covers [from, to) with a range-bounded query, walking
// pages while upstream keeps returning full ones. Page budget bounds
// upstream calls during dense weeks. The exact [from, to) filter is applied
// here because PandaScore's range upper bound is inclusive.
func (c *Client) fetchEventsInRange(ctx context.Context, from, to time.Time, maxPages int) ([]ScheduleEvent, error) {
	if maxPages <= 0 {
		maxPages = 8
	}
	var collected []ScheduleEvent
	for page := 1; page <= maxPages; page++ {
		events, rawCount, err := c.fetchEventsPage(ctx, from, to, page)
		if err != nil {
			return nil, err
		}
		collected = append(collected, events...)
		if rawCount < pageSize {
			break
		}
		// A still-full final page means the window holds more matches than
		// the page budget covers; with ascending sort the tail of the window
		// would silently vanish from replies, so leave a diagnostic trail.
		if page == maxPages {
			log.Warn("lol_page_budget_exhausted", "maxPages", maxPages, "from", from, "to", to)
		}
	}

	out := make([]ScheduleEvent, 0, len(collected))
	for _, e := range collected {
		t, err := time.Parse(time.RFC3339, e.StartTime)
		if err != nil {
			continue
		}
		if !t.Before(from) && t.Before(to) {
			out = append(out, e)
		}
	}
	return out, nil
}

// cacheKey is `matches:<from-iso>:<to-iso>` — a stable key for a date range.
func cacheKey(from, to time.Time) string {
	return "matches:" + from.UTC().Format(time.RFC3339) + ":" + to.UTC().Format(time.RFC3339)
}

// GetEventsLive fetches the requested range directly from upstream without
// consulting or writing the fallback cache. Five pages = 500 raw matches;
// PandaScore returns ~270/week across all its leagues, so a dense in-season
// week still fits with room to spare, and quota cost is negligible at
// 1000 req/h.
func (c *Client) GetEventsLive(ctx context.Context, from, to time.Time) ([]ScheduleEvent, error) {
	return c.fetchEventsInRange(ctx, from, to, 5)
}

// GetEventsWithFallback is live-first. It always tries upstream, writes a
// successful response to cache, and uses a recent cached response only when the
// upstream call fails.
func (c *Client) GetEventsWithFallback(ctx context.Context, cache CacheStore, from, to time.Time) ([]ScheduleEvent, error) {
	key := cacheKey(from, to)
	now := time.Now().UTC().UnixMilli()

	cached, _, cacheErr := cache.Get(ctx, key)
	hasCached := cacheErr == nil

	events, fetchErr := c.GetEventsLive(ctx, from, to)
	if fetchErr == nil {
		rec := cacheRecord{Ts: now, Events: events}
		if err := cache.Put(ctx, key, rec); err != nil {
			log.Warn("lol_kv_put_fail", "err", err)
		}
		return events, nil
	}

	// Upstream failed — fall back to stale cache if recent enough.
	if hasCached && now-cached.Ts < staleMaxAge.Milliseconds() {
		log.Warn("lol_stale_fallback", "err", fetchErr)
		return cached.Events, nil
	}
	return nil, fetchErr
}

// truncate clips a string to a rune-boundary prefix whose byte length is
// <= maxLen, appending "..." if cut. Keeps log output bounded — upstream
// error pages can be large, and team names mix in Korean/Chinese characters
// that a raw byte slice would split mid-codepoint (producing replacement
// glyphs in the logs).
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	cut := maxLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
