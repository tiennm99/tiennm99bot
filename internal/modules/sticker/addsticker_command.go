package sticker

import (
	"bytes"
	"context"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

// usageAddSticker is the "you must reply to something" line.
const usageAddSticker = "Reply to a sticker, image, video or GIF with this command."

// imageDocumentMimes is the allowlist for a replied document handled as a still
// image. Anything else is rejected before a single byte is downloaded.
var imageDocumentMimes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
}

// movingDocumentMimes is the allowlist for a replied document handled as video.
//
// image/gif lives here rather than with the images: Go's stdlib would decode
// only its first frame, and a GIF is sent to be animated. Transcoding it to
// WEBM keeps the motion.
var movingDocumentMimes = map[string]bool{
	"image/gif":        true,
	"video/mp4":        true,
	"video/webm":       true,
	"video/quicktime":  true,
	"video/x-matroska": true,
}

// stickerSource is a resolved sticker ready to be added to a set: whatever the
// replied message carried, reduced to a file_id and the format that file is in.
type stickerSource struct {
	fileID string   // usable directly as InputSticker.Sticker
	format string   // one of the stickerFormat* constants
	emoji  []string // inherited from a replied sticker; at most one element
}

// addStickerCommand returns /addsticker — public, and deliberately unprefixed
// so the name matches what @Stickers uses.
//
// Every caller writes to the same shared pack, named by STICKER_PACK_NAME. The
// caller's own identity is not used anywhere: AddStickerToSet takes the *set
// owner's* user ID, so there is nothing per-user to store, key, or lock, and
// no ownership to check. That is what keeps the command stateless.
//
// The resolver is built here and captured by the handler so the bot's username
// is fetched at most once per process rather than once per invocation. It
// starts from botUsername, which is empty when startup could not learn it.
func addStickerCommand(botUsername string) modules.Command {
	resolver := &botUsernameResolver{username: botUsername}
	return modules.Command{
		Name:        "addsticker",
		Visibility:  modules.VisibilityPublic,
		Description: "Add the replied sticker, image or video to the shared pack",
		Parameters:  "[emoji...]",
		Handler: func(ctx context.Context, b *bot.Bot, update *models.Update) error {
			return handleAddSticker(ctx, b, update, resolver)
		},
	}
}

func handleAddSticker(ctx context.Context, b *bot.Bot, update *models.Update, resolver *botUsernameResolver) error {
	msg := update.Message
	if msg == nil {
		return nil
	}

	// The deadline is chosen from the source type, which the replied message
	// states without any API call. A transcode cannot fit the image budget, and
	// giving every invocation the video budget would let the still path hold the
	// single dispatcher worker far longer than it ever needs.
	ctx, cancel := stickerHandlerContext(ctx, hasMovingSource(msg.ReplyToMessage))
	defer cancel()

	pack, err := loadStickerPack()
	if err != nil {
		// A misconfiguration, not something the caller can fix: log it and
		// answer generically rather than explaining the bot's own env.
		return replyErr(ctx, b, msg, "util_addsticker_config", err)
	}

	// Checked before anything is downloaded or uploaded: a pack this bot cannot
	// manage is a dead end, and the create path below would strand the work.
	username, err := resolver.resolve(ctx, b)
	if err != nil {
		return replyErr(ctx, b, msg, "util_addsticker_username", err)
	}
	if pack.Name == "" {
		pack.Name = defaultPackName(username)
	}
	title, err := packTitle(pack.Name, username)
	if err != nil {
		return replyMisconfigured(ctx, b, msg, "util_addsticker_pack_name", err, packNotBotOwnedRefusal)
	}

	// Every argument is an emoji: there is no pack token to disambiguate, so a
	// stray word fails here rather than being silently read as something else.
	emoji, err := parseEmoji(strings.Fields(chathelper.ArgAfterCommand(msg.Text)))
	if err != nil {
		return replyErr(ctx, b, msg, "util_addsticker_emoji", err)
	}

	source, err := resolveStickerSource(ctx, b, pack.OwnerID, msg)
	if err != nil {
		return replyErr(ctx, b, msg, "util_addsticker_source", err)
	}

	// Precedence: explicit args, then the replied sticker's own emoji, then the
	// default. Telegram requires at least one.
	if len(emoji) == 0 {
		emoji = source.emoji
	}
	if len(emoji) == 0 {
		emoji = []string{defaultEmoji}
	}

	sticker := models.InputSticker{
		Sticker:   source.fileID,
		Format:    source.format,
		EmojiList: emoji,
	}

	// Add first, create only when the add proves the set is missing. The
	// alternative — probe with getStickerSet, then add — costs an extra call on
	// every invocation forever to save one on the single call that creates the
	// pack, and getStickerSet returns the set's whole sticker list to do it.
	_, err = b.AddStickerToSet(ctx, &bot.AddStickerToSetParams{
		UserID:  pack.OwnerID, // the set owner, never the caller
		Name:    pack.Name,
		Sticker: sticker,
	})
	switch {
	case err == nil:
		// No sticker count: nothing is stored, and reading one back would cost
		// a getStickerSet call that returns the whole set on every add.
		return chathelper.Reply(ctx, b, msg, "Added to the pack.\n"+stickerShareLink(pack.Name))
	case isStickerSetMissing(err):
		return createStickerPack(ctx, b, msg, pack, title, sticker)
	default:
		return replyAPIError(ctx, b, msg, "util_addsticker", err)
	}
}

// createStickerPack creates the shared pack with sticker as its first member.
//
// Reached only from a positive "the set does not exist", and a set cannot be
// created empty — so the sticker that triggered the creation is the one that
// seeds it. The owner is the same account every later add is attributed to,
// which is what keeps the set writable afterwards.
func createStickerPack(ctx context.Context, b *bot.Bot, msg *models.Message, pack stickerPack, title string, sticker models.InputSticker) error {
	_, err := b.CreateNewStickerSet(ctx, &bot.CreateNewStickerSetParams{
		UserID:   pack.OwnerID,
		Name:     pack.Name,
		Title:    title,
		Stickers: []models.InputSticker{sticker},
	})
	switch {
	case err == nil:
		return chathelper.Reply(ctx, b, msg, "Created the shared pack with this sticker.\n"+stickerShareLink(pack.Name))
	case isPackNameOccupied(err):
		// The add said the set does not exist and the create says the name is
		// taken. Both are true from this bot's side: a set stands there that it
		// cannot write to — created for a different owner, or by another bot.
		return replyMisconfigured(ctx, b, msg, "util_addsticker_name_taken", err, packNameTakenRefusal)
	default:
		return replyAPIError(ctx, b, msg, "util_addsticker_create", err)
	}
}

// resolveStickerSource turns the replied message into a sticker source.
//
// ownerID is the *pack owner*, needed only by the photo branch: UploadStickerFile
// associates the uploaded file with a user, and it must be the same account
// that owns the set the file is about to join.
func resolveStickerSource(ctx context.Context, b *bot.Bot, ownerID int64, msg *models.Message) (stickerSource, error) {
	replied := msg.ReplyToMessage
	if replied == nil {
		return stickerSource{}, refuse(usageAddSticker)
	}

	if st := replied.Sticker; st != nil {
		format, err := stickerFormatOf(st)
		if err != nil {
			return stickerSource{}, err
		}
		src := stickerSource{fileID: st.FileID, format: format}
		if st.Emoji != "" {
			// models.Sticker.Emoji is a single string, so a replied sticker
			// contributes at most one emoji.
			src.emoji = []string{st.Emoji}
		}
		return src, nil
	}

	if hasMovingSource(replied) {
		return resolveVideoSource(ctx, b, ownerID, replied)
	}

	return resolvePhotoSource(ctx, b, ownerID, replied)
}

// hasMovingSource reports whether the replied message carries footage that must
// be transcoded rather than resampled.
//
// Read from the message alone, with no API call, so the handler can pick its
// deadline before committing to either path.
func hasMovingSource(replied *models.Message) bool {
	if replied == nil {
		return false
	}
	if replied.Animation != nil || replied.Video != nil || replied.VideoNote != nil {
		return true
	}
	return replied.Document != nil && movingDocumentMimes[replied.Document.MimeType]
}

// resolveVideoSource turns a replied video, GIF or animation into an uploaded
// video sticker.
//
// Same shape as resolvePhotoSource — download, convert, upload — but the
// conversion shells out to ffmpeg, and the upload declares the video format so
// Telegram validates it as a WEBM rather than a still.
func resolveVideoSource(ctx context.Context, b *bot.Bot, ownerID int64, replied *models.Message) (stickerSource, error) {
	fileID, err := videoFileID(replied)
	if err != nil {
		return stickerSource{}, err
	}

	mediaCtx, cancelMedia := mediaContext(ctx)
	defer cancelMedia()

	raw, err := downloadFile(mediaCtx, b, fileID, maxVideoSourceBytes)
	if err != nil {
		return stickerSource{}, err
	}
	webm, err := toStickerWEBM(mediaCtx, raw)
	if err != nil {
		return stickerSource{}, err
	}

	uploaded, err := b.UploadStickerFile(mediaCtx, &bot.UploadStickerFileParams{
		UserID:        ownerID,
		Sticker:       &models.InputFileUpload{Filename: "sticker.webm", Data: bytes.NewReader(webm)},
		StickerFormat: stickerFormatVideo,
	})
	if err != nil {
		return stickerSource{}, err
	}
	return stickerSource{fileID: uploaded.FileID, format: stickerFormatVideo}, nil
}

// videoFileID picks the moving file to convert, refusing an oversized one
// before any byte is downloaded.
func videoFileID(replied *models.Message) (string, error) {
	var fileID string
	var size int64
	switch {
	case replied.Animation != nil:
		fileID, size = replied.Animation.FileID, replied.Animation.FileSize
	case replied.Video != nil:
		fileID, size = replied.Video.FileID, replied.Video.FileSize
	case replied.VideoNote != nil:
		fileID, size = replied.VideoNote.FileID, int64(replied.VideoNote.FileSize)
	case replied.Document != nil:
		fileID, size = replied.Document.FileID, replied.Document.FileSize
	default:
		return "", refuse(usageAddSticker)
	}
	if size > maxVideoSourceBytes {
		return "", refuse(tooLargeRefusal(maxVideoSourceBytes))
	}
	return fileID, nil
}

// stickerFormatOf reports which InputSticker.Format a replied sticker's file is
// already in, so it can be copied by file_id without conversion.
//
// A sticker that reached a user came from a set Telegram itself accepted, so it
// already satisfies every dimension, duration and size rule for its format —
// which is exactly why animated and video stickers need no ffmpeg here, while
// an ordinary video or GIF file does.
//
// Type is the half that is easy to miss: a mask sticker and a custom-emoji
// sticker can be any format, and both are invalid in a regular set, so
// switching on the format booleans alone would let them through to fail at the
// API with an opaque error.
func stickerFormatOf(st *models.Sticker) (string, error) {
	if st.Type != "" && st.Type != "regular" {
		return "", refuse("That is a mask or custom-emoji sticker, which cannot go in a regular pack.")
	}
	switch {
	case st.IsVideo:
		return stickerFormatVideo, nil
	case st.IsAnimated:
		return stickerFormatAnimated, nil
	default:
		return stickerFormatStatic, nil
	}
}

// resolvePhotoSource turns a replied photo or image document into an uploaded
// sticker file.
//
// Raw bytes cannot ride along on AddStickerToSet: the form builder honours
// attach:// only for []models.InputSticker, and the single InputSticker in
// AddStickerToSetParams falls through to a default that drops the attachment
// silently. So the image is uploaded first and the returned file_id is used.
func resolvePhotoSource(ctx context.Context, b *bot.Bot, ownerID int64, replied *models.Message) (stickerSource, error) {
	fileID, err := photoFileID(replied)
	if err != nil {
		return stickerSource{}, err
	}

	// Reserve the caller's reply tail: everything from here to the upload is
	// the slow leg. See mediaContext.
	mediaCtx, cancelMedia := mediaContext(ctx)
	defer cancelMedia()

	raw, err := downloadFile(mediaCtx, b, fileID, maxSourceBytes)
	if err != nil {
		return stickerSource{}, err
	}
	png, err := toStickerPNG(raw)
	if err != nil {
		return stickerSource{}, err
	}

	uploaded, err := b.UploadStickerFile(mediaCtx, &bot.UploadStickerFileParams{
		UserID:        ownerID,
		Sticker:       &models.InputFileUpload{Filename: "sticker.png", Data: bytes.NewReader(png)},
		StickerFormat: stickerFormatStatic,
	})
	if err != nil {
		return stickerSource{}, err
	}
	// Consumed immediately, so the file_id's undocumented validity window never
	// matters. Do not restructure this into upload-now-use-later.
	return stickerSource{fileID: uploaded.FileID, format: stickerFormatStatic}, nil
}

// photoFileID picks the file to convert from a replied message.
func photoFileID(replied *models.Message) (string, error) {
	if len(replied.Photo) > 0 {
		// Pick the largest by size rather than trusting the array's order.
		best := replied.Photo[0]
		for _, size := range replied.Photo[1:] {
			if size.FileSize > best.FileSize {
				best = size
			}
		}
		if best.FileSize > maxSourceBytes {
			return "", refuse(tooLargeRefusal(maxSourceBytes))
		}
		return best.FileID, nil
	}

	if doc := replied.Document; doc != nil {
		if !imageDocumentMimes[doc.MimeType] {
			return "", refuse("That file is not a supported image. Send a PNG, JPEG or WEBP.")
		}
		if doc.FileSize > maxSourceBytes {
			return "", refuse(tooLargeRefusal(maxSourceBytes))
		}
		return doc.FileID, nil
	}

	return "", refuse(usageAddSticker)
}
