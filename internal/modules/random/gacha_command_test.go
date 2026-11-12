package random

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/modules/random/cardpack"
	"github.com/tiennm99/tiennm99bot/internal/modules/random/caseopen"
	"github.com/tiennm99/tiennm99bot/internal/modules/random/frames"
	"github.com/tiennm99/tiennm99bot/internal/modules/random/genshin"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

var mp4Bytes = []byte("\x00\x00\x00\x18ftypisom-stub")

func TestParseGachaOptions(t *testing.T) {
	got := parseGachaOptions(" Pizza, 4* Pho , 3*Rice, 3 * Bun, 5* Cake, 5*, , 7* Tea, a*b, Pho 4* ")
	want := []gachaOption{
		{Label: "Pizza", Rarity: 5},
		{Label: "Pho", Rarity: 4},
		{Label: "Rice", Rarity: 3},
		{Label: "Bun", Rarity: 3},
		{Label: "Cake", Rarity: 5},
		{Label: "7* Tea", Rarity: 5},
		{Label: "a*b", Rarity: 5},
		{Label: "Pho 4*", Rarity: 5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseGachaOptions() = %#v, want %#v", got, want)
	}
}

func TestGacha_EmptyArgsRepliesUsage(t *testing.T) {
	for _, text := range []string{"/gacha", "/gacha , ,", "/gacha 5*"} {
		t.Run(text, func(t *testing.T) {
			rb := installRandom(t, 999)
			rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, text))

			if got := rb.LastSent().Text(); got != gachaUsage {
				t.Errorf("gacha reply = %q, want usage %q", got, gachaUsage)
			}
		})
	}
}

// stubCardPack replaces the in-process card-pack renderer for one test,
// recording what it was asked to draw.
func stubCardPack(t *testing.T, err error) *struct {
	label  string
	rarity int
} {
	t.Helper()
	got := &struct {
		label  string
		rarity int
	}{}
	renderCardPack = func(_ context.Context, label string, rarity int, _ uint64) ([]byte, error) {
		got.label, got.rarity = label, rarity
		if err != nil {
			return nil, err
		}
		return mp4Bytes, nil
	}
	t.Cleanup(func() { renderCardPack = cardpack.Render })
	return got
}

func TestGacha_SendsTheRenderedPack(t *testing.T) {
	got := stubCardPack(t, nil)

	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/gacha 4* Pho"))

	if got.label != "Pho" || got.rarity != 4 {
		t.Fatalf("render(%q, %d), want (Pho, 4)", got.label, got.rarity)
	}
	sent := rb.Sent()
	if len(sent) != 3 {
		t.Fatalf("calls = %+v, want placeholder, sendAnimation, deleteMessage", sent)
	}
	if sent[0].Method != "sendMessage" || sent[0].Text() != gachaPlaceholder {
		t.Fatalf("first call = %+v, want placeholder %q", sent[0], gachaPlaceholder)
	}
	if sent[2].Method != "deleteMessage" || sent[2].Form["message_id"] != "1" {
		t.Fatalf("last call = %+v, want deleteMessage of the placeholder", sent[2])
	}
	call := sent[1]
	if call.Method != "sendAnimation" {
		t.Fatalf("method = %q, want sendAnimation", call.Method)
	}
	if want := `Result: <span class="tg-spoiler">★★★★ Pho</span>`; call.Form["caption"] != want {
		t.Fatalf("caption = %q, want %q", call.Form["caption"], want)
	}
	for field, want := range map[string]string{"parse_mode": "HTML", "duration": "7", "width": "540", "height": "960"} {
		if got := call.Form[field]; got != want {
			t.Fatalf("%s = %q, want %q", field, got, want)
		}
	}
}

func TestGacha_RenderFailureFallsBackToText(t *testing.T) {
	stubCardPack(t, frames.ErrFFmpegMissing)

	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/gacha 3* Rice"))

	sent := rb.Sent()
	if len(sent) != 2 {
		t.Fatalf("calls = %+v, want placeholder then in-place edit", sent)
	}
	if sent[1].Method != "editMessageText" || sent[1].Text() != "★★★ Rice" {
		t.Fatalf("fallback call = %+v, want editMessageText ★★★ Rice", sent[1])
	}
}

func TestGacha_SendAnimationFailureFallsBackToText(t *testing.T) {
	stubCardPack(t, nil)

	rb := installRandom(t, 999)
	rb.FailMethod("sendAnimation", http.StatusInternalServerError, "")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/gacha 3* Rice"))

	call := rb.LastSent()
	if call.Method != "editMessageText" || call.Text() != "★★★ Rice" {
		t.Fatalf("fallback call = %+v, want editMessageText ★★★ Rice", call)
	}
}

func TestGacha_ForwardsMessageThreadID(t *testing.T) {
	stubCardPack(t, nil)

	rb := installRandom(t, 999)
	update := testutil.NewSupergroupMessage(-100, 7, "/gacha 3* Rice")
	update.Message.MessageThreadID = 42
	rb.Bot.ProcessUpdate(context.Background(), update)

	animations := 0
	for _, call := range rb.Sent() {
		switch call.Method {
		case "sendMessage", "sendAnimation":
			if got := call.Form["message_thread_id"]; got != "42" {
				t.Fatalf("%s message_thread_id = %q, want 42", call.Method, got)
			}
			if call.Method == "sendAnimation" {
				animations++
			}
		}
	}
	if animations != 1 {
		t.Fatalf("sendAnimation calls = %d, want 1", animations)
	}
}

func TestGenshin_RendersInProcessAndAnyoneCanRunIt(t *testing.T) {
	var gotLabel string
	var gotRarity int
	renderGenshin = func(_ context.Context, label string, rarity int) ([]byte, error) {
		gotLabel, gotRarity = label, rarity
		return mp4Bytes, nil
	}
	t.Cleanup(func() { renderGenshin = genshin.Render })

	// 7 is neither the owner (999) nor an admin.
	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/genshin 4* Bún bò"))

	if gotLabel != "Bún bò" || gotRarity != 4 {
		t.Fatalf("render(%q, %d), want (Bún bò, 4)", gotLabel, gotRarity)
	}
	var animation *testutil.SentCall
	sent := rb.Sent()
	for i := range sent {
		if sent[i].Method == "sendAnimation" {
			animation = &sent[i]
		}
	}
	if animation == nil {
		t.Fatalf("calls = %+v, want a sendAnimation", rb.Sent())
	}
	for field, want := range map[string]string{"duration": "7", "width": "640", "height": "360"} {
		if got := animation.Form[field]; got != want {
			t.Fatalf("%s = %q, want %s", field, got, want)
		}
	}
}

func TestGenshin_RenderFailureRepliesWithStars(t *testing.T) {
	renderGenshin = func(context.Context, string, int) ([]byte, error) { return nil, frames.ErrFFmpegMissing }
	t.Cleanup(func() { renderGenshin = genshin.Render })

	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/genshin 3* Pizza"))

	if got := rb.LastSent().Text(); got != "★★★ Pizza" {
		t.Fatalf("reply = %q, want ★★★ Pizza", got)
	}
}

func TestGenshin_EmptyArgsRepliesGenshinUsage(t *testing.T) {
	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/genshin"))

	if got := rb.LastSent().Text(); got != genshinUsage {
		t.Errorf("genshin reply = %q, want usage %q", got, genshinUsage)
	}
}

func TestCSGO_RendersTheReelOfAllOptions(t *testing.T) {
	var gotItems []caseopen.Item
	gotWinner := -1
	renderCase = func(_ context.Context, items []caseopen.Item, winner int, _ uint64) ([]byte, error) {
		gotItems, gotWinner = items, winner
		return mp4Bytes, nil
	}
	t.Cleanup(func() { renderCase = caseopen.Render })

	// 7 is neither the owner (999) nor an admin.
	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/csgo Pizza, 4* Pho, 3* Rice"))

	want := []caseopen.Item{{Label: "Pizza", Rarity: 5}, {Label: "Pho", Rarity: 4}, {Label: "Rice", Rarity: 3}}
	if !reflect.DeepEqual(gotItems, want) {
		t.Fatalf("items = %+v, want %+v", gotItems, want)
	}
	if gotWinner < 0 || gotWinner >= len(want) {
		t.Fatalf("winner = %d, want an index into the items", gotWinner)
	}
	var animation *testutil.SentCall
	sent := rb.Sent()
	for i := range sent {
		if sent[i].Method == "sendAnimation" {
			animation = &sent[i]
		}
	}
	if animation == nil {
		t.Fatalf("calls = %+v, want a sendAnimation", sent)
	}
	for field, want := range map[string]string{"duration": "7", "width": "640", "height": "360"} {
		if got := animation.Form[field]; got != want {
			t.Fatalf("%s = %q, want %s", field, got, want)
		}
	}
}

func TestCSGO_EmptyArgsRepliesUsage(t *testing.T) {
	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/csgo"))

	if got := rb.LastSent().Text(); got != csgoUsage {
		t.Errorf("csgo reply = %q, want usage %q", got, csgoUsage)
	}
}

func TestCSGO_RenderFailureRepliesWithStars(t *testing.T) {
	renderCase = func(context.Context, []caseopen.Item, int, uint64) ([]byte, error) {
		return nil, frames.ErrFFmpegMissing
	}
	t.Cleanup(func() { renderCase = caseopen.Render })

	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/csgo 4* Pho"))

	if got := rb.LastSent().Text(); got != "★★★★ Pho" {
		t.Fatalf("reply = %q, want ★★★★ Pho", got)
	}
}
