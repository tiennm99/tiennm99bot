package wordledaily

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

func TestAPI_WinUpdatesStatsAndHidesTheAnswerUntilTheEnd(t *testing.T) {
	h := newHarness(t)
	tok := h.dmToken(1)
	ans := h.answer()
	v := h.state(tok)
	if v.Num != 1 || v.Date != "2026-10-09" || v.Status != guessgame.StatusPlaying || len(v.Guesses) != 0 || v.Max != 6 || v.Len != 5 || v.Player != "Alice" {
		t.Fatalf("fresh state = %+v", v)
	}
	if v.NextAt != guessgame.Epoch.Add(24*time.Hour).Unix() {
		t.Fatalf("next_at = %d", v.NextAt)
	}
	for _, w := range h.wrong(2) {
		rec := h.post("guess", `{"token":"`+tok+`","num":1,"word":"`+w+`"}`)
		body := strings.ToLower(rec.Body.String())
		if rec.Code != http.StatusOK || strings.Contains(body, `"answer"`) || strings.Contains(body, `"stats"`) || strings.Contains(body, `"share"`) {
			t.Fatalf("mid-game answer leaked or failed: %d %s", rec.Code, body)
		}
	}
	v = h.guess(tok, 1, strings.ToUpper(ans))
	if v.Status != guessgame.StatusWon || v.Answer != strings.ToUpper(ans) || len(v.Guesses) != 3 || v.Guesses[2].Marks != "ccccc" {
		t.Fatalf("win = %+v", v)
	}
	if st := v.Stats; st == nil || st.Played != 1 || st.WinPct != 100 || st.Cur != 1 || st.Max != 1 || st.Dist[2] != 1 {
		t.Fatalf("stats = %+v", v.Stats)
	}
	if !strings.HasPrefix(v.Share, "Wordle Daily #1 3/6\n\n") || !strings.HasSuffix(v.Share, "🟩🟩🟩🟩🟩") || strings.ContainsAny(strings.SplitN(v.Share, "\n\n", 2)[1], "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("share = %q", v.Share)
	}
	// No replay: a finished puzzle refuses another guess and keeps its board.
	if code := h.guessErr(tok, 1, h.wrong(1)[0], http.StatusUnprocessableEntity); code != "finished" {
		t.Fatalf("guess after win = %s", code)
	}
	if v := h.state(tok); v.Status != guessgame.StatusWon || len(v.Guesses) != 3 || v.Answer == "" {
		t.Fatalf("reopened = %+v", v)
	}
}

func TestAPI_LossAfterSixGuesses(t *testing.T) {
	h := newHarness(t)
	tok := h.dmToken(1)
	var v view
	for _, w := range h.wrong(6) {
		v = h.guess(tok, 1, w)
	}
	if v.Status != guessgame.StatusLost || v.Answer != strings.ToUpper(h.answer()) || len(v.Guesses) != 6 {
		t.Fatalf("loss = %+v", v)
	}
	if st := v.Stats; st == nil || st.Played != 1 || st.WinPct != 0 || st.Cur != 0 || st.Dist[5] != 0 {
		t.Fatalf("stats = %+v", v.Stats)
	}
	if !strings.HasPrefix(v.Share, "Wordle Daily #1 X/6") {
		t.Fatalf("share = %q", v.Share)
	}
	if len(h.rep.snapshot()) != 0 {
		t.Fatal("a loss was reported")
	}
}

func TestAPI_RejectedWordsAreNotRecorded(t *testing.T) {
	h := newHarness(t)
	tok := h.dmToken(1)
	for word, code := range map[string]string{"abc": "length", "abcdef": "length", "": "length", "zzzzz": "unknown"} {
		if got := h.guessErr(tok, 1, word, http.StatusUnprocessableEntity); got != code {
			t.Errorf("guess %q = %s, want %s", word, got, code)
		}
	}
	if v := h.state(tok); len(v.Guesses) != 0 {
		t.Fatalf("rejected words recorded: %+v", v.Guesses)
	}
}

func TestAPI_DuplicateLetterMarks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// Pin puzzle #1 to a word with a doubled letter.
	if err := storage.Typed[guessgame.PuzzleDoc](h.coll).Put(ctx, guessgame.PuzzleKey(1), guessgame.PuzzleDoc{Num: 1, Answer: "abbey"}); err != nil {
		t.Fatal(err)
	}
	v := h.guess(h.dmToken(1), 1, "babes")
	if v.Guesses[0].Word != "BABES" || v.Guesses[0].Marks != "ppccw" {
		t.Fatalf("marks = %+v", v.Guesses)
	}
}

func TestAPI_StaleBoardGetsNewPuzzleAfterRollover(t *testing.T) {
	h := newHarness(t)
	tok := h.dmToken(1)
	h.guess(tok, 1, h.wrong(1)[0])
	h.clock.advance(12 * time.Hour) // 07:00 ICT next day
	tok = h.dmToken(1)              // the old link expired meanwhile
	if code := h.guessErr(tok, 1, h.wrong(1)[0], http.StatusConflict); code != "new_puzzle" {
		t.Fatalf("stale guess = %s", code)
	}
	v := h.state(tok)
	if v.Num != 2 || len(v.Guesses) != 0 || v.Status != guessgame.StatusPlaying || v.Date != "2026-10-10" {
		t.Fatalf("new day = %+v", v)
	}
}

// win plays today's answer for user in their private chat.
func (h *harness) win(user int64) view {
	h.t.Helper()
	v := h.state(h.dmToken(user))
	return h.guess(h.dmToken(user), v.Num, h.answer())
}

func TestStats_StreakContinuesBreaksAndDisplaysZero(t *testing.T) {
	h := newHarness(t)
	h.win(1)
	h.clock.advance(24 * time.Hour)
	v := h.win(1)
	if v.Stats.Cur != 2 || v.Stats.Max != 2 || v.Stats.Played != 2 {
		t.Fatalf("day 2 = %+v", v.Stats)
	}
	h.clock.advance(48 * time.Hour) // day 3 missed
	st, err := h.svc.LoadStats(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := guessgame.DisplayStreak(st, h.svc.PuzzleNum(h.clock.now())); got != 0 {
		t.Fatalf("streak after a missed day shows %d", got)
	}
	v = h.win(1)
	if v.Stats.Cur != 1 || v.Stats.Max != 2 || v.Stats.Played != 3 || v.Stats.Dist[0] != 3 {
		t.Fatalf("day 4 = %+v", v.Stats)
	}
	h.clock.advance(24 * time.Hour)
	tok := h.dmToken(1)
	for _, w := range h.wrong(6) {
		v = h.guess(tok, 5, w)
	}
	if v.Stats.Cur != 0 || v.Stats.Max != 2 || v.Stats.Played != 4 || v.Stats.WinPct != 75 {
		t.Fatalf("loss = %+v", v.Stats)
	}
}

// flakyPlays fails the next Put.
type flakyPlays struct {
	storage.DocStore[guessgame.Progress]
	fail bool
}

func (f *flakyPlays) Put(ctx context.Context, id string, v guessgame.Progress) error {
	if f.fail {
		f.fail = false
		return errors.New("store down")
	}
	return f.DocStore.Put(ctx, id, v)
}

func TestFinish_FailedProgressWriteDoesNotDoubleCount(t *testing.T) {
	h := newHarness(t)
	flaky := &flakyPlays{DocStore: h.svc.Plays}
	h.svc.Plays = flaky
	tok := h.dmToken(1)
	h.guess(tok, 1, h.wrong(1)[0])
	flaky.fail = true
	rec := h.post("guess", `{"token":"`+tok+`","num":1,"word":"`+h.answer()+`"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed write = %d", rec.Code)
	}
	v := h.guess(tok, 1, h.answer())
	if v.Status != guessgame.StatusWon || v.Stats.Played != 1 || v.Stats.Dist[1] != 1 {
		t.Fatalf("retried finish = %+v %+v", v, v.Stats)
	}
}

// A finishing guess whose board write failed counts nowhere, so the player
// can play on with other words and the stats follow the board.
func TestFinish_FailedBoardWriteThenOtherWordsKeepsStatsInStep(t *testing.T) {
	h := newHarness(t)
	flaky := &flakyPlays{DocStore: h.svc.Plays}
	h.svc.Plays = flaky
	tok := h.dmToken(1)
	words := h.wrong(6)
	h.guess(tok, 1, words[0])
	flaky.fail = true
	if rec := h.post("guess", `{"token":"`+tok+`","num":1,"word":"`+h.answer()+`"}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed write = %d", rec.Code)
	}
	var v view
	for _, w := range words[1:] {
		v = h.guess(tok, 1, w)
	}
	if v.Status != guessgame.StatusLost || len(v.Guesses) != 6 {
		t.Fatalf("board = %+v", v)
	}
	if st := v.Stats; st.Played != 1 || st.WinPct != 0 || st.Cur != 0 || st.Max != 0 || slices.Max(st.Dist) != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

// flakyStats fails the next Put.
type flakyStats struct {
	storage.DocStore[guessgame.UserStats]
	fail bool
}

func (f *flakyStats) Put(ctx context.Context, id string, v guessgame.UserStats) error {
	if f.fail {
		f.fail = false
		return errors.New("store down")
	}
	return f.DocStore.Put(ctx, id, v)
}

// A board saved as won whose stats write failed is folded into the stats on
// the next load, once, with the group bookkeeping that finishing runs.
func TestFinish_FailedStatsWriteIsRepairedFromTheBoard(t *testing.T) {
	h := newHarness(t)
	flaky := &flakyStats{DocStore: h.svc.Stats}
	h.svc.Stats = flaky
	tok := h.tokenFor(1, groupChat, 7, 0, "Alice")
	h.guess(tok, 1, h.wrong(1)[0])
	flaky.fail = true
	if rec := h.post("guess", `{"token":"`+tok+`","num":1,"word":"`+h.answer()+`"}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed write = %d", rec.Code)
	}
	// The board is finished: another word is refused, and that repairs
	// the stats and the group streak.
	if code := h.guessErr(tok, 1, h.wrong(2)[1], http.StatusUnprocessableEntity); code != "finished" {
		t.Fatalf("guess after finish = %s", code)
	}
	v := h.state(tok)
	if v.Status != guessgame.StatusWon || len(v.Guesses) != 2 {
		t.Fatalf("board = %+v", v)
	}
	if st := v.Stats; st == nil || st.Played != 1 || st.WinPct != 100 || st.Cur != 1 || st.Dist[1] != 1 {
		t.Fatalf("stats = %+v", v.Stats)
	}
	if v = h.state(tok); v.Stats.Played != 1 {
		t.Fatalf("counted twice: %+v", v.Stats)
	}
	if st, _ := h.svc.ChatStreakAt(context.Background(), groupChat, 0); st.Streak != 1 || st.LastNum != 1 {
		t.Fatalf("group streak = %+v", st)
	}
	if calls := h.rep.snapshot(); len(calls) != 1 || calls[0].score != 5 {
		t.Fatalf("reports = %+v", calls)
	}
}

func TestRestart_KeepsTheBoard(t *testing.T) {
	h := newHarness(t)
	tok := h.dmToken(1)
	w := h.wrong(1)[0]
	h.guess(tok, 1, w)
	h.restart()
	v := h.state(tok)
	if len(v.Guesses) != 1 || v.Guesses[0].Word != strings.ToUpper(w) {
		t.Fatalf("after restart = %+v", v)
	}
}

func TestAPI_TokenErrors(t *testing.T) {
	h := newHarness(t)
	// A token signed with noitu's key derivation never verifies here.
	noituKey := htmlgame.DeriveKey(testRoot, "tiennm99bot/noitu/token/v1")
	noituTok, _ := htmlgame.Sign(noituKey, guessgame.Claims{UserID: 1, ChatID: 1, MessageID: 1, Expiry: h.clock.now().Add(time.Hour).Unix()})
	for name, tok := range map[string]string{"noitu key": noituTok, "garbage": "abc.def", "empty": ""} {
		rec := h.post("state", `{"token":"`+tok+`"}`)
		if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "bad_token" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	tok := h.dmToken(1)
	h.clock.advance(guessgame.TokenTTL)
	rec := h.post("state", `{"token":"`+tok+`"}`)
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "expired" {
		t.Fatalf("expired: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAPI_RateLimitAndRequestShape(t *testing.T) {
	h := newHarness(t)
	tok := h.dmToken(1)
	for range requestsPerMinute {
		h.state(tok)
	}
	rec := h.post("state", `{"token":"`+tok+`"}`)
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "rate_limited" {
		t.Fatalf("flood: %d", rec.Code)
	}
	// Another player is not limited, and the window slides.
	h.state(h.dmToken(2))
	h.clock.advance(time.Minute)
	h.state(tok)

	for _, body := range []string{`{"token":"x","score":7}`, `{"token":"x"}{}`, `[]`, `{"token":"x","num":1,"word":"` + strings.Repeat("a", 40) + `"}`} {
		path := "state"
		if strings.Contains(body, "word") {
			path = "guess"
		}
		if rec := h.post(path, body); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "bad_request" {
			t.Errorf("%s %s: %d", path, body, rec.Code)
		}
	}
}
