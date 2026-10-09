package noitu

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules"
)

const (
	cardChat   = -100500
	cardMsg    = 7
	cardThread = 3
)

// pvpToken signs a Play token for user on the test card.
func (h *harness) pvpToken(user int64, name string) string {
	h.t.Helper()
	return h.pvpTokenOn(user, name, cardMsg)
}

func (h *harness) pvpTokenOn(user int64, name string, msg int) string {
	h.t.Helper()
	return h.pvpTokenAt(user, name, cardChat, msg)
}

func (h *harness) pvpTokenAt(user int64, name string, chat int64, msg int) string {
	h.t.Helper()
	tok, err := signToken(testKey, claims{UserID: user, Name: name, ChatID: chat, MessageID: msg, ThreadID: cardThread, PvP: true, Expiry: h.clock.now().Add(tokenTTL).Unix()})
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

func (h *harness) join(user int64, name string) roomView {
	h.t.Helper()
	var v roomView
	h.call("room/join", map[string]string{"token": h.pvpToken(user, name)}, http.StatusOK, &v)
	return v
}

func (h *harness) roomState(member string) roomView {
	h.t.Helper()
	var v roomView
	h.call("room/state", map[string]any{"member": member}, http.StatusOK, &v)
	return v
}

func (h *harness) roomStart(member string, wantStatus int) roomView {
	h.t.Helper()
	var v roomView
	h.call("room/start", map[string]string{"member": member}, wantStatus, &v)
	return v
}

// roomMove advances the clock past the per-member move limit, then plays.
func (h *harness) roomMove(member, word string) roomMoveResponse {
	h.t.Helper()
	h.clock.advance(time.Second)
	var r roomMoveResponse
	h.call("room/move", map[string]string{"member": member, "word": word}, http.StatusOK, &r)
	return r
}

func (h *harness) roomErr(path string, body any) (int, string) {
	h.t.Helper()
	rec := h.postJSON(path, body)
	return rec.Code, h.apiError(rec)
}

// waitRoomReport polls until the finished game's score report settles.
func (h *harness) waitRoomReport(member string) roomView {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		v := h.roomState(member)
		if v.Result == nil {
			h.t.Fatalf("no result: %+v", v)
		}
		if v.Result.ScoreReported != reportPending || time.Now().After(deadline) {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRoom_JoinStartPlayAndReport(t *testing.T) {
	h := newHarness(t, testCorpus)
	a := h.join(1, "An")
	if a.Status != roomLobby || !a.Host || len(a.Players) != 1 || !a.Players[0].You || a.Seat != noSeat {
		t.Fatalf("first join = %+v", a)
	}
	if code, e := h.roomErr("room/start", map[string]string{"member": a.Member}); code != http.StatusConflict || e != "not_enough_players" {
		t.Fatalf("start alone: %d %s", code, e)
	}
	b := h.join(2, "Bình")
	if b.Host || len(b.Players) != 2 || b.Players[0].Name != "An" || !b.Players[0].Host || !b.Players[1].You {
		t.Fatalf("second join = %+v", b)
	}
	if code, e := h.roomErr("room/start", map[string]string{"member": b.Member}); code != http.StatusForbidden || e != "not_host" {
		t.Fatalf("start by guest: %d %s", code, e)
	}

	v := h.roomStart(a.Member, http.StatusOK)
	if v.Status != roomPlaying || v.Seat != 0 || v.Turn != 0 || v.Current != "hồng" || v.Game != 1 || len(v.Chain) != 1 || v.Chain[0].Seat != noSeat {
		t.Fatalf("started = %+v", v)
	}
	if v.DeadlineMS-v.ServerNowMS != turnLimit.Milliseconds() {
		t.Fatalf("deadline %d now %d", v.DeadlineMS, v.ServerNowMS)
	}
	if code, e := h.roomErr("room/start", map[string]string{"member": a.Member}); code != http.StatusConflict || e != "game_running" {
		t.Fatalf("second start: %d %s", code, e)
	}

	// Playing out of turn is refused and changes nothing.
	r := h.roomMove(b.Member, "hồng tâm")
	if r.Result.Accepted || r.Result.Reason != "not_your_turn" || r.Result.Message != msgNotYetTurn || len(r.State.Chain) != 1 {
		t.Fatalf("out of turn = %+v", r)
	}
	r = h.roomMove(a.Member, "hồng tâm")
	if !r.Result.Accepted || r.State.Turn != 1 || r.State.Chain[1].Name != "An" || r.State.Chain[1].Seat != 0 {
		t.Fatalf("a's move = %+v", r)
	}
	if r = h.roomMove(b.Member, "hồng hào"); r.Result.Accepted || r.Result.Reason != "wrong_link" || !strings.Contains(r.Result.Message, "“tâm”") {
		t.Fatalf("wrong link = %+v", r.Result)
	}
	h.roomMove(b.Member, "tâm sự")
	r = h.roomMove(a.Member, "sự cố") // leaves b a dead end
	if r.State.Status != roomPlaying || r.State.Turn != 1 {
		t.Fatalf("dead end ended the game early: %+v", r.State)
	}
	aScore := r.State.Players[0].Score
	bScore := r.State.Players[1].Score

	var end roomView
	h.call("room/give-up", map[string]string{"member": b.Member}, http.StatusOK, &end)
	if end.Status != roomLobby || end.Result == nil || end.Result.Winner != "An" || end.Result.Words != 3 || end.Result.EndReason != "no_legal_move" {
		t.Fatalf("after give up = %+v / %+v", end, end.Result)
	}
	st := end.Result.Standings
	if len(st) != 2 || st[0].Name != "An" || st[0].Score != aScore+winnerBonus || st[0].Bonus != winnerBonus || st[0].Words != 2 ||
		st[1].Name != "Bình" || !st[1].You || st[1].Score != bScore || st[1].OutReason != "no_legal_move" {
		t.Fatalf("standings = %+v", st)
	}

	if got := h.waitRoomReport(a.Member); got.Result.ScoreReported != reportOK || !got.Result.Standings[0].You {
		t.Fatalf("report = %+v", got.Result)
	}
	calls := h.reporter.snapshot()
	if len(calls) != 2 || calls[0].score != aScore+winnerBonus || calls[0].claims.UserID != 1 || calls[1].claims.UserID != 2 {
		t.Fatalf("reports = %+v", calls)
	}
	for _, c := range calls {
		if c.claims.ChatID != cardChat || c.claims.MessageID != cardMsg || c.claims.PvP {
			t.Fatalf("report not on the card: %+v", c.claims)
		}
	}
	if ann := h.reporter.announcements(); len(ann) != 1 || ann[0] != "Ván nối từ kết thúc: An thắng sau 3 từ!" {
		t.Fatalf("announcements = %q", ann)
	}
	if h.reporter.cards[0].ThreadID != cardThread || h.reporter.cards[0].MessageID != cardMsg {
		t.Fatalf("announced on %+v", h.reporter.cards[0])
	}

	// An unchanged poll is cheap; the same card plays again.
	final := h.roomState(a.Member)
	var ping roomPing
	h.call("room/state", map[string]any{"member": a.Member, "version": final.Version}, http.StatusOK, &ping)
	if !ping.Unchanged || ping.Version != final.Version {
		t.Fatalf("unchanged poll = %+v", ping)
	}
	again := h.roomStart(a.Member, http.StatusOK)
	if again.Status != roomPlaying || again.Game != 2 || len(again.Chain) != 1 || again.Result != nil || again.Version <= final.Version {
		t.Fatalf("second game = %+v", again)
	}
}

func TestRoom_NonMembersAndWrongModeAreRejected(t *testing.T) {
	h := newHarness(t, testCorpus)
	a := h.join(1, "An")
	id, _, _ := strings.Cut(a.Member, ".")
	for _, member := range []string{"", "nope", id, id + ".forged", "x." + strings.Repeat("0", 32)} {
		for _, path := range []string{"room/state", "room/start", "room/move", "room/give-up"} {
			if code, e := h.roomErr(path, map[string]string{"member": member}); code != http.StatusNotFound || e != "no_room" {
				t.Errorf("%s with %q: %d %s", path, member, code, e)
			}
		}
	}
	if code, e := h.roomErr("room/join", map[string]string{"token": h.token(1)}); code != http.StatusBadRequest || e != "bad_request" {
		t.Fatalf("join with a solo token: %d %s", code, e)
	}
	tok := h.pvpToken(1, "An")
	if code, e := h.roomErr("room/join", map[string]string{"token": tok[:len(tok)-2] + "xx"}); code != http.StatusUnauthorized || e != "bad_token" {
		t.Fatalf("tampered token: %d %s", code, e)
	}
	if rec := h.post("room/join", `{"token":"x","room":"y"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	// Moves and give-ups need a running game.
	if code, e := h.roomErr("room/give-up", map[string]string{"member": a.Member}); code != http.StatusConflict || e != "no_game" {
		t.Fatalf("give up in the lobby: %d %s", code, e)
	}
	h.clock.advance(time.Second)
	if code, e := h.roomErr("room/move", map[string]string{"member": a.Member, "word": "hồng hào"}); code != http.StatusConflict || e != "no_game" {
		t.Fatalf("move in the lobby: %d %s", code, e)
	}
}

func TestRoom_RejoinKeepsTheMembershipAndLateJoinersWatch(t *testing.T) {
	h := newHarness(t, testCorpus)
	a := h.join(1, "An")
	b := h.join(2, "Bình")
	if again := h.join(1, "An"); again.Member != a.Member || !again.Host || len(again.Players) != 2 {
		t.Fatalf("rejoin = %+v", again)
	}
	h.roomStart(a.Member, http.StatusOK)
	w := h.join(3, "<b>Chi</b>")
	if w.Status != roomPlaying || w.Seat != noSeat || w.Watchers != 1 || len(w.Players) != 2 {
		t.Fatalf("late joiner = %+v", w)
	}
	if r := h.roomMove(w.Member, "hồng hào"); r.Result.Accepted || r.Result.Message != msgWatching {
		t.Fatalf("watcher move = %+v", r.Result)
	}
	if code, e := h.roomErr("room/give-up", map[string]string{"member": w.Member}); code != http.StatusConflict || e != "not_in_game" {
		t.Fatalf("watcher give up: %d %s", code, e)
	}
	// Leaving mid-game out of turn: the turn and its clock stay with a.
	var v roomView
	h.call("room/give-up", map[string]string{"member": b.Member}, http.StatusOK, &v)
	if v.Status != roomLobby || v.Result.Winner != "An" || v.Result.Standings[1].OutReason != "gave_up" {
		t.Fatalf("after b leaves = %+v / %+v", v, v.Result)
	}
	// Back in the lobby, the watcher is a player for the next game, and the
	// name comes back exactly as given: the page escapes it.
	if v.Watchers != 0 || len(v.Players) != 3 || v.Players[2].Name != "<b>Chi</b>" {
		t.Fatalf("lobby after the game = %+v", v.Players)
	}
	// Nobody played a word: no bonus, no report and no announcement.
	if v.Result.Standings[0].Bonus != 0 || v.Result.Standings[0].Score != 0 || v.Result.Words != 0 || v.Result.ScoreReported != reportSkipped {
		t.Fatalf("zero-word result = %+v", v.Result)
	}
	time.Sleep(20 * time.Millisecond) // a wrongly started publisher would have run by now
	if calls, ann := h.reporter.snapshot(), h.reporter.announcements(); len(calls) != 0 || len(ann) != 0 {
		t.Fatalf("zero-word game reported %+v / announced %q", calls, ann)
	}
}

func TestRoom_TimeoutEliminatesAndALateWordCountsAsTimeout(t *testing.T) {
	h := newHarness(t, testCorpus)
	a := h.join(1, "An")
	b := h.join(2, "Bình")
	c := h.join(3, "Chi")
	h.roomStart(a.Member, http.StatusOK)

	h.clock.advance(turnLimit + turnGrace + time.Millisecond)
	v := h.roomState(c.Member)
	if v.Players[0].Alive || v.Players[0].OutReason != "timeout" || v.Turn != 1 || v.Status != roomPlaying || v.Current != "hồng" {
		t.Fatalf("after a's timeout = %+v", v)
	}
	// b's turn began when a's ended, grace included.
	if want := h.clock.now().Add(turnLimit - time.Millisecond); v.DeadlineMS != want.UnixMilli() {
		t.Fatalf("b's deadline = %d, want %d", v.DeadlineMS, want.UnixMilli())
	}
	if r := h.roomMove(a.Member, "hồng hào"); r.Result.Accepted || r.Result.Message != msgEliminated {
		t.Fatalf("eliminated move = %+v", r.Result)
	}

	h.clock.advance(turnLimit + turnGrace)
	var r roomMoveResponse
	h.call("room/move", map[string]string{"member": b.Member, "word": "hồng hào"}, http.StatusOK, &r)
	if r.Result.Accepted || r.Result.Reason != "timeout" || r.State.Status != roomLobby || r.State.Result.Winner != "Chi" {
		t.Fatalf("late move = %+v / %+v", r.Result, r.State.Result)
	}
	// Nobody played a word, so nothing is reported.
	if r.State.Result.ScoreReported != reportSkipped || r.State.Result.Standings[0].Bonus != 0 {
		t.Fatalf("zero-word result = %+v", r.State.Result)
	}
}

// A game whose players all closed the page still ends on the clock through
// the sweep, and is still reported and announced.
func TestRoom_SweepSettlesAnAbandonedGame(t *testing.T) {
	h := newHarness(t, testCorpus)
	a := h.join(1, "An")
	h.join(2, "Bình")
	h.roomStart(a.Member, http.StatusOK)
	h.roomMove(a.Member, "hồng hào")
	h.clock.advance(5 * time.Minute)
	if err := h.svc.sweepCron(t.Context(), modules.Deps{}); err != nil {
		t.Fatal(err)
	}
	waitCalls(t, h.reporter, 1)
	calls := h.reporter.snapshot()
	if len(calls) != 1 || calls[0].claims.UserID != 1 {
		t.Fatalf("reports = %+v", calls)
	}
	if ann := h.reporter.announcements(); len(ann) != 1 || !strings.Contains(ann[0], "An thắng sau 1 từ") {
		t.Fatalf("announcements = %q", ann)
	}
}

func TestRoom_ExpiryAndStaleMembers(t *testing.T) {
	h := newHarness(t, testCorpus)
	a := h.join(1, "An")
	b := h.join(2, "Bình")
	// a keeps polling; b's page closed. b's own late poll is what drops b,
	// and it must not be answered as a member.
	h.clock.advance(memberStaleTTL / 2)
	h.roomState(a.Member)
	h.clock.advance(memberStaleTTL/2 + time.Second)
	if code, e := h.roomErr("room/state", map[string]string{"member": b.Member}); code != http.StatusNotFound || e != "no_room" {
		t.Fatalf("stale member poll: %d %s", code, e)
	}
	if v := h.roomState(a.Member); len(v.Players) != 1 || !v.Host {
		t.Fatalf("lobby after b went stale = %+v", v.Players)
	}

	// A lobby somebody polls lives on, however long nobody starts a game.
	for range 30 {
		h.clock.advance(memberStaleTTL - time.Second)
		h.roomState(a.Member)
		h.svc.sweepRooms(h.clock.now())
	}
	if n := len(h.svc.rooms.all()); n != 1 {
		t.Fatalf("polled lobby dropped: %d rooms", n)
	}

	// Once nobody polls, the room goes with its last member.
	h.clock.advance(memberStaleTTL + time.Second)
	h.svc.sweepRooms(h.clock.now())
	if n := len(h.svc.rooms.all()); n != 0 {
		t.Fatalf("%d rooms left", n)
	}
	if code, e := h.roomErr("room/state", map[string]string{"member": a.Member}); code != http.StatusNotFound || e != "no_room" {
		t.Fatalf("expired room: %d %s", code, e)
	}
	// Joining again opens a fresh room on the same card.
	if v := h.join(1, "An"); v.Member == a.Member || !v.Host {
		t.Fatalf("rejoin after expiry = %+v", v)
	}
}

// A running game keeps every member, polling or not: only the lobby drops
// the absent.
func TestRoom_RunningGameNeverDropsMembers(t *testing.T) {
	h := newHarness(t, testCorpus)
	a := h.join(1, "An")
	b := h.join(2, "Bình")
	h.roomStart(a.Member, http.StatusOK)
	w := h.join(3, "Chi") // watches, then stops polling
	for _, mv := range []struct{ member, word string }{
		{a.Member, "hồng tâm"}, {b.Member, "tâm sự"}, {a.Member, "sự cố"},
	} {
		h.clock.advance(turnLimit - 5*time.Second)
		if r := h.roomMove(mv.member, mv.word); !r.Result.Accepted {
			t.Fatalf("%s: %+v", mv.word, r.Result)
		}
	}
	// b now faces a dead end; the game is still running past memberStaleTTL.
	v := h.roomState(w.Member)
	if v.Status != roomPlaying || v.Watchers != 1 || v.Seat != noSeat {
		t.Fatalf("watcher after %v = %+v", 3*(turnLimit-4*time.Second), v)
	}
}

// A room the sweep has closed answers no_room even before it leaves the
// store, and a join that races the removal gets a fresh room, not busy.
func TestRoom_ClosedRoomIsReplacedNotJoined(t *testing.T) {
	h := newHarness(t, testCorpus)
	a := h.join(1, "An")
	rooms := h.svc.rooms.all()
	if len(rooms) != 1 {
		t.Fatalf("%d rooms", len(rooms))
	}
	rooms[0].closed.Store(true) // the sweep's first step, before remove
	if code, e := h.roomErr("room/state", map[string]string{"member": a.Member}); code != http.StatusNotFound || e != "no_room" {
		t.Fatalf("closed room poll: %d %s", code, e)
	}
	v := h.join(1, "An")
	if v.Member == a.Member || !v.Host {
		t.Fatalf("join on a closed room = %+v", v)
	}
	if got := h.svc.rooms.all(); len(got) != 1 || got[0] == rooms[0] {
		t.Fatal("the closed room was not replaced")
	}
}

func TestRoom_GiveUpByAnEliminatedPlayerIsRefused(t *testing.T) {
	h := newHarness(t, testCorpus)
	a := h.join(1, "An")
	h.join(2, "Bình")
	h.join(3, "Chi")
	h.roomStart(a.Member, http.StatusOK)
	h.clock.advance(turnLimit + turnGrace + time.Millisecond)
	if code, e := h.roomErr("room/give-up", map[string]string{"member": a.Member}); code != http.StatusConflict || e != "not_in_game" {
		t.Fatalf("eliminated give up: %d %s", code, e)
	}
	if v := h.roomState(a.Member); v.Status != roomPlaying || v.Players[0].OutReason != "timeout" {
		t.Fatalf("state = %+v", v)
	}
}

func TestRoom_MaxChainEndsTheGame(t *testing.T) {
	h := newHarness(t, testCorpus)
	a := h.join(1, "An")
	h.join(2, "Bình")
	h.roomStart(a.Member, http.StatusOK)
	rm := h.svc.rooms.all()[0]
	rm.mu.Lock()
	for len(rm.chain) < maxChain-1 {
		rm.chain = append(rm.chain, rm.chain[0])
	}
	rm.mu.Unlock()
	r := h.roomMove(a.Member, "hồng hào")
	if !r.Result.Accepted || r.State.Status != roomLobby || r.State.Result == nil || r.State.Result.EndReason != "max_moves" || r.State.Result.Winner != "An" {
		t.Fatalf("max chain = %+v / %+v", r.Result, r.State.Result)
	}
}

func TestRoom_FailedReportShowsFailed(t *testing.T) {
	h := newHarness(t, testCorpus)
	h.reporter.err = fmt.Errorf("Forbidden")
	a := h.join(1, "An")
	b := h.join(2, "Bình")
	h.roomStart(a.Member, http.StatusOK)
	h.roomMove(a.Member, "hồng hào")
	h.call("room/give-up", map[string]string{"member": b.Member}, http.StatusOK, nil)
	if got := h.waitRoomReport(a.Member); got.Result.ScoreReported != reportFailed {
		t.Fatalf("score_reported = %q", got.Result.ScoreReported)
	}
}

// Starts are limited per room and per host, so a start/give-up loop cannot
// flood the chat or the card.
func TestRoom_StartRateLimits(t *testing.T) {
	h := newHarness(t, testCorpus)
	a := h.join(1, "An")
	b := h.join(2, "Bình")
	h.roomStart(a.Member, http.StatusOK)
	h.call("room/give-up", map[string]string{"member": b.Member}, http.StatusOK, nil)
	if code, e := h.roomErr("room/start", map[string]string{"member": a.Member}); code != http.StatusTooManyRequests || e != "too_fast" {
		t.Fatalf("immediate restart: %d %s", code, e)
	}
	starts := 1
	for {
		h.clock.advance(roomStartInterval)
		rec := h.postJSON("room/start", map[string]string{"member": a.Member})
		if rec.Code != http.StatusOK {
			if rec.Code != http.StatusTooManyRequests || h.apiError(rec) != "too_fast" {
				t.Fatalf("start %d: %d %s", starts+1, rec.Code, rec.Body.String())
			}
			break
		}
		starts++
		h.call("room/give-up", map[string]string{"member": b.Member}, http.StatusOK, nil)
	}
	// The host's join counted as one of their startsPerMinute.
	if starts != startsPerMinute-1 {
		t.Fatalf("%d starts in a minute, want %d", starts, startsPerMinute-1)
	}
}

func TestRoom_Limits(t *testing.T) {
	h := newHarness(t, testCorpus)
	for user := int64(1); user <= maxRoomMembers; user++ {
		h.join(user, "P")
	}
	if code, e := h.roomErr("room/join", map[string]string{"token": h.pvpToken(maxRoomMembers+1, "P")}); code != http.StatusConflict || e != "room_full" {
		t.Fatalf("full room: %d %s", code, e)
	}
	// Only the first MaxSeats members play; the rest wait in the lobby.
	v := h.join(1, "P")
	if len(v.Players) != 8 || v.Watchers != maxRoomMembers-8 {
		t.Fatalf("lobby = %d players / %d watchers", len(v.Players), v.Watchers)
	}
	started := h.roomStart(v.Member, http.StatusOK)
	if len(started.Players) != 8 || started.Watchers != maxRoomMembers-8 {
		t.Fatalf("game = %d players / %d watchers", len(started.Players), started.Watchers)
	}

	// The room cap answers busy.
	h2 := newHarness(t, testCorpus)
	for msg := 1; msg <= maxRooms; msg++ {
		h2.call("room/join", map[string]string{"token": h2.pvpTokenAt(int64(msg), "P", -int64(msg), msg)}, http.StatusOK, nil)
	}
	tok := h2.pvpTokenAt(maxRooms+1, "P", -maxRooms-1, maxRooms+1)
	if code, e := h2.roomErr("room/join", map[string]string{"token": tok}); code != http.StatusServiceUnavailable || e != "busy" {
		t.Fatalf("room cap: %d %s", code, e)
	}

	// One chat holds at most maxChatRooms live rooms; other chats are free.
	h4 := newHarness(t, testCorpus)
	for msg := 1; msg <= maxChatRooms; msg++ {
		h4.call("room/join", map[string]string{"token": h4.pvpTokenOn(int64(msg), "P", msg)}, http.StatusOK, nil)
	}
	if code, e := h4.roomErr("room/join", map[string]string{"token": h4.pvpTokenOn(99, "P", 99)}); code != http.StatusConflict || e != "too_many_rooms" {
		t.Fatalf("chat room cap: %d %s", code, e)
	}
	h4.call("room/join", map[string]string{"token": h4.pvpTokenAt(99, "P", -1, 99)}, http.StatusOK, nil)

	// Joins share the per-user start limit.
	h3 := newHarness(t, testCorpus)
	for range startsPerMinute {
		h3.join(1, "P")
	}
	if code, e := h3.roomErr("room/join", map[string]string{"token": h3.pvpToken(1, "P")}); code != http.StatusTooManyRequests || e != "too_fast" {
		t.Fatalf("join flood: %d %s", code, e)
	}
}

func TestRoom_MoveRateLimitAndWordLength(t *testing.T) {
	h := newHarness(t, testCorpus)
	a := h.join(1, "An")
	h.join(2, "Bình")
	h.roomStart(a.Member, http.StatusOK)
	h.roomMove(a.Member, "hồng xyz")
	if code, e := h.roomErr("room/move", map[string]string{"member": a.Member, "word": "hồng hào"}); code != http.StatusTooManyRequests || e != "too_fast" {
		t.Fatalf("fast move: %d %s", code, e)
	}
	h.clock.advance(time.Second)
	if code, _ := h.roomErr("room/move", map[string]string{"member": a.Member, "word": strings.Repeat("a ", 9)}); code != http.StatusBadRequest {
		t.Fatalf("long word: %d", code)
	}
}

// The sweep marks a room it drops as closed, so a request already holding
// the room sees it gone.
func TestRoom_SweepClosesTheRoomItDrops(t *testing.T) {
	h := newHarness(t, testCorpus)
	h.join(1, "An")
	held := h.svc.rooms.all()[0]
	h.clock.advance(memberStaleTTL + time.Second)
	h.svc.sweepRooms(h.clock.now())
	if !held.closed.Load() {
		t.Fatal("the swept room is not marked closed")
	}
	if n := len(h.svc.rooms.all()); n != 0 {
		t.Fatalf("%d rooms left", n)
	}
}

// A join that got a room from open before the sweep closed it adds nobody
// to it and asks for a retry.
func TestRoom_JoinRetriesARoomClosedAfterOpen(t *testing.T) {
	h := newHarness(t, testCorpus)
	now := h.clock.now()
	c := claims{UserID: 1, Name: "An", ChatID: cardChat, MessageID: cardMsg, ThreadID: cardThread, PvP: true}
	rm, err := h.svc.rooms.open(c)
	if err != nil {
		t.Fatal(err)
	}
	rm.closed.Store(true) // the sweep ran between open and lock
	if _, retry := h.svc.joinRoom(rm, c, now); !retry {
		t.Fatal("join on a room closed after open did not retry")
	}
	rm.mu.Lock()
	n := len(rm.members)
	rm.mu.Unlock()
	if n != 0 {
		t.Fatalf("closed room gained %d members", n)
	}
	fresh, err := h.svc.rooms.open(c)
	if err != nil || fresh == rm {
		t.Fatalf("retry open = %p, %v; want a new room", fresh, err)
	}
	if reply, retry := h.svc.joinRoom(fresh, c, now); retry || reply.status != http.StatusOK {
		t.Fatalf("retry join = %+v, %v", reply, retry)
	}
}

// Results on one card publish one game after another: while the first
// game's report is held, a second game on the same card announces nothing,
// but a game on another card publishes freely.
func TestRoom_PublishingIsSerializedPerCard(t *testing.T) {
	h := newHarness(t, testCorpus)
	const otherMsg = cardMsg + 1
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.reporter.beforeReport = func(c claims) {
		if c.MessageID != cardMsg {
			return
		}
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	// playOut runs one game on the card a and b joined: a plays a word, b
	// gives up, so a wins with one word.
	playOut := func(a, b roomView) {
		h.roomStart(a.Member, http.StatusOK)
		if r := h.roomMove(a.Member, "hồng hào"); !r.Result.Accepted {
			t.Fatalf("move: %+v", r.Result)
		}
		h.call("room/give-up", map[string]string{"member": b.Member}, http.StatusOK, nil)
	}

	a := h.join(1, "An")
	b := h.join(2, "Bình")
	playOut(a, b)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first report never started")
	}

	// A second game on the same card ends while the first still reports.
	h.clock.advance(roomStartInterval)
	playOut(a, b)

	// A game on another card publishes while the first card is held.
	var c, d roomView
	h.call("room/join", map[string]string{"token": h.pvpTokenOn(3, "Chi", otherMsg)}, http.StatusOK, &c)
	h.call("room/join", map[string]string{"token": h.pvpTokenOn(4, "Dũng", otherMsg)}, http.StatusOK, &d)
	playOut(c, d)
	deadline := time.Now().Add(2 * time.Second)
	for h.reporter.announcedOn(otherMsg) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the other card's result was blocked by the first card")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if calls := waitCalls(t, h.reporter, 1); len(calls) != 1 || calls[0].claims.MessageID != otherMsg {
		t.Fatalf("reports while the first card is held = %+v", calls)
	}
	if n := h.reporter.announcedOn(cardMsg); n != 1 {
		t.Fatalf("%d announcements on the held card, want only the first game's", n)
	}

	close(release)
	released = true
	deadline = time.Now().Add(2 * time.Second)
	for h.reporter.announcedOn(cardMsg) != 2 {
		if time.Now().After(deadline) {
			t.Fatal("the second game never published after the first finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if calls := waitCalls(t, h.reporter, 3); len(calls) != 3 {
		t.Fatalf("reports = %+v, want 3", calls)
	}
}
