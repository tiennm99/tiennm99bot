package wordledaily

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/subscription"
)

func mustList(t *testing.T, h *harness) []subscription.Subscriber {
	t.Helper()
	subs, err := subscription.List(context.Background(), h.svc.subscribers)
	if err != nil {
		t.Fatal(err)
	}
	return subs
}

func subscribe(t *testing.T, h *harness, chat int64, thread int) {
	t.Helper()
	if _, err := subscription.Add(context.Background(), h.svc.subscribers, chat, thread); err != nil {
		t.Fatal(err)
	}
}

// playGroupDay has Alice and Dan win in 2, Eve win in 3 and Bob lose on
// puzzle #1 in the group, from the card in topic thread.
func playGroupDay(t *testing.T, h *harness, thread int) {
	t.Helper()
	w := h.wrong(6)
	ans := h.answer()
	play := func(user int64, name string, words ...string) {
		tok := h.tokenFor(user, groupChat, 7, thread, name)
		for _, word := range words {
			h.guess(tok, 1, word)
		}
	}
	play(1, "Alice", w[0], ans)
	h.clock.advance(time.Minute)
	play(2, "Bob", w...)
	play(3, "Dan", w[1], ans)
	play(4, "Eve", w[2], w[3], ans)
	h.sched.run()
	h.rb.Reset()
}

func (h *harness) nextDay() { h.clock.advance(24 * time.Hour) }

func TestPush_RecapThenCardWithCrownTiesAndLoss(t *testing.T) {
	h := newHarness(t)
	playGroupDay(t, h, 77)
	subscribe(t, h, groupChat, 77)
	h.nextDay()
	if err := h.svc.runDailyPush(context.Background(), h.rb.Bot); err != nil {
		t.Fatal(err)
	}
	sent := h.rb.Sent()
	if len(sent) != 2 || sent[0].Method != "sendMessage" || sent[1].Method != "sendGame" {
		t.Fatalf("sent %+v", sent)
	}
	want := "Wordle Daily #1 — yesterday's results\nAnswer: " + strings.ToUpper(h.svc.answerFor(1)) + "\n👑 2/6: Alice, Dan\n3/6: Eve\nX/6: Bob"
	if got := sent[0].Text(); got != want {
		t.Fatalf("recap:\n%s\nwant:\n%s", got, want)
	}
	for _, c := range sent {
		if c.Form["message_thread_id"] != "77" || c.ChatID() != "-100" {
			t.Fatalf("not sent to the topic: %+v", c)
		}
	}
	if sent[1].Form["game_short_name"] != GameShortName {
		t.Fatalf("card = %v", sent[1].Form)
	}
	// The same puzzle is never pushed twice.
	h.rb.Reset()
	if err := h.svc.runDailyPush(context.Background(), h.rb.Bot); err != nil || len(h.rb.Sent()) != 0 {
		t.Fatalf("second run sent %+v, %v", h.rb.Sent(), err)
	}
}

func TestPush_StreakLineAndReset(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	subscribe(t, h, groupChat, 0)
	// Someone in the group finishes on #1 and #2: a two-day streak.
	h.state(h.dmToken(1))
	h.guess(h.tokenFor(1, groupChat, 7, 0, "Alice"), 1, h.answer())
	h.nextDay()
	h.guess(h.tokenFor(1, groupChat, 8, 0, "Alice"), 2, h.answer())
	h.nextDay()
	h.rb.Reset()
	if err := h.svc.runDailyPush(ctx, h.rb.Bot); err != nil {
		t.Fatal(err)
	}
	if got := h.rb.Sent()[0].Text(); got != "Wordle Daily #2 — yesterday's results\nAnswer: "+strings.ToUpper(h.svc.answerFor(2))+"\n🔥 Your group is on a 2 day streak!\n👑 1/6: Alice" {
		t.Fatalf("streak recap = %q", got)
	}
	// Nobody plays #3: the next push says the streak ended, then sends the card.
	h.nextDay()
	h.rb.Reset()
	if err := h.svc.runDailyPush(ctx, h.rb.Bot); err != nil {
		t.Fatal(err)
	}
	sent := h.rb.Sent()
	if len(sent) != 2 || sent[0].Text() != "Wordle Daily #3 — yesterday's results\nAnswer: "+strings.ToUpper(h.svc.answerFor(3))+"\nNobody solved it. Group streak reset." || sent[1].Method != "sendGame" {
		t.Fatalf("reset push = %+v", sent)
	}
	// The day after, with no streak left, the recap is just the answer.
	h.nextDay()
	h.rb.Reset()
	if err := h.svc.runDailyPush(ctx, h.rb.Bot); err != nil {
		t.Fatal(err)
	}
	if sent := h.rb.Sent(); len(sent) != 2 || sent[0].Text() != "Wordle Daily #4 — yesterday's results\nAnswer: "+strings.ToUpper(h.svc.answerFor(4)) || sent[1].Method != "sendGame" {
		t.Fatalf("quiet push = %+v", sent)
	}
}

// A loss keeps nobody's streak going: the group streak counts wins only.
func TestPush_LossBreaksTheGroupStreak(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	subscribe(t, h, groupChat, 0)
	h.guess(h.tokenFor(1, groupChat, 7, 0, "Alice"), 1, h.answer())
	h.nextDay()
	h.guess(h.tokenFor(1, groupChat, 8, 0, "Alice"), 2, h.answer())
	h.nextDay()
	tok := h.tokenFor(1, groupChat, 9, 0, "Alice")
	for _, w := range h.wrong(6) {
		h.guess(tok, 3, w)
	}
	h.nextDay()
	h.rb.Reset()
	if err := h.svc.runDailyPush(ctx, h.rb.Bot); err != nil {
		t.Fatal(err)
	}
	want := "Wordle Daily #3 — yesterday's results\nAnswer: " + strings.ToUpper(h.svc.answerFor(3)) + "\nNobody solved it. Group streak reset.\nX/6: Alice"
	if got := h.rb.Sent()[0].Text(); got != want {
		t.Fatalf("loss recap = %q, want %q", got, want)
	}
}

func TestPush_PrivateSubscriberGetsCardOnly(t *testing.T) {
	h := newHarness(t)
	h.win(5)
	subscribe(t, h, 5, 0)
	h.nextDay()
	h.rb.Reset()
	if err := h.svc.runDailyPush(context.Background(), h.rb.Bot); err != nil {
		t.Fatal(err)
	}
	if sent := h.rb.Sent(); len(sent) != 1 || sent[0].Method != "sendGame" || sent[0].ChatID() != "5" {
		t.Fatalf("sent %+v", sent)
	}
}

func TestPush_BlockedRecapPrunesAndSkipsTheCard(t *testing.T) {
	h := newHarness(t)
	playGroupDay(t, h, 0)
	subscribe(t, h, groupChat, 0)
	subscribe(t, h, 9, 0)
	h.nextDay()
	h.rb.FailMethodCode("sendMessage", 403, "Forbidden: bot was kicked from the supergroup chat; bot is not a member of the supergroup chat")
	if err := h.svc.runDailyPush(context.Background(), h.rb.Bot); err != nil {
		t.Fatal(err)
	}
	games := sentMethod(h.rb, "sendGame")
	if len(games) != 1 || games[0].ChatID() != "9" {
		t.Fatalf("cards = %+v", games)
	}
	if subs := mustList(t, h); len(subs) != 1 || subs[0].ChatID != 9 {
		t.Fatalf("subscribers = %+v", subs)
	}
}

func TestPush_NoSubscribersAndNoBot(t *testing.T) {
	h := newHarness(t)
	if err := h.svc.runDailyPush(context.Background(), h.rb.Bot); err != nil || len(h.rb.Sent()) != 0 {
		t.Fatalf("no subscribers: %+v, %v", h.rb.Sent(), err)
	}
	if err := h.svc.dailyPushHandler(context.Background(), modules.Deps{}); err == nil {
		t.Fatal("push without a bot succeeded")
	}
	// The push pins the day's answer even with nobody subscribed.
	if _, _, err := h.svc.puzzles.Get(context.Background(), puzzleKey(1)); err != nil {
		t.Fatalf("puzzle not pinned: %v", err)
	}
}

// The daily push deletes the boards, chat days and pinned answers older
// than yesterday, even without subscribers, and keeps stats and streaks.
func TestPush_PrunesOldPuzzleDays(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for day := 1; day <= 3; day++ {
		h.guess(h.tokenFor(1, groupChat, 7, 0, "Alice"), day, h.answer())
		h.sched.run()
		if day < 3 {
			h.nextDay()
		}
	}
	if err := h.svc.runDailyPush(ctx, h.rb.Bot); err != nil {
		t.Fatal(err)
	}
	keys, err := h.svc.plays.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	want := "cday:2:-100:0: cday:3:-100:0: cstreak:-100:0: play:2:1 play:3:1 puzzle:2 puzzle:3 stats:1"
	if got := strings.Join(keys, " "); got != want {
		t.Fatalf("keys after prune:\n%s\nwant:\n%s", got, want)
	}
}
