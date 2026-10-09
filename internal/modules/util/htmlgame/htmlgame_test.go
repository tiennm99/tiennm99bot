package htmlgame

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

type testClaims struct {
	User   int64 `json:"u"`
	Expiry int64 `json:"e"`
}

func (c testClaims) Valid() bool      { return c.User > 0 && c.Expiry > 0 }
func (c testClaims) ExpiresAt() int64 { return c.Expiry }

func TestToken_RoundTripTamperOversizeExpiry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	in := testClaims{User: 7, Expiry: now.Add(time.Hour).Unix()}
	tok, err := Sign(testKey, in)
	if err != nil {
		t.Fatal(err)
	}
	var got testClaims
	if err := Verify(testKey, tok, now, &got); err != nil || got != in {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	body, sig, _ := strings.Cut(tok, ".")
	invalid, _ := Sign(testKey, testClaims{Expiry: in.Expiry})
	for name, bad := range map[string]string{
		"tampered body": strings.ToUpper(body[:4]) + body[4:] + "." + sig,
		"bad sig":       body + "." + sig[:len(sig)-2] + "AA",
		"no dot":        body,
		"empty":         "",
		"oversize":      strings.Repeat("a", MaxTokenBytes+1),
		"invalid":       invalid,
	} {
		if err := Verify(testKey, bad, now, &testClaims{}); !errors.Is(err, ErrBadToken) {
			t.Errorf("%s: err = %v, want ErrBadToken", name, err)
		}
	}
	if err := Verify([]byte("other-key-other-key-other-key-xx"), tok, now, &testClaims{}); !errors.Is(err, ErrBadToken) {
		t.Errorf("wrong key: %v", err)
	}
	if err := Verify(nil, tok, now, &testClaims{}); !errors.Is(err, ErrBadToken) {
		t.Errorf("no key: %v", err)
	}
	if err := Verify(testKey, tok, now.Add(time.Hour), &testClaims{}); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("expired: %v", err)
	}
}

func TestDeriveKey_LabelsSeparateKeys(t *testing.T) {
	a := DeriveKey([]byte("root"), "game/a")
	b := DeriveKey([]byte("root"), "game/b")
	if len(a) != 32 || string(a) == string(b) {
		t.Fatalf("keys a=%x b=%x", a, b)
	}
	if string(a) != string(DeriveKey([]byte("root"), "game/a")) {
		t.Fatal("not deterministic")
	}
	if DeriveKey(nil, "x") != nil {
		t.Fatal("key from an empty root")
	}
}

func TestParseBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                           "",
		"https://game.example":       "https://game.example",
		" https://game.example/ ":    "https://game.example",
		"https://game.example/bot//": "https://game.example/bot",
		"http://game.example":        "",
		"game.example":               "",
		"https://u:p@game.example":   "",
		"https://game.example/?x=1":  "",
		"https://game.example/#frag": "",
		"https:///no-host":           "",
	}
	for in, want := range cases {
		if got := ParseBaseURL(in, "test"); got != want {
			t.Errorf("ParseBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSecurityHeadersAndAssets(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html>")},
		"app.js":     {Data: []byte("1")},
		"app.css":    {Data: []byte("a{}")},
	}
	mux := http.NewServeMux()
	for _, name := range []string{"index.html", "app.js", "app.css"} {
		mux.HandleFunc("GET /games/x/"+name, ServeAsset(fsys, name))
	}
	mux.HandleFunc("GET /games/x/missing", ServeAsset(fsys, "missing.js"))
	mux.HandleFunc("POST /games/x/api/ping", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	h := SecurityHeaders("/games/x/", mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/games/x/api/ping", nil))
	hdr := rec.Header()
	if hdr.Get("Cache-Control") != "no-store" || hdr.Get("Referrer-Policy") != "no-referrer" ||
		hdr.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(hdr.Get("Content-Security-Policy"), "default-src 'self'") ||
		hdr.Get("Content-Type") != "application/json; charset=utf-8" || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("API response %v %q", hdr, rec.Body.String())
	}
	if strings.Contains(hdr.Get("Content-Security-Policy"), "frame-ancestors") {
		t.Fatal("framing restricted; Telegram Web embeds games in an iframe")
	}
	for name, ctype := range map[string]string{"index.html": "text/html", "app.js": "text/javascript", "app.css": "text/css"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/games/x/"+name, nil))
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), ctype) ||
			rec.Header().Get("Cache-Control") != "public, max-age=300" {
			t.Errorf("GET %s: %d %v", name, rec.Code, rec.Header())
		}
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/games/x/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing asset: %d", rec.Code)
	}
}

func TestDecodeJSON(t *testing.T) {
	type req struct {
		Token string `json:"token"`
	}
	for body, ok := range map[string]bool{
		`{"token":"x"}`:           true,
		`{"token":"x","score":1}`: false,
		`{"token":"x"}{}`:         false,
		`not json`:                false,
		`{"token":"` + strings.Repeat("a", MaxBodyBytes) + `"}`: false,
	} {
		var dst req
		r := httptest.NewRequest(http.MethodPost, "/", io.NopCloser(bytes.NewBufferString(body)))
		err := DecodeJSON(httptest.NewRecorder(), r, &dst)
		if (err == nil) != ok {
			t.Errorf("DecodeJSON(%.40q) err = %v, want ok=%v", body, err, ok)
		}
	}
}

func TestLimiter_SlidingWindow(t *testing.T) {
	l := &Limiter{Per: 3, Window: time.Minute}
	t0 := time.Unix(1_800_000_000, 0)
	for i := range 3 {
		if !l.Allow(1, t0.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("request %d refused", i)
		}
	}
	if l.Allow(1, t0.Add(10*time.Second)) {
		t.Fatal("fourth request in the window allowed")
	}
	if !l.Allow(2, t0.Add(10*time.Second)) {
		t.Fatal("another key is limited")
	}
	// The first request leaves the window after a minute.
	if !l.Allow(1, t0.Add(time.Minute+time.Second/2)) {
		t.Fatal("window did not slide")
	}
	if l.Allow(1, t0.Add(time.Minute+time.Second/2)) {
		t.Fatal("window slid too far")
	}
	// Idle keys are pruned.
	l.Allow(3, t0.Add(10*time.Minute))
	l.mu.Lock()
	n := len(l.hits)
	l.mu.Unlock()
	if n != 1 {
		t.Fatalf("idle keys kept: %d", n)
	}
}

func TestReportScore_ChatInlineAndNotModified(t *testing.T) {
	rb := testutil.NewRecordingBot(t)
	ctx := context.Background()
	if err := ReportScore(ctx, rb.Bot, Address{ChatID: -1005, MessageID: 8}, 42, 5); err != nil {
		t.Fatalf("ReportScore: %v", err)
	}
	f := rb.LastSent().Form
	if rb.LastSent().Method != "setGameScore" || f["user_id"] != "42" || f["score"] != "5" || f["chat_id"] != "-1005" || f["message_id"] != "8" || f["force"] != "" {
		t.Fatalf("setGameScore form = %v", f)
	}
	rb.FailMethodCode("setGameScore", 400, "Bad Request: BOT_SCORE_NOT_MODIFIED")
	err := ReportScore(ctx, rb.Bot, Address{ChatID: -1005, MessageID: 8}, 42, 1)
	if !ScoreNotModified(err) {
		t.Fatalf("not-modified err = %v", err)
	}
	if ScoreNotModified(nil) || ScoreNotModified(errors.New("other")) {
		t.Fatal("ScoreNotModified matched a non-match")
	}
	// An inline address takes the raw call, which goes to the live API; with
	// a cancelled context it fails before leaving the process.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := ReportScore(cctx, rb.Bot, Address{InlineID: "BAAA"}, 42, 5); err == nil {
		t.Fatal("inline report with a cancelled context succeeded")
	}
	if rb.LastSent().Form["inline_message_id"] != "" {
		t.Fatal("inline report went through the library")
	}
}
