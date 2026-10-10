package loldle

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

var (
	testRoot        = []byte("0123456789abcdef0123456789abcdef")
	testStart       = guessgame.Epoch.Add(12 * time.Hour) // 19:00 ICT on puzzle #1's day
	testBase        = "https://game.example"
	editStub        = `{"message_id":1,"date":0,"chat":{"id":-100,"type":"supergroup"}}`
	groupChat int64 = -100
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
	addr  htmlgame.Address
	user  int64
	score int
}

type fakeReporter struct {
	mu    sync.Mutex
	calls []reportCall
}

func (r *fakeReporter) Report(_ context.Context, a htmlgame.Address, userID int64, score int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, reportCall{a, userID, score})
	return nil
}

func (r *fakeReporter) snapshot() []reportCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]reportCall(nil), r.calls...)
}

// fakeScheduler holds summary flushes until the test runs them.
type fakeScheduler struct {
	mu  sync.Mutex
	fns []func()
}

func (f *fakeScheduler) schedule(_ time.Duration, fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fns = append(f.fns, fn)
}

func (f *fakeScheduler) run() {
	for {
		f.mu.Lock()
		if len(f.fns) == 0 {
			f.mu.Unlock()
			return
		}
		fn := f.fns[0]
		f.fns = f.fns[1:]
		f.mu.Unlock()
		fn()
	}
}

// testPick makes unlimited rounds deterministic: Ahri, then Jinx, then Ahri
// again.
func testPick(prev string) string {
	if prev == "Ahri" {
		return "Jinx"
	}
	return "Ahri"
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
		baseURL:   testBase,
		rootKey:   testRoot,
		now:       clock.now,
		store:     coll,
		reporter:  rep,
		api:       api,
		schedule:  sched.schedule,
		async:     func(f func()) { f() },
		champions: loadChampions(),
		pick:      testPick,
	}
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

// pinDaily makes champion the answer of daily puzzle num.
func (h *harness) pinDaily(num int, champion string) {
	h.t.Helper()
	if err := h.svc.Puzzles.Put(context.Background(), guessgame.PuzzleKey(num), guessgame.PuzzleDoc{Num: num, Answer: champion}); err != nil {
		h.t.Fatal(err)
	}
}

// token signs a token for user on card msg in chat; unlimited marks a
// recorded /loldle card.
func (h *harness) token(user, chat int64, msg int, name string, unlimited bool) string {
	h.t.Helper()
	c := guessgame.Claims{UserID: user, Name: name, ChatID: chat, MessageID: msg, Expiry: h.clock.now().Add(guessgame.TokenTTL).Unix()}
	if unlimited {
		c.Mode = guessgame.ModeUnlimited
	}
	tok, err := h.svc.signToken(c)
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

type result struct {
	code int
	body string
	v    view
	err  string
}

func (h *harness) post(path string, body any) result {
	h.t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, routePrefix+"api/"+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	out, _ := io.ReadAll(rec.Result().Body)
	r := result{code: rec.Code, body: string(out)}
	if r.code == http.StatusOK {
		if err := json.Unmarshal(out, &r.v); err != nil {
			h.t.Fatalf("decode %q: %v", out, err)
		}
	} else {
		var e struct {
			Code string `json:"error"`
		}
		_ = json.Unmarshal(out, &e)
		r.err = e.Code
	}
	return r
}

func (h *harness) state(tok string) result { return h.post("state", map[string]any{"token": tok}) }

func (h *harness) dailyGuess(tok string, num int, name string) result {
	return h.post("guess", map[string]any{"token": tok, "num": num, "name": name})
}

func (h *harness) roundGuess(tok string, seq int, name string) result {
	return h.post("guess", map[string]any{"token": tok, "seq": seq, "name": name})
}

// install registers mod through the real registry and dispatcher, with
// owner as the bot owner.
func install(t *testing.T, mod modules.Module, owner int64) *testutil.RecordingBot {
	t.Helper()
	rb := testutil.NewRecordingBot(t)
	reg, err := modules.Build([]string{ShortName}, map[string]modules.Factory{
		ShortName: func(modules.Deps) modules.Module { return mod },
	}, storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	modules.Install(rb.Bot, reg, modules.Auth{BotOwnerID: owner})
	return rb
}

// send runs one command from user in chat (a group when chat < 0) and
// returns the bot's last text reply.
func send(t *testing.T, rb *testutil.RecordingBot, chat, user int64, text string) string {
	t.Helper()
	rb.Reset()
	var u *models.Update
	if chat < 0 {
		u = testutil.NewGroupMessage(chat, user, text)
	} else {
		u = testutil.NewPrivateMessage(user, text)
	}
	rb.Bot.ProcessUpdate(context.Background(), u)
	sent := rb.Sent()
	for i := len(sent) - 1; i >= 0; i-- {
		if sent[i].Method == "sendMessage" {
			return sent[i].Text()
		}
	}
	return ""
}

func sentMethod(rb *testutil.RecordingBot, method string) []testutil.SentCall {
	var out []testutil.SentCall
	for _, c := range rb.Sent() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}
