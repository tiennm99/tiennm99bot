package noitu

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/dict"
)

func TestAPI_PlayToBotStuckWinsAndReportsOnce(t *testing.T) {
	h := newHarness(t, testCorpus)
	v := h.start(42, "")
	if v.Status != "playing" || v.Difficulty != "medium" || v.Current != "hồng" || v.Player != "Tí" {
		t.Fatalf("start view = %+v", v)
	}
	if len(v.Chain) != 1 || v.Chain[0].Word != "hoa hồng" || v.Chain[0].By != "bot" || len(v.Chain[0].Meanings) != 1 {
		t.Fatalf("opening chain = %+v", v.Chain)
	}
	if v.DeadlineMS-v.ServerNowMS != turnLimit.Milliseconds() || v.TurnLimitMS != 30000 {
		t.Fatalf("deadline %d now %d limit %d", v.DeadlineMS, v.ServerNowMS, v.TurnLimitMS)
	}

	r := h.move(v.Session, "Hồng Tâm")
	if !r.Result.Accepted || r.Result.PlayerWord != "hồng tâm" || r.Result.BotWord != "tâm sự" {
		t.Fatalf("move 1 = %+v", r.Result)
	}
	if r.State.Current != "sự" || r.State.Status != "playing" {
		t.Fatalf("after move 1: %+v", r.State)
	}
	r = h.move(v.Session, "sự cố")
	if !r.Result.Accepted || r.Result.BotWord != "" || r.State.Status != "won" || r.State.EndReason != "bot_stuck" {
		t.Fatalf("move 2 = %+v / %+v", r.Result, r.State)
	}
	if r.State.DeadlineMS != 0 {
		t.Errorf("finished game still has a deadline: %d", r.State.DeadlineMS)
	}

	sum := 0
	for _, e := range r.State.Chain {
		if e.By == "player" {
			sum += e.Points
		}
	}
	if sum == 0 || r.State.Score != sum {
		t.Fatalf("score %d, want the sum of player points %d", r.State.Score, sum)
	}

	final := h.waitReport(v.Session)
	if final.ScoreReported != reportOK {
		t.Fatalf("score_reported = %q, want ok", final.ScoreReported)
	}
	calls := h.reporter.snapshot()
	if len(calls) != 1 || calls[0].score != sum || calls[0].claims.ChatID != 42 || calls[0].claims.MessageID != 5 {
		t.Fatalf("reporter calls = %+v", calls)
	}
	// Further calls never report again.
	h.call("give-up", map[string]string{"session": v.Session}, http.StatusOK, nil)
	h.state(v.Session)
	if n := len(h.reporter.snapshot()); n != 1 {
		t.Fatalf("reporter called %d times, want once", n)
	}
	rec := h.post("move", fmt.Sprintf(`{"session":%q,"word":"cố gắng"}`, v.Session))
	if rec.Code != http.StatusConflict || h.apiError(rec) != "game_over" {
		t.Fatalf("move after end: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAPI_RejectReasonsKeepTheTurn(t *testing.T) {
	h := newHarness(t, testCorpus)
	v := h.start(42, "easy")
	cases := []struct{ word, reason string }{
		{"hồng", "too_few_syllables"},
		{"hồng xyz", "not_in_dictionary"},
		{"tâm sự", "wrong_link"},
	}
	for _, c := range cases {
		r := h.move(v.Session, c.word)
		if r.Result.Accepted || r.Result.Reason != c.reason || r.Result.Message == "" {
			t.Errorf("%q: result %+v, want %s", c.word, r.Result, c.reason)
		}
		if r.State.DeadlineMS != v.DeadlineMS || r.State.Current != "hồng" || len(r.State.Chain) != 1 {
			t.Errorf("%q: a rejected move changed the turn: %+v", c.word, r.State)
		}
	}
	if r := h.move(v.Session, "tâm sự"); !strings.Contains(r.Result.Message, "“hồng”") {
		t.Errorf("wrong_link message %q does not name the syllable", r.Result.Message)
	}
	r := h.move(v.Session, "hồng hào") // bot answers "hào hoa"
	if r.Result.BotWord != "hào hoa" {
		t.Fatalf("bot reply = %q", r.Result.BotWord)
	}
	r = h.move(v.Session, "hoa hồng")
	if r.Result.Accepted || r.Result.Reason != "already_used" {
		t.Fatalf("reused opening: %+v", r.Result)
	}
	h.clock.advance(time.Second)
	rec := h.post("move", fmt.Sprintf(`{"session":%q,"word":%q}`, v.Session, strings.Repeat("a ", 9)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("over-long word: status %d", rec.Code)
	}
}

func TestAPI_TimeoutAfterGraceEndsTheGame(t *testing.T) {
	h := newHarness(t, testCorpus)
	v := h.start(42, "")
	// Still inside the grace window: accepted, but without speed points.
	h.clock.advance(turnLimit + turnGrace - time.Second)
	var r moveResponse
	h.call("move", map[string]string{"session": v.Session, "word": "hồng hào"}, http.StatusOK, &r)
	if !r.Result.Accepted {
		t.Fatalf("move in grace rejected: %+v", r.Result)
	}
	h.clock.advance(turnLimit + turnGrace + time.Millisecond)
	h.call("move", map[string]string{"session": v.Session, "word": "hoa lá"}, http.StatusOK, &r)
	if r.Result.Accepted || r.Result.Reason != "timeout" || r.State.Status != "lost" || r.State.EndReason != "timeout" {
		t.Fatalf("late move = %+v / %+v", r.Result, r.State)
	}
	if fmt.Sprint(r.State.Suggestions) != "[hoa lá]" {
		t.Fatalf("suggestions = %v", r.State.Suggestions)
	}
	if got := h.waitReport(v.Session); got.ScoreReported != reportOK {
		t.Fatalf("score_reported = %q", got.ScoreReported)
	}
}

func TestAPI_StateSettlesTimeoutAndSweepReports(t *testing.T) {
	h := newHarness(t, testCorpus)
	v := h.start(42, "")
	h.clock.advance(turnLimit + turnGrace + time.Second)
	got := h.state(v.Session)
	if got.Status != "lost" || got.EndReason != "timeout" || got.ScoreReported != reportSkipped {
		t.Fatalf("state after deadline = %+v", got)
	}

	// A player who scored and then closed the page is settled by the sweep.
	v2 := h.start(43, "")
	h.move(v2.Session, "hồng hào")
	h.clock.advance(time.Minute)
	if err := h.svc.sweepCron(t.Context(), modules.Deps{}); err != nil {
		t.Fatal(err)
	}
	if h.waitReport(v2.Session).ScoreReported != reportOK {
		t.Fatal("sweep did not report the abandoned game")
	}
	h.clock.advance(finishedTTL + time.Second)
	h.svc.sweep(h.clock.now())
	rec := h.post("state", fmt.Sprintf(`{"session":%q}`, v2.Session))
	if rec.Code != http.StatusNotFound || h.apiError(rec) != "no_session" {
		t.Fatalf("expired session: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAPI_GiveUpWithoutPointsSkipsReport(t *testing.T) {
	h := newHarness(t, testCorpus)
	v := h.start(42, "hard")
	var got sessionView
	h.call("give-up", map[string]string{"session": v.Session}, http.StatusOK, &got)
	if got.Status != "lost" || got.EndReason != "gave_up" || got.ScoreReported != reportSkipped {
		t.Fatalf("give up = %+v", got)
	}
	if len(got.Suggestions) != 2 {
		t.Fatalf("suggestions = %v", got.Suggestions)
	}
	if len(h.reporter.snapshot()) != 0 {
		t.Fatal("zero score was reported")
	}
}

func TestAPI_ReportOutcomes(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("Bad Request: BOT_SCORE_NOT_MODIFIED"), reportOK},
		{fmt.Errorf("Forbidden"), reportFailed},
	} {
		h := newHarness(t, testCorpus)
		h.reporter.err = c.err
		v := h.start(42, "")
		h.move(v.Session, "hồng hào")
		h.call("give-up", map[string]string{"session": v.Session}, http.StatusOK, nil)
		if got := h.waitReport(v.Session).ScoreReported; got != c.want {
			t.Errorf("reporter err %v: score_reported %q, want %q", c.err, got, c.want)
		}
	}
}

func TestAPI_InlineTokenReportsInlineAddress(t *testing.T) {
	h := newHarness(t, testCorpus)
	tok, _ := signToken(testKey, claims{UserID: 9, InlineID: "BAAAAInline", Expiry: h.clock.now().Add(time.Hour).Unix()})
	var v sessionView
	h.call("start", map[string]string{"token": tok}, http.StatusOK, &v)
	h.move(v.Session, "hồng hào")
	h.call("give-up", map[string]string{"session": v.Session}, http.StatusOK, nil)
	h.waitReport(v.Session)
	calls := h.reporter.snapshot()
	if len(calls) != 1 || calls[0].claims.InlineID != "BAAAAInline" || calls[0].claims.ChatID != 0 {
		t.Fatalf("reporter calls = %+v", calls)
	}
}

func TestAPI_TokenErrors(t *testing.T) {
	h := newHarness(t, testCorpus)
	tok := h.token(42)
	rec := h.post("start", fmt.Sprintf(`{"token":%q}`, tok[:len(tok)-2]+"xx"))
	if rec.Code != http.StatusUnauthorized || h.apiError(rec) != "bad_token" {
		t.Fatalf("tampered token: %d %s", rec.Code, rec.Body.String())
	}
	h.clock.advance(tokenTTL)
	rec = h.post("start", fmt.Sprintf(`{"token":%q}`, tok))
	if rec.Code != http.StatusUnauthorized || h.apiError(rec) != "token_expired" {
		t.Fatalf("expired token: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAPI_RequestLimits(t *testing.T) {
	h := newHarness(t, testCorpus)
	big := `{"token":"` + strings.Repeat("a", maxBodyBytes) + `"}`
	if rec := h.post("start", big); rec.Code != http.StatusBadRequest || h.apiError(rec) != "bad_request" {
		t.Fatalf("oversized body: %d", rec.Code)
	}
	if rec := h.post("start", `{"token":"x","score":1000}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	if rec := h.post("start", `{"token":"x"}{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("trailing data: %d", rec.Code)
	}
	tok := h.token(42)
	if rec := h.post("start", fmt.Sprintf(`{"token":%q,"difficulty":"insane"}`, tok)); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown difficulty: %d", rec.Code)
	}
	if rec := h.post("state", `{"session":"nope"}`); rec.Code != http.StatusNotFound || h.apiError(rec) != "no_session" {
		t.Fatalf("unknown session: %d", rec.Code)
	}

	v := h.start(42, "")
	h.move(v.Session, "hồng hào")
	rec := h.post("move", fmt.Sprintf(`{"session":%q,"word":"hoa lá"}`, v.Session))
	if rec.Code != http.StatusTooManyRequests || h.apiError(rec) != "too_fast" {
		t.Fatalf("fast move: %d %s", rec.Code, rec.Body.String())
	}

	// Ten starts a minute per user, counting the one above.
	for i := 0; i < startsPerMinute-1; i++ {
		h.start(42, "")
	}
	rec = h.post("start", fmt.Sprintf(`{"token":%q}`, h.token(42)))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("start flood: %d", rec.Code)
	}
	h.clock.advance(time.Minute)
	h.start(42, "")
}

func TestAPI_SessionCap(t *testing.T) {
	h := newHarness(t, testCorpus)
	for user := int64(1); user <= maxSessions; user++ {
		h.start(user, "easy")
	}
	rec := h.post("start", fmt.Sprintf(`{"token":%q}`, h.token(maxSessions+1)))
	if rec.Code != http.StatusServiceUnavailable || h.apiError(rec) != "busy" {
		t.Fatalf("over cap: %d %s", rec.Code, rec.Body.String())
	}
	// Restarting replaces the player's own session instead of adding one.
	h.start(1, "easy")
}

func TestAPI_NewStartReplacesOwnSession(t *testing.T) {
	h := newHarness(t, testCorpus)
	first := h.start(42, "")
	h.start(42, "")
	if rec := h.post("state", fmt.Sprintf(`{"session":%q}`, first.Session)); rec.Code != http.StatusNotFound {
		t.Fatalf("replaced session still answers: %d", rec.Code)
	}
}

// A player who closes the page mid-game and starts again on the same message
// still gets the abandoned game's score reported.
func TestAPI_RestartMidGameReportsTheReplacedScore(t *testing.T) {
	h := newHarness(t, testCorpus)
	first := h.start(42, "")
	r := h.move(first.Session, "hồng hào")
	if !r.Result.Accepted || r.State.Score <= 0 {
		t.Fatalf("scoring move = %+v / score %d", r.Result, r.State.Score)
	}
	h.start(42, "")
	calls := waitCalls(t, h.reporter, 1)
	if len(calls) != 1 || calls[0].score != r.State.Score || calls[0].claims.MessageID != 5 {
		t.Fatalf("reporter calls = %+v, want one with score %d", calls, r.State.Score)
	}
}

// Each user holds at most maxUserSessions live sessions; a further start on
// another message settles and drops the oldest.
func TestAPI_PerUserSessionCap(t *testing.T) {
	h := newHarness(t, testCorpus)
	var ids []string
	for msg := 1; msg <= maxUserSessions; msg++ {
		var v sessionView
		h.call("start", map[string]string{"token": h.tokenFor(42, msg)}, http.StatusOK, &v)
		ids = append(ids, v.Session)
	}
	scored := h.move(ids[0], "hồng hào").State.Score
	var v sessionView
	h.call("start", map[string]string{"token": h.tokenFor(42, maxUserSessions+1)}, http.StatusOK, &v)

	if rec := h.post("state", fmt.Sprintf(`{"session":%q}`, ids[0])); rec.Code != http.StatusNotFound {
		t.Fatalf("oldest session still answers: %d", rec.Code)
	}
	for _, id := range append(ids[1:], v.Session) {
		h.state(id)
	}
	calls := waitCalls(t, h.reporter, 1)
	if len(calls) != 1 || calls[0].score != scored || calls[0].claims.MessageID != 1 {
		t.Fatalf("reporter calls = %+v, want the evicted game's %d", calls, scored)
	}
	// Another user is unaffected.
	h.start(43, "")
}

func waitCalls(t *testing.T, r *fakeReporter, n int) []reportCall {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		calls := r.snapshot()
		if len(calls) >= n || time.Now().After(deadline) {
			return calls
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// cycleCorpus is a three-word cycle: after one exchange the human must answer
// the opening's first syllable, whose only word is the used opening.
const cycleCorpus = "hoa hồng\t\nhồng hào\t\nhào hoa\t\n"

// deadEnd starts a game for user and plays into the dead end.
func (h *harness) deadEnd(user int64) sessionView {
	h.t.Helper()
	v := h.start(user, "easy")
	sess := h.svc.sessions.get(v.Session)
	sess.mu.Lock()
	reply := sess.eng.LegalMoves()[0]
	sess.mu.Unlock()
	r := h.move(v.Session, reply)
	if !r.Result.Accepted || r.Result.BotWord == "" || r.State.Status != "playing" {
		h.t.Fatalf("exchange = %+v / %+v", r.Result, r.State)
	}
	return r.State
}

func TestAPI_DeadEndEndsAsNoLegalMove(t *testing.T) {
	h := newHarness(t, cycleCorpus)
	v := h.deadEnd(42)
	var got sessionView
	h.call("give-up", map[string]string{"session": v.Session}, http.StatusOK, &got)
	if got.Status != "lost" || got.EndReason != "no_legal_move" || len(got.Suggestions) != 0 {
		t.Fatalf("give up in a dead end = %+v", got)
	}

	v = h.deadEnd(43)
	h.clock.advance(turnLimit + turnGrace + time.Second)
	if got := h.state(v.Session); got.Status != "lost" || got.EndReason != "no_legal_move" {
		t.Fatalf("dead end on the clock = %+v", got)
	}
}

func TestAPI_HeadersAndRoutes(t *testing.T) {
	h := newHarness(t, testCorpus)
	rec := h.post("start", `{}`)
	hdr := rec.Header()
	if !strings.Contains(hdr.Get("Content-Security-Policy"), "default-src 'self'") ||
		hdr.Get("Referrer-Policy") != "no-referrer" ||
		hdr.Get("Cache-Control") != "no-store" ||
		hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("API headers = %v", hdr)
	}
	if hdr.Get("X-Frame-Options") != "" || strings.Contains(hdr.Get("Content-Security-Policy"), "frame-ancestors") {
		t.Fatal("framing restricted; Telegram Web embeds games in an iframe")
	}

	for path, ctype := range map[string]string{"": "text/html", "app.js": "text/javascript", "app.css": "text/css"} {
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, routePrefix+path+"?t=abc", nil))
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), ctype) || rec.Body.Len() == 0 {
			t.Errorf("GET %q: %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
		if rec.Header().Get("Cache-Control") != "public, max-age=300" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("GET %q headers = %v", path, rec.Header())
		}
	}
	for _, path := range []string{"web.go", "secret", "api/start"} {
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, routePrefix+path, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("GET %q served", path)
		}
	}
}

// One game against the real corpus, playing a legal word each turn, proves
// the embedded dictionary, the engine and the opponent fit together.
func TestAPI_RealCorpusGame(t *testing.T) {
	store, err := dict.Default()
	if err != nil {
		t.Fatal(err)
	}
	h := newHarnessWithStore(t, store)
	v := h.start(42, "hard")
	if len(v.Chain) != 1 || v.Current == "" {
		t.Fatalf("start view = %+v", v)
	}
	accepted, replies := 0, 0
	for range 5 {
		sess := h.svc.sessions.get(v.Session)
		sess.mu.Lock()
		legal := sess.eng.LegalMoves()
		sess.mu.Unlock()
		if len(legal) == 0 {
			break
		}
		r := h.move(v.Session, legal[0])
		if !r.Result.Accepted {
			t.Fatalf("legal word %q rejected: %+v", legal[0], r.Result)
		}
		accepted++
		if r.State.Status != "playing" {
			break
		}
		if r.Result.BotWord == "" || r.State.Current == "" {
			t.Fatalf("game still playing without a bot reply: %+v / %+v", r.Result, r.State)
		}
		replies++
		v = r.State
	}
	if accepted == 0 || replies == 0 {
		t.Fatalf("accepted %d words with %d bot replies; the real corpus must sustain a game", accepted, replies)
	}
}
