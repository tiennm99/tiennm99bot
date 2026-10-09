package noitu

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

func TestPvPCommand_RegistersCardKeepingTopic(t *testing.T) {
	h := newHarness(t, testCorpus)
	rb := installModule(t, h.mod)
	update := testutil.NewSupergroupMessage(-100777, 42, "/noitu")
	update.Message.MessageThreadID = 12
	rb.Bot.ProcessUpdate(context.Background(), update)

	last := rb.LastSent()
	if last.Method != "sendGame" || last.Form["game_short_name"] != "noitu" || last.ChatID() != "-100777" || last.Form["message_thread_id"] != "12" {
		t.Fatalf("sent %+v", rb.Sent())
	}
	card, _, err := h.svc.cfg.cards.Get(context.Background(), cardKey(-100777, 1))
	if err != nil {
		t.Fatalf("card not registered: %v", err)
	}
	want := pvpCard{ChatID: -100777, MessageID: 1, ThreadID: 12, TouchedAt: h.clock.now().Unix()}
	if card != want {
		t.Fatalf("card = %+v, want %+v", card, want)
	}
}

func TestPvPCommand_RefusesPrivateChatsAndChannels(t *testing.T) {
	h := newHarness(t, testCorpus)
	for name, update := range map[string]*models.Update{
		"private": testutil.NewPrivateMessage(42, "/noitu"),
		"channel": testutil.NewChannelMessage(-100888, "/noitu"),
	} {
		rb := installModule(t, h.mod)
		rb.Bot.ProcessUpdate(context.Background(), update)
		if last := rb.LastSent(); last.Method != "sendMessage" || last.Text() != msgPvPNeedsGroup || len(rb.Sent()) != 1 {
			t.Errorf("%s: sent %+v", name, rb.Sent())
		}
	}
}

// A card that cannot be recorded would play solo while looking like a room,
// so it is deleted again and the chat is told.
func TestPvPCommand_StoreFailureTakesTheCardBack(t *testing.T) {
	h := newHarness(t, testCorpus)
	h.svc.cfg.cards = storage.Typed[pvpCard](storage.NewMemoryProvider().Collection("Not A Valid Name"))
	rb := installModule(t, h.svc.module())
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewGroupMessage(-100777, 42, "/noitu"))
	var methods []string
	for _, c := range rb.Sent() {
		methods = append(methods, c.Method)
	}
	if fmt.Sprint(methods) != "[sendGame deleteMessage sendMessage]" || rb.LastSent().Text() != msgPvPFail {
		t.Fatalf("sent %+v", rb.Sent())
	}

	rb = installModule(t, h.svc.module())
	rb.FailMethodCode("sendGame", 400, "Bad Request: GAME_SHORTNAME_INVALID")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewGroupMessage(-100777, 42, "/noitu"))
	if last := rb.LastSent(); last.Method != "sendMessage" || last.Text() != msgSendGameFail {
		t.Fatalf("sendGame failure: sent %+v", rb.Sent())
	}
}

// playToken presses Play on update and returns the claims of the answered URL.
func (h *harness) playToken(update *models.Update) claims {
	h.t.Helper()
	rb := installModule(h.t, h.mod)
	rb.Bot.ProcessUpdate(context.Background(), update)
	u, err := url.Parse(lastAnswer(h.t, rb)["url"])
	if err != nil {
		h.t.Fatal(err)
	}
	c, err := verifyToken(testKey, u.Query().Get("t"), h.clock.now())
	if err != nil {
		h.t.Fatalf("token: %v", err)
	}
	return c
}

func TestPlay_OnACardIssuesAPvPToken(t *testing.T) {
	h := newHarness(t, testCorpus)
	ctx := context.Background()
	if err := h.svc.cfg.cards.Put(ctx, cardKey(-100500, 7), pvpCard{ChatID: -100500, MessageID: 7, ThreadID: 3, TouchedAt: h.clock.now().Unix()}); err != nil {
		t.Fatal(err)
	}
	c := h.playToken(testutil.NewGameCallback(42, -100500, 7, ShortName))
	if !c.PvP || c.ThreadID != 3 || c.ChatID != -100500 || c.MessageID != 7 || c.UserID != 42 {
		t.Fatalf("card claims = %+v", c)
	}
	// A forwarded copy is another message, and an inline game is never a
	// card: both play solo.
	if c := h.playToken(testutil.NewGameCallback(42, -100500, 8, ShortName)); c.PvP {
		t.Fatalf("unregistered message gave a pvp token: %+v", c)
	}
	if c := h.playToken(testutil.NewInlineGameCallback(42, "AgAAAInline", ShortName)); c.PvP {
		t.Fatalf("inline message gave a pvp token: %+v", c)
	}
	// The noitubot game always plays solo, whatever is stored for its message.
	if c := h.playToken(testutil.NewGameCallback(42, -100500, 7, SoloShortName)); c.PvP {
		t.Fatalf("noitubot game gave a pvp token: %+v", c)
	}
}

func TestPlay_CardLookupFailureAnswersAlert(t *testing.T) {
	h := newHarness(t, testCorpus)
	h.svc.cfg.cards = storage.Typed[pvpCard](storage.NewMemoryProvider().Collection("Not A Valid Name"))
	rb := installModule(t, h.svc.module())
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewGameCallback(42, -100500, 7, ShortName))
	if a := lastAnswer(t, rb); a["text"] != msgCardLookup || a["show_alert"] != "true" || a["url"] != "" {
		t.Fatalf("answer = %v", a)
	}
}

func TestPvPToken_NeedsAChatMessageAndIsRefusedForSolo(t *testing.T) {
	h := newHarness(t, testCorpus)
	exp := h.clock.now().Add(time.Hour).Unix()
	inline, _ := signToken(testKey, claims{UserID: 1, InlineID: "x", PvP: true, Expiry: exp})
	if _, err := verifyToken(testKey, inline, h.clock.now()); err == nil {
		t.Fatal("a pvp token on an inline message verified")
	}
	rec := h.post("start", fmt.Sprintf(`{"token":%q}`, h.pvpToken(42, "An")))
	if rec.Code != http.StatusBadRequest || h.apiError(rec) != "bad_request" {
		t.Fatalf("solo start with a pvp token: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCards_PlayRefreshesAndCleanupForgetsStaleCards(t *testing.T) {
	h := newHarness(t, testCorpus)
	ctx := context.Background()
	cards := h.svc.cfg.cards
	now := h.clock.now()
	old := now.Add(-cardTTL - time.Hour).Unix()
	for _, c := range []pvpCard{
		{ChatID: -1, MessageID: 1, TouchedAt: old},
		{ChatID: -1, MessageID: 2, TouchedAt: old},
		{ChatID: -1, MessageID: 3, TouchedAt: now.Unix()},
	} {
		if err := cards.Put(ctx, cardKey(c.ChatID, c.MessageID), c); err != nil {
			t.Fatal(err)
		}
	}
	// Playing card 2 keeps it alive.
	if c := h.playToken(testutil.NewGameCallback(42, -1, 2, ShortName)); !c.PvP {
		t.Fatal("stale card no longer a card before cleanup")
	}
	if err := h.svc.cleanupCards(ctx, modules.Deps{}); err != nil {
		t.Fatal(err)
	}
	keys, err := cards.List(ctx, cardKeyPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(keys) != "[pvp:-1:2 pvp:-1:3]" {
		t.Fatalf("cards after cleanup = %v", keys)
	}
}

func TestBotReporter_AnnounceRepliesToTheCardInItsTopic(t *testing.T) {
	rb := testutil.NewRecordingBot(t)
	r := botReporter{b: rb.Bot}
	if err := r.Announce(context.Background(), claims{ChatID: cardChat, MessageID: cardMsg, ThreadID: cardThread}, "<b>An</b> thắng"); err != nil {
		t.Fatalf("Announce: %v", err)
	}
	last := rb.LastSent()
	if last.Method != "sendMessage" || last.ChatID() != "-100500" || last.Form["message_thread_id"] != "3" ||
		last.Text() != "<b>An</b> thắng" || last.Form["parse_mode"] != "" ||
		!strings.Contains(last.Form["reply_parameters"], `"message_id":7`) {
		t.Fatalf("sendMessage form = %v", last.Form)
	}
}

// Cards live only in groups, so a private chat never touches the card store
// and a storage fault cannot block its solo game.
func TestPlay_PrivateChatSkipsTheCardLookup(t *testing.T) {
	h := newHarness(t, testCorpus)
	h.svc.cfg.cards = storage.Typed[pvpCard](storage.NewMemoryProvider().Collection("Not A Valid Name"))
	h.mod = h.svc.module()
	if c := h.playToken(testutil.NewGameCallback(42, 42, 7, ShortName)); c.PvP || c.ChatID != 42 {
		t.Fatalf("private chat claims = %+v", c)
	}
}
