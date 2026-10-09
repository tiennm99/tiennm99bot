package alias

import (
	"context"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// The kinds an alias can hold. Each maps to exactly one Telegram send method,
// which is the whole reason the kind is stored rather than inferred later: a
// bare file_id does not say which send call will accept it.
const (
	kindSticker   = "sticker"
	kindPhoto     = "photo"
	kindAnimation = "animation"
	kindVideo     = "video"
	kindVideoNote = "video_note"
	kindAudio     = "audio"
	kindVoice     = "voice"
	kindDocument  = "document"
	kindText      = "text"
)

// unsupportedRefusal lists what can be saved, so a refusal teaches rather than
// only denies.
const unsupportedRefusal = "That message cannot be saved. Reply to a sticker, photo, GIF, video, video note, audio, voice message, file, or plain text."

// forwardAdvice is the one action that recovers a message this bot cannot
// read. A forwarded copy is a new message sent by a user, so it arrives with
// its content intact and captures like anything else.
//
// Shared by every refusal below, because "forward it and reply to the copy" is
// the answer to all of them — only the reason differs.
const forwardAdvice = "Forward it into this chat, then reply to your copy with /alias <name>."

// otherBotRefusal explains why another bot's message arrived empty.
//
// Telegram strips another bot's message out of a reply unless this bot has
// Bot-to-Bot Communication Mode enabled in BotFather and receives every group
// message — as a group admin, or with privacy mode off. Both are chat and
// BotFather settings, not code, so the refusal names them alongside the
// forward workaround rather than letting unsupportedRefusal imply the format
// was wrong.
const otherBotRefusal = "Telegram hid that bot's message from me, so I cannot save it. " +
	"I can read other bots' messages only when I am an admin in this chat and Bot-to-Bot Communication is on for me in @BotFather. " +
	forwardAdvice

// strippedReplyRefusal answers a reply Telegram delivered empty: it carried a
// message id but no content field at all.
//
// Separate from otherBotRefusal because the sender is not always marked — an
// anonymous or service-posted message can arrive the same way — and separate
// from unsupportedRefusal because listing the supported kinds would be a lie
// about what went wrong. The message may well have been a photo; this bot was
// simply never shown it.
const strippedReplyRefusal = "That message reached me with no content, so there is nothing for me to save. " + forwardAdvice

// noReplyRefusal answers /alias <name> that arrived with no reply attached.
//
// It cannot tell a caller who forgot to reply from one whose reply Telegram did
// not pass along, so it addresses both in order of likelihood.
const noReplyRefusal = "Reply to the message you want to save. If you did reply, Telegram did not pass that message to me. " + forwardAdvice

// fromAnotherBot reports whether a reply that captured nothing came from a bot.
//
// Only consulted after capture fails: this bot's *own* messages are readable,
// so anything of ours captures normally and never reaches here.
func fromAnotherBot(replied *models.Message) bool {
	return replied != nil && replied.From != nil && replied.From.IsBot
}

// contentFields names every content field a replied message can carry, with a
// test for its presence.
//
// One table, so the "did anything arrive at all" check and the debug line's
// field list cannot drift apart. It deliberately covers more than capture
// handles: a poll or a location is content this module refuses, which is a
// different answer to the caller than content that never arrived.
var contentFields = []struct {
	name    string
	present func(*models.Message) bool
}{
	{"text", func(m *models.Message) bool { return m.Text != "" }},
	{"caption", func(m *models.Message) bool { return m.Caption != "" }},
	{"sticker", func(m *models.Message) bool { return m.Sticker != nil }},
	{"photo", func(m *models.Message) bool { return len(m.Photo) > 0 }},
	{"animation", func(m *models.Message) bool { return m.Animation != nil }},
	{"video", func(m *models.Message) bool { return m.Video != nil }},
	{"video_note", func(m *models.Message) bool { return m.VideoNote != nil }},
	{"audio", func(m *models.Message) bool { return m.Audio != nil }},
	{"voice", func(m *models.Message) bool { return m.Voice != nil }},
	{"document", func(m *models.Message) bool { return m.Document != nil }},
	{"location", func(m *models.Message) bool { return m.Location != nil }},
	{"contact", func(m *models.Message) bool { return m.Contact != nil }},
	{"poll", func(m *models.Message) bool { return m.Poll != nil }},
	{"dice", func(m *models.Message) bool { return m.Dice != nil }},
	{"venue", func(m *models.Message) bool { return m.Venue != nil }},
	{"game", func(m *models.Message) bool { return m.Game != nil }},
}

// hasContent reports whether replied carries any content field at all.
//
// False is the signature of a message that was delivered stripped rather than
// one whose kind is unsupported, and the two need different advice.
func hasContent(replied *models.Message) bool {
	if replied == nil {
		return false
	}
	for _, f := range contentFields {
		if f.present(replied) {
			return true
		}
	}
	return false
}

// capture reduces a replied message to a storable alias.
//
// Order matters where Telegram populates more than one field: a GIF arrives as
// an Animation *and* a Document, and a video note as a VideoNote, so the more
// specific kind is claimed first or the alias would come back as a plain file.
func capture(replied *models.Message) (Alias, bool) {
	switch {
	case replied == nil:
		return Alias{}, false
	case replied.Sticker != nil:
		return Alias{Kind: kindSticker, FileID: replied.Sticker.FileID}, true
	case replied.Animation != nil:
		return Alias{Kind: kindAnimation, FileID: replied.Animation.FileID, Text: replied.Caption, Entities: replied.CaptionEntities}, true
	case replied.VideoNote != nil:
		return Alias{Kind: kindVideoNote, FileID: replied.VideoNote.FileID}, true
	case replied.Video != nil:
		return Alias{Kind: kindVideo, FileID: replied.Video.FileID, Text: replied.Caption, Entities: replied.CaptionEntities}, true
	case replied.Voice != nil:
		return Alias{Kind: kindVoice, FileID: replied.Voice.FileID, Text: replied.Caption, Entities: replied.CaptionEntities}, true
	case replied.Audio != nil:
		return Alias{Kind: kindAudio, FileID: replied.Audio.FileID, Text: replied.Caption, Entities: replied.CaptionEntities}, true
	case len(replied.Photo) > 0:
		return Alias{Kind: kindPhoto, FileID: largestPhoto(replied.Photo), Text: replied.Caption, Entities: replied.CaptionEntities}, true
	case replied.Document != nil:
		return Alias{Kind: kindDocument, FileID: replied.Document.FileID, Text: replied.Caption, Entities: replied.CaptionEntities}, true
	case replied.Text != "":
		// Entities come along: the text is re-sent unchanged, so the offsets
		// they carry stay valid and the formatting survives.
		return Alias{Kind: kindText, Text: replied.Text, Entities: replied.Entities}, true
	}
	return Alias{}, false
}

// largestPhoto picks the best size by file size rather than trusting the
// array's order.
func largestPhoto(sizes []models.PhotoSize) string {
	best := sizes[0]
	for _, size := range sizes[1:] {
		if size.FileSize > best.FileSize {
			best = size
		}
	}
	return best.FileID
}

// send posts a stored alias into the chat msg came from.
//
// MessageThreadID is forwarded on every call for the reason chathelper.Reply
// documents: without it Telegram routes the message to a forum supergroup's
// General topic instead of the topic the command was typed in.
func send(ctx context.Context, b *bot.Bot, msg *models.Message, a Alias) error {
	chatID := msg.Chat.ID
	thread := msg.MessageThreadID
	file := &models.InputFileString{Data: a.FileID}

	var err error
	switch a.Kind {
	case kindSticker:
		// SendStickerParams carries no Caption field — a sticker cannot have one.
		_, err = b.SendSticker(ctx, &bot.SendStickerParams{
			ChatID: chatID, MessageThreadID: thread, Sticker: file,
		})
	case kindPhoto:
		_, err = b.SendPhoto(ctx, &bot.SendPhotoParams{
			ChatID: chatID, MessageThreadID: thread, Photo: file, Caption: a.Text, CaptionEntities: a.Entities,
		})
	case kindAnimation:
		_, err = b.SendAnimation(ctx, &bot.SendAnimationParams{
			ChatID: chatID, MessageThreadID: thread, Animation: file, Caption: a.Text, CaptionEntities: a.Entities,
		})
	case kindVideo:
		_, err = b.SendVideo(ctx, &bot.SendVideoParams{
			ChatID: chatID, MessageThreadID: thread, Video: file, Caption: a.Text, CaptionEntities: a.Entities,
		})
	case kindVideoNote:
		// Nor does a video note: Telegram renders it as a bare round clip.
		_, err = b.SendVideoNote(ctx, &bot.SendVideoNoteParams{
			ChatID: chatID, MessageThreadID: thread, VideoNote: file,
		})
	case kindAudio:
		_, err = b.SendAudio(ctx, &bot.SendAudioParams{
			ChatID: chatID, MessageThreadID: thread, Audio: file, Caption: a.Text, CaptionEntities: a.Entities,
		})
	case kindVoice:
		_, err = b.SendVoice(ctx, &bot.SendVoiceParams{
			ChatID: chatID, MessageThreadID: thread, Voice: file, Caption: a.Text, CaptionEntities: a.Entities,
		})
	case kindDocument:
		_, err = b.SendDocument(ctx, &bot.SendDocumentParams{
			ChatID: chatID, MessageThreadID: thread, Document: file, Caption: a.Text, CaptionEntities: a.Entities,
		})
	case kindText:
		_, err = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID, MessageThreadID: thread, Text: a.Text, Entities: a.Entities,
		})
	default:
		// A kind written by a newer version of this module, or a corrupted
		// record. Neither is the caller's fault and neither is retryable.
		return errUnknownKind
	}
	return err
}
