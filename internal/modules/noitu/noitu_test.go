package noitu

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/web"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

func TestToken_RoundTripTamperExpiryAndKey(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := claims{UserID: 7, Name: "An", ChatID: -100123, MessageID: 9, Expiry: now.Add(tokenTTL).Unix()}
	tok, err := signToken(testKey, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := verifyToken(testKey, tok, now)
	if err != nil || got != c {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	body, sig, _ := strings.Cut(tok, ".")
	forged, _ := signToken([]byte("another-key-another-key-another!!"), claims{UserID: 8, ChatID: 1, MessageID: 1, Expiry: c.Expiry})
	_, forgedSig, _ := strings.Cut(forged, ".")
	for name, bad := range map[string]string{
		"tampered body": strings.ToUpper(body[:4]) + body[4:] + "." + sig,
		"swapped sig":   body + "." + forgedSig,
		"no dot":        body,
		"empty":         "",
	} {
		if _, err := verifyToken(testKey, bad, now); !errors.Is(err, errBadToken) {
			t.Errorf("%s: err = %v, want errBadToken", name, err)
		}
	}
	if _, err := verifyToken([]byte("wrong-key-wrong-key-wrong-key-xx"), tok, now); !errors.Is(err, errBadToken) {
		t.Errorf("wrong key: err = %v", err)
	}
	if _, err := verifyToken(testKey, tok, now.Add(tokenTTL)); !errors.Is(err, errTokenExpired) {
		t.Errorf("expired: err = %v", err)
	}
	if _, err := verifyToken(nil, tok, now); !errors.Is(err, errBadToken) {
		t.Errorf("no key: err = %v", err)
	}
	// A signed token must still name exactly one game message.
	both, _ := signToken(testKey, claims{UserID: 7, ChatID: 1, MessageID: 1, InlineID: "x", Expiry: c.Expiry})
	neither, _ := signToken(testKey, claims{UserID: 7, Expiry: c.Expiry})
	for _, bad := range []string{both, neither} {
		if _, err := verifyToken(testKey, bad, now); !errors.Is(err, errBadToken) {
			t.Errorf("bad address accepted: %v", err)
		}
	}
}

func TestTokenKey(t *testing.T) {
	rb := testutil.NewRecordingBot(t)
	derived := tokenKey("", modules.Deps{Bot: rb.Bot})
	if len(derived) != 32 || string(derived) == "test-token" {
		t.Fatalf("derived key = %x", derived)
	}
	if string(derived) != string(deriveKey("test-token")) {
		t.Fatal("derivation is not deterministic")
	}
	if tokenKey("", modules.Deps{}) != nil {
		t.Fatal("key without a bot or secret")
	}
	secret := strings.Repeat("s", 32)
	if string(tokenKey(secret, modules.Deps{Bot: rb.Bot})) != secret {
		t.Fatal("NOITU_GAME_SECRET not preferred")
	}
	if tokenKey("short", modules.Deps{Bot: rb.Bot}) != nil {
		t.Fatal("short secret accepted")
	}
}

func TestParseBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                           "",
		"https://game.example":       "https://game.example",
		" https://game.example/ ":    "https://game.example",
		"https://game.example/bot//": "https://game.example/bot",
		"http://game.example":        "",
		"game.example":               "",
		"https://u:p@game.example":   "",
		"https://game.example/?x=1":  "",
		"https://game.example/#frag": "",
		"https:///no-host":           "",
	}
	for in, want := range cases {
		if got := parseBaseURL(in); got != want {
			t.Errorf("parseBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNew_DisabledWithoutBaseURL(t *testing.T) {
	t.Setenv(baseURLEnv, "")
	t.Setenv(secretEnv, "")
	rb := testutil.NewRecordingBot(t)
	mod := New(modules.Deps{Bot: rb.Bot})
	if len(mod.HTTP) != 0 || len(mod.Crons) != 0 {
		t.Fatalf("disabled game exposes routes %v / crons %v", mod.HTTP, mod.Crons)
	}
	var names []string
	for _, c := range mod.Commands {
		names = append(names, c.Name)
	}
	if strings.Join(names, " ") != "noitu noitubot noitutop" {
		t.Fatalf("commands = %v", names)
	}
	for _, c := range mod.Commands {
		if c.Visibility != modules.VisibilityPublic || c.Description == "" || c.Parameters != "" {
			t.Fatalf("command %+v is not a described public command", c)
		}
	}
	if len(mod.Games) != 1 || mod.Games[0].ShortName != "noitu" {
		t.Fatalf("games = %+v", mod.Games)
	}
	// /noitu registers cards even while the game is disabled, so their
	// cleanup still runs; nothing else does.
	mod = New(modules.Deps{Bot: rb.Bot, Store: storage.NewMemoryProvider().Collection("noitu")})
	if len(mod.HTTP) != 0 || len(mod.Crons) != 1 || mod.Crons[0].Name != "noitu_pvp_cards" {
		t.Fatalf("disabled game with a store: routes %v / crons %+v", mod.HTTP, mod.Crons)
	}
}

func TestNew_EnabledFromEnv(t *testing.T) {
	t.Setenv(baseURLEnv, "https://game.example/")
	t.Setenv(secretEnv, "")
	rb := testutil.NewRecordingBot(t)
	mod := New(modules.Deps{Bot: rb.Bot})
	if len(mod.HTTP) != 1 || mod.HTTP[0].Pattern != "/games/noitu/" {
		t.Fatalf("routes = %+v", mod.HTTP)
	}
	// Without a bot there is neither a derived key nor a reporter.
	if mod := New(modules.Deps{}); len(mod.HTTP) != 0 {
		t.Fatal("enabled without a bot or secret")
	}
}

// installModule registers mod through the real registry and dispatcher.
func installModule(t *testing.T, mod modules.Module) *testutil.RecordingBot {
	t.Helper()
	rb := testutil.NewRecordingBot(t)
	reg, err := modules.Build([]string{"noitu"}, map[string]modules.Factory{
		"noitu": func(modules.Deps) modules.Module { return mod },
	}, storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	modules.Install(rb.Bot, reg, modules.Auth{})
	return rb
}

func lastAnswer(t *testing.T, rb *testutil.RecordingBot) map[string]string {
	t.Helper()
	for _, c := range rb.Sent() {
		if c.Method == "answerCallbackQuery" {
			return c.Form
		}
	}
	t.Fatalf("no answerCallbackQuery; sent %+v", rb.Sent())
	return nil
}

func TestPlay_DisabledAnswersAlert(t *testing.T) {
	rb := installModule(t, newWithConfig(config{now: time.Now}))
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewGameCallback(42, 42, 7, ShortName))
	a := lastAnswer(t, rb)
	if a["text"] != msgDisabled || a["show_alert"] != "true" || a["url"] != "" {
		t.Fatalf("answer = %v", a)
	}
}

func TestPlay_EnabledAnswersSignedURL(t *testing.T) {
	h := newHarness(t, testCorpus)
	for name, update := range map[string]*models.Update{
		"chat":   testutil.NewGameCallback(42, -100500, 7, ShortName),
		"inline": testutil.NewInlineGameCallback(42, "AgAAAInline", ShortName),
	} {
		t.Run(name, func(t *testing.T) {
			rb := installModule(t, h.mod)
			rb.Bot.ProcessUpdate(context.Background(), update)
			a := lastAnswer(t, rb)
			prefix := testBase + "/games/noitu/?t="
			if !strings.HasPrefix(a["url"], prefix) {
				t.Fatalf("url = %q", a["url"])
			}
			u, _ := url.Parse(a["url"])
			c, err := verifyToken(testKey, u.Query().Get("t"), h.clock.now())
			if err != nil {
				t.Fatalf("token: %v", err)
			}
			if c.UserID != 42 || c.Name != "Test" || c.Expiry != h.clock.now().Add(tokenTTL).Unix() {
				t.Fatalf("claims = %+v", c)
			}
			if name == "chat" && (c.ChatID != -100500 || c.MessageID != 7 || c.InlineID != "") {
				t.Fatalf("chat claims = %+v", c)
			}
			if name == "inline" && (c.InlineID != "AgAAAInline" || c.ChatID != 0) {
				t.Fatalf("inline claims = %+v", c)
			}
		})
	}
}

func TestPlay_InaccessibleMessageAndNoTarget(t *testing.T) {
	h := newHarness(t, testCorpus)
	update := testutil.NewGameCallback(42, 0, 0, ShortName)
	update.CallbackQuery.Message = models.MaybeInaccessibleMessage{
		Type:                models.MaybeInaccessibleMessageTypeInaccessibleMessage,
		InaccessibleMessage: &models.InaccessibleMessage{Chat: models.Chat{ID: -5}, MessageID: 3},
	}
	rb := installModule(t, h.mod)
	rb.Bot.ProcessUpdate(context.Background(), update)
	u, _ := url.Parse(lastAnswer(t, rb)["url"])
	if c, err := verifyToken(testKey, u.Query().Get("t"), h.clock.now()); err != nil || c.ChatID != -5 || c.MessageID != 3 {
		t.Fatalf("inaccessible claims = %+v, %v", c, err)
	}

	update.CallbackQuery.Message = models.MaybeInaccessibleMessage{}
	rb = installModule(t, h.mod)
	rb.Bot.ProcessUpdate(context.Background(), update)
	if a := lastAnswer(t, rb); a["text"] != msgNoGameTarget || a["show_alert"] != "true" {
		t.Fatalf("no target answer = %v", a)
	}
}

func TestCommand_SendsGameKeepingTopic(t *testing.T) {
	rb := installModule(t, newWithConfig(config{now: time.Now}))
	update := testutil.NewSupergroupMessage(-100777, 42, "/noitubot")
	update.Message.MessageThreadID = 12
	rb.Bot.ProcessUpdate(context.Background(), update)
	last := rb.LastSent()
	if last.Method != "sendGame" || last.Form["game_short_name"] != "noitu" || last.ChatID() != "-100777" || last.Form["message_thread_id"] != "12" {
		t.Fatalf("sent %+v", rb.Sent())
	}
	if last.Form["reply_markup"] != "" {
		t.Fatalf("sendGame carries a keyboard: %q", last.Form["reply_markup"])
	}
}

func TestCommand_ChannelAndFailure(t *testing.T) {
	rb := installModule(t, newWithConfig(config{now: time.Now}))
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewChannelMessage(-100888, "/noitubot"))
	if last := rb.LastSent(); last.Method != "sendMessage" || last.Text() != msgChannel {
		t.Fatalf("channel: sent %+v", rb.Sent())
	}

	rb = installModule(t, newWithConfig(config{now: time.Now}))
	rb.FailMethodCode("sendGame", 400, "Bad Request: GAME_SHORTNAME_INVALID")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(42, "/noitubot"))
	if last := rb.LastSent(); last.Method != "sendMessage" || last.Text() != msgSendGameFail {
		t.Fatalf("failure: sent %+v", rb.Sent())
	}
}

func TestBotReporter_ChatMessageAndNotModified(t *testing.T) {
	rb := testutil.NewRecordingBot(t)
	r := botReporter{b: rb.Bot}
	if err := r.Report(context.Background(), claims{UserID: 42, ChatID: -1005, MessageID: 8}, 77); err != nil {
		t.Fatalf("Report: %v", err)
	}
	f := rb.LastSent().Form
	if rb.LastSent().Method != "setGameScore" || f["user_id"] != "42" || f["score"] != "77" || f["chat_id"] != "-1005" || f["message_id"] != "8" || f["force"] != "" {
		t.Fatalf("setGameScore form = %v", f)
	}
	rb.FailMethodCode("setGameScore", 400, "Bad Request: BOT_SCORE_NOT_MODIFIED")
	err := r.Report(context.Background(), claims{UserID: 42, ChatID: -1005, MessageID: 8}, 1)
	if err == nil || !scoreNotModified(err) {
		t.Fatalf("not-modified err = %v", err)
	}
}

var inlineScriptRe = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)

func TestWebAssets(t *testing.T) {
	for _, name := range []string{"index.html", "app.css", "app.js"} {
		if b, err := web.FS.ReadFile(name); err != nil || len(b) == 0 {
			t.Fatalf("asset %s: %v", name, err)
		}
	}
	index, _ := web.FS.ReadFile("index.html")
	for _, m := range inlineScriptRe.FindAllStringSubmatch(string(index), -1) {
		if strings.TrimSpace(m[2]) != "" || !strings.Contains(m[1], "src=") {
			t.Errorf("inline script breaks the CSP: %q", m[0])
		}
	}
	if strings.Contains(string(index), " style=") || strings.Contains(string(index), "<style") {
		t.Error("inline style breaks the CSP")
	}
	if !strings.Contains(string(index), "CC BY-SA 4.0") || !strings.Contains(string(index), "Wiktionary") {
		t.Error("dictionary attribution missing from the page footer")
	}
}
