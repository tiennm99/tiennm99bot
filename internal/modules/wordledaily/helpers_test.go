package wordledaily

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
	"github.com/tiennm99/tiennm99bot/internal/modules/wordle/wordlist"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

var (
	testRoot          = []byte("0123456789abcdef0123456789abcdef")
	testAnswers       = wordlist.Answers()
	_, testDict       = wordlist.Load()
	testStart         = guessgame.Epoch.Add(12 * time.Hour) // 19:00 ICT on puzzle #1's day
	testBase          = "https://game.example"
	editStub          = `{"message_id":1,"date":0,"chat":{"id":-100,"type":"supergroup"}}`
	groupChat   int64 = -100
)

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
	addr   htmlgame.Address
	user   int64
	score  int
	result error
}

type fakeReporter struct {
	mu    sync.Mutex
	calls []reportCall
	err   error
}

func (r *fakeReporter) Report(_ context.Context, a htmlgame.Address, userID int64, score int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, reportCall{a, userID, score, r.err})
	return r.err
}

func (r *fakeReporter) snapshot() []reportCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]reportCall(nil), r.calls...)
}

// fakeScheduler holds summary flushes until the test runs them.
type fakeScheduler struct {
	mu     sync.Mutex
	fns    []func()
	delays []time.Duration
}

func (f *fakeScheduler) schedule(d time.Duration, fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fns = append(f.fns, fn)
	f.delays = append(f.delays, d)
}

func (f *fakeScheduler) pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fns)
}

// run1 runs the oldest pending flush only.
func (f *fakeScheduler) run1() {
	f.mu.Lock()
	if len(f.fns) == 0 {
		f.mu.Unlock()
		return
	}
	fn := f.fns[0]
	f.fns, f.delays = f.fns[1:], f.delays[1:]
	f.mu.Unlock()
	fn()
}

// run runs every pending flush, including ones they schedule.
func (f *fakeScheduler) run() {
	for {
		f.mu.Lock()
		if len(f.fns) == 0 {
			f.mu.Unlock()
			return
		}
		fn := f.fns[0]
		f.fns, f.delays = f.fns[1:], f.delays[1:]
		f.mu.Unlock()
		fn()
	}
}

type harness struct {
	t       *testing.T
	svc     *service
	clock   *fakeClock
	rep     *fakeReporter
	rb      *testutil.RecordingBot
	sched   *fakeScheduler
	coll    storage.Collection
	handler http.Handler
}

func testConfig(clock *fakeClock, coll storage.Collection, rep guessgame.ScoreReporter, api guessgame.TelegramAPI, sched *fakeScheduler) config {
	return config{
		baseURL:  testBase,
		rootKey:  testRoot,
		now:      clock.now,
		store:    coll,
		reporter: rep,
		api:      api,
		schedule: sched.schedule,
		async:    func(f func()) { f() },
		answers:  testAnswers,
		dict:     testDict,
		pick:     testPick,
	}
}

// testPick makes unlimited rounds deterministic: crane, then slate, then
// crane again.
func testPick(prev string) string {
	if prev == "crane" {
		return "slate"
	}
	return "crane"
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t:     t,
		clock: &fakeClock{t: testStart},
		rep:   &fakeReporter{},
		rb:    testutil.NewRecordingBot(t),
		sched: &fakeScheduler{},
		coll:  storage.NewMemoryProvider().Collection(ShortName),
	}
	h.rb.StubMethod("editMessageText", editStub)
	h.svc = newService(testConfig(h.clock, h.coll, h.rep, h.rb.Bot, h.sched))
	mod := h.svc.module()
	if len(mod.HTTP) != 1 || len(mod.Crons) != 2 {
		t.Fatalf("enabled module: routes %d crons %d", len(mod.HTTP), len(mod.Crons))
	}
	h.handler = mod.HTTP[0].Handler
	return h
}

// restart builds a fresh service over the same store, as after a deploy.
func (h *harness) restart() {
	h.svc = newService(testConfig(h.clock, h.coll, h.rep, h.rb.Bot, h.sched))
	h.handler = h.svc.handler()
}

// tokenFor signs a token for user on card msg in chat (thread is its topic).
func (h *harness) tokenFor(user, chat int64, msg, thread int, name string) string {
	h.t.Helper()
	tok, err := h.svc.signToken(guessgame.Claims{UserID: user, Name: name, ChatID: chat, MessageID: msg, ThreadID: thread, Expiry: h.clock.now().Add(guessgame.TokenTTL).Unix()})
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

// dmToken is a token for user's card in their private chat.
func (h *harness) dmToken(user int64) string { return h.tokenFor(user, user, 5, 0, "Alice") }

func (h *harness) answer() string {
	h.t.Helper()
	a, err := h.svc.ResolvePuzzle(context.Background(), h.svc.PuzzleNum(h.clock.now()))
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

// wrong returns n distinct dictionary words that are not today's answer.
func (h *harness) wrong(n int) []string {
	ans := h.answer()
	var out []string
	for _, w := range testAnswers {
		if w != ans && len(out) < n {
			out = append(out, w)
		}
	}
	return out
}

func (h *harness) post(path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, routePrefix+"api/"+path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func (h *harness) call(path string, body any, wantStatus int) view {
	h.t.Helper()
	raw, _ := json.Marshal(body)
	rec := h.post(path, string(raw))
	if rec.Code != wantStatus {
		h.t.Fatalf("%s: status %d, want %d; body %s", path, rec.Code, wantStatus, rec.Body.String())
	}
	var v view
	if wantStatus == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			h.t.Fatalf("%s: decode %q: %v", path, rec.Body.String(), err)
		}
	}
	return v
}

func (h *harness) state(tok string) view {
	h.t.Helper()
	return h.call("state", map[string]string{"token": tok}, http.StatusOK)
}

func (h *harness) guess(tok string, num int, word string) view {
	h.t.Helper()
	return h.call("guess", map[string]any{"token": tok, "num": num, "word": word}, http.StatusOK)
}

// guessErr posts a guess expected to fail and returns its error code.
func (h *harness) guessErr(tok string, num int, word string, wantStatus int) string {
	h.t.Helper()
	raw, _ := json.Marshal(map[string]any{"token": tok, "num": num, "word": word})
	rec := h.post("guess", string(raw))
	if rec.Code != wantStatus {
		h.t.Fatalf("guess %q: status %d, want %d; body %s", word, rec.Code, wantStatus, rec.Body.String())
	}
	return errorCode(h.t, rec)
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Code    string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	if e.Message == "" {
		t.Errorf("error %q has no message", e.Code)
	}
	return e.Code
}

// sentMethod returns the recorded calls of one API method.
func sentMethod(rb *testutil.RecordingBot, method string) []testutil.SentCall {
	var out []testutil.SentCall
	for _, c := range rb.Sent() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}
