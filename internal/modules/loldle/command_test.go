package loldle

import (
	"context"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// chatOnly is the module without the web game: in-chat play only.
func chatOnly(h *harness) *service {
	return newService(config{store: h.coll, now: h.clock.now, champions: loadChampions(), pick: testPick})
}

func TestModule_Registration(t *testing.T) {
	mod := newService(config{champions: loadChampions()}).module()
	var names []string
	for _, c := range mod.Commands {
		names = append(names, c.Name)
		want := modules.VisibilityPublic
		if c.Name == "loldle_setmax" {
			want = modules.VisibilityPrivate
		}
		if c.Visibility != want || c.Description == "" {
			t.Errorf("command %+v", c)
		}
	}
	if strings.Join(names, " ") != "loldle loldle_giveup loldle_stats loldle_setmax loldledaily loldledaily_subscribe loldledaily_unsubscribe" {
		t.Fatalf("commands = %v", names)
	}
	if mod.Commands[0].Parameters != "[champion]" {
		t.Fatalf("parameters = %q", mod.Commands[0].Parameters)
	}
	if len(mod.Games) != 1 || mod.Games[0].ShortName != "loldle" || len(mod.HTTP) != 0 || len(mod.Crons) != 0 {
		t.Fatalf("disabled module: %+v", mod)
	}
	h := newHarness(t)
	mod = h.svc.module()
	if mod.HTTP[0].Pattern != "/games/loldle/" || mod.Crons[0].Name != "loldledaily_daily_push" || mod.Crons[0].Schedule != "0 0 * * *" ||
		mod.Crons[1].Name != "loldle_unlimited_cards" || mod.Crons[1].Schedule != "40 20 * * *" {
		t.Fatalf("enabled module: %+v %+v", mod.HTTP, mod.Crons)
	}
}

func TestNew_FromEnv(t *testing.T) {
	t.Setenv("GAME_BASE_URL", "https://game.example/")
	t.Setenv(secretEnv, "")
	rb := testutil.NewRecordingBot(t)
	if mod := New(modules.Deps{Bot: rb.Bot, Store: storage.NewMemoryProvider().Collection(ShortName)}); len(mod.HTTP) != 1 {
		t.Fatalf("routes = %+v", mod.HTTP)
	}
	if mod := New(modules.Deps{Store: storage.NewMemoryProvider().Collection(ShortName)}); len(mod.HTTP) != 0 {
		t.Fatal("enabled without a bot or secret")
	}
	t.Setenv(secretEnv, "short")
	if mod := New(modules.Deps{Bot: rb.Bot, Store: storage.NewMemoryProvider().Collection(ShortName)}); len(mod.HTTP) != 0 {
		t.Fatal("enabled with a short secret")
	}
}

func TestLoldle_NoArgSendsARecordedCard(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module(), 0)
	send(t, rb, groupChat, 1, "/loldle")
	games := sentMethod(rb, "sendGame")
	if len(games) != 1 || games[0].Form["game_short_name"] != GameShortName || games[0].ChatID() != "-100" {
		t.Fatalf("sent %+v", rb.Sent())
	}
	docs, _ := storage.Typed[guessgame.CardRecord](h.coll).Scan(context.Background(), "ucard:")
	if len(docs) != 1 || docs[0].Val.ChatID != groupChat {
		t.Fatalf("cards = %+v", docs)
	}
	// Play on it opens unlimited mode; any other card plays daily.
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewGameCallback(1, groupChat, docs[0].Val.MessageID, GameShortName))
	answers := sentMethod(rb, "answerCallbackQuery")
	if len(answers) != 1 || !strings.HasPrefix(answers[0].Form["url"], testBase+routePrefix+"?t=") {
		t.Fatalf("answers = %+v", answers)
	}
	tok := strings.TrimPrefix(answers[0].Form["url"], testBase+routePrefix+"?t=")
	if c, err := h.svc.verifyToken(urlUnescape(t, tok), h.clock.now()); err != nil || !c.Unlimited() {
		t.Fatalf("claims = %+v, %v", c, err)
	}
	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewGameCallback(1, groupChat, 999, GameShortName))
	tok = strings.TrimPrefix(sentMethod(rb, "answerCallbackQuery")[0].Form["url"], testBase+routePrefix+"?t=")
	if c, _ := h.svc.verifyToken(urlUnescape(t, tok), h.clock.now()); c.Unlimited() {
		t.Fatalf("unrecorded card opened unlimited: %+v", c)
	}
}

func TestLoldle_NoArgWithoutTheWebGameShowsTheBoard(t *testing.T) {
	h := newHarness(t)
	rb := install(t, chatOnly(h).module(), 0)
	if got := send(t, rb, 1, 1, "/loldle"); !strings.Contains(got, "Guess 0/8") || !strings.Contains(got, "No guesses yet") {
		t.Fatalf("board = %q", got)
	}
	send(t, rb, 1, 1, "/loldle akali")
	if got := send(t, rb, 1, 1, "/loldle"); !strings.Contains(got, "Guess 1/8") || !strings.Contains(got, "AKALI") || !strings.Contains(got, "2010 ⬆️") {
		t.Fatalf("board = %q", got)
	}
	if len(sentMethod(rb, "sendGame")) != 0 {
		t.Fatal("disabled game sent a card")
	}
}

func TestLoldle_GuessesPlayTheCallersRound(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module(), 0)
	if got := send(t, rb, groupChat, 1, "/loldle akali"); !strings.Contains(got, "AKALI") || !strings.HasSuffix(got, "Guess 1/8.") {
		t.Fatalf("guess = %q", got)
	}
	if got := send(t, rb, groupChat, 1, "/loldle zzzz"); got != `Champion not found: "zzzz".` {
		t.Fatalf("unknown = %q", got)
	}
	if got := send(t, rb, groupChat, 1, "/loldle ka"); got != `Ambiguous champion "ka". Type the full champion name.` {
		t.Fatalf("ambiguous = %q", got)
	}
	if got := send(t, rb, groupChat, 1, "/loldle AKALI"); !strings.Contains(got, "<b>Akali</b> was already guessed") {
		t.Fatalf("duplicate = %q", got)
	}
	// Bob in the same group has his own round.
	got := send(t, rb, groupChat, 2, "/loldle ahri")
	if !strings.Contains(got, "🎉 First try! Ahri") || !strings.Contains(got, "🔥 Streak: 1 (1/8)") || len(sentMethod(rb, "sendSticker")) != 1 {
		t.Fatalf("bob = %q", got)
	}
	// The page and the chat share Alice's round.
	h.roundGuess(h.token(1, groupChat, 5, "Alice", true), 1, "Wukong")
	got = send(t, rb, groupChat, 1, "/loldle ahri")
	if !strings.Contains(got, "🎉") || !strings.Contains(got, "(3/8)") {
		t.Fatalf("alice = %q", got)
	}
	// After a finish the next guess opens the next round (Jinx).
	if got := send(t, rb, groupChat, 1, "/loldle ahri"); !strings.HasSuffix(got, "Guess 1/8.") {
		t.Fatalf("next round = %q", got)
	}
}

func TestLoldle_LossAndGiveup(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module(), 7)
	send(t, rb, 7, 7, "/loldle_setmax 2")
	if got := send(t, rb, 7, 7, "/loldle_giveup"); !strings.Contains(got, "No active round") {
		t.Fatalf("no round = %q", got)
	}
	send(t, rb, 7, 7, "/loldle akali")
	got := send(t, rb, 7, 7, "/loldle wukong")
	if !strings.Contains(got, "❌ Out of guesses. Answer was Ahri.") || len(sentMethod(rb, "sendSticker")) != 1 {
		t.Fatalf("loss = %q", got)
	}
	if got := send(t, rb, 7, 7, "/loldle_giveup"); !strings.Contains(got, "No active round") {
		t.Fatalf("giveup after a loss = %q", got)
	}
	send(t, rb, 7, 7, "/loldle akali") // round 2: Jinx
	if got := send(t, rb, 7, 7, "/loldle_giveup"); !strings.HasPrefix(got, "🏳️ Answer was Jinx.") || len(sentMethod(rb, "sendSticker")) != 1 {
		t.Fatalf("giveup = %q", got)
	}
	if got := send(t, rb, 7, 7, "/loldle_stats"); got != "📊 LoLdle stats for Test\nPlayed: 2\nWins: 0 (0%)\nCurrent streak: 0\nBest streak: 0" {
		t.Fatalf("stats = %q", got)
	}
}

func TestLoldleSetMax_AppliesToRoundsStartedInThatChatOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	rb := install(t, h.svc.module(), 7)
	send(t, rb, groupChat, 1, "/loldle akali") // starts with the default 8
	if got := send(t, rb, groupChat, 7, "/loldle_setmax 3"); got != "✅ Loldle max guesses set to 3 (applies to the next round)." {
		t.Fatalf("setmax = %q", got)
	}
	if got := send(t, rb, groupChat, 1, "/loldle wukong"); !strings.HasSuffix(got, "Guess 2/8.") {
		t.Fatalf("running round = %q", got)
	}
	send(t, rb, groupChat, 1, "/loldle_giveup")
	if got := send(t, rb, groupChat, 1, "/loldle akali"); !strings.HasSuffix(got, "Guess 1/3.") {
		t.Fatalf("next round = %q", got)
	}
	// A card from the group starts rounds of 3 too; a private card keeps 8.
	if r := h.post("new", map[string]any{"token": h.token(1, groupChat, 5, "A", true), "seq": 2}); r.v.Max != 3 {
		t.Fatalf("group card round = %s", r.body)
	}
	if r := h.state(h.token(2, 2, 5, "B", true)); r.v.Max != 8 {
		t.Fatalf("private card round = %s", r.body)
	}
	for _, arg := range []string{"0", "11", "x"} {
		if got := send(t, rb, groupChat, 7, "/loldle_setmax "+arg); got != "Usage: /loldle_setmax <1-10>" {
			t.Errorf("setmax %s = %q", arg, got)
		}
	}
	// Only the owner may run it.
	rb.Reset()
	rb.Bot.ProcessUpdate(ctx, testutil.NewGroupMessage(groupChat, 1, "/loldle_setmax 5"))
	if n, _ := getMaxGuesses(ctx, h.svc.settings, "-100"); n != 3 {
		t.Fatalf("non-owner changed setmax to %d", n)
	}
}

func TestLoldle_ChannelRefused(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module(), 0)
	for _, cmd := range []string{"/loldle", "/loldle_giveup", "/loldle_stats"} {
		rb.Reset()
		rb.Bot.ProcessUpdate(context.Background(), testutil.NewChannelMessage(-300, cmd))
		if got := rb.LastSent().Text(); got != msgChannel {
			t.Errorf("%s = %q", cmd, got)
		}
	}
	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewChannelMessage(-300, "/loldledaily"))
	if got := rb.LastSent().Text(); got != msgDailyChannel {
		t.Errorf("/loldledaily = %q", got)
	}
}

// Anonymous admins and posts on behalf of a chat arrive from one shared
// stand-in bot id, so they are refused instead of sharing one round.
func TestLoldle_RefusesStandInSenders(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module(), 0)
	for _, cmd := range []string{"/loldle Ahri", "/loldle_giveup", "/loldle_stats"} {
		rb.Reset()
		u := testutil.NewGroupMessage(-100, 136817688, cmd)
		u.Message.From.IsBot = true
		u.Message.SenderChat = &models.Chat{ID: -1001, Type: models.ChatTypeChannel}
		rb.Bot.ProcessUpdate(context.Background(), u)
		if got := rb.LastSent().Text(); got != msgNotYourself {
			t.Errorf("%s on behalf of a channel = %q", cmd, got)
		}
	}
}

// A round whose answer left the data is replaced without counting.
func TestLoldle_TargetGoneStartsANewRound(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	rounds := storage.Typed[guessgame.Round](h.coll)
	if err := rounds.Put(ctx, guessgame.RoundKey(1), guessgame.Round{Seq: 4, Target: "Nobody", Guesses: []guessgame.Guess{}, Status: guessgame.StatusPlaying, MaxGuesses: 8}); err != nil {
		t.Fatal(err)
	}
	rb := install(t, h.svc.module(), 0)
	if got := send(t, rb, 1, 1, "/loldle ahri"); !strings.HasPrefix(got, "Champion data was updated since this round started.") {
		t.Fatalf("reply = %q", got)
	}
	rd, _, _ := rounds.Get(ctx, guessgame.RoundKey(1))
	st, _ := h.svc.rounds.LoadStats(ctx, 1)
	if rd.Seq != 5 || rd.Target != "Ahri" || st.Played != 0 {
		t.Fatalf("round %+v stats %+v", rd, st)
	}
}
