package alias_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/alias"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// installAlias builds a registry holding only the alias module. Every command
// it registers is public, so no auth is needed for them to dispatch.
func installAlias(t *testing.T) *testutil.RecordingBot {
	t.Helper()
	rb := testutil.NewRecordingBot(t)
	reg, err := modules.Build([]string{"alias"},
		map[string]modules.Factory{"alias": alias.New},
		storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	modules.Install(rb.Bot, reg, modules.Auth{})
	return rb
}

// aliasCmd builds "/alias <name>" replying to the given message.
func aliasCmd(name string, replied *models.Message) *models.Update {
	upd := testutil.NewPrivateMessage(7, "/alias "+name)
	upd.Message.ReplyToMessage = replied
	return upd
}

func callTo(rb *testutil.RecordingBot, method string) (testutil.SentCall, bool) {
	for _, c := range rb.Sent() {
		if c.Method == method {
			return c, true
		}
	}
	return testutil.SentCall{}, false
}

// Every supported kind must round-trip: saved by file_id, then sent back
// through the one send method that accepts it.
func TestAlias_RoundTripsEveryKind(t *testing.T) {
	cases := []struct {
		name    string
		replied *models.Message
		method  string
		field   string
		fileID  string
	}{
		{
			name:    "sticker",
			replied: &models.Message{Sticker: &models.Sticker{FileID: "sticker-id"}},
			method:  "sendSticker", field: "sticker", fileID: "sticker-id",
		},
		{
			name:    "photo",
			replied: &models.Message{Photo: []models.PhotoSize{{FileID: "small", FileSize: 10}, {FileID: "photo-id", FileSize: 900}}},
			method:  "sendPhoto", field: "photo", fileID: "photo-id",
		},
		{
			name:    "animation",
			replied: &models.Message{Animation: &models.Animation{FileID: "anim-id"}},
			method:  "sendAnimation", field: "animation", fileID: "anim-id",
		},
		{
			name:    "video",
			replied: &models.Message{Video: &models.Video{FileID: "video-id"}},
			method:  "sendVideo", field: "video", fileID: "video-id",
		},
		{
			name:    "video note",
			replied: &models.Message{VideoNote: &models.VideoNote{FileID: "note-id"}},
			method:  "sendVideoNote", field: "video_note", fileID: "note-id",
		},
		{
			name:    "audio",
			replied: &models.Message{Audio: &models.Audio{FileID: "audio-id"}},
			method:  "sendAudio", field: "audio", fileID: "audio-id",
		},
		{
			name:    "voice",
			replied: &models.Message{Voice: &models.Voice{FileID: "voice-id"}},
			method:  "sendVoice", field: "voice", fileID: "voice-id",
		},
		{
			name:    "document",
			replied: &models.Message{Document: &models.Document{FileID: "doc-id"}},
			method:  "sendDocument", field: "document", fileID: "doc-id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rb := installAlias(t)
			rb.Bot.ProcessUpdate(context.Background(), aliasCmd("thing", tc.replied))
			rb.AssertSentText(t, "Use <code>/insert thing</code>")

			rb.Reset()
			rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/insert thing"))

			call, ok := callTo(rb, tc.method)
			if !ok {
				t.Fatalf("no %s call; got %+v", tc.method, rb.Sent())
			}
			// The stored file_id is handed straight back, never re-uploaded.
			if got := call.Form[tc.field]; got != tc.fileID {
				t.Errorf("%s = %q, want the saved file_id %q", tc.field, got, tc.fileID)
			}
			if _, ok := callTo(rb, "sendDocument"); ok && tc.method != "sendDocument" {
				t.Error("media was re-sent as a document")
			}
		})
	}
}

func TestAlias_TextRoundTrips(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(),
		aliasCmd("greeting", &models.Message{Text: "xin chào"}))

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/insert greeting"))

	if got := rb.LastSent().Text(); got != "xin chào" {
		t.Errorf("insert sent %q, want the saved text", got)
	}
}

// A photo's caption is part of what was saved, so it comes back with it.
func TestAlias_KeepsCaption(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(), aliasCmd("pic", &models.Message{
		Photo:   []models.PhotoSize{{FileID: "photo-id", FileSize: 100}},
		Caption: "a caption",
	}))

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/insert pic"))

	call, ok := callTo(rb, "sendPhoto")
	if !ok {
		t.Fatalf("no sendPhoto call; got %+v", rb.Sent())
	}
	if got := call.Form["caption"]; got != "a caption" {
		t.Errorf("caption = %q, want it preserved", got)
	}
}

// A GIF arrives as an Animation *and* a Document. The specific kind must win,
// or /insert would send it as a plain file.
func TestAlias_AnimationBeatsDocument(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(), aliasCmd("gif", &models.Message{
		Animation: &models.Animation{FileID: "anim-id"},
		Document:  &models.Document{FileID: "doc-id"},
	}))

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/insert gif"))

	if _, ok := callTo(rb, "sendAnimation"); !ok {
		t.Errorf("want sendAnimation; got %+v", rb.Sent())
	}
	if _, ok := callTo(rb, "sendDocument"); ok {
		t.Error("a GIF was sent as a document")
	}
}

// Global namespace, last assignment wins — and the reply says so, because an
// overwrite silently discards whatever the name held before.
func TestAlias_OverwriteAnnouncesReplacement(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(),
		aliasCmd("dup", &models.Message{Sticker: &models.Sticker{FileID: "first"}}))

	rb.Reset()
	// A different user, to prove the namespace is shared rather than per-caller.
	upd := testutil.NewPrivateMessage(99, "/alias dup")
	upd.Message.ReplyToMessage = &models.Message{Text: "second"}
	rb.Bot.ProcessUpdate(context.Background(), upd)

	got := rb.LastSent().Text()
	if !strings.Contains(got, "Replaced") || !strings.Contains(got, "was a sticker") {
		t.Errorf("reply = %q, want it to name what it replaced", got)
	}

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/insert dup"))
	if got := rb.LastSent().Text(); got != "second" {
		t.Errorf("insert sent %q, want the newer binding", got)
	}
}

// Lookups fold case; the stored spelling is what gets echoed back.
func TestAlias_NameIsCaseInsensitive(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(),
		aliasCmd("LOUD", &models.Message{Text: "shouted"}))

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/insert loud"))
	if got := rb.LastSent().Text(); got != "shouted" {
		t.Errorf("insert sent %q, want the alias found case-insensitively", got)
	}
}

// A leading @ is a natural slip for a username-shaped name, not a new request.
func TestAlias_StripsLeadingAt(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(),
		aliasCmd("@handle", &models.Message{Text: "saved"}))

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/insert handle"))
	if got := rb.LastSent().Text(); got != "saved" {
		t.Errorf("insert sent %q, want the @-stripped name to resolve", got)
	}
}

func TestAlias_RejectsBadNames(t *testing.T) {
	// Spaces are what separate a name from a sentence; the rest are shapes a
	// username cannot take.
	for _, name := range []string{"", "two words", "9lives", "has-dash", "hasдot.", strings.Repeat("a", 33)} {
		t.Run("name="+name, func(t *testing.T) {
			rb := installAlias(t)
			rb.Bot.ProcessUpdate(context.Background(),
				aliasCmd(name, &models.Message{Text: "content"}))

			rb.AssertSentText(t, "The name is one word")
		})
	}
}

func TestAlias_RejectsUnsupportedMessage(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(),
		aliasCmd("place", &models.Message{Location: &models.Location{Latitude: 1, Longitude: 2}}))

	rb.AssertSentText(t, "cannot be saved")
}

func TestInsert_UnknownNameExplainsHowToSaveOne(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/insert nothing"))

	rb.AssertSentText(t, "Nothing is saved")
	rb.AssertSentText(t, "/alias nothing")
}

// A file_id can stop working — the file was deleted, or Telegram rejects it.
// The caller gets an actionable sentence, not an opaque failure.
func TestInsert_DeadFileIDIsExplained(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(),
		aliasCmd("gone", &models.Message{Sticker: &models.Sticker{FileID: "stale"}}))

	rb.Reset()
	rb.FailMethodCode("sendSticker", 400, "Bad Request: wrong file identifier")
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/insert gone"))

	rb.AssertSentText(t, "can no longer be sent")
}

// Forum supergroups route by topic: a reply without the thread id lands in
// General instead of where the command was typed.
func TestInsert_KeepsForumTopic(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(),
		aliasCmd("topical", &models.Message{Sticker: &models.Sticker{FileID: "sticker-id"}}))

	rb.Reset()
	upd := testutil.NewSupergroupMessage(-100, 7, "/insert topical")
	upd.Message.MessageThreadID = 42
	rb.Bot.ProcessUpdate(context.Background(), upd)

	call, ok := callTo(rb, "sendSticker")
	if !ok {
		t.Fatalf("no sendSticker call; got %+v", rb.Sent())
	}
	if got := call.Form["message_thread_id"]; got != "42" {
		t.Errorf("message_thread_id = %q, want the topic the command came from", got)
	}
}

func TestAliases_EmptyStoreExplainsHowToSave(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/aliases"))

	rb.AssertSentText(t, "No aliases saved yet")
}

// Names are listed sorted and counted. Order must be stable, or the same
// command run twice reads as a different list.
func TestAliases_ListsSortedNames(t *testing.T) {
	rb := installAlias(t)
	for _, name := range []string{"zeta", "alpha", "Mid"} {
		rb.Bot.ProcessUpdate(context.Background(),
			aliasCmd(name, &models.Message{Text: "x"}))
	}

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/aliases"))

	call := rb.LastSent()
	got := call.Text()
	if !strings.HasPrefix(got, "3 aliases:") {
		t.Errorf("reply = %q, want it to open with the count", got)
	}
	// One line per alias: a copyable command plus what it holds. Keys are
	// folded, so "Mid" lists as "mid" — which is what /insert takes.
	want := "\n<code>/alpha</code> — text" +
		"\n<code>/mid</code> — text" +
		"\n<code>/zeta</code> — text"
	if !strings.Contains(got, want) {
		t.Errorf("reply = %q, want sorted lines %q", got, want)
	}
	// The markup is inert without the parse mode.
	if pm := call.Form["parse_mode"]; pm != "HTML" {
		t.Errorf("parse_mode = %q, want HTML", pm)
	}
}

// Each kind is named in the list, so /aliases says what it will send.
func TestAliases_ShowsEachKind(t *testing.T) {
	rb := installAlias(t)
	cases := map[string]*models.Message{
		"words": {Text: "hi"},
		"stick": {Sticker: &models.Sticker{FileID: "s"}},
		"clip":  {Video: &models.Video{FileID: "v"}},
		"loop":  {Animation: &models.Animation{FileID: "a"}},
		"pic":   {Photo: []models.PhotoSize{{FileID: "p", FileSize: 1}}},
	}
	for name, replied := range cases {
		rb.Bot.ProcessUpdate(context.Background(), aliasCmd(name, replied))
	}

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/aliases"))

	got := rb.LastSent().Text()
	for _, want := range []string{
		"<code>/words</code> — text",
		"<code>/stick</code> — sticker",
		"<code>/clip</code> — video",
		"<code>/loop</code> — GIF",
		"<code>/pic</code> — photo",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reply missing %q; got %q", want, got)
		}
	}
}

// The reply must fit one Telegram message (4096 chars), and the trimming
// notice must not itself be what overflows it.
func TestAliases_TrimsToOneMessage(t *testing.T) {
	rb := installAlias(t)
	const total = 400
	for i := 0; i < total; i++ {
		rb.Bot.ProcessUpdate(context.Background(),
			aliasCmd(fmt.Sprintf("name%026d", i), &models.Message{Text: "x"}))
	}

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/aliases"))

	got := rb.LastSent().Text()
	if len(got) > 4096 {
		t.Errorf("reply is %d bytes, past Telegram's 4096 limit", len(got))
	}
	if !strings.Contains(got, "more.") {
		t.Errorf("reply = %q...; want a trimming notice", got[:min(120, len(got))])
	}
	if !strings.HasPrefix(got, fmt.Sprintf("%d aliases:", total)) {
		t.Errorf("reply should still report the true total; got %q", got[:min(60, len(got))])
	}
}

// Formatting is part of what was saved: bold, italic, code and links come back
// as entities, so the text is re-sent looking like the original.
func TestAlias_PreservesTextFormatting(t *testing.T) {
	rb := installAlias(t)
	upd := aliasCmd("styled", &models.Message{
		Text: "bold code",
		Entities: []models.MessageEntity{
			{Type: models.MessageEntityTypeBold, Offset: 0, Length: 4},
			{Type: models.MessageEntityTypeCode, Offset: 5, Length: 4},
		},
	})
	rb.Bot.ProcessUpdate(context.Background(), upd)

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/insert styled"))

	call, ok := callTo(rb, "sendMessage")
	if !ok {
		t.Fatalf("no sendMessage call; got %+v", rb.Sent())
	}
	if got := call.Form["text"]; got != "bold code" {
		t.Errorf("text = %q, want it unchanged so the offsets stay valid", got)
	}
	// Entities ride along as JSON; offsets are relative to the text above.
	ents := call.Form["entities"]
	for _, want := range []string{`"type":"bold"`, `"type":"code"`, `"offset":5`, `"length":4`} {
		if !strings.Contains(ents, want) {
			t.Errorf("entities = %q, missing %s", ents, want)
		}
	}
}

// A caption's formatting survives the same way.
func TestAlias_PreservesCaptionFormatting(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(), aliasCmd("pic", &models.Message{
		Photo:   []models.PhotoSize{{FileID: "photo-id", FileSize: 1}},
		Caption: "italic",
		CaptionEntities: []models.MessageEntity{
			{Type: models.MessageEntityTypeItalic, Offset: 0, Length: 6},
		},
	}))

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/insert pic"))

	call, ok := callTo(rb, "sendPhoto")
	if !ok {
		t.Fatalf("no sendPhoto call; got %+v", rb.Sent())
	}
	if got := call.Form["caption_entities"]; !strings.Contains(got, `"type":"italic"`) {
		t.Errorf("caption_entities = %q, want the italic run preserved", got)
	}
}

// Telegram never delivers another bot's message content, so the refusal has to
// say that rather than implying the format was unsupported.
func TestAlias_ExplainsAnotherBotsMessage(t *testing.T) {
	rb := installAlias(t)
	// What arrives when the reply points at another bot's message: a sender
	// that is a bot, and no readable content.
	rb.Bot.ProcessUpdate(context.Background(), aliasCmd("botmsg", &models.Message{
		From: &models.User{ID: 555, IsBot: true, FirstName: "OtherBot"},
	}))

	rb.AssertSentText(t, "hid that bot's message")
}

// A human's unsupported message still gets the format advice, not the bot one.
func TestAlias_UnsupportedFromHumanKeepsFormatAdvice(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(), aliasCmd("place", &models.Message{
		From:     &models.User{ID: 7, FirstName: "Test"},
		Location: &models.Location{Latitude: 1, Longitude: 2},
	}))

	rb.AssertSentText(t, "cannot be saved")
}

// A reply Telegram delivered empty must not be blamed on the message format:
// the caller gets the one action that recovers it.
func TestAlias_StrippedReplySuggestsForwarding(t *testing.T) {
	rb := installAlias(t)
	// A message id and a human sender, but no content field at all — what
	// arrives when Telegram passes the reply along without its payload.
	rb.Bot.ProcessUpdate(context.Background(), aliasCmd("gone", &models.Message{
		ID:   9,
		From: &models.User{ID: 7, FirstName: "Test"},
	}))

	rb.AssertSentText(t, "no content")
	rb.AssertSentText(t, "Forward it into this chat")
}

// /alias with no reply attached covers both readings: a caller who forgot to
// reply, and a reply Telegram dropped on the way.
func TestAlias_NoReplySuggestsForwarding(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(), aliasCmd("gone", nil))

	rb.AssertSentText(t, "Reply to the message you want to save")
	rb.AssertSentText(t, "Forward it into this chat")
}

// The other-bot refusal carries the same advice, so every unreadable reply
// ends with an action rather than only a reason.
func TestAlias_OtherBotRefusalSuggestsForwarding(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(), aliasCmd("botmsg", &models.Message{
		From: &models.User{ID: 555, IsBot: true, FirstName: "OtherBot"},
	}))

	rb.AssertSentText(t, "Forward it into this chat")
}
