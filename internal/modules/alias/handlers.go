package alias

import (
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	// handlerTimeout bounds every command handler and the fallback. The bot
	// dispatches updates inline on a single worker with no deadline of its own,
	// so without this the library's 60s per-call HTTP ceiling is the only bound.
	handlerTimeout = 10 * time.Second

	// maxNameLen matches Telegram's own username cap, which is the format these
	// names imitate.
	maxNameLen = 32

	// maxListBytes keeps /aliases inside Telegram's 4096-character sendMessage
	// limit, with room to spare for the trimming notice and multi-byte names.
	//
	// The budget counts the <code> markup, not only the names: Telegram measures
	// the message it is sent, and at 13 bytes a pair the tags outweigh a short
	// name.
	maxListBytes = 3800
)

// nameRe is the username shape: starts with a letter, then letters, digits or
// underscores.
//
// Telegram's own minimum is 5 characters; this deliberately allows 1, because
// the point of an alias is to be shorter than what it replaces and "gg" is a
// perfectly good name for a sticker.
var nameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

const usageAlias = "Reply to a message with /alias <name> to save it. The name is one word: letters, digits and underscores, starting with a letter."

const genericFailure = "Something went wrong. Try again in a moment."

// errUnknownKind marks a stored record this build cannot send.
var errUnknownKind = errors.New("alias: unknown kind")

// parseName validates the single name argument /alias, /insert and /unalias
// take, and the bare command name the fallback resolves.
//
// A leading "@" is stripped rather than rejected: the names are username-shaped
// and typing one with the sigil is a natural mistake, not a different request.
func parseName(raw string) (display, key string, err error) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return "", "", errors.New("empty")
	}
	if len(fields) > 1 {
		// Spaces are what separate a name from a sentence, so this is the check
		// that keeps "/alias my funny sticker" from silently saving "my".
		return "", "", errors.New("not one word")
	}
	name := strings.TrimPrefix(fields[0], "@")
	if name == "" || len(name) > maxNameLen || !nameRe.MatchString(name) {
		return "", "", errors.New("bad shape")
	}
	// Lookups are case-insensitive, so the key is folded while the display form
	// keeps whatever the assigner typed.
	return name, strings.ToLower(name), nil
}

// handleAlias saves the replied message under a name.
func (s *state) handleAlias(ctx context.Context, b *bot.Bot, update *models.Update) error {
	ctx, cancel := context.WithTimeout(ctx, handlerTimeout)
	defer cancel()

	msg := update.Message
	if msg == nil {
		return nil
	}

	// Capture first, only so the debug line below can report what arrived even
	// on the early returns. It is a pure read of the update — no store, no API
	// — so running it before the name checks costs nothing and changes nothing
	// about which reply the caller sees.
	entry, ok := capture(msg.ReplyToMessage)
	log.Debug("alias_capture", replyShape(msg.ReplyToMessage, entry, ok)...)

	display, key, err := parseName(chathelper.ArgAfterCommand(msg.Text))
	if err != nil {
		return chathelper.Reply(ctx, b, msg, usageAlias)
	}
	if msg.ReplyToMessage == nil {
		// Not necessarily a caller who forgot to reply: Telegram also delivers
		// /alias with the reply detached, and the two are indistinguishable
		// here, so the answer has to cover both.
		return chathelper.Reply(ctx, b, msg, noReplyRefusal)
	}

	// A real command always wins at dispatch, so an alias sharing its name
	// would be unreachable as /name and only work through /insert — a trap
	// worth refusing up front rather than explaining later. Checked against the
	// live registry, so it covers every module loaded in this deploy.
	if s.reg != nil {
		if _, taken := s.reg.AllCommands[key]; taken {
			return chathelper.ReplyHTML(ctx, b, msg, fmt.Sprintf(
				"<code>/%s</code> is already a command of mine. Pick another name.",
				html.EscapeString(key)))
		}
	}

	if !ok {
		if fromAnotherBot(msg.ReplyToMessage) {
			return chathelper.Reply(ctx, b, msg, otherBotRefusal)
		}
		// Nothing arrived to judge. Listing the supported kinds here would
		// blame the format of a message this bot was never shown — the reply
		// may well have been a photo.
		if !hasContent(msg.ReplyToMessage) {
			return chathelper.Reply(ctx, b, msg, strippedReplyRefusal)
		}
		return chathelper.Reply(ctx, b, msg, unsupportedRefusal)
	}
	entry.Name = display
	entry.CreatedAt = chathelper.NowMillis()
	if msg.From != nil {
		entry.OwnerID = msg.From.ID
	}

	// Read before writing purely to word the reply. The write is unconditional
	// either way — last assignment wins — so a race here costs a wrong noun in
	// one sentence, not a wrong binding.
	previous, existed, err := s.get(ctx, key)
	if err != nil {
		log.Error("alias_save_lookup", "err", err)
		return chathelper.Reply(ctx, b, msg, genericFailure)
	}

	if err := s.store.Put(ctx, key, entry); err != nil {
		log.Error("alias_save", "err", err)
		return chathelper.Reply(ctx, b, msg, genericFailure)
	}

	if existed {
		return chathelper.ReplyHTML(ctx, b, msg, fmt.Sprintf(
			"Replaced <code>/insert %s</code> — it was a %s, now it is a %s.",
			html.EscapeString(display), describe(previous.Kind), describe(entry.Kind)))
	}
	return chathelper.ReplyHTML(ctx, b, msg, fmt.Sprintf(
		"Saved. Use <code>/insert %s</code> to send that %s.",
		html.EscapeString(display), describe(entry.Kind)))
}

// handleInsert sends back whatever is stored under a name.
func (s *state) handleInsert(ctx context.Context, b *bot.Bot, update *models.Update) error {
	ctx, cancel := context.WithTimeout(ctx, handlerTimeout)
	defer cancel()

	msg := update.Message
	if msg == nil {
		return nil
	}

	display, key, err := parseName(chathelper.ArgAfterCommand(msg.Text))
	if err != nil {
		return chathelper.Reply(ctx, b, msg, "Usage: /insert <name>")
	}

	entry, found, err := s.get(ctx, key)
	if err != nil {
		log.Error("alias_insert_lookup", "err", err)
		return chathelper.Reply(ctx, b, msg, genericFailure)
	}
	if !found {
		return chathelper.ReplyHTML(ctx, b, msg, fmt.Sprintf(
			"Nothing is saved as <code>%s</code>. Reply to a message with <code>/alias %s</code> to save one.",
			html.EscapeString(display), html.EscapeString(display)))
	}

	if err := send(ctx, b, msg, entry); err != nil {
		// A file_id can stop working — the sender deleted the file, or the
		// record predates a kind this build understands. Neither is worth an
		// opaque failure, and neither is retryable by the caller.
		log.Error("alias_insert_send", "name", key, "kind", entry.Kind, "err", err)
		return chathelper.ReplyHTML(ctx, b, msg, fmt.Sprintf(
			"<code>%s</code> can no longer be sent. Save it again with <code>/alias %s</code>.",
			html.EscapeString(display), html.EscapeString(display)))
	}
	return nil
}

// handleUnalias deletes a saved name.
//
// Anyone may delete anyone's alias, which is the same rule /alias already
// follows by overwriting: the namespace is shared, so the permission model is
// too. A per-owner restriction would leave an alias whose assigner has left the
// chat permanently unremovable.
func (s *state) handleUnalias(ctx context.Context, b *bot.Bot, update *models.Update) error {
	ctx, cancel := context.WithTimeout(ctx, handlerTimeout)
	defer cancel()

	msg := update.Message
	if msg == nil {
		return nil
	}

	display, key, err := parseName(chathelper.ArgAfterCommand(msg.Text))
	if err != nil {
		return chathelper.Reply(ctx, b, msg, "Usage: /unalias <name>")
	}

	// Read first so a missing name is reported as such. Delete on a missing key
	// is not distinguishable from a successful one in the store contract, and
	// "deleted" for something that never existed reads as a bug.
	if _, found, err := s.get(ctx, key); err != nil {
		log.Error("alias_unalias_lookup", "err", err)
		return chathelper.Reply(ctx, b, msg, genericFailure)
	} else if !found {
		return chathelper.ReplyHTML(ctx, b, msg, fmt.Sprintf(
			"Nothing is saved as <code>%s</code>.", html.EscapeString(display)))
	}

	if err := s.store.Delete(ctx, key); err != nil {
		log.Error("alias_unalias", "err", err)
		return chathelper.Reply(ctx, b, msg, genericFailure)
	}
	return chathelper.ReplyHTML(ctx, b, msg, fmt.Sprintf(
		"Deleted <code>%s</code>.", html.EscapeString(display)))
}

// handleFallback answers a /command nobody registered by treating it as an
// alias name — the whole point of the feature: /cheer instead of /insert cheer.
//
// Silent on a miss, deliberately. This sees every unrecognised command in every
// chat the bot is in, so replying would turn a typo like /pign into noise, and
// would confirm to anyone probing which names are taken.
func (s *state) handleFallback(ctx context.Context, b *bot.Bot, name string, update *models.Update) error {
	ctx, cancel := context.WithTimeout(ctx, handlerTimeout)
	defer cancel()

	msg := update.Message
	if msg == nil {
		return nil
	}
	// The name arrives already lowercased by the dispatcher, but it has not been
	// through parseName: reject anything that could not have been stored, so a
	// malformed command never reaches the store as a key.
	if _, key, err := parseName(name); err != nil {
		return nil
	} else if entry, found, err := s.get(ctx, key); err != nil {
		log.Error("alias_fallback_lookup", "name", key, "err", err)
		return nil
	} else if found {
		return send(ctx, b, msg, entry)
	}
	return nil
}

// handleAliases lists every saved name with what it holds.
func (s *state) handleAliases(ctx context.Context, b *bot.Bot, update *models.Update) error {
	ctx, cancel := context.WithTimeout(ctx, handlerTimeout)
	defer cancel()

	msg := update.Message
	if msg == nil {
		return nil
	}

	// Scan, not List plus a read per name: the kind lives in the document, so a
	// name-only listing would cost a round trip per alias to label each line.
	// Scan also returns key order, which is what makes the same command twice
	// produce the same list.
	docs, err := s.store.Scan(ctx, "")
	if err != nil {
		log.Error("alias_list", "err", err)
		return chathelper.Reply(ctx, b, msg, genericFailure)
	}
	if len(docs) == 0 {
		return chathelper.Reply(ctx, b, msg,
			"No aliases saved yet. Reply to a message with /alias <name> to save one.")
	}
	return chathelper.ReplyHTML(ctx, b, msg, renderNames(docs))
}

// renderNames formats the list as Telegram HTML, trimmed to fit one message.
//
// One line per alias, each showing what the name holds, with the invocation
// wrapped in <code> so tapping it copies a command ready to send.
func renderNames(docs []storage.Doc[Alias]) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d aliases:", len(docs))

	for i, doc := range docs {
		// Escaped despite parseName restricting names to [a-zA-Z0-9_]: the
		// validation and the rendering are far apart, and a later relaxation of
		// the name rules must not silently become an HTML injection.
		line := "\n<code>/" + html.EscapeString(doc.ID) + "</code> — " + kindLabel(doc.Val.Kind)
		// Reserve room for the "…and N more" tail before committing to a line,
		// so the trim can never be what pushes the message over the limit.
		if sb.Len()+len(line) > maxListBytes {
			fmt.Fprintf(&sb, "\n…and %d more.", len(docs)-i)
			return sb.String()
		}
		sb.WriteString(line)
	}
	return sb.String()
}

// kindLabel names a kind for the list.
//
// Differs from describe in exactly one case: text reads as "message" in a
// sentence ("send that message") but as "text" in a column of kinds, next to
// sticker and video. An empty kind means a stored document without one.
func kindLabel(kind string) string {
	switch kind {
	case kindText:
		return "text"
	case "":
		return "unreadable"
	}
	return describe(kind)
}

// get reads an alias. A missing name is not an error — it is the normal state
// for a name nobody has claimed.
func (s *state) get(ctx context.Context, key string) (Alias, bool, error) {
	a, _, err := s.store.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return Alias{}, false, nil
	}
	if err != nil {
		return Alias{}, false, err
	}
	return a, true, nil
}

// describe names a kind in the words a user would use for it.
func describe(kind string) string {
	switch kind {
	case kindSticker:
		return "sticker"
	case kindPhoto:
		return "photo"
	case kindAnimation:
		return "GIF"
	case kindVideo:
		return "video"
	case kindVideoNote:
		return "video note"
	case kindAudio:
		return "audio track"
	case kindVoice:
		return "voice message"
	case kindDocument:
		return "file"
	case kindText:
		return "message"
	}
	return "message"
}
