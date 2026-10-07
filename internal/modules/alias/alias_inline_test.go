package alias_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// inlineQuery builds an inline-mode update: "@botname <query>" typed in any chat.
func inlineQuery(userID int64, query string) *models.Update {
	return &models.Update{
		ID: 1,
		InlineQuery: &models.InlineQuery{
			ID:    "q1",
			From:  &models.User{ID: userID, FirstName: "Test"},
			Query: query,
		},
	}
}

// inlineResults decodes the results the bot answered with. answerInlineQuery
// sends them as a JSON array in the "results" form field.
func inlineResults(t *testing.T, rb *testutil.RecordingBot) []map[string]any {
	t.Helper()
	call, ok := callTo(rb, "answerInlineQuery")
	if !ok {
		t.Fatalf("no answerInlineQuery call; got %+v", rb.Sent())
	}
	var out []map[string]any
	if err := json.Unmarshal([]byte(call.Form["results"]), &out); err != nil {
		t.Fatalf("decode results %q: %v", call.Form["results"], err)
	}
	return out
}

// Each kind must be offered as the cached inline type that carries it — the
// reason a file_id is stored rather than bytes.
func TestInline_OffersEachKindAsItsCachedType(t *testing.T) {
	cases := []struct {
		alias    string
		replied  *models.Message
		wantType string
		idField  string
	}{
		{"pic", &models.Message{Photo: []models.PhotoSize{{FileID: "photo-id", FileSize: 9}}}, "photo", "photo_file_id"},
		{"stick", &models.Message{Sticker: &models.Sticker{FileID: "sticker-id"}}, "sticker", "sticker_file_id"},
		{"movie", &models.Message{Video: &models.Video{FileID: "video-id"}}, "video", "video_file_id"},
		{"loop", &models.Message{Animation: &models.Animation{FileID: "anim-id"}}, "gif", "gif_file_id"},
		{"song", &models.Message{Audio: &models.Audio{FileID: "audio-id"}}, "audio", "audio_file_id"},
		{"note", &models.Message{Voice: &models.Voice{FileID: "voice-id"}}, "voice", "voice_file_id"},
		{"paper", &models.Message{Document: &models.Document{FileID: "doc-id"}}, "document", "document_file_id"},
		{"words", &models.Message{Text: "hello"}, "article", ""},
	}
	for _, tc := range cases {
		t.Run(tc.alias, func(t *testing.T) {
			rb := installAlias(t)
			rb.Bot.ProcessUpdate(context.Background(), aliasCmd(tc.alias, tc.replied))

			rb.Reset()
			rb.Bot.ProcessUpdate(context.Background(), inlineQuery(7, tc.alias))

			results := inlineResults(t, rb)
			if len(results) != 1 {
				t.Fatalf("got %d results, want 1: %+v", len(results), results)
			}
			if got := results[0]["type"]; got != tc.wantType {
				t.Errorf("type = %v, want %q", got, tc.wantType)
			}
			if got := results[0]["id"]; got != tc.alias {
				t.Errorf("id = %v, want the alias name %q", got, tc.alias)
			}
			if tc.idField != "" {
				if got, ok := results[0][tc.idField].(string); !ok || !strings.HasSuffix(got, "-id") {
					t.Errorf("%s = %v, want the saved file_id", tc.idField, results[0][tc.idField])
				}
			}
		})
	}
}

// Telegram defines no cached inline type for a video note, so it must be left
// out rather than downgraded to a plain video — that would change what the
// user saved.
func TestInline_SkipsVideoNotes(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(),
		aliasCmd("round", &models.Message{VideoNote: &models.VideoNote{FileID: "note-id"}}))
	rb.Bot.ProcessUpdate(context.Background(),
		aliasCmd("flat", &models.Message{Text: "text"}))

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), inlineQuery(7, ""))

	results := inlineResults(t, rb)
	if len(results) != 1 {
		t.Fatalf("got %d results, want only the non-video-note one: %+v", len(results), results)
	}
	if got := results[0]["id"]; got != "flat" {
		t.Errorf("id = %v, want the text alias", got)
	}
}

// An empty query lists everything; a non-empty one filters by prefix.
func TestInline_FiltersByPrefix(t *testing.T) {
	rb := installAlias(t)
	for _, name := range []string{"cheer", "cheese", "boo"} {
		rb.Bot.ProcessUpdate(context.Background(), aliasCmd(name, &models.Message{Text: name}))
	}

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), inlineQuery(7, ""))
	if got := len(inlineResults(t, rb)); got != 3 {
		t.Errorf("empty query returned %d results, want all 3", got)
	}

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), inlineQuery(7, "che"))
	results := inlineResults(t, rb)
	if len(results) != 2 {
		t.Fatalf("prefix query returned %d results, want 2: %+v", len(results), results)
	}
	// Sorted, so the order is stable between identical queries.
	if results[0]["id"] != "cheer" || results[1]["id"] != "cheese" {
		t.Errorf("results = %v, %v; want cheer then cheese", results[0]["id"], results[1]["id"])
	}
}

// The prefix is folded the same way names are, so typing uppercase still finds
// the alias.
func TestInline_PrefixIsCaseInsensitive(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(), aliasCmd("cheer", &models.Message{Text: "yay"}))

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), inlineQuery(7, "CHE"))

	if got := len(inlineResults(t, rb)); got != 1 {
		t.Errorf("got %d results for an uppercase prefix, want 1", got)
	}
}

// A query matching nothing must still be answered, or the caller's client
// spins on an unanswered inline query.
func TestInline_NoMatchesStillAnswers(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(), inlineQuery(7, "nothing"))

	call, ok := callTo(rb, "answerInlineQuery")
	if !ok {
		t.Fatalf("no answerInlineQuery call; got %+v", rb.Sent())
	}
	if got := call.Form["inline_query_id"]; got != "q1" {
		t.Errorf("inline_query_id = %q, want the query's id", got)
	}
}

// Telegram caps answerInlineQuery at 50 results.
func TestInline_CapsAtFiftyResults(t *testing.T) {
	rb := installAlias(t)
	for i := 0; i < 60; i++ {
		rb.Bot.ProcessUpdate(context.Background(),
			aliasCmd(uniqueName(i), &models.Message{Text: "x"}))
	}

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), inlineQuery(7, ""))

	// Exactly the cap, not merely "at most": 60 were saved, so a smaller
	// number would mean the listing quietly lost entries.
	if got := len(inlineResults(t, rb)); got != 50 {
		t.Errorf("returned %d results, want exactly Telegram's 50 cap", got)
	}
}

// uniqueName builds a valid, distinct alias name for index i.
func uniqueName(i int) string {
	return "n" + strings.Repeat("x", i/10) + string(rune('a'+i%10))
}

// The cap counts results the picker can actually show. A kind with no cached
// inline type must not consume a slot, or a handful of video notes would
// shrink an otherwise full answer.
func TestInline_VideoNotesDoNotConsumeCapSlots(t *testing.T) {
	rb := installAlias(t)
	for i := 0; i < 10; i++ {
		rb.Bot.ProcessUpdate(context.Background(),
			aliasCmd(uniqueName(i), &models.Message{VideoNote: &models.VideoNote{FileID: "note-id"}}))
	}
	for i := 10; i < 70; i++ {
		rb.Bot.ProcessUpdate(context.Background(),
			aliasCmd(uniqueName(i), &models.Message{Text: "x"}))
	}

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), inlineQuery(7, ""))

	if got := len(inlineResults(t, rb)); got != 50 {
		t.Errorf("returned %d results, want the full 50 despite the skipped video notes", got)
	}
}
