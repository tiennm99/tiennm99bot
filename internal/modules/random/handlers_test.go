package random

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/modules"
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

func TestWheelOfNames_UsesRemoteAPIWhenConfigured(t *testing.T) {
	var got wheelRenderRequest
	var gotAuthorization string
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/gif" {
			t.Errorf("path = %q, want /api/gif", r.URL.Path)
		}
		gotAuthorization = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("Decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "image/gif")
		_, _ = w.Write([]byte("GIF89a-remote"))
	}))
	defer server.Close()
	t.Setenv(rendererURLEnv, server.URL)

	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/wheelofnames Alice, Bob, Carol"))

	if calls != 1 {
		t.Fatalf("remote calls = %d, want 1", calls)
	}
	if gotAuthorization != "" {
		t.Fatalf("Authorization = %q, want none", gotAuthorization)
	}
	if !slices.Equal(got.Options, []string{"Alice", "Bob", "Carol"}) {
		t.Fatalf("options = %#v, want parsed options", got.Options)
	}
	if got.WinnerIndex < 0 || got.WinnerIndex >= len(got.Options) {
		t.Fatalf("winnerIndex = %d, want in range", got.WinnerIndex)
	}
	assertWheelRemoteDefaults(t, got)

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
	wantCaption := wheelResultCaption(got.Options, got.WinnerIndex)
	if got := call.Form["caption"]; got != wantCaption {
		t.Fatalf("caption = %q, want %q", got, wantCaption)
	}
	if got := call.Form["parse_mode"]; got != "HTML" {
		t.Fatalf("parse_mode = %q, want HTML", got)
	}
	if got := call.Form["duration"]; got != "7" {
		t.Fatalf("duration = %q, want 7", got)
	}
	if got := call.Form["width"]; got != "512" {
		t.Fatalf("width = %q, want 512", got)
	}
	if got := call.Form["height"]; got != "512" {
		t.Fatalf("height = %q, want 512", got)
	}
}

func TestWheelOfNames_RemoteFailureFallsBackToRandomReply(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv(rendererURLEnv, server.URL)

	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/wheelofnames Alice"))

	if calls != 1 {
		t.Fatalf("remote calls = %d, want 1", calls)
	}
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv(rendererURLEnv, server.URL)

	rb := installRandom(t, 999)
	rb.FailMethod("editMessageText", http.StatusInternalServerError, "")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/wheelofnames Alice"))

	call := rb.LastSent()
	if call.Method != "sendMessage" || call.Text() != "Alice" {
		t.Fatalf("fallback call = %+v, want sendMessage Alice", call)
	}
}

// Without a renderer there is nothing to wait for, so the winner must land
// straight away with no placeholder flashing before it.
func TestWheelOfNames_NotConfiguredFallsBackToRandomReply(t *testing.T) {
	rb := installRandom(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/wheelofnames Alice"))

	calls := rb.Sent()
	if len(calls) != 1 {
		t.Fatalf("calls = %+v, want a single winner reply", calls)
	}
	if calls[0].Method != "sendMessage" || calls[0].Text() != "Alice" {
		t.Fatalf("fallback call = %+v, want sendMessage Alice", calls[0])
	}
}

func TestWheelOfNames_SendAnimationFailureFallsBackToRandomReply(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/gif")
		_, _ = w.Write([]byte("GIF89a-remote"))
	}))
	defer server.Close()
	t.Setenv(rendererURLEnv, server.URL)

	rb := installRandom(t, 999)
	rb.FailMethod("sendAnimation", http.StatusInternalServerError, "")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/wheelofnames Alice"))

	calls := rb.Sent()
	if len(calls) != 3 {
		t.Fatalf("calls = %+v, want placeholder, sendAnimation, in-place edit", calls)
	}
	if calls[0].Method != "sendMessage" || calls[0].Text() != wheelPlaceholder {
		t.Fatalf("first call = %+v, want placeholder %q", calls[0], wheelPlaceholder)
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/gif")
		_, _ = w.Write([]byte("GIF89a-remote"))
	}))
	defer server.Close()
	t.Setenv(rendererURLEnv, server.URL)

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
