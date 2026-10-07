package misc

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// installMisc wires the misc module to a recording bot with a fresh
// in-memory store. Returns the bot and the typed store (so tests can pre-seed
// or read), plus an Auth that permits Owner + Admin so /ping_stats /the_answer dispatch.
func installMisc(t *testing.T, ownerID int64) (*testutil.RecordingBot, storage.DocStore[lastPing]) {
	t.Helper()
	rb := testutil.NewRecordingBot(t)
	provider := storage.NewMemoryProvider()
	coll := provider.Collection("misc")
	mod := New(modules.Deps{Store: coll})
	store := storage.Typed[lastPing](coll)

	reg := &modules.Registry{
		Modules:     []modules.Module{{Name: "misc", Commands: mod.Commands}},
		AllCommands: map[string]modules.Command{},
	}
	for _, c := range mod.Commands {
		reg.AllCommands[c.Name] = c
	}
	auth := modules.Auth{BotOwnerID: ownerID}
	modules.Install(rb.Bot, reg, auth)
	return rb, store
}

func TestPing_RepliesPongAndWritesStore(t *testing.T) {
	rb, store := installMisc(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(999, "/ping"))

	if got := rb.LastSent().Text(); got != "pong" {
		t.Errorf("ping reply = %q, want %q", got, "pong")
	}
	stored, _, err := store.Get(context.Background(), lastPingKey)
	if err != nil {
		t.Fatalf("expected lastPing in store: %v", err)
	}
	if stored.At <= 0 {
		t.Errorf("lastPing.At = %d, want positive", stored.At)
	}
	// Sanity: timestamp is within a minute of now (rules out stale fixture).
	if delta := time.Now().UTC().UnixMilli() - stored.At; delta > 60_000 || delta < 0 {
		t.Errorf("lastPing.At delta from now = %dms, want within 60s", delta)
	}
}

func TestPingStats_NeverWhenStoreEmpty(t *testing.T) {
	rb, _ := installMisc(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(999, "/ping_stats"))

	if got := rb.LastSent().Text(); got != "last ping: never" {
		t.Errorf("ping_stats reply = %q, want 'last ping: never'", got)
	}
}

func TestPingStats_AfterPing(t *testing.T) {
	rb, _ := installMisc(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(999, "/ping"))
	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(999, "/ping_stats"))

	got := rb.LastSent().Text()
	if !strings.HasPrefix(got, "last ping: ") {
		t.Errorf("ping_stats reply = %q, want 'last ping: ...'", got)
	}
	if strings.Contains(got, "never") {
		t.Errorf("ping_stats still says 'never' after /ping: %q", got)
	}
}

func TestPingStats_DeniedToNonAdmin(t *testing.T) {
	rb, _ := installMisc(t, 999) // owner = 999
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/ping_stats"))

	if calls := rb.Sent(); len(calls) != 0 {
		t.Errorf("non-admin /ping_stats produced replies: %+v", calls)
	}
}

// messageFrom is the inline counterpart of testutil.NewPrivateMessage
// for cases that need control over From (username, names). The dispatcher
// requires a bot_command entity, so we lift that from the helper API by reusing
// NewPrivateMessage and overwriting From.
func messageFrom(t *testing.T, text string, from *models.User) *models.Update {
	t.Helper()
	u := testutil.NewPrivateMessage(from.ID, text)
	u.Message.From = from
	return u
}

func TestTrongTruongHop_DefaultText(t *testing.T) {
	rb, _ := installMisc(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), messageFrom(t, "/trongtruonghop",
		&models.User{ID: 7, Username: "boss", FirstName: "Boss"}))

	got := rb.LastSent().Text()
	want := fmt.Sprintf(trongTruongHopTemplate, defaultTarget, "@boss", "@boss")
	if got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

func TestTrongTruongHop_CustomArg(t *testing.T) {
	rb, _ := installMisc(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), messageFrom(t, "/trongtruonghop Acme Corp",
		&models.User{ID: 7, Username: "boss", FirstName: "Boss"}))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "Acme Corp") {
		t.Errorf("reply missing custom arg Acme Corp: %q", got)
	}
	if strings.Contains(got, defaultTarget) {
		t.Errorf("reply unexpectedly contains default target: %q", got)
	}
	if n := strings.Count(got, "@boss"); n != 2 {
		t.Errorf("reply mentions @boss %d times, want 2: %q", n, got)
	}
}

func TestTTHAlias_CustomArg(t *testing.T) {
	rb, _ := installMisc(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), messageFrom(t, "/tth Acme Corp",
		&models.User{ID: 7, Username: "boss", FirstName: "Boss"}))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "Acme Corp") {
		t.Errorf("reply missing custom arg Acme Corp: %q", got)
	}
	if n := strings.Count(got, "@boss"); n != 2 {
		t.Errorf("reply mentions @boss %d times, want 2: %q", n, got)
	}
}

func TestTrongTruongHop_HTMLEscapesArg(t *testing.T) {
	rb, _ := installMisc(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), messageFrom(t, "/trongtruonghop <script>",
		&models.User{ID: 7, Username: "boss", FirstName: "Boss"}))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("reply did not HTML-escape arg: %q", got)
	}
	if strings.Contains(got, "<script>") {
		t.Errorf("reply leaked raw <script>: %q", got)
	}
}

func TestTrongTruongHopVNG_DefaultText(t *testing.T) {
	rb, _ := installMisc(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), messageFrom(t, "/trongtruonghopvng ignored arg",
		&models.User{ID: 7, Username: "boss", FirstName: "Boss"}))

	got := rb.LastSent().Text()
	want := fmt.Sprintf(trongTruongHopTemplate, vngTarget, "@boss", "@boss")
	if got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

func TestTTHVNGAlias_DefaultText(t *testing.T) {
	rb, _ := installMisc(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), messageFrom(t, "/tthvng ignored arg",
		&models.User{ID: 7, Username: "boss", FirstName: "Boss"}))

	got := rb.LastSent().Text()
	want := fmt.Sprintf(trongTruongHopTemplate, vngTarget, "@boss", "@boss")
	if got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

func TestTrongTruongHop_NoUsernameFallsBackToDisplayNameMention(t *testing.T) {
	rb, _ := installMisc(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), messageFrom(t, "/trongtruonghop",
		&models.User{ID: 42, FirstName: "Anh", LastName: "Le"}))

	got := rb.LastSent().Text()
	wantLink := `<a href="tg://user?id=42">Anh Le</a>`
	if n := strings.Count(got, wantLink); n != 2 {
		t.Errorf("reply contains display-name mention %q %d times, want 2: %q", wantLink, n, got)
	}
}

func TestTrongTruongHopVNG_NoUsernameFallsBackToDisplayNameMention(t *testing.T) {
	rb, _ := installMisc(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), messageFrom(t, "/trongtruonghopvng ignored arg",
		&models.User{ID: 42, FirstName: "Anh", LastName: "Le"}))

	got := rb.LastSent().Text()
	wantLink := `<a href="tg://user?id=42">Anh Le</a>`
	if n := strings.Count(got, wantLink); n != 2 {
		t.Errorf("reply contains display-name mention %q %d times, want 2: %q", wantLink, n, got)
	}
}

func TestFF_DeniedToNonAdmin(t *testing.T) {
	rb, _ := installMisc(t, 999)

	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/ff"))
	if calls := rb.Sent(); len(calls) != 0 {
		t.Errorf("non-admin /ff replied: %+v", calls)
	}
}

func TestFF_RepliesTemplate(t *testing.T) {
	rb, _ := installMisc(t, 999)

	// Args are ignored — the reply is the same canned rant every time.
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(999, "/ff ignored arg"))
	if got := rb.LastSent().Text(); got != ffTemplate {
		t.Errorf("/ff reply = %q, want the ff template", got)
	}
}

// The template signs off with an uppercase /FF, so tapping that link must reach
// the same handler the lowercase form does.
func TestFF_UppercaseFormDispatches(t *testing.T) {
	rb, _ := installMisc(t, 999)

	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(999, "/FF"))
	if got := rb.LastSent().Text(); got != ffTemplate {
		t.Errorf("/FF reply = %q, want the ff template", got)
	}
}

// Public, unlike its /ff counterpart — anyone in the group can be made to
// file the paperwork.
func TestXLT1_AllowedToNonAdmin(t *testing.T) {
	rb, _ := installMisc(t, 999)

	rb.Bot.ProcessUpdate(context.Background(), messageFrom(t, "/xlt1",
		&models.User{ID: 7, Username: "nobody"}))
	if got := rb.LastSent().Text(); got != fmt.Sprintf(xlt1Template, "@nobody", "@nobody") {
		t.Errorf("non-admin /xlt1 reply = %q, want the xlt1 petition", got)
	}
}

func TestXLT1_RepliesPetitionWithSenderMention(t *testing.T) {
	rb, _ := installMisc(t, 999)

	// Args are ignored — only the sender mention is substituted, into both the
	// "Tôi tên là" field and the signature block.
	rb.Bot.ProcessUpdate(context.Background(), messageFrom(t, "/xlt1 ignored arg",
		&models.User{ID: 999, Username: "miti99"}))

	got := rb.LastSent().Text()
	if want := fmt.Sprintf(xlt1Template, "@miti99", "@miti99"); got != want {
		t.Errorf("/xlt1 reply = %q, want the xlt1 petition", got)
	}
	if n := strings.Count(got, "@miti99"); n != 2 {
		t.Errorf("reply mentions sender %d times, want 2: %q", n, got)
	}
}

// Rendered with parse_mode HTML for the tg://user mention, so any raw <, > or &
// left in the prose would make Telegram reject the send.
func TestXLT1_TemplateHasNoUnescapedHTML(t *testing.T) {
	for _, ch := range []string{"<", ">", "&"} {
		if strings.Contains(xlt1Template, ch) {
			t.Errorf("xlt1Template contains raw %q, unsafe for parse_mode HTML", ch)
		}
	}
}

func TestTheAnswer_OwnerOnly(t *testing.T) {
	rb, _ := installMisc(t, 999)

	// Non-owner: silent denial
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/the_answer"))
	if calls := rb.Sent(); len(calls) != 0 {
		t.Errorf("non-owner /the_answer replied: %+v", calls)
	}

	// Owner: reply
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(999, "/the_answer"))
	if got := rb.LastSent().Text(); got != "The answer." {
		t.Errorf("owner /the_answer reply = %q, want 'The answer.'", got)
	}
}
