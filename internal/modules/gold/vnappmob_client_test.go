package gold

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

func newTestVNAppMobClient(srv *httptest.Server, coll storage.Collection) *VNAppMobClient {
	return &VNAppMobClient{
		HTTP:    srv.Client(),
		BaseURL: srv.URL,
		cache:   storage.Typed[apiKeyCache](coll),
		nowFn:   func() time.Time { return time.Unix(1000, 0) },
	}
}

func makeJWT(exp int64) string {
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, _ := json.Marshal(map[string]any{"exp": exp, "scope": "gold", "permission": "read"})
	h := base64.RawURLEncoding.EncodeToString(header)
	p := base64.RawURLEncoding.EncodeToString(payload)
	return h + "." + p + ".dummy-signature"
}

func newTestColl() storage.Collection {
	return storage.NewMemoryProvider().Collection("gold")
}

func TestJWTExp(t *testing.T) {
	now := time.Unix(1000, 0)
	tests := []struct {
		name    string
		token   string
		wantExp int64
		wantErr bool
	}{
		{
			name:    "valid",
			token:   makeJWT(now.Add(48 * time.Hour).Unix()),
			wantExp: now.Add(48 * time.Hour).Unix(),
		},
		{
			name:    "missing exp",
			token:   makeJWT(0),
			wantErr: true,
		},
		{
			name:    "malformed",
			token:   "not-a-jwt",
			wantErr: true,
		},
		{
			name:    "bad base64 payload",
			token:   "header.!!!.sig",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := jwtExp(tc.token)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got exp=%d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("jwtExp: %v", err)
			}
			if got != tc.wantExp {
				t.Fatalf("exp: got %d, want %d", got, tc.wantExp)
			}
		})
	}
}

func TestIsExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	c := &VNAppMobClient{nowFn: func() time.Time { return now }}

	if !c.isExpired(0) {
		t.Fatal("exp=0 should be expired")
	}
	if !c.isExpired(now.Add(12 * time.Hour).Unix()) {
		t.Fatal("key expiring in 12h should be expired (within 24h buffer)")
	}
	if c.isExpired(now.Add(48 * time.Hour).Unix()) {
		t.Fatal("key expiring in 48h should not be expired")
	}
}

func TestRefreshKey(t *testing.T) {
	exp := time.Unix(1000, 0).Add(14 * 24 * time.Hour).Unix()
	var refreshHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("refresh: want GET, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/api/request_api_key") {
			t.Errorf("refresh path: got %s", r.URL.Path)
		}
		if scope := r.URL.Query().Get("scope"); scope != "gold" {
			t.Errorf("refresh scope: got %q", scope)
		}
		atomic.AddInt32(&refreshHits, 1)
		fmt.Fprint(w, makeJWT(exp))
	}))
	defer srv.Close()

	c := newTestVNAppMobClient(srv, newTestColl())

	key, err := c.getKey(context.Background())
	if err != nil {
		t.Fatalf("getKey: %v", err)
	}
	if key == "" {
		t.Fatal("expected non-empty key")
	}
	if atomic.LoadInt32(&refreshHits) != 1 {
		t.Fatalf("refresh hits: got %d, want 1", refreshHits)
	}

	// A second call should reuse the cached key without hitting refresh again.
	key2, err := c.getKey(context.Background())
	if err != nil {
		t.Fatalf("getKey cached: %v", err)
	}
	if key2 != key {
		t.Fatal("cached key changed unexpectedly")
	}
	if atomic.LoadInt32(&refreshHits) != 1 {
		t.Fatalf("refresh hits after cache: got %d, want 1", refreshHits)
	}
}

func TestRefreshKey_ObjectWrapper(t *testing.T) {
	exp := time.Unix(1000, 0).Add(14 * 24 * time.Hour).Unix()
	jwt := makeJWT(exp)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/request_api_key" {
			t.Errorf("refresh path: got %s", r.URL.Path)
		}
		fmt.Fprintf(w, `{"results":%q}`, jwt)
	}))
	defer srv.Close()

	c := newTestVNAppMobClient(srv, newTestColl())
	key, err := c.getKey(context.Background())
	if err != nil {
		t.Fatalf("getKey: %v", err)
	}
	if key != jwt {
		t.Fatalf("key mismatch: got %q, want %q", key, jwt)
	}
}

func TestRefreshKey_QuotedString(t *testing.T) {
	exp := time.Unix(1000, 0).Add(14 * 24 * time.Hour).Unix()
	jwt := makeJWT(exp)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%q", jwt)
	}))
	defer srv.Close()

	c := newTestVNAppMobClient(srv, newTestColl())
	key, err := c.getKey(context.Background())
	if err != nil {
		t.Fatalf("getKey: %v", err)
	}
	if key != jwt {
		t.Fatalf("key mismatch: got %q, want %q", key, jwt)
	}
}

func TestFetchSJCPrice(t *testing.T) {
	exp := time.Unix(1000, 0).Add(14 * 24 * time.Hour).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/request_api_key":
			fmt.Fprint(w, makeJWT(exp))
		case "/api/v2/gold/sjc":
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				t.Errorf("missing bearer token, got %q", auth)
			}
			_, _ = w.Write([]byte(`{"results":[{"buy_1l":"90000000.0","sell_1l":"91000000.0"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestVNAppMobClient(srv, newTestColl())
	buy, sell, err := c.FetchSJCPrice(context.Background())
	if err != nil {
		t.Fatalf("FetchSJCPrice: %v", err)
	}
	if buy != 90_000_000 || sell != 91_000_000 {
		t.Fatalf("price: got buy=%v sell=%v", buy, sell)
	}
}

func TestFetchSJCPrice_401Or403Refreshes(t *testing.T) {
	exp := time.Unix(1000, 0).Add(14 * 24 * time.Hour).Unix()
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			var sjcHits, refreshHits int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/request_api_key":
					atomic.AddInt32(&refreshHits, 1)
					fmt.Fprint(w, makeJWT(exp))
				case "/api/v2/gold/sjc":
					n := atomic.AddInt32(&sjcHits, 1)
					if n == 1 {
						w.WriteHeader(status)
						return
					}
					_, _ = w.Write([]byte(`{"results":[{"buy_1l":"90000000.0","sell_1l":"91000000.0"}]}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			c := newTestVNAppMobClient(srv, newTestColl())
			buy, sell, err := c.FetchSJCPrice(context.Background())
			if err != nil {
				t.Fatalf("FetchSJCPrice: %v", err)
			}
			if buy != 90_000_000 || sell != 91_000_000 {
				t.Fatalf("price: got buy=%v sell=%v", buy, sell)
			}
			if atomic.LoadInt32(&sjcHits) != 2 {
				t.Fatalf("SJC hits: got %d, want 2", sjcHits)
			}
			// First refresh gets the initial key; the auth failure triggers a second refresh.
			if atomic.LoadInt32(&refreshHits) != 2 {
				t.Fatalf("refresh hits: got %d, want 2", refreshHits)
			}
		})
	}
}

func TestFetchSJCPrice_FallbackError(t *testing.T) {
	exp := time.Unix(1000, 0).Add(14 * 24 * time.Hour).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/request_api_key":
			fmt.Fprint(w, makeJWT(exp))
		case "/api/v2/gold/sjc":
			_, _ = w.Write([]byte(`{"results":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestVNAppMobClient(srv, newTestColl())
	_, _, err := c.FetchSJCPrice(context.Background())
	if !errors.Is(err, ErrNoGoldPrice) {
		t.Fatalf("got %v, want ErrNoGoldPrice", err)
	}
}

func TestFetchSJCPrice_InvalidValues(t *testing.T) {
	exp := time.Unix(1000, 0).Add(14 * 24 * time.Hour).Unix()
	cases := []struct {
		name string
		body string
	}{
		{name: "zero values", body: `{"results":[{"buy_1l":0,"sell_1l":0}]}`},
		{name: "missing fields", body: `{"results":[{"buy_1l":90000000}]}`},
		{name: "nan values", body: `{"results":[{"buy_1l":"NaN","sell_1l":"NaN"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/request_api_key":
					fmt.Fprint(w, makeJWT(exp))
				case "/api/v2/gold/sjc":
					_, _ = w.Write([]byte(tc.body))
				}
			}))
			defer srv.Close()
			c := newTestVNAppMobClient(srv, newTestColl())
			_, _, err := c.FetchSJCPrice(context.Background())
			if err == nil {
				t.Fatal("expected error for invalid values")
			}
		})
	}
}

// A rejected GOLD_VNAPP_API_KEY must not be retried: the client switches to a
// self-managed key for this call and every later one.
func TestFetchSJCPrice_RejectedEnvTokenSwitchesToManagedKey(t *testing.T) {
	exp := time.Unix(1000, 0).Add(14 * 24 * time.Hour).Unix()
	managed := makeJWT(exp)
	var envHits, refreshHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/request_api_key":
			atomic.AddInt32(&refreshHits, 1)
			fmt.Fprint(w, managed)
		case "/api/v2/gold/sjc":
			if r.Header.Get("Authorization") != "Bearer "+managed {
				atomic.AddInt32(&envHits, 1)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"results":[{"buy_1l":"90000000.0","sell_1l":"91000000.0"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestVNAppMobClient(srv, newTestColl())
	c.Token = "expired-env-key"
	for i := range 2 {
		if _, _, err := c.FetchSJCPrice(context.Background()); err != nil {
			t.Fatalf("FetchSJCPrice call %d: %v", i+1, err)
		}
	}
	if got := atomic.LoadInt32(&envHits); got != 1 {
		t.Fatalf("requests with env key: got %d, want 1", got)
	}
	if got := atomic.LoadInt32(&refreshHits); got != 1 {
		t.Fatalf("refresh hits: got %d, want 1", got)
	}
}
