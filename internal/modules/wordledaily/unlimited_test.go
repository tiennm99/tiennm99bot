package wordledaily

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
	"github.com/tiennm99/tiennm99bot/internal/modules/wordle/wordlist"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// unlimitedToken signs a token for user on a recorded /wordle card.
func (h *harness) unlimitedToken(user, chat int64, msg int, name string) string {
	h.t.Helper()
	tok, err := h.svc.signToken(guessgame.Claims{UserID: user, Name: name, ChatID: chat, MessageID: msg,
		Mode: guessgame.ModeUnlimited, Expiry: h.clock.now().Add(guessgame.TokenTTL).Unix()})
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

// uguess posts an unlimited guess and returns the raw response.
func (h *harness) uguess(tok string, seq int, word string) *httpResult {
	h.t.Helper()
	raw, _ := json.Marshal(map[string]any{"token": tok, "seq": seq, "word": word})
	return newResult(h.t, h.post("guess", string(raw)))
}

func (h *harness) unew(tok string, seq int) *httpResult {
	h.t.Helper()
	raw, _ := json.Marshal(map[string]any{"token": tok, "seq": seq})
	return newResult(h.t, h.post("new", string(raw)))
}

func (h *harness) ustate(tok string) *httpResult {
	h.t.Helper()
	return newResult(h.t, h.post("state", `{"token":"`+tok+`"}`))
}

type httpResult struct {
	code int
	body string
	v    view
	err  string
}

func newResult(t *testing.T, rec interface {
	Result() *http.Response
}) *httpResult {
	t.Helper()
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	r := &httpResult{code: res.StatusCode, body: b.String()}
	if r.code == http.StatusOK {
		if err := json.Unmarshal([]byte(r.body), &r.v); err != nil {
			t.Fatalf("decode %q: %v", r.body, err)
		}
	} else {
		var e struct {
			Code string `json:"error"`
		}
		_ = json.Unmarshal([]byte(r.body), &e)
		r.err = e.Code
	}
	return r
}

func TestUnlimited_RoundLifecycleNeverLeaksTheAnswer(t *testing.T) {
	h := newHarness(t)
	tok := h.unlimitedToken(1, groupChat, 50, "Alice")
	r := h.ustate(tok)
	v := r.v
	if r.code != http.StatusOK || v.Mode != "unlimited" || v.Seq != 1 || v.Max != 6 || v.Len != 5 || v.Status != guessgame.StatusPlaying ||
		v.Num != 0 || v.NextAt != 0 || v.Answer != "" || v.Stats != nil || v.Share != "" || v.Player != "Alice" {
		t.Fatalf("fresh round = %d %+v", r.code, v)
	}
	if strings.Contains(strings.ToLower(r.body), "crane") {
		t.Fatalf("state leaks the target: %s", r.body)
	}
	r = h.uguess(tok, 1, "slate")
	if r.code != http.StatusOK || len(r.v.Guesses) != 1 || r.v.Guesses[0].Word != "SLATE" || r.v.Guesses[0].Marks != "wwcwc" {
		t.Fatalf("guess = %d %s", r.code, r.body)
	}
	if strings.Contains(strings.ToLower(r.body), "crane") || strings.Contains(r.body, `"answer"`) || strings.Contains(r.body, `"stats"`) {
		t.Fatalf("mid-round answer leaked: %s", r.body)
	}
	if r = h.uguess(tok, 1, "zzzzz"); r.code != http.StatusUnprocessableEntity || r.err != "unknown" {
		t.Fatalf("unknown word = %d %s", r.code, r.body)
	}
	r = h.uguess(tok, 1, "CRANE")
	if r.v.Status != guessgame.StatusWon || r.v.Answer != "CRANE" || r.v.Stats == nil || r.v.Stats.Played != 1 || r.v.Stats.WinPct != 100 ||
		r.v.Stats.Cur != 1 || r.v.Stats.Dist[1] != 1 || r.v.Share != "" {
		t.Fatalf("win = %s", r.body)
	}
	if r = h.uguess(tok, 1, "slate"); r.code != http.StatusUnprocessableEntity || r.err != "finished" {
		t.Fatalf("guess after the win = %d %s", r.code, r.body)
	}
	// New game, then a double click with the old seq starts nothing more.
	if r = h.unew(tok, 1); r.v.Seq != 2 || r.v.Status != guessgame.StatusPlaying || len(r.v.Guesses) != 0 || r.v.Answer != "" {
		t.Fatalf("new = %s", r.body)
	}
	if r = h.unew(tok, 1); r.v.Seq != 2 {
		t.Fatalf("stale new = %s", r.body)
	}
	if r = h.uguess(tok, 1, "slate"); r.code != http.StatusConflict || r.err != "new_round" {
		t.Fatalf("stale guess = %d %s", r.code, r.body)
	}
	// The win is never reported: scores stay a daily leaderboard.
	if n := len(h.rep.snapshot()); n != 0 {
		t.Fatalf("unlimited win reported %d times", n)
	}
	// Daily is untouched by unlimited play.
	if v := h.state(h.tokenFor(1, groupChat, 7, 0, "Alice")); v.Mode != "daily" || v.Num != 1 || len(v.Guesses) != 0 {
		t.Fatalf("daily = %+v", v)
	}
}

func TestUnlimited_NewGameCountsALossOnlyAfterAGuess(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tok := h.unlimitedToken(1, 1, 50, "Alice")
	h.ustate(tok)
	if r := h.unew(tok, 1); r.v.Seq != 2 || r.v.Abandoned != "" {
		t.Fatalf("new = %s", r.body)
	}
	if st, _ := h.svc.rounds.LoadStats(ctx, 1); st.Played != 0 {
		t.Fatalf("an unguessed round counted: %+v", st)
	}
	h.uguess(tok, 2, "crane") // round 2 is slate
	r := h.unew(tok, 2)
	// The page shows the given-up round's word, as /wordle_new does.
	if r.v.Seq != 3 || r.v.Abandoned != "SLATE" || r.v.Answer != "" {
		t.Fatalf("new = %s", r.body)
	}
	if st, _ := h.svc.rounds.LoadStats(ctx, 1); st.Played != 1 || st.Wins != 0 || st.CurStreak != 0 {
		t.Fatalf("abandoned round = %+v", st)
	}
}

func TestUnlimited_EachPlayerHasTheirOwnRound(t *testing.T) {
	h := newHarness(t)
	alice := h.unlimitedToken(1, groupChat, 50, "Alice")
	bob := h.unlimitedToken(2, groupChat, 50, "Bob")
	h.uguess(alice, 1, "slate")
	h.uguess(alice, 1, "crane")
	if r := h.ustate(bob); r.v.Seq != 1 || len(r.v.Guesses) != 0 || r.v.Status != guessgame.StatusPlaying {
		t.Fatalf("bob = %s", r.body)
	}
	// Nothing about unlimited play reaches the group's daily results.
	h.sched.run()
	if n := len(sentMethod(h.rb, "sendMessage")); n != 0 {
		t.Fatalf("unlimited play posted %d results messages", n)
	}
}

func TestUnlimited_NewOnADailyCardIsRefused(t *testing.T) {
	h := newHarness(t)
	if r := h.unew(h.dmToken(1), 1); r.code != http.StatusForbidden || r.err != "bad_mode" {
		t.Fatalf("new on a daily card = %d %s", r.code, r.body)
	}
	if r := h.unew(h.unlimitedToken(1, 1, 5, "A"), 0); r.code != http.StatusBadRequest {
		t.Fatalf("new without a seq = %d", r.code)
	}
}

func TestClaims_OldTokensDecodeAsDaily(t *testing.T) {
	h := newHarness(t)
	// A token signed before the mode existed carries no "md".
	old, _ := json.Marshal(map[string]any{"u": 1, "c": 5, "m": 9, "e": h.clock.now().Add(guessgame.TokenTTL).Unix()})
	var c guessgame.Claims
	if err := json.Unmarshal(old, &c); err != nil || c.Unlimited() || !c.Valid() {
		t.Fatalf("old claims = %+v, %v", c, err)
	}
	if (guessgame.Claims{UserID: 1, InlineID: "x", Expiry: 1, Mode: "x"}).Valid() {
		t.Fatal("unknown mode accepted")
	}
}

// lastCard returns the newest recorded unlimited card in chat.
func lastCard(t *testing.T, h *harness, chat int64) int {
	t.Helper()
	docs, err := storage.Typed[guessgame.CardRecord](h.coll).Scan(context.Background(), "ucard:")
	if err != nil {
		t.Fatal(err)
	}
	id := 0
	for _, d := range docs {
		if d.Val.ChatID == chat {
			id = max(id, d.Val.MessageID)
		}
	}
	if id == 0 {
		t.Fatalf("no card recorded in chat %d", chat)
	}
	return id
}

func cardCount(t *testing.T, h *harness) int {
	t.Helper()
	keys, err := storage.Typed[guessgame.CardRecord](h.coll).List(context.Background(), "ucard:")
	if err != nil {
		t.Fatal(err)
	}
	return len(keys)
}

func TestPlay_RecordedWordleCardOpensUnlimitedEverythingElseDaily(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module())
	ctx := context.Background()
	rb.Bot.ProcessUpdate(ctx, testutil.NewSupergroupMessage(groupChat, 1, "/wordle"))
	games := sentMethod(rb, "sendGame")
	if len(games) != 1 || games[0].Form["game_short_name"] != GameShortName || games[0].ChatID() != "-100" {
		t.Fatalf("sent %+v", rb.Sent())
	}
	card := lastCard(t, h, groupChat)
	rb.Bot.ProcessUpdate(ctx, testutil.NewGameCallback(42, groupChat, card, GameShortName))
	if c := decodeURLToken(t, h, answerForm(t, rb)["url"]); !c.Unlimited() || c.ChatID != groupChat || c.MessageID != card {
		t.Fatalf("recorded card claims = %+v", c)
	}
	// /wordledaily cards are not recorded, so they play daily, like a card
	// sent before unlimited mode existed and an inline share.
	rb.Bot.ProcessUpdate(ctx, testutil.NewSupergroupMessage(groupChat, 1, "/wordledaily"))
	if n := cardCount(t, h); n != 1 || len(sentMethod(rb, "sendGame")) != 2 {
		t.Fatalf("daily card recorded: %d records", n)
	}
	rb.Bot.ProcessUpdate(ctx, testutil.NewGameCallback(42, groupChat, card+1, GameShortName))
	if c := decodeURLToken(t, h, answerForm(t, rb)["url"]); c.Unlimited() {
		t.Fatalf("old card claims = %+v", c)
	}
	rb.Bot.ProcessUpdate(ctx, testutil.NewInlineGameCallback(42, "BAAA", GameShortName))
	if c := decodeURLToken(t, h, answerForm(t, rb)["url"]); c.Unlimited() || c.InlineID != "BAAA" {
		t.Fatalf("inline claims = %+v", c)
	}
	// A private chat's /wordle card is recorded too.
	rb.Bot.ProcessUpdate(ctx, testutil.NewPrivateMessage(7, "/wordle"))
	dm := lastCard(t, h, 7)
	rb.Bot.ProcessUpdate(ctx, testutil.NewGameCallback(7, 7, dm, GameShortName))
	if c := decodeURLToken(t, h, answerForm(t, rb)["url"]); !c.Unlimited() {
		t.Fatalf("dm card claims = %+v", c)
	}
}

func TestPlay_CardLookupFailureAlertsInsteadOfPlayingDaily(t *testing.T) {
	h := newHarness(t)
	h.svc.cards = guessgame.NewCards(storage.NewMemoryProvider().Collection("Bad Name!"), h.clock.now, ShortName)
	rb := install(t, h.svc.module())
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewGameCallback(42, groupChat, 3, GameShortName))
	if f := answerForm(t, rb); f["text"] != msgCardLookup || f["show_alert"] != "true" || f["url"] != "" {
		t.Fatalf("answer = %v", f)
	}
}

func TestCommand_UnrecordedCardIsDeleted(t *testing.T) {
	h := newHarness(t)
	h.svc.cards = guessgame.NewCards(storage.NewMemoryProvider().Collection("Bad Name!"), h.clock.now, ShortName)
	rb := install(t, h.svc.module())
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewSupergroupMessage(groupChat, 1, "/wordle"))
	if len(sentMethod(rb, "sendGame")) != 1 || len(sentMethod(rb, "deleteMessage")) != 1 || rb.LastSent().Text() != msgWordleSendFail {
		t.Fatalf("sent %+v", rb.Sent())
	}
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
	for i := len(rb.Sent()) - 1; i >= 0; i-- {
		if c := rb.Sent()[i]; c.Method == "sendMessage" {
			return c.Text()
		}
	}
	return ""
}

func TestCommand_WordleGuessesPlayTheCallersRound(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module())
	if got := send(t, rb, groupChat, 1, "/wordle slate"); got != "SLATE\n⬜⬜🟩⬜🟩\n\nGuess 1/6." {
		t.Fatalf("guess = %q", got)
	}
	// Bob in the same group plays his own round.
	if got := send(t, rb, groupChat, 2, "/wordle crane"); !strings.Contains(got, "🎉 Solved in 1/6! Streak: 1.") {
		t.Fatalf("bob = %q", got)
	}
	for word, want := range map[string]string{"abc": "Word must be exactly 5 letters.", "zzzzz": "Not in the word list.", "!!!": "Please provide a 5-letter word."} {
		if got := send(t, rb, groupChat, 1, "/wordle "+word); got != want {
			t.Errorf("/wordle %s = %q, want %q", word, got, want)
		}
	}
	// The page and the chat share the round.
	h.uguess(h.unlimitedToken(1, groupChat, 50, "Alice"), 1, "abbey")
	if got := send(t, rb, groupChat, 1, "/wordle crane"); !strings.Contains(got, "🎉 Solved in 3/6! Streak: 1.") {
		t.Fatalf("alice = %q", got)
	}
	if got := send(t, rb, groupChat, 1, "/wordle crane"); got != "Current round is over. Use /wordle_new to start another. Answer was CRANE." {
		t.Fatalf("after the win = %q", got)
	}
}

func TestCommand_WordleWithoutWordSendsTheCardOrShowsTheBoard(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module())
	send(t, rb, 1, 1, "/wordle")
	if len(sentMethod(rb, "sendGame")) != 1 {
		t.Fatalf("enabled /wordle sent %+v", rb.Sent())
	}
	// Without the web game the board is shown in the chat.
	svc := newService(config{store: h.coll, now: h.clock.now, answers: testAnswers, dict: testDict, pick: testPick})
	rb = install(t, svc.module())
	if got := send(t, rb, 1, 1, "/wordle"); got != "Guess 0/6. Use `/wordle <word>`.\n\nNo guesses yet. Reply with `/wordle <word>`." {
		t.Fatalf("board = %q", got)
	}
	send(t, rb, 1, 1, "/wordle slate")
	if got := send(t, rb, 1, 1, "/wordle"); got != "Guess 1/6. Use `/wordle <word>`.\n\nSLATE\n⬜⬜🟩⬜🟩" {
		t.Fatalf("board = %q", got)
	}
	if len(sentMethod(rb, "sendGame")) != 0 {
		t.Fatal("disabled game sent a card")
	}
}

func TestCommand_WordleNewGivesUpAndSendsACard(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module())
	ctx := context.Background()
	send(t, rb, groupChat, 1, "/wordle slate")
	rb.Reset()
	rb.Bot.ProcessUpdate(ctx, testutil.NewGroupMessage(groupChat, 1, "/wordle_new"))
	sent := rb.Sent()
	if len(sent) != 2 || sent[0].Text() != "🏳️ Previous round abandoned (auto-giveup). Answer was CRANE." || sent[1].Method != "sendGame" {
		t.Fatalf("sent %+v", sent)
	}
	if cardCount(t, h) != 1 {
		t.Fatal("/wordle_new card not recorded")
	}
	st, _ := h.svc.rounds.LoadStats(ctx, 1)
	if st.Played != 1 || st.Wins != 0 {
		t.Fatalf("stats = %+v", st)
	}
	// A round nobody guessed on is replaced without a word or a loss.
	rb.Reset()
	rb.Bot.ProcessUpdate(ctx, testutil.NewGroupMessage(groupChat, 1, "/wordle_new"))
	if sent := rb.Sent(); len(sent) != 1 || sent[0].Method != "sendGame" {
		t.Fatalf("sent %+v", sent)
	}
	if st, _ := h.svc.rounds.LoadStats(ctx, 1); st.Played != 1 {
		t.Fatalf("stats = %+v", st)
	}
	// Without the web game it says so in the chat.
	svc := newService(config{store: h.coll, now: h.clock.now, answers: testAnswers, dict: testDict, pick: testPick})
	rb = install(t, svc.module())
	if got := send(t, rb, 3, 3, "/wordle_new"); got != msgNewHint {
		t.Fatalf("disabled /wordle_new = %q", got)
	}
}

func TestCommand_WordleGiveupAndStats(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module())
	if got := send(t, rb, 1, 1, "/wordle_giveup"); got != "No active round. /wordle_new to start one." {
		t.Fatalf("no round = %q", got)
	}
	if got := send(t, rb, 1, 1, "/wordle_stats"); !strings.Contains(got, "Played: 0\nWins: 0 (0%)") {
		t.Fatalf("empty stats = %q", got)
	}
	send(t, rb, 1, 1, "/wordle slate")
	if got := send(t, rb, 1, 1, "/wordle_giveup"); got != "🏳️ Answer was CRANE. /wordle_new for another." {
		t.Fatalf("giveup = %q", got)
	}
	if got := send(t, rb, 1, 1, "/wordle_giveup"); got != "Already gave up — CRANE." {
		t.Fatalf("again = %q", got)
	}
	// Round 2 (slate) is won, round 3 (crane) lost on guesses.
	send(t, rb, 1, 1, "/wordle_new")
	send(t, rb, 1, 1, "/wordle slate")
	if got := send(t, rb, 1, 1, "/wordle_giveup"); got != "Already solved — SLATE." {
		t.Fatalf("after a win = %q", got)
	}
	send(t, rb, 1, 1, "/wordle_new")
	for _, w := range []string{"abbey", "slate", "zebra", "pious", "fjord", "night"} {
		send(t, rb, 1, 1, "/wordle "+w)
	}
	if got := send(t, rb, 1, 1, "/wordle_giveup"); got != "Current round is over. Use /wordle_new to start another. Answer was CRANE." {
		t.Fatalf("after a loss = %q", got)
	}
	got := send(t, rb, 1, 1, "/wordle_stats")
	if got != "📊 Wordle stats for Test\nPlayed: 3\nWins: 1 (33%)\nCurrent streak: 0\nBest streak: 1" {
		t.Fatalf("stats = %q", got)
	}
	// Each player has their own stats, in groups too.
	if got := send(t, rb, groupChat, 2, "/wordle_stats"); !strings.Contains(got, "Played: 0") {
		t.Fatalf("other player's stats = %q", got)
	}
}

func TestCommand_WordleInAChannelIsRefused(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module())
	for _, cmd := range []string{"/wordle", "/wordle_new", "/wordle_giveup", "/wordle_stats"} {
		rb.Reset()
		rb.Bot.ProcessUpdate(context.Background(), testutil.NewChannelMessage(-300, cmd))
		if got := rb.LastSent().Text(); got != msgWordleChannel {
			t.Errorf("%s in a channel = %q", cmd, got)
		}
	}
}

// Anonymous admins and posts on behalf of a chat arrive from one shared
// stand-in bot id, so they are refused instead of sharing one round.
func TestCommand_WordleRefusesStandInSenders(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module())
	for _, cmd := range []string{"/wordle crane", "/wordle_new", "/wordle_giveup", "/wordle_stats"} {
		rb.Reset()
		u := testutil.NewGroupMessage(-100, 1087968824, cmd)
		u.Message.From.IsBot = true
		u.Message.SenderChat = &models.Chat{ID: -100, Type: models.ChatTypeSupergroup}
		rb.Bot.ProcessUpdate(context.Background(), u)
		if got := rb.LastSent().Text(); got != msgNotYourself {
			t.Errorf("%s from an anonymous admin = %q", cmd, got)
		}
	}
}

// legacyWordle seeds the retired wordle module's collection.
func legacyWordle(t *testing.T, coll storage.Collection) {
	t.Helper()
	ctx := context.Background()
	stats := storage.Typed[legacyStats](coll)
	at := int64(1_700_000_000_000)
	for key, st := range map[string]legacyStats{
		"stats:1":    {Played: 10, Wins: 7, Streak: 3, BestStreak: 5, LastResultAt: &at},
		"stats:2":    {Played: 4, Wins: 1},
		"stats:-100": {Played: 50, Wins: 40, Streak: 9, BestStreak: 9},
	} {
		if err := stats.Put(ctx, key, st); err != nil {
			t.Fatal(err)
		}
	}
	games := storage.Typed[legacyGame](coll)
	game := func(target string, solved, giveup bool, words ...string) legacyGame {
		g := legacyGame{Target: target, Guesses: []legacyGuess{}, Solved: solved, Giveup: giveup, StartedAt: 123}
		for _, w := range words {
			g.Guesses = append(g.Guesses, legacyGuess{Word: w, Results: wordlist.Compare(w, target)})
		}
		return g
	}
	for key, g := range map[string]legacyGame{
		"game:1":    game("crane", false, false, "slate", "abbey"),
		"game:3":    game("crane", true, false, "crane"),
		"game:4":    game("crane", false, true),
		"game:5":    game("crane", false, false),
		"game:-100": game("crane", false, false, "slate"),
	} {
		if err := games.Put(ctx, key, g); err != nil {
			t.Fatal(err)
		}
	}
}

// assertLegacyMigration runs the migration twice over provider and checks
// what it carried over.
func assertLegacyMigration(t *testing.T, provider storage.Provider) {
	t.Helper()
	ctx := context.Background()
	legacy, coll, system := provider.Collection(LegacyShortName), provider.Collection(ShortName), provider.Collection("system")
	legacyWordle(t, legacy)
	// Player 2 already has unlimited stats: they are kept.
	ustats := storage.Typed[guessgame.RoundStats](coll)
	if err := ustats.Put(ctx, guessgame.RoundStatsKey(2), guessgame.RoundStats{Played: 1, Wins: 1, Dist: []int{1, 0, 0, 0, 0, 0}, LastSeq: 1}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := InitStore(ctx, legacy, coll, system); err != nil {
			t.Fatal(err)
		}
	}
	st, _, err := ustats.Get(ctx, guessgame.RoundStatsKey(1))
	if err != nil || st.Played != 10 || st.Wins != 7 || st.CurStreak != 3 || st.MaxStreak != 5 || len(st.Dist) != 6 || st.LastAt != 1_700_000_000_000 {
		t.Fatalf("player 1 stats = %+v, %v", st, err)
	}
	if st, _, _ := ustats.Get(ctx, guessgame.RoundStatsKey(2)); st.Played != 1 {
		t.Fatalf("existing stats overwritten: %+v", st)
	}
	keys, _ := storage.Typed[guessgame.Round](coll).List(ctx, "u")
	if got := strings.Join(keys, " "); got != "uround:1 uround:5 ustats:1 ustats:2" {
		t.Fatalf("migrated keys = %s", got)
	}
	rd, _, err := storage.Typed[guessgame.Round](coll).Get(ctx, guessgame.RoundKey(1))
	if err != nil || rd.Seq != 1 || rd.Target != "crane" || rd.Status != guessgame.StatusPlaying || rd.MaxGuesses != 6 || rd.StartedAt != 123 ||
		len(rd.Guesses) != 2 || rd.Guesses[0] != (guessgame.Guess{Word: "slate", Marks: "wwcwc"}) {
		t.Fatalf("player 1 round = %+v, %v", rd, err)
	}
	if rd, _, _ := storage.Typed[guessgame.Round](coll).Get(ctx, guessgame.RoundKey(5)); rd.StartedAt != 0 {
		t.Fatalf("an unguessed round has a clock: %+v", rd)
	}
	rec, _, err := storage.Typed[struct {
		Status string `json:"status" bson:"status"`
		Count  int64  `json:"count" bson:"count"`
	}](system).Get(ctx, legacyMigrationKey)
	if err != nil || rec.Status != "complete" || rec.Count != 3 {
		t.Fatalf("marker = %+v, %v", rec, err)
	}
	// Legacy documents are kept as they were.
	if keys, _ := storage.Typed[legacyStats](legacy).List(ctx, ""); len(keys) != 8 {
		t.Fatalf("legacy keys = %v", keys)
	}
}

func TestInitStore_CarriesLegacyWordlePlayersOnce(t *testing.T) {
	assertLegacyMigration(t, storage.NewMemoryProvider())
}

// After the marker, legacy data written later is not carried.
func TestInitStore_CompletedMarkerSkips(t *testing.T) {
	p := storage.NewMemoryProvider()
	ctx := context.Background()
	if err := InitStore(ctx, p.Collection(LegacyShortName), p.Collection(ShortName), p.Collection("system")); err != nil {
		t.Fatal(err)
	}
	legacyWordle(t, p.Collection(LegacyShortName))
	if err := InitStore(ctx, p.Collection(LegacyShortName), p.Collection(ShortName), p.Collection("system")); err != nil {
		t.Fatal(err)
	}
	if keys, _ := storage.Typed[guessgame.Round](p.Collection(ShortName)).List(ctx, ""); len(keys) != 0 {
		t.Fatalf("migrated after the marker: %v", keys)
	}
}

// The migrated stats carry on: the next win continues the streak.
func TestInitStore_MigratedStatsContinue(t *testing.T) {
	h := newHarness(t)
	p := storage.NewMemoryProvider()
	legacyWordle(t, p.Collection(LegacyShortName))
	if err := InitStore(context.Background(), p.Collection(LegacyShortName), h.coll, p.Collection("system")); err != nil {
		t.Fatal(err)
	}
	tok := h.unlimitedToken(1, 1, 5, "A")
	if r := h.ustate(tok); len(r.v.Guesses) != 2 {
		t.Fatalf("migrated round = %s", r.body)
	}
	r := h.uguess(tok, 1, "crane")
	if s := r.v.Stats; s == nil || s.Played != 11 || s.Cur != 4 || s.Max != 5 || s.Dist[2] != 1 || s.WinPct != 73 {
		t.Fatalf("stats after the migrated round = %s", r.body)
	}
}
