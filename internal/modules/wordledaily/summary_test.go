package wordledaily

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSummary_FirstJoinRepliesLaterGuessesEditOnce(t *testing.T) {
	h := newHarness(t)
	alice := h.tokenFor(1, groupChat, 7, 77, "Alice")
	words := h.wrong(5)
	h.guess(alice, 1, words[0])
	if h.sched.pending() != 1 || h.sched.delays[0] != 0 {
		t.Fatalf("first touch: pending %d delays %v", h.sched.pending(), h.sched.delays)
	}
	h.sched.run()
	sends := sentMethod(h.rb, "sendMessage")
	if len(sends) != 1 {
		t.Fatalf("sends = %+v", h.rb.Sent())
	}
	f := sends[0].Form
	if f["chat_id"] != "-100" || f["message_thread_id"] != "77" || f["disable_notification"] != "true" ||
		!strings.Contains(f["reply_parameters"], `"message_id":7`) || !strings.Contains(f["reply_parameters"], `"allow_sending_without_reply":true`) {
		t.Fatalf("summary send form = %v", f)
	}
	if !strings.HasPrefix(f["text"], "Wordle Daily #1 · live results\n\nAlice playing 1/6\n") {
		t.Fatalf("summary text = %q", f["text"])
	}

	// Four quick guesses (one by another player) schedule one delayed edit.
	h.clock.advance(time.Second)
	for _, w := range words[1:4] {
		h.guess(alice, 1, w)
	}
	h.guess(h.tokenFor(2, groupChat, 7, 77, "Bob"), 1, words[4])
	if h.sched.pending() != 1 || h.sched.delays[0] != summaryInterval-time.Second {
		t.Fatalf("coalesced: pending %d delays %v", h.sched.pending(), h.sched.delays)
	}
	h.sched.run()
	edits := sentMethod(h.rb, "editMessageText")
	if len(edits) != 1 || len(sentMethod(h.rb, "sendMessage")) != 1 {
		t.Fatalf("edits = %d, sends = %d", len(edits), len(sentMethod(h.rb, "sendMessage")))
	}
	if e := edits[0].Form; e["message_id"] != "1" || !strings.Contains(e["text"], "Alice playing 4/6") || !strings.Contains(e["text"], "Bob playing 1/6") {
		t.Fatalf("edit = %v", e)
	}
}

func TestSummary_DeletedMessageIsResentAtMostEveryTenMinutes(t *testing.T) {
	h := newHarness(t)
	tok := h.tokenFor(1, groupChat, 7, 0, "Alice")
	words := h.wrong(4)
	h.guess(tok, 1, words[0])
	h.sched.run()

	h.rb.FailMethodCode("editMessageText", 400, "Bad Request: message is not modified: specified new message content is the same")
	h.clock.advance(summaryInterval)
	h.guess(tok, 1, words[1])
	h.sched.run()
	if n := len(sentMethod(h.rb, "sendMessage")); n != 1 {
		t.Fatalf("not-modified edit resent: %d sends", n)
	}

	h.rb.FailMethodCode("editMessageText", 400, "Bad Request: message to edit not found")
	h.clock.advance(summaryInterval)
	h.guess(tok, 1, words[2])
	h.sched.run()
	if n := len(sentMethod(h.rb, "sendMessage")); n != 1 {
		t.Fatalf("resent within ten minutes: %d sends", n)
	}
	h.clock.advance(resendInterval)
	h.guess(tok, 1, words[3])
	h.sched.run()
	if n := len(sentMethod(h.rb, "sendMessage")); n != 2 {
		t.Fatalf("not resent after ten minutes: %d sends", n)
	}
	cd, _, err := h.svc.chatDays.Get(context.Background(), chatDayKey(1, groupChat, 0))
	if err != nil || cd.MsgID != 2 {
		t.Fatalf("stored summary id = %+v, %v", cd, err)
	}
}

func TestSummary_NoneInPrivateChatsOrInline(t *testing.T) {
	h := newHarness(t)
	h.guess(h.dmToken(1), 1, h.wrong(1)[0])
	inline, _ := h.svc.signToken(claims{UserID: 2, InlineID: "BAAA", Expiry: h.clock.now().Add(tokenTTL).Unix()})
	h.guess(inline, 1, h.wrong(1)[0])
	h.sched.run()
	if len(h.rb.Sent()) != 0 {
		t.Fatalf("sent %+v", h.rb.Sent())
	}
	if keys, _ := h.svc.chatDays.List(context.Background(), "cday:"); len(keys) != 0 {
		t.Fatalf("chat days %v", keys)
	}
}

func TestSummary_PlayerFinishedElsewhereJoinsOnOpen(t *testing.T) {
	h := newHarness(t)
	h.win(1)
	h.state(h.tokenFor(1, groupChat, 7, 0, "Alice"))
	h.sched.run()
	sends := sentMethod(h.rb, "sendMessage")
	if len(sends) != 1 || !strings.Contains(sends[0].Text(), "Alice 1/6\n🟩🟩🟩🟩🟩") {
		t.Fatalf("sends = %+v", sends)
	}
	st, _ := h.svc.chatStreakAt(context.Background(), groupChat, 0)
	if st.Streak != 1 || st.LastNum != 1 {
		t.Fatalf("group streak = %+v", st)
	}
}

var asciiLetters = regexp.MustCompile(`[A-Za-z]`)

func TestRenderLive_OrderGridsAndTruncation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	put := func(user int64, name, status string, finishedAt int64, marks ...string) {
		p := progress{Num: 1, UserID: user, Name: name, Status: status, FinishedAt: finishedAt}
		for _, m := range marks {
			p.Guesses = append(p.Guesses, guess{Word: "crane", Marks: m})
		}
		if err := h.svc.plays.Put(ctx, playKey(1, user), p); err != nil {
			t.Fatal(err)
		}
	}
	put(1, "Carol", statusPlaying, 0, "wwpww", "wccww")
	put(2, "Bob", statusLost, 50, "wwwww", "wwwww", "wwwww", "wwwww", "wwwww", "pwwww")
	put(3, "Alice", statusWon, 90, "pwwpw", "ccccc")
	put(4, "Dan", statusWon, 40, "wpwww", "wpcww", "ccccc")
	put(5, "Eve", statusWon, 30, "wpwww", "wpcww", "ccccc")
	put(6, "Frank", statusPlaying, 0, "wwwww")
	if err := h.svc.streaks.Put(ctx, chatStreakKey(groupChat, 0), chatStreak{Streak: 5, LastNum: 1}); err != nil {
		t.Fatal(err)
	}
	cd := chatDay{Num: 1, ChatID: groupChat}
	for _, u := range []int64{1, 2, 3, 4, 5, 6, 99} { // 99 never guessed
		cd.Players = append(cd.Players, chatPlayer{UserID: u, Name: "p" + strconv.FormatInt(u, 10)})
	}
	text, err := h.svc.renderLive(ctx, cd)
	if err != nil {
		t.Fatal(err)
	}
	want := "Wordle Daily #1 · live results\n🔥 Group streak: 5 days\n\n" +
		"Alice 2/6\n🟨⬜⬜🟨⬜\n🟩🟩🟩🟩🟩\n" +
		"Eve 3/6\n⬜🟨⬜⬜⬜\n⬜🟨🟩⬜⬜\n🟩🟩🟩🟩🟩\n" +
		"Dan 3/6\n⬜🟨⬜⬜⬜\n⬜🟨🟩⬜⬜\n🟩🟩🟩🟩🟩\n" +
		"Bob X/6\n⬜⬜⬜⬜⬜\n⬜⬜⬜⬜⬜\n⬜⬜⬜⬜⬜\n⬜⬜⬜⬜⬜\n⬜⬜⬜⬜⬜\n🟨⬜⬜⬜⬜\n" +
		"Carol playing 2/6\n⬜⬜🟨⬜⬜\n⬜🟩🟩⬜⬜\n" +
		"Frank playing 1/6\n⬜⬜⬜⬜⬜"
	if text != want {
		t.Fatalf("live text:\n%s\nwant:\n%s", text, want)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.ContainsAny(line, "🟩🟨⬜") && asciiLetters.MatchString(line) {
			t.Fatalf("grid line shows letters: %q", line)
		}
	}

	// Many players: the message stops under Telegram's limit.
	cd.Players = nil
	for u := int64(100); u < 160; u++ {
		put(u, strings.Repeat("N", 60), statusLost, u, "wwwww", "wwwww", "wwwww", "wwwww", "wwwww", "wwwww")
		cd.Players = append(cd.Players, chatPlayer{UserID: u})
	}
	text, _ = h.svc.renderLive(ctx, cd)
	if utf16Len(text) > 4096 || !regexp.MustCompile(`\n\+\d+ more$`).MatchString(text) {
		t.Fatalf("truncated text: %d units, tail %q", utf16Len(text), text[len(text)-20:])
	}
}

// A transient failure (429 with retry_after, a 5xx) reschedules the update
// instead of leaving the message stale; Telegram's wait is honoured.
func TestSummary_TransientFailureIsRetried(t *testing.T) {
	h := newHarness(t)
	tok := h.tokenFor(1, groupChat, 7, 0, "Alice")
	words := h.wrong(1)
	h.guess(tok, 1, words[0])
	h.sched.run()

	h.rb.FailMethod("editMessageText", 429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 30","parameters":{"retry_after":30}}`)
	h.clock.advance(summaryInterval)
	h.guess(tok, 1, h.answer())
	h.sched.run1()
	if h.sched.pending() != 1 || h.sched.delays[0] != 30*time.Second {
		t.Fatalf("after 429: pending %d delays %v", h.sched.pending(), h.sched.delays)
	}
	h.rb.FailMethod("editMessageText", 502, "")
	h.sched.run1()
	if h.sched.pending() != 1 || h.sched.delays[0] != 2*summaryInterval {
		t.Fatalf("after 5xx: pending %d delays %v", h.sched.pending(), h.sched.delays)
	}
	h.rb.ClearFailure("editMessageText")
	h.sched.run()
	edits := sentMethod(h.rb, "editMessageText")
	if last := edits[len(edits)-1].Form["text"]; !strings.Contains(last, "Alice 2/6") {
		t.Fatalf("final edit = %q", last)
	}

	// A refused request is not retried, and retries stop after a bound.
	h.rb.FailMethodCode("editMessageText", 403, "Forbidden: bot was kicked from the supergroup chat")
	h.svc.touchSummary(1, groupChat, 0)
	h.sched.run1()
	if h.sched.pending() != 0 {
		t.Fatalf("forbidden retried: %v", h.sched.delays)
	}
	h.rb.FailMethod("editMessageText", 500, "")
	h.clock.advance(summaryInterval)
	h.svc.touchSummary(1, groupChat, 0)
	h.sched.run()
	if n := len(sentMethod(h.rb, "editMessageText")) - len(edits) - 1; n != 1+maxSummaryRetries {
		t.Fatalf("bounded retries: %d edits", n)
	}
}

// A deleted message inside the resend interval is sent once the interval
// has passed, so the final results still reach the chat.
func TestSummary_DeletedMessageIsResentWhenTheIntervalEnds(t *testing.T) {
	h := newHarness(t)
	tok := h.tokenFor(1, groupChat, 7, 0, "Alice")
	h.guess(tok, 1, h.wrong(1)[0])
	h.sched.run()
	h.rb.FailMethodCode("editMessageText", 400, "Bad Request: message to edit not found")
	h.clock.advance(time.Minute)
	h.guess(tok, 1, h.answer())
	h.sched.run1()
	if h.sched.pending() != 1 || h.sched.delays[0] != resendInterval-time.Minute {
		t.Fatalf("deferred resend: pending %d delays %v", h.sched.pending(), h.sched.delays)
	}
	h.clock.advance(resendInterval)
	h.sched.run()
	sends := sentMethod(h.rb, "sendMessage")
	if len(sends) != 2 || !strings.Contains(sends[1].Text(), "Alice 2/6") {
		t.Fatalf("sends = %+v", sends)
	}
}

// The flush lock is keyed by chat topic, not by day, so the lock map stays
// bounded by the number of chats however long the process runs.
func TestSummary_LockKeyIsPerTopicNotPerDay(t *testing.T) {
	h := newHarness(t)
	for day := 1; day <= 3; day++ {
		h.guess(h.tokenFor(1, groupChat, 7, 0, "Alice"), day, h.wrong(1)[0])
		h.sched.run()
		h.nextDay()
	}
	// One user lock and one summary lock, whatever the number of days.
	if n := h.svc.locks.Len(); n != 2 {
		t.Fatalf("lock keys = %d, want 2", n)
	}
}
