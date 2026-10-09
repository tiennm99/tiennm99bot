package noitu

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// topOf reads one player's leaderboard entry on the test card's chat.
func (h *harness) topOf(user int64) topEntry {
	h.t.Helper()
	e, _, err := h.svc.cfg.top.Get(context.Background(), topKey(cardChat, user))
	if err != nil {
		h.t.Fatalf("leaderboard entry of %d: %v", user, err)
	}
	return e
}

func TestLeaderboard_RecordsEveryRoomGameThatHadAWord(t *testing.T) {
	h := newHarness(t, testCorpus)
	a := h.join(1, "An")
	b := h.join(2, "Bình")

	// Game 1: An leaves Bình a dead end and wins.
	h.roomStart(a.Member, http.StatusOK)
	h.roomMove(a.Member, "hồng tâm")
	h.roomMove(b.Member, "tâm sự")
	h.roomMove(a.Member, "sự cố")
	h.call("room/give-up", map[string]string{"member": b.Member}, http.StatusOK, nil)
	first := h.waitRoomReport(a.Member).Result.Standings
	aFirst, bFirst := int64(first[0].Score), int64(first[1].Score)

	if got, want := h.topOf(1), (topEntry{ChatID: cardChat, UserID: 1, Name: "An", Games: 1, Wins: 1, Best: aFirst, Total: aFirst}); got != want {
		t.Fatalf("An after game 1 = %+v, want %+v", got, want)
	}
	if got, want := h.topOf(2), (topEntry{ChatID: cardChat, UserID: 2, Name: "Bình", Games: 1, Best: bFirst, Total: bFirst}); got != want {
		t.Fatalf("Bình after game 1 = %+v, want %+v", got, want)
	}

	// Game 2: nobody plays a word, so it does not count.
	h.clock.advance(roomStartInterval)
	h.roomStart(a.Member, http.StatusOK)
	h.call("room/give-up", map[string]string{"member": a.Member}, http.StatusOK, nil)
	if res := h.roomState(a.Member).Result; res == nil || res.Words != 0 {
		t.Fatalf("game 2 result = %+v", res)
	}

	// Game 3: An plays a word then leaves; Bình wins, and takes a new name.
	b = h.join(2, "Bé Bình")
	h.clock.advance(roomStartInterval)
	h.roomStart(a.Member, http.StatusOK)
	h.roomMove(a.Member, "hồng tâm")
	h.call("room/give-up", map[string]string{"member": a.Member}, http.StatusOK, nil)
	third := h.waitRoomReport(b.Member).Result.Standings
	bThird, aThird := int64(third[0].Score), int64(third[1].Score)

	if got, want := h.topOf(1), (topEntry{ChatID: cardChat, UserID: 1, Name: "An", Games: 2, Wins: 1, Best: max(aFirst, aThird), Total: aFirst + aThird}); got != want {
		t.Fatalf("An after game 3 = %+v, want %+v", got, want)
	}
	if got, want := h.topOf(2), (topEntry{ChatID: cardChat, UserID: 2, Name: "Bé Bình", Games: 2, Wins: 1, Best: max(bFirst, bThird), Total: bFirst + bThird}); got != want {
		t.Fatalf("Bình after game 3 = %+v, want %+v", got, want)
	}
}

func TestLeaderboard_ZeroWordGameRecordsNothing(t *testing.T) {
	h := newHarness(t, testCorpus)
	a := h.join(1, "An")
	h.join(2, "Bình")
	h.roomStart(a.Member, http.StatusOK)
	h.call("room/give-up", map[string]string{"member": a.Member}, http.StatusOK, nil)
	// No publisher runs for such a game, so there is nothing to wait for.
	docs, err := h.svc.cfg.top.Scan(context.Background(), topKeyPrefix)
	if err != nil || len(docs) != 0 {
		t.Fatalf("leaderboard after a zero-word game = %+v, %v", docs, err)
	}
}

// Two cards of one chat can finish games with the same player at once; the
// versioned write keeps both.
func TestLeaderboard_ConcurrentGamesKeepEveryUpdate(t *testing.T) {
	h := newHarness(t, testCorpus)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.svc.recordTop(cardChat, []roomStanding{{userID: 1, Name: "An", Score: 10 * (i + 1), won: i == 0}})
		}()
	}
	wg.Wait()
	if got := h.topOf(1); got.Games != 2 || got.Wins != 1 || got.Best != 20 || got.Total != 30 {
		t.Fatalf("entry = %+v", got)
	}
}

func TestLeaderboard_StoreFailureIsNotFatal(t *testing.T) {
	h := newHarness(t, testCorpus)
	h.svc.cfg.top = storage.Typed[topEntry](storage.NewMemoryProvider().Collection("Not A Valid Name"))
	h.svc.recordTop(cardChat, []roomStanding{{userID: 1, Name: "An", Score: 10, won: true}})

	rb := installModule(t, h.svc.module())
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewGroupMessage(cardChat, 1, "/noitutop"))
	if last := rb.LastSent(); last.Method != "sendMessage" || last.Text() != msgTopFail {
		t.Fatalf("sent %+v", rb.Sent())
	}
}

func TestSortTop_WinsThenBestThenFewerGames(t *testing.T) {
	entries := []topEntry{
		{UserID: 1, Wins: 1, Best: 90, Games: 3},
		{UserID: 2, Wins: 2, Best: 10, Games: 9},
		{UserID: 3, Wins: 1, Best: 90, Games: 2},
		{UserID: 4, Wins: 1, Best: 95, Games: 9},
		{UserID: 5, Wins: 1, Best: 90, Games: 2, Total: 300},
		{UserID: 6, Wins: 1, Best: 90, Games: 2},
	}
	sortTop(entries)
	var order []int64
	for _, e := range entries {
		order = append(order, e.UserID)
	}
	if fmt.Sprint(order) != "[2 4 5 3 6 1]" {
		t.Fatalf("order = %v", order)
	}
}

func TestTopCommand_RefusesOutsideGroups(t *testing.T) {
	h := newHarness(t, testCorpus)
	for name, update := range map[string]*models.Update{
		"private": testutil.NewPrivateMessage(42, "/noitutop"),
		"channel": testutil.NewChannelMessage(-100888, "/noitutop"),
	} {
		rb := installModule(t, h.mod)
		rb.Bot.ProcessUpdate(context.Background(), update)
		if last := rb.LastSent(); last.Method != "sendMessage" || last.Text() != msgTopNeedsGroup || len(rb.Sent()) != 1 {
			t.Errorf("%s: sent %+v", name, rb.Sent())
		}
	}
}

func TestTopCommand_EmptyGroup(t *testing.T) {
	h := newHarness(t, testCorpus)
	// Another chat's records never show, even one whose ID shares the prefix.
	h.svc.recordTop(-1005, []roomStanding{{userID: 1, Name: "An", Score: 10, won: true}})
	rb := installModule(t, h.mod)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewGroupMessage(-100, 42, "/noitutop"))
	if last := rb.LastSent(); last.Method != "sendMessage" || last.Text() != msgTopEmpty {
		t.Fatalf("sent %+v", rb.Sent())
	}
}

func TestTopCommand_ShowsTopTenPlusTheCallerEscaped(t *testing.T) {
	h := newHarness(t, testCorpus)
	ctx := context.Background()
	// Player i has i wins, so player 12 ranks first and player 1 last.
	for i := int64(1); i <= 12; i++ {
		name := fmt.Sprintf("P%02d", i)
		if i == 12 {
			name = "<b>Tí & Tèo</b>"
		}
		e := topEntry{ChatID: cardChat, UserID: i, Name: name, Games: 20, Wins: i, Best: 100 + i, Total: 1000 + i}
		if err := h.svc.cfg.top.Put(ctx, topKey(cardChat, i), e); err != nil {
			t.Fatal(err)
		}
	}

	send := func(caller int64) testutil.SentCall {
		t.Helper()
		rb := installModule(t, h.mod)
		rb.Bot.ProcessUpdate(ctx, testutil.NewSupergroupMessage(cardChat, caller, "/noitutop"))
		last := rb.LastSent()
		if last.Method != "sendMessage" || last.Form["parse_mode"] != "HTML" {
			t.Fatalf("sent %+v", rb.Sent())
		}
		return last
	}

	text := send(1).Text()
	if !strings.HasPrefix(text, "<b>Bảng xếp hạng nối từ</b> (12 người chơi)\n<pre>") {
		t.Fatalf("header: %q", text)
	}
	if strings.Contains(text, "<b>Tí") || !strings.Contains(text, "&lt;b&gt;Tí &amp; Tèo&lt;/b&gt;") {
		t.Fatalf("name not escaped: %q", text)
	}
	lines := strings.Split(strings.TrimSuffix(text, "</pre>"), "\n")
	// Header line, table header, separator, 10 rows, the gap, the caller.
	if len(lines) != 15 {
		t.Fatalf("%d lines: %q", len(lines), text)
	}
	if !strings.HasPrefix(lines[3], "1 ") || !strings.Contains(lines[3], "Tí") || !strings.HasPrefix(lines[12], "10") ||
		!strings.Contains(lines[12], "P03") || strings.TrimSpace(lines[13]) != "…" ||
		!strings.HasPrefix(lines[14], "12") || !strings.Contains(lines[14], "P01") {
		t.Fatalf("rows: %q", lines)
	}

	// A caller inside the top ten, or without a record, gets no extra line.
	for _, caller := range []int64{5, 99} {
		if text := send(caller).Text(); strings.Count(text, "\n") != 12 || strings.Contains(text, "…") {
			t.Fatalf("caller %d: %q", caller, text)
		}
	}
}
