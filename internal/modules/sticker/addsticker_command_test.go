package sticker_test

import (
	"context"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/miti99bot/internal/modules"
	"github.com/tiennm99/miti99bot/internal/modules/sticker"
	"github.com/tiennm99/miti99bot/internal/storage"
	"github.com/tiennm99/miti99bot/internal/testutil"
)

// installSticker builds a registry holding only the sticker module. /addsticker
// is public, so no auth is needed for it to dispatch.
func installSticker(t *testing.T) *testutil.RecordingBot {
	t.Helper()
	rb := testutil.NewRecordingBot(t)
	reg, err := modules.Build([]string{"sticker"},
		map[string]modules.Factory{"sticker": sticker.New},
		storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	modules.Install(rb.Bot, reg, modules.Auth{})
	return rb
}

// installAddSticker builds the sticker module with the shared pack configured and
// getMe stubbed.
//
// getMe must be stubbed explicitly: the bot runs with WithSkipGetMe(), and
// RecordingBot's default reply for an unknown method is a bare `true`, which
// cannot decode into a User. The username matters — it is half of the
// "_by_<bot_username>" suffix that proves the pack is this bot's.
func installAddSticker(t *testing.T, packName, botUsername string) *testutil.RecordingBot {
	t.Helper()
	t.Setenv("OWNER_ID", "999")
	t.Setenv("STICKER_PACK_NAME", packName)
	rb := installSticker(t)
	rb.StubMethod("getMe", `{"id":1,"is_bot":true,"username":"`+botUsername+`"}`)
	return rb
}

// stickerReply builds "/addsticker <args>" replying to a static sticker.
func stickerReply(userID int64, args, fileID, emoji string) *models.Update {
	text := "/addsticker"
	if args != "" {
		text += " " + args
	}
	upd := testutil.NewPrivateMessage(userID, text)
	upd.Message.ReplyToMessage = &models.Message{
		Sticker: &models.Sticker{FileID: fileID, Type: "regular", Emoji: emoji},
	}
	return upd
}

// callTo returns the first recorded call to the named API method.
func callTo(rb *testutil.RecordingBot, method string) (testutil.SentCall, bool) {
	for _, c := range rb.Sent() {
		if c.Method == method {
			return c, true
		}
	}
	return testutil.SentCall{}, false
}

// The whole point of the shared-pack model: the sticker is added on behalf of
// the *pack owner*, not the caller, so a non-owner writing to the pack is the
// normal case rather than a refusal.
func TestAddSticker_NonOwnerWritesToConfiguredPack(t *testing.T) {
	rb := installAddSticker(t, "shared_by_testbot", "testbot")

	rb.Bot.ProcessUpdate(context.Background(), stickerReply(7, "", "src-file-id", ""))

	call, ok := callTo(rb, "addStickerToSet")
	if !ok {
		t.Fatalf("no addStickerToSet call; got %+v", rb.Sent())
	}
	if got := call.Form["user_id"]; got != "999" {
		t.Errorf("user_id = %q, want the pack owner %q", got, "999")
	}
	if got := call.Form["name"]; got != "shared_by_testbot" {
		t.Errorf("name = %q, want %q", got, "shared_by_testbot")
	}
	if got := call.Form["sticker"]; !strings.Contains(got, "src-file-id") {
		t.Errorf("sticker = %q, want it to carry the replied file_id", got)
	}
	rb.AssertSentText(t, "https://t.me/addstickers/shared_by_testbot")
	if _, ok := callTo(rb, "createNewStickerSet"); ok {
		t.Error("createNewStickerSet called for a pack that already exists")
	}
}

// The default pack follows the running bot: its name ends in the username
// getMe reports, so the same build works under any bot token.
func TestAddSticker_DefaultPackFollowsBotUsername(t *testing.T) {
	rb := installAddSticker(t, "", "otherbot")

	rb.Bot.ProcessUpdate(context.Background(), stickerReply(999, "", "src", ""))

	call, ok := callTo(rb, "addStickerToSet")
	if !ok {
		t.Fatalf("no addStickerToSet call; got %+v", rb.Sent())
	}
	if got := call.Form["name"]; got != "miti99_by_otherbot" {
		t.Errorf("name = %q, want the default pack for @otherbot", got)
	}
}

// A username known at startup (BOT_USERNAME or the startup getMe) is used as
// is: the command must not spend a getMe call re-learning it.
func TestAddSticker_SeededUsernameSkipsGetMe(t *testing.T) {
	t.Setenv("OWNER_ID", "999")
	t.Setenv("STICKER_PACK_NAME", "")
	rb := testutil.NewRecordingBot(t)
	reg, err := modules.Build([]string{"sticker"},
		map[string]modules.Factory{"sticker": sticker.New},
		storage.NewMemoryProvider(), modules.BuildOptions{BotUsername: "seededbot"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	modules.Install(rb.Bot, reg, modules.Auth{})

	rb.Bot.ProcessUpdate(context.Background(), stickerReply(999, "", "src", ""))

	if _, ok := callTo(rb, "getMe"); ok {
		t.Error("getMe called although the username was seeded")
	}
	call, ok := callTo(rb, "addStickerToSet")
	if !ok {
		t.Fatalf("no addStickerToSet call; got %+v", rb.Sent())
	}
	if got := call.Form["name"]; got != "miti99_by_seededbot" {
		t.Errorf("name = %q, want the default pack for @seededbot", got)
	}
}

// Precedence: explicit emoji args beat the replied sticker's own emoji.
func TestAddSticker_EmojiPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		args       string
		replyEmoji string
		want       string
	}{
		{name: "explicit args win", args: "😂🔥", replyEmoji: "🎉", want: "😂"},
		{name: "inherits replied emoji", args: "", replyEmoji: "🎉", want: "🎉"},
		{name: "falls back to default", args: "", replyEmoji: "", want: "⭐"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rb := installAddSticker(t, "shared_by_testbot", "testbot")

			rb.Bot.ProcessUpdate(context.Background(), stickerReply(999, tc.args, "src", tc.replyEmoji))

			call, ok := callTo(rb, "addStickerToSet")
			if !ok {
				t.Fatalf("no addStickerToSet call; got %+v", rb.Sent())
			}
			if got := call.Form["sticker"]; !strings.Contains(got, tc.want) {
				t.Errorf("sticker = %q, want emoji_list to carry %q", got, tc.want)
			}
		})
	}
}

// A missing set is created rather than reported, seeded with the sticker that
// triggered it, under the same owner every later add is attributed to.
func TestAddSticker_CreatesPackWhenMissing(t *testing.T) {
	rb := installAddSticker(t, "shared_by_testbot", "testbot")
	rb.FailMethodCode("addStickerToSet", 400, "Bad Request: STICKERSET_INVALID")

	rb.Bot.ProcessUpdate(context.Background(), stickerReply(7, "🔥", "src-file-id", ""))

	call, ok := callTo(rb, "createNewStickerSet")
	if !ok {
		t.Fatalf("no createNewStickerSet call; got %+v", rb.Sent())
	}
	if got := call.Form["user_id"]; got != "999" {
		t.Errorf("user_id = %q, want the pack owner %q", got, "999")
	}
	if got := call.Form["name"]; got != "shared_by_testbot" {
		t.Errorf("name = %q, want %q", got, "shared_by_testbot")
	}
	// The title is the slug half of the name — everything before "_by_<bot>".
	if got := call.Form["title"]; got != "shared" {
		t.Errorf("title = %q, want %q", got, "shared")
	}
	if got := call.Form["stickers"]; !strings.Contains(got, "src-file-id") || !strings.Contains(got, "🔥") {
		t.Errorf("stickers = %q, want the triggering sticker and its emoji", got)
	}
	rb.AssertSentText(t, "Created the shared pack")
}

// A set the add cannot see and the create cannot claim is one this bot cannot
// write to: created for a different owner, or by another bot.
func TestAddSticker_NameTakenBySetItCannotManage(t *testing.T) {
	rb := installAddSticker(t, "shared_by_testbot", "testbot")
	rb.FailMethodCode("addStickerToSet", 400, "Bad Request: STICKERSET_INVALID")
	rb.FailMethodCode("createNewStickerSet", 400, "Bad Request: PACK_SHORT_NAME_OCCUPIED")

	rb.Bot.ProcessUpdate(context.Background(), stickerReply(999, "", "src", ""))

	rb.AssertSentText(t, "already exists and this bot cannot manage it")
}

// The "_by_<bot_username>" suffix is Telegram's proof of authorship, so a
// mismatch is provable offline — before any download, upload, or API call.
func TestAddSticker_RefusesPackNotCreatedByThisBot(t *testing.T) {
	for _, packName := range []string{"shared_by_otherbot", "no_suffix_at_all", "_by_testbot"} {
		t.Run(packName, func(t *testing.T) {
			rb := installAddSticker(t, packName, "testbot")

			rb.Bot.ProcessUpdate(context.Background(), stickerReply(999, "", "src", ""))

			rb.AssertSentText(t, "not one this bot can manage")
			if _, ok := callTo(rb, "addStickerToSet"); ok {
				t.Error("addStickerToSet called for a pack this bot cannot manage")
			}
			if _, ok := callTo(rb, "createNewStickerSet"); ok {
				t.Error("createNewStickerSet called for a name this bot cannot use")
			}
		})
	}
}

func TestAddSticker_NoReply_ShowsUsage(t *testing.T) {
	rb := installAddSticker(t, "shared_by_testbot", "testbot")

	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(999, "/addsticker"))

	rb.AssertSentText(t, "Reply to a sticker, image, video or GIF")
	if _, ok := callTo(rb, "addStickerToSet"); ok {
		t.Error("addStickerToSet called with nothing to add")
	}
}

// A misconfigured owner is not the caller's fault and must not leak the bot's
// own env into a reply.
func TestAddSticker_OwnerIDUnset_RepliesGenerically(t *testing.T) {
	rb := installAddSticker(t, "shared_by_testbot", "testbot")
	t.Setenv("OWNER_ID", "") // overrides the helper's value for this test

	rb.Bot.ProcessUpdate(context.Background(), stickerReply(999, "", "src", ""))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "Something went wrong") {
		t.Errorf("reply = %q, want the generic failure", got)
	}
	if strings.Contains(got, "OWNER_ID") {
		t.Errorf("reply leaked the env var name: %q", got)
	}
	if _, ok := callTo(rb, "addStickerToSet"); ok {
		t.Error("addStickerToSet called without a pack owner")
	}
}

// A sticker already in a set satisfies its format's rules, so it is copied by
// file_id with no conversion — the format just has to be reported correctly.
func TestAddSticker_CopiesEachStickerFormat(t *testing.T) {
	cases := []struct {
		name    string
		sticker *models.Sticker
		want    string
	}{
		{name: "static", sticker: &models.Sticker{FileID: "src", Type: "regular"}, want: "static"},
		{name: "animated", sticker: &models.Sticker{FileID: "src", IsAnimated: true}, want: "animated"},
		{name: "video", sticker: &models.Sticker{FileID: "src", IsVideo: true}, want: "video"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rb := installAddSticker(t, "shared_by_testbot", "testbot")
			upd := testutil.NewPrivateMessage(999, "/addsticker")
			upd.Message.ReplyToMessage = &models.Message{Sticker: tc.sticker}

			rb.Bot.ProcessUpdate(context.Background(), upd)

			call, ok := callTo(rb, "addStickerToSet")
			if !ok {
				t.Fatalf("no addStickerToSet call; got %+v", rb.Sent())
			}
			if got := call.Form["sticker"]; !strings.Contains(got, `"format":"`+tc.want+`"`) {
				t.Errorf("sticker = %q, want format %q", got, tc.want)
			}
			// No upload: a file_id copy never touches uploadStickerFile.
			if _, ok := callTo(rb, "uploadStickerFile"); ok {
				t.Error("uploadStickerFile called for a file_id copy")
			}
		})
	}
}

// Format is per-sticker but sticker_type is fixed at creation, and the shared
// pack is regular — so these are refused whatever format they are in.
func TestAddSticker_RejectsMaskAndCustomEmojiStickers(t *testing.T) {
	for _, stickerType := range []string{"mask", "custom_emoji"} {
		t.Run(stickerType, func(t *testing.T) {
			rb := installAddSticker(t, "shared_by_testbot", "testbot")
			upd := testutil.NewPrivateMessage(999, "/addsticker")
			upd.Message.ReplyToMessage = &models.Message{
				Sticker: &models.Sticker{FileID: "src", Type: stickerType, IsVideo: true},
			}

			rb.Bot.ProcessUpdate(context.Background(), upd)

			rb.AssertSentText(t, "mask or custom-emoji")
			if _, ok := callTo(rb, "addStickerToSet"); ok {
				t.Error("addStickerToSet called with a mask or custom-emoji source")
			}
		})
	}
}

// Moving sources take the transcode path, which begins with getFile. Asserting
// the routing rather than the finished sticker keeps the test independent of
// whether the host has ffmpeg — the conversion itself is covered by
// TestToStickerWEBM_* against real ffmpeg.
func TestAddSticker_RoutesMovingSourcesToTheVideoPath(t *testing.T) {
	cases := []struct {
		name    string
		replied *models.Message
	}{
		{name: "animation (telegram GIF)", replied: &models.Message{Animation: &models.Animation{FileID: "src", MimeType: "video/mp4"}}},
		{name: "video", replied: &models.Message{Video: &models.Video{FileID: "src", MimeType: "video/mp4"}}},
		{name: "video note", replied: &models.Message{VideoNote: &models.VideoNote{FileID: "src"}}},
		{name: "gif document", replied: &models.Message{Document: &models.Document{FileID: "src", MimeType: "image/gif"}}},
		{name: "mp4 document", replied: &models.Message{Document: &models.Document{FileID: "src", MimeType: "video/mp4"}}},
		{name: "webm document", replied: &models.Message{Document: &models.Document{FileID: "src", MimeType: "video/webm"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rb := installAddSticker(t, "shared_by_testbot", "testbot")
			upd := testutil.NewPrivateMessage(999, "/addsticker")
			upd.Message.ReplyToMessage = tc.replied

			rb.Bot.ProcessUpdate(context.Background(), upd)

			if _, ok := callTo(rb, "getFile"); !ok {
				t.Errorf("getFile not called; the source did not reach the video path: %+v", rb.Sent())
			}
			// The stubbed download cannot yield a real clip, so the add must
			// never happen — what matters is that it failed at the conversion
			// rather than being refused as an unsupported reply.
			if _, ok := callTo(rb, "addStickerToSet"); ok {
				t.Error("addStickerToSet called with an unconvertible source")
			}
		})
	}
}

// An oversized clip is refused from the message metadata alone, so nothing is
// downloaded and ffmpeg is never reached.
func TestAddSticker_RefusesOversizedVideo(t *testing.T) {
	rb := installAddSticker(t, "shared_by_testbot", "testbot")
	upd := testutil.NewPrivateMessage(999, "/addsticker")
	upd.Message.ReplyToMessage = &models.Message{
		Video: &models.Video{FileID: "src", MimeType: "video/mp4", FileSize: 11 << 20},
	}

	rb.Bot.ProcessUpdate(context.Background(), upd)

	rb.AssertSentText(t, "too large")
	for _, method := range []string{"getFile", "uploadStickerFile", "addStickerToSet"} {
		if _, ok := callTo(rb, method); ok {
			t.Errorf("%s called for an oversized source", method)
		}
	}
}

// A document that is neither a supported image nor convertible video still
// gets the image advice, not a video error.
func TestAddSticker_RefusesUnsupportedDocument(t *testing.T) {
	rb := installAddSticker(t, "shared_by_testbot", "testbot")
	upd := testutil.NewPrivateMessage(999, "/addsticker")
	upd.Message.ReplyToMessage = &models.Message{
		Document: &models.Document{FileID: "src", MimeType: "application/pdf"},
	}

	rb.Bot.ProcessUpdate(context.Background(), upd)

	rb.AssertSentText(t, "not a supported image")
	if _, ok := callTo(rb, "getFile"); ok {
		t.Error("getFile called for an unsupported document")
	}
}

func TestAddSticker_NonEmojiArgRefused(t *testing.T) {
	rb := installAddSticker(t, "shared_by_testbot", "testbot")

	rb.Bot.ProcessUpdate(context.Background(), stickerReply(999, "hello", "src", ""))

	rb.AssertSentText(t, "is not an emoji")
	if _, ok := callTo(rb, "addStickerToSet"); ok {
		t.Error("addStickerToSet called with a non-emoji argument")
	}
}

// Telegram's MTProto codes arrive as "Bad Request: <CODE>" and must be
// translated, never echoed.
func TestAddSticker_APIRefusalsTranslated(t *testing.T) {
	cases := []struct {
		name        string
		description string
		want        string
	}{
		{name: "pack full", description: "Bad Request: STICKERS_TOO_MUCH", want: "full (120 stickers)"},
		{name: "bad emoji", description: "Bad Request: invalid sticker emojis", want: "rejected those emoji"},
		{name: "bad name", description: "Bad Request: PACK_SHORT_NAME_INVALID", want: "rejected the shared pack's name"},
		{name: "unmapped", description: "Bad Request: SOMETHING_NEW", want: "Something went wrong"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rb := installAddSticker(t, "shared_by_testbot", "testbot")
			rb.FailMethodCode("addStickerToSet", 400, tc.description)

			rb.Bot.ProcessUpdate(context.Background(), stickerReply(999, "", "src", ""))

			rb.AssertSentText(t, tc.want)
		})
	}
}
