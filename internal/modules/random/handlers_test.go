package random

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/random/frames"
	"github.com/tiennm99/tiennm99bot/internal/modules/random/wheel"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// installRandom wires the random module to a recording bot with a fresh
// in-memory store and an Auth whose owner is ownerID.
func installRandom(t *testing.T, ownerID int64) *testutil.RecordingBot {
	t.Helper()
	rb := testutil.NewRecordingBot(t)
	mod := New(modules.Deps{Store: storage.NewMemoryProvider().Collection("random")})
	reg := &modules.Registry{
		Modules:     []modules.Module{{Name: "random", Commands: mod.Commands}},
		AllCommands: map[string]modules.Command{},
	}
	for _, c := range mod.Commands {
		reg.AllCommands[c.Name] = c
	}
	modules.Install(rb.Bot, reg, modules.Auth{BotOwnerID: ownerID})
	return rb
}

func TestRandom_UsageWhenMissingOptions(t *testing.T) {
	for _, text := range []string{"/random", "/random , ,"} {
		t.Run(text, func(t *testing.T) {
			rb := installRandom(t, 999)
			rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, text))

			if got := rb.LastSent().Text(); got != randomUsage {
				t.Errorf("random reply = %q, want usage %q", got, randomUsage)
			}
		})
	}
}

func TestRandom_SingleOption(t *testing.T) {
	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/random Alice"))

	if got := rb.LastSent().Text(); got != "Alice" {
		t.Errorf("random reply = %q, want Alice", got)
	}
}

func TestRandom_PicksFromTrimmedOptions(t *testing.T) {
	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/random Alice, Bob, Carol"))

	got := rb.LastSent().Text()
	if got != "Alice" && got != "Bob" && got != "Carol" {
		t.Errorf("random reply = %q, want one of Alice/Bob/Carol", got)
	}
}

func TestRandom_IgnoresEmptySegments(t *testing.T) {
	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/random , Alice , , Bob ,"))

	got := rb.LastSent().Text()
	if got != "Alice" && got != "Bob" {
		t.Errorf("random reply = %q, want Alice or Bob", got)
	}
}

func TestWheelOfNames_UsageWhenMissingOptions(t *testing.T) {
	for _, text := range []string{"/wheelofnames", "/wheelofnames , ,"} {
		t.Run(text, func(t *testing.T) {
			rb := installRandom(t, 999)
			rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, text))

			if got := rb.LastSent().Text(); got != wheelUsage {
				t.Errorf("wheelofnames reply = %q, want usage %q", got, wheelUsage)
			}
		})
	}
}

func TestWheelOfNames_ResultCaptionEscapesHTML(t *testing.T) {
	got := wheelResultCaption([]string{`<Alice & Bob>`}, 0)
	want := `Result: <span class="tg-spoiler">&lt;Alice &amp; Bob&gt;</span>`
	if got != want {
		t.Fatalf("wheelResultCaption() = %q, want %q", got, want)
	}
}

func TestWheelOfNames_ResultCaptionTruncatesLongResult(t *testing.T) {
	got := wheelResultCaption([]string{strings.Repeat("a", wheelResultCaptionMaxRunes+1)}, 0)
	want := `Result: <span class="tg-spoiler">` + strings.Repeat("a", wheelResultCaptionMaxRunes) + `...</span>`
	if got != want {
		t.Fatalf("wheelResultCaption() length = %d, want truncated caption length %d", len(got), len(want))
	}
}

func TestWheelOfNames_ResultCaptionPadsShortWinnerToLongestOption(t *testing.T) {
	options := []string{"Bob", "Alexandria"}
	got := wheelResultCaption(options, 0)
	want := `Result: <span class="tg-spoiler">___Bob____</span>`
	if got != want {
		t.Fatalf("wheelResultCaption() = %q, want %q", got, want)
	}
	if longest := wheelResultCaption(options, 1); len([]rune(longest)) != len([]rune(got)) {
		t.Fatalf("caption lengths differ: winner %q vs %q", got, longest)
	}
}

// stubWheel replaces the in-process renderer for one test, recording what it
// was asked to draw.
func stubWheel(t *testing.T, err error) *struct {
	options []string
	winner  int
} {
	t.Helper()
	got := &struct {
		options []string
		winner  int
	}{winner: -1}
	renderWheel = func(_ context.Context, options []string, winner int, _ uint64) ([]byte, error) {
		got.options, got.winner = options, winner
		if err != nil {
			return nil, err
		}
		return mp4Bytes, nil
	}
	t.Cleanup(func() { renderWheel = wheel.Render })
	return got
}

func TestWheelOfNames_SendsTheRenderedSpin(t *testing.T) {
	got := stubWheel(t, nil)

	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/wheelofnames Alice, Bob, Carol"))

	if !slices.Equal(got.options, []string{"Alice", "Bob", "Carol"}) {
		t.Fatalf("options = %#v, want parsed options", got.options)
	}
	if got.winner < 0 || got.winner >= len(got.options) {
		t.Fatalf("winner = %d, want in range", got.winner)
	}
	sent := rb.Sent()
	if len(sent) != 3 {
		t.Fatalf("calls = %+v, want placeholder, sendAnimation, deleteMessage", sent)
	}
	if sent[0].Method != "sendMessage" || sent[0].Text() != wheelPlaceholder {
		t.Fatalf("first call = %+v, want placeholder %q", sent[0], wheelPlaceholder)
	}
	// The placeholder cannot be edited into media, so it is dropped once the
	// animation lands and the animation itself carries the result.
	if sent[2].Method != "deleteMessage" || sent[2].Form["message_id"] != "1" {
		t.Fatalf("last call = %+v, want deleteMessage of the placeholder", sent[2])
	}
	call := sent[1]
	if call.Method != "sendAnimation" {
		t.Fatalf("method = %q, want sendAnimation", call.Method)
	}
	if want := wheelResultCaption(got.options, got.winner); call.Form["caption"] != want {
		t.Fatalf("caption = %q, want %q", call.Form["caption"], want)
	}
	for field, want := range map[string]string{"parse_mode": "HTML", "duration": "7", "width": "512", "height": "512"} {
		if call.Form[field] != want {
			t.Fatalf("%s = %q, want %s", field, call.Form[field], want)
		}
	}
}

func TestWheelOfNames_RenderFailureFallsBackToRandomReply(t *testing.T) {
	stubWheel(t, frames.ErrFFmpegMissing)

	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/wheelofnames Alice"))

	sent := rb.Sent()
	if len(sent) != 2 {
		t.Fatalf("calls = %+v, want placeholder then in-place edit", sent)
	}
	if sent[0].Method != "sendMessage" || sent[0].Text() != wheelPlaceholder {
		t.Fatalf("first call = %+v, want placeholder %q", sent[0], wheelPlaceholder)
	}
	if sent[1].Method != "editMessageText" || sent[1].Text() != "Alice" {
		t.Fatalf("fallback call = %+v, want editMessageText Alice", sent[1])
	}
	if got := sent[1].Form["message_id"]; got != "1" {
		t.Fatalf("edited message_id = %q, want the placeholder id 1", got)
	}
}

// A failed edit still has to deliver the winner, so the handler falls back to a
// fresh reply rather than leaving "Spinning..." on screen forever.
func TestWheelOfNames_PlaceholderEditFailureFallsBackToReply(t *testing.T) {
	stubWheel(t, frames.ErrFFmpegMissing)

	rb := installRandom(t, 999)
	rb.FailMethod("editMessageText", http.StatusInternalServerError, "")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/wheelofnames Alice"))

	call := rb.LastSent()
	if call.Method != "sendMessage" || call.Text() != "Alice" {
		t.Fatalf("fallback call = %+v, want sendMessage Alice", call)
	}
}

// Past the wheel's segment limit there is nothing to draw, so the winner lands
// straight away with no placeholder flashing before it.
func TestWheelOfNames_TooManyOptionsRepliesWithWinner(t *testing.T) {
	got := stubWheel(t, nil)
	options := make([]string, wheel.MaxOptions+1)
	for i := range options {
		options[i] = "o" + strings.Repeat("x", i)
	}

	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/wheelofnames "+strings.Join(options, ",")))

	calls := rb.Sent()
	if len(calls) != 1 || calls[0].Method != "sendMessage" || !slices.Contains(options, calls[0].Text()) {
		t.Fatalf("calls = %+v, want a single winner reply", calls)
	}
	if got.winner != -1 {
		t.Fatal("the wheel was rendered past its segment limit")
	}
}

func TestWheelOfNames_SendAnimationFailureFallsBackToRandomReply(t *testing.T) {
	stubWheel(t, nil)

	rb := installRandom(t, 999)
	rb.FailMethod("sendAnimation", http.StatusInternalServerError, "")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/wheelofnames Alice"))

	calls := rb.Sent()
	if len(calls) != 3 {
		t.Fatalf("calls = %+v, want placeholder, sendAnimation, in-place edit", calls)
	}
	if calls[1].Method != "sendAnimation" {
		t.Fatalf("second method = %q, want sendAnimation", calls[1].Method)
	}
	// The placeholder survives a failed animation precisely so the winner can
	// replace it instead of stranding "Spinning..." in the chat.
	if calls[2].Method != "editMessageText" || calls[2].Text() != "Alice" {
		t.Fatalf("fallback call = %+v, want editMessageText Alice", calls[2])
	}
}

func TestWheelOfNames_ForwardsMessageThreadID(t *testing.T) {
	stubWheel(t, nil)

	rb := installRandom(t, 999)
	update := testutil.NewSupergroupMessage(-100, 7, "/wheelofnames Alice")
	update.Message.MessageThreadID = 42
	rb.Bot.ProcessUpdate(context.Background(), update)

	for _, call := range rb.Sent() {
		switch call.Method {
		case "sendMessage", "sendAnimation":
			if got := call.Form["message_thread_id"]; got != "42" {
				t.Fatalf("%s message_thread_id = %q, want 42", call.Method, got)
			}
		}
	}
	if got := rb.Sent()[1].Method; got != "sendAnimation" {
		t.Fatalf("second method = %q, want sendAnimation", got)
	}
}
