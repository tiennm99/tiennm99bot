package noitu

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/dict"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

const testBase = "https://game.example"

// testCorpus is a tiny word graph. The opening falls back to the best
// connected word, "hoa hồng":
//
//	hồng → hồng hào → hào hoa → hoa lá (dead) | hoa hồng (opening)
//	hồng → hồng tâm → tâm sự  → sự cố (dead)
const testCorpus = "hoa hồng\tdanh từ|loài hoa\nhồng hào\t\nhồng tâm\t\ntâm sự\t\nsự cố\t\nhào hoa\t\nhoa lá\t\n"

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type reportCall struct {
	claims claims
	score  int
}

type fakeReporter struct {
	mu    sync.Mutex
	calls []reportCall
	err   error
}

func (r *fakeReporter) Report(_ context.Context, c claims, score int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, reportCall{c, score})
	return r.err
}

func (r *fakeReporter) snapshot() []reportCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]reportCall(nil), r.calls...)
}

type harness struct {
	t        *testing.T
	svc      *service
	mod      modules.Module
	clock    *fakeClock
	reporter *fakeReporter
	handler  http.Handler
}

func newHarness(t *testing.T, corpus string) *harness {
	t.Helper()
	store, err := dict.Parse(corpus)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return newHarnessWithStore(t, store)
}

func newHarnessWithStore(t *testing.T, store *dict.Store) *harness {
	t.Helper()
	h := &harness{t: t, clock: &fakeClock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}, reporter: &fakeReporter{}}
	seed := uint64(0)
	cfg := config{
		baseURL:  testBase,
		key:      testKey,
		now:      h.clock.now,
		newRNG:   func() *rand.Rand { seed++; return rand.New(rand.NewPCG(seed, 42)) },
		reporter: h.reporter,
		loadDict: func() (*dict.Store, error) { return store, nil },
	}
	h.svc = newService(cfg)
	h.mod = h.svc.module()
	if len(h.mod.HTTP) != 1 {
		t.Fatalf("enabled module has %d routes, want 1", len(h.mod.HTTP))
	}
	h.handler = h.mod.HTTP[0].Handler
	return h
}

// token signs a chat-message token for user valid from the harness clock.
func (h *harness) token(user int64) string {
	h.t.Helper()
	return h.tokenFor(user, 5)
}

// tokenFor signs a token for user on game message msg in the user's chat.
func (h *harness) tokenFor(user int64, msg int) string {
	h.t.Helper()
	tok, err := signToken(testKey, claims{UserID: user, Name: "Tí", ChatID: user, MessageID: msg, Expiry: h.clock.now().Add(tokenTTL).Unix()})
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

// post sends a raw body and returns the recorder.
func (h *harness) post(path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, routePrefix+"api/"+path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// call posts v and decodes the answer into out, failing on an unexpected status.
func (h *harness) call(path string, v any, wantStatus int, out any) {
	h.t.Helper()
	body, _ := json.Marshal(v)
	rec := h.post(path, string(body))
	if rec.Code != wantStatus {
		h.t.Fatalf("%s: status %d, want %d; body %s", path, rec.Code, wantStatus, rec.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			h.t.Fatalf("%s: decode %q: %v", path, rec.Body.String(), err)
		}
	}
}

func (h *harness) start(user int64, difficulty string) sessionView {
	h.t.Helper()
	var v sessionView
	h.call("start", map[string]string{"token": h.token(user), "difficulty": difficulty}, http.StatusOK, &v)
	return v
}

// move advances the clock past the per-move rate limit, then plays word.
func (h *harness) move(session, word string) moveResponse {
	h.t.Helper()
	h.clock.advance(time.Second)
	var r moveResponse
	h.call("move", map[string]string{"session": session, "word": word}, http.StatusOK, &r)
	return r
}

func (h *harness) state(session string) sessionView {
	h.t.Helper()
	var v sessionView
	h.call("state", map[string]string{"session": session}, http.StatusOK, &v)
	return v
}

// waitReport polls the state until the asynchronous score report settles.
func (h *harness) waitReport(session string) sessionView {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		v := h.state(session)
		if v.ScoreReported != reportPending || time.Now().After(deadline) {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) apiError(rec *httptest.ResponseRecorder) string {
	h.t.Helper()
	var e struct {
		Code    string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		h.t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	if e.Message == "" {
		h.t.Errorf("error %q has no message", e.Code)
	}
	return e.Code
}
