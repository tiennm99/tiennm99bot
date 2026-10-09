package wordledaily

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/subscription"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// install registers mod through the real registry and dispatcher.
func install(t *testing.T, mod modules.Module) *testutil.RecordingBot {
	t.Helper()
	rb := testutil.NewRecordingBot(t)
	reg, err := modules.Build([]string{ShortName}, map[string]modules.Factory{
		ShortName: func(modules.Deps) modules.Module { return mod },
	}, storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	modules.Install(rb.Bot, reg, modules.Auth{})
	return rb
}

func answerForm(t *testing.T, rb *testutil.RecordingBot) map[string]string {
	t.Helper()
	calls := sentMethod(rb, "answerCallbackQuery")
	if len(calls) == 0 {
		t.Fatalf("no answerCallbackQuery; sent %+v", rb.Sent())
	}
	return calls[len(calls)-1].Form
}

func TestModule_Registration(t *testing.T) {
	mod := newService(config{}).module()
	var names []string
	for _, c := range mod.Commands {
		names = append(names, c.Name)
		if c.Visibility != modules.VisibilityPublic || c.Description == "" || c.Parameters != "" {
			t.Errorf("command %+v is not a described public command", c)
		}
	}
	if strings.Join(names, " ") != "wordledaily wordledaily_subscribe wordledaily_unsubscribe" {
		t.Fatalf("commands = %v", names)
	}
	if len(mod.Games) != 1 || mod.Games[0].ShortName != "wordle" {
		t.Fatalf("games = %+v", mod.Games)
	}
	if len(mod.HTTP) != 0 || len(mod.Crons) != 0 {
		t.Fatalf("disabled module exposes routes %v / crons %v", mod.HTTP, mod.Crons)
	}
	h := newHarness(t)
	if mod := h.svc.module(); mod.HTTP[0].Pattern != "/games/wordledaily/" || mod.Crons[0].Name != "wordledaily_daily_push" || mod.Crons[0].Schedule != "0 0 * * *" {
		t.Fatalf("enabled module: %+v %+v", mod.HTTP, mod.Crons)
	}
}

func TestNew_FromEnv(t *testing.T) {
	t.Setenv("GAME_BASE_URL", "https://game.example/")
	t.Setenv(secretEnv, "")
	rb := testutil.NewRecordingBot(t)
	mod := New(modules.Deps{Bot: rb.Bot, Store: storage.NewMemoryProvider().Collection(ShortName)})
	if len(mod.HTTP) != 1 {
		t.Fatalf("routes = %+v", mod.HTTP)
	}
	if mod := New(modules.Deps{Store: storage.NewMemoryProvider().Collection(ShortName)}); len(mod.HTTP) != 0 {
		t.Fatal("enabled without a bot or secret")
	}
	t.Setenv(secretEnv, "short")
	if mod := New(modules.Deps{Bot: rb.Bot, Store: storage.NewMemoryProvider().Collection(ShortName)}); len(mod.HTTP) != 0 {
		t.Fatal("enabled with a short secret")
	}
	if string(rootKey(strings.Repeat("s", 32), modules.Deps{Bot: rb.Bot})) != strings.Repeat("s", 32) {
		t.Fatal("secret not preferred over the bot token")
	}
}

func TestPlay_DisabledAnswersAlert(t *testing.T) {
	rb := install(t, newService(config{}).module())
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewGameCallback(1, 1, 5, GameShortName))
	f := answerForm(t, rb)
	if f["text"] != msgDisabled || f["show_alert"] != "true" || f["url"] != "" {
		t.Fatalf("answer = %v", f)
	}
}

func decodeURLToken(t *testing.T, h *harness, raw string) claims {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || !strings.HasPrefix(raw, testBase+routePrefix+"?t=") {
		t.Fatalf("url = %q", raw)
	}
	c, err := h.svc.verifyToken(u.Query().Get("t"), h.clock.now())
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	return c
}

func TestPlay_AnswersSignedURLForEveryCardForm(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module())

	topic := testutil.NewGameCallback(42, groupChat, 9, GameShortName)
	topic.CallbackQuery.From.LastName = "Nguyen"
	topic.CallbackQuery.Message.Message.IsTopicMessage = true
	topic.CallbackQuery.Message.Message.MessageThreadID = 77
	rb.Bot.ProcessUpdate(context.Background(), topic)
	c := decodeURLToken(t, h, answerForm(t, rb)["url"])
	if c.UserID != 42 || c.ChatID != groupChat || c.MessageID != 9 || c.ThreadID != 77 || c.Name != "Test Nguyen" || c.InlineID != "" {
		t.Fatalf("topic claims = %+v", c)
	}
	if c.Expiry != h.clock.now().Add(tokenTTL).Unix() {
		t.Fatalf("expiry = %d", c.Expiry)
	}

	inaccessible := &models.Update{CallbackQuery: &models.CallbackQuery{
		ID: "q", From: models.User{ID: 42, FirstName: "A"}, GameShortName: GameShortName,
		Message: models.MaybeInaccessibleMessage{
			Type:                models.MaybeInaccessibleMessageTypeInaccessibleMessage,
			InaccessibleMessage: &models.InaccessibleMessage{Chat: models.Chat{ID: -5}, MessageID: 3},
		},
	}}
	rb.Bot.ProcessUpdate(context.Background(), inaccessible)
	if c := decodeURLToken(t, h, answerForm(t, rb)["url"]); c.ChatID != -5 || c.MessageID != 3 {
		t.Fatalf("inaccessible claims = %+v", c)
	}

	rb.Bot.ProcessUpdate(context.Background(), testutil.NewInlineGameCallback(42, "BAAAInline", GameShortName))
	if c := decodeURLToken(t, h, answerForm(t, rb)["url"]); c.InlineID != "BAAAInline" || c.ChatID != 0 {
		t.Fatalf("inline claims = %+v", c)
	}

	none := testutil.NewInlineGameCallback(42, "", GameShortName)
	rb.Bot.ProcessUpdate(context.Background(), none)
	if f := answerForm(t, rb); f["text"] != msgNoGameTarget || f["show_alert"] != "true" {
		t.Fatalf("no target = %v", f)
	}
}

func TestClaims_Valid(t *testing.T) {
	for name, c := range map[string]claims{
		"no user":         {ChatID: 1, MessageID: 1, Expiry: 1},
		"no expiry":       {UserID: 1, ChatID: 1, MessageID: 1},
		"no address":      {UserID: 1, Expiry: 1},
		"both addresses":  {UserID: 1, ChatID: 1, MessageID: 1, InlineID: "x", Expiry: 1},
		"inline + thread": {UserID: 1, InlineID: "x", ThreadID: 3, Expiry: 1},
	} {
		if c.Valid() {
			t.Errorf("%s: valid", name)
		}
	}
	if !(claims{UserID: 1, InlineID: "x", Expiry: 1}).Valid() || !(claims{UserID: 1, ChatID: -1, MessageID: 2, ThreadID: 3, Expiry: 1}).Valid() {
		t.Fatal("good claims refused")
	}
	if got := displayName(strings.Repeat("a", 70), "b"); len([]rune(got)) != maxNameRunes {
		t.Fatalf("name not truncated: %d", len(got))
	}
}

func TestCommand_SendsGameKeepingTopic(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module())
	u := testutil.NewSupergroupMessage(groupChat, 1, "/wordledaily")
	u.Message.MessageThreadID, u.Message.IsTopicMessage = 77, true
	rb.Bot.ProcessUpdate(context.Background(), u)
	games := sentMethod(rb, "sendGame")
	if len(games) != 1 || games[0].Form["game_short_name"] != GameShortName || games[0].Form["message_thread_id"] != "77" || games[0].ChatID() != "-100" {
		t.Fatalf("sent %+v", rb.Sent())
	}
	// A reply chain in an ordinary supergroup carries a thread id too, but
	// it is not a topic: the card goes to the chat.
	rb.Reset()
	u = testutil.NewSupergroupMessage(groupChat, 1, "/wordledaily")
	u.Message.MessageThreadID = 55
	rb.Bot.ProcessUpdate(context.Background(), u)
	if games := sentMethod(rb, "sendGame"); len(games) != 1 || games[0].Form["message_thread_id"] != "" {
		t.Fatalf("reply-chain card = %+v", rb.Sent())
	}
}

func TestCommand_ChannelDisabledAndFailure(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module())
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewChannelMessage(-300, "/wordledaily"))
	if last := rb.LastSent(); last.Text() != msgChannel {
		t.Fatalf("channel: %+v", rb.Sent())
	}
	rb.FailMethodCode("sendGame", 400, "Bad Request: wrong game short name")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/wordledaily"))
	if last := rb.LastSent(); last.Text() != msgSendGameFail {
		t.Fatalf("failure: %+v", rb.Sent())
	}

	rb = install(t, newService(config{}).module())
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(1, "/wordledaily"))
	if last := rb.LastSent(); last.Text() != msgDisabled || len(sentMethod(rb, "sendGame")) != 0 {
		t.Fatalf("disabled: %+v", rb.Sent())
	}
}

func TestSubscribeAndUnsubscribe(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module())
	ctx := context.Background()
	send := func(text string, thread int) string {
		u := testutil.NewSupergroupMessage(groupChat, 1, text)
		u.Message.MessageThreadID, u.Message.IsTopicMessage = thread, thread != 0
		rb.Bot.ProcessUpdate(ctx, u)
		return rb.LastSent().Text()
	}
	if got := send("/wordledaily_subscribe", 77); !strings.HasPrefix(got, "✅ Subscribed this topic to Wordle Daily: a new puzzle and yesterday's group results every day at 07:00 ICT.") {
		t.Fatalf("subscribe = %q", got)
	}
	if got := send("/wordledaily_subscribe", 77); got != "Already subscribed in this topic." {
		t.Fatalf("again = %q", got)
	}
	if got := send("/wordledaily_unsubscribe", 0); got != "This chat wasn't subscribed." {
		t.Fatalf("unsubscribe general = %q", got)
	}
	if got := send("/wordledaily_unsubscribe", 77); got != "Unsubscribed this topic." {
		t.Fatalf("unsubscribe topic = %q", got)
	}
	rb.Bot.ProcessUpdate(ctx, testutil.NewChannelMessage(-300, "/wordledaily_subscribe"))
	if got := rb.LastSent().Text(); got != msgChannel {
		t.Fatalf("channel subscribe = %q", got)
	}
	// A private chat may subscribe too; it gets the card without a recap.
	rb.Bot.ProcessUpdate(ctx, testutil.NewPrivateMessage(5, "/wordledaily_subscribe"))
	if got := rb.LastSent().Text(); !strings.HasPrefix(got, "✅ Subscribed this chat") {
		t.Fatalf("dm subscribe = %q", got)
	}
	raw, _ := json.Marshal(mustList(t, h))
	if string(raw) != `[{"chat_id":5}]` {
		t.Fatalf("subscribers = %s", raw)
	}
}

// A subscription made by replying in an ordinary supergroup is the whole
// chat's, the scope its plays and group results are stored under, so the
// 07:00 push carries that chat's recap.
func TestSubscribe_ReplyChainIsTheWholeChat(t *testing.T) {
	h := newHarness(t)
	rb := install(t, h.svc.module())
	ctx := context.Background()
	reply := func(text string) string {
		u := testutil.NewSupergroupMessage(groupChat, 1, text)
		u.Message.MessageThreadID = 55 // a reply chain, not a forum topic
		rb.Bot.ProcessUpdate(ctx, u)
		return rb.LastSent().Text()
	}
	if got := reply("/wordledaily_subscribe"); !strings.HasPrefix(got, "✅ Subscribed this chat") {
		t.Fatalf("subscribe = %q", got)
	}
	if raw, _ := json.Marshal(mustList(t, h)); string(raw) != `[{"chat_id":-100}]` {
		t.Fatalf("subscribers = %s", raw)
	}
	playGroupDay(t, h, 0)
	h.nextDay()
	if err := h.svc.runDailyPush(ctx, h.rb.Bot); err != nil {
		t.Fatal(err)
	}
	sends := sentMethod(h.rb, "sendMessage")
	if len(sends) != 1 || !strings.Contains(sends[0].Text(), "yesterday's results") || sends[0].Form["message_thread_id"] != "" {
		t.Fatalf("push = %+v", h.rb.Sent())
	}
	if got := reply("/wordledaily_unsubscribe"); got != "Unsubscribed this chat." {
		t.Fatalf("unsubscribe = %q", got)
	}
}

// Without a base URL the push cron is not registered, so subscribing must
// not promise a push; leaving still works.
func TestSubscribe_DisabledGameRefuses(t *testing.T) {
	coll := storage.NewMemoryProvider().Collection(ShortName)
	svc := newService(config{store: coll})
	if svc.enabled() || len(svc.module().Crons) != 0 {
		t.Fatal("game unexpectedly enabled")
	}
	rb := install(t, svc.module())
	ctx := context.Background()
	rb.Bot.ProcessUpdate(ctx, testutil.NewSupergroupMessage(groupChat, 1, "/wordledaily_subscribe"))
	if got := rb.LastSent().Text(); got != msgDisabled {
		t.Fatalf("subscribe = %q", got)
	}
	if subs, err := subscription.List(ctx, svc.subscribers); err != nil || len(subs) != 0 {
		t.Fatalf("subscribers = %v, %v", subs, err)
	}
	if _, err := subscription.Add(ctx, svc.subscribers, groupChat, 0); err != nil {
		t.Fatal(err)
	}
	rb.Bot.ProcessUpdate(ctx, testutil.NewSupergroupMessage(groupChat, 1, "/wordledaily_unsubscribe"))
	if got := rb.LastSent().Text(); got != "Unsubscribed this chat." {
		t.Fatalf("unsubscribe = %q", got)
	}
}
