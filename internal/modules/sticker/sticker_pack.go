package sticker

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/miti99bot/internal/log"
	"github.com/tiennm99/miti99bot/internal/modules/util/chathelper"
)

const (
	// stickerPackNameEnv overrides which set /addsticker writes to. The name
	// must end in "_by_<bot_username>", the only thing that makes a set
	// bot-manageable; packTitle checks it before any upload. A set that does
	// not exist yet is created by the first successful /addsticker.
	stickerPackNameEnv = "STICKER_PACK_NAME"

	// defaultStickerPackSlug names the shared pack used when the env is unset.
	// The full name is this slug plus "_by_<bot_username>", so the default
	// follows whichever bot runs the code.
	defaultStickerPackSlug = "miti99"

	// stickerPackOwnerEnv reuses the bot-wide owner setting rather than
	// introducing a second variable: AddStickerToSet needs the *set owner's*
	// user ID, and the default pack above belongs to the bot owner. A pack
	// owned by any other account needs this env pointed at that account.
	stickerPackOwnerEnv = "OWNER_ID"

	// InputSticker.Format values. Since Bot API 7.2 the format is a property of
	// each sticker rather than of the set — createNewStickerSet lost its
	// sticker_format parameter and StickerSet lost is_animated/is_video — so
	// one pack holds all three side by side and no migration is needed to
	// start adding a new one.
	stickerFormatStatic   = "static"   // .WEBP or .PNG
	stickerFormatAnimated = "animated" // .TGS
	stickerFormatVideo    = "video"    // .WEBM

	// maxStickersPerPack is Telegram's documented ceiling for a regular set.
	// Not enforced locally — the server is the authority and a local copy would
	// go stale — but quoted back to the user when the server refuses.
	maxStickersPerPack = 120

	// maxSetNameLen is Telegram's cap on a sticker set's short name.
	maxSetNameLen = 64

	// setNameSuffix prefixes the mandatory "_by_<bot_username>" tail.
	setNameSuffix = "_by_"

	// stickerHandlerTimeout bounds the /addsticker handler for a still source.
	//
	// Nothing else does. The bot registers handlers with
	// bot.WithNotAsyncHandlers() and one worker, so updates run inline on the
	// polling goroutine, and the handler context is rootCtx, which carries no
	// deadline — the only remaining ceiling is the library's shared 60s HTTP
	// client, per call. The photo path makes three sequential API calls and
	// could otherwise freeze the bot for every user for minutes.
	stickerHandlerTimeout = 10 * time.Second

	// stickerVideoHandlerTimeout is the same bound for a source that has to be
	// transcoded: the download can be ten times larger and an ffmpeg encode
	// sits between it and the upload.
	//
	// The still budget is kept separate rather than raising both, because this
	// number is time the whole bot is unresponsive and only one of the two
	// paths needs it.
	stickerVideoHandlerTimeout = 45 * time.Second
)

// stickerPack is the resolved target of /addsticker.
type stickerPack struct {
	Name    string // Telegram set name, e.g. "miti99_by_examplebot"; empty until defaulted
	OwnerID int64  // the account the set belongs to; AddStickerToSet demands it
}

// errNoPackOwner means the owner ID is unset, so no sticker can be added.
// Internal, not user-facing: nothing the caller does fixes a misconfiguration.
var errNoPackOwner = errors.New("util: sticker pack owner ID unset")

// loadStickerPack resolves the shared pack from the environment. Name is left
// empty when STICKER_PACK_NAME is unset: the default depends on the bot's
// username, which the caller fills in with defaultPackName once resolved.
//
// Read per invocation rather than captured at startup, matching how gold and
// lol read their credentials: it keeps the command testable with t.Setenv and
// costs nothing next to the API calls that follow.
func loadStickerPack() (stickerPack, error) {
	name := strings.TrimSpace(os.Getenv(stickerPackNameEnv))
	ownerID, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(stickerPackOwnerEnv)), 10, 64)
	if err != nil || ownerID == 0 {
		return stickerPack{}, errNoPackOwner
	}
	return stickerPack{Name: name, OwnerID: ownerID}, nil
}

// defaultPackName is the shared pack's name for a bot with this username.
func defaultPackName(botUsername string) string {
	return defaultStickerPackSlug + setNameSuffix + botUsername
}

// stickerShareLink is the public URL of a sticker set.
func stickerShareLink(setName string) string {
	return "https://t.me/addstickers/" + setName
}

// errNoUsername means the bot's own username is unavailable, so the configured
// pack cannot be checked against it. Internal and transient, not user-facing.
var errNoUsername = errors.New("util: bot has no username")

// errPackNotBotOwned means the configured name cannot belong to a set this bot
// created. Internal: the reply is a fixed operator-facing sentence.
var errPackNotBotOwned = errors.New("util: configured pack is not manageable by this bot")

// packNotBotOwnedRefusal answers a configured name this bot provably cannot
// manage. It names no environment variable — the detail goes to the log.
const packNotBotOwnedRefusal = "The shared pack is not one this bot can manage. Ask the bot owner to check its configuration."

// packNameTakenRefusal answers a name that is occupied by a set this bot cannot
// write to: created by another bot, or by this bot for a different owner.
const packNameTakenRefusal = "A sticker set with that name already exists and this bot cannot manage it. Ask the bot owner to check it."

// packTitle validates that name could only be a set this bot created, and
// returns the slug half of it for use as the title at creation time.
//
// The "_by_<bot_username>" suffix is Telegram's own proof of authorship:
// createNewStickerSet *requires* the calling bot's username there, so a name
// without it cannot have come from this bot, and a name with it cannot have
// come from another. That makes the check purely local — no API call, and no
// need for a field the API does not expose.
//
// It cannot prove the *owner*, only the creator. StickerSet carries no owner
// ID, so "created by this bot but for a different user" is indistinguishable
// here and only surfaces when Telegram refuses the write.
func packTitle(name, botUsername string) (string, error) {
	if botUsername == "" {
		return "", errNoUsername
	}
	if len(name) > maxSetNameLen {
		return "", errPackNotBotOwned
	}
	suffix := setNameSuffix + botUsername
	// Strictly longer, not equal: the slug half must carry at least one
	// character, and Telegram requires a name to begin with a letter.
	if len(name) <= len(suffix) || !strings.EqualFold(name[len(name)-len(suffix):], suffix) {
		return "", errPackNotBotOwned
	}
	return name[:len(name)-len(suffix)], nil
}

// botUsernameResolver caches the bot's username.
//
// main seeds it from BOT_USERNAME or a startup getMe. The bot starts with
// bot.WithSkipGetMe(), so when neither produced a username nothing populates
// one until this asks. Failures are never cached: a transient GetMe error must
// not disable /addsticker for the process's lifetime.
type botUsernameResolver struct {
	mu       sync.Mutex
	username string
}

// resolve returns the bot's username, calling GetMe at most once per success.
// It takes the handler's *bot.Bot rather than Deps.Bot, which is documented
// nil-safe and is nil under BuildOptions{}.
func (r *botUsernameResolver) resolve(ctx context.Context, b *bot.Bot) (string, error) {
	r.mu.Lock()
	cached := r.username
	r.mu.Unlock()
	if cached != "" {
		return cached, nil
	}

	me, err := b.GetMe(ctx)
	if err != nil {
		return "", err
	}
	if me == nil || me.Username == "" {
		return "", errNoUsername
	}

	r.mu.Lock()
	r.username = me.Username
	r.mu.Unlock()
	return me.Username, nil
}

// isStickerSetMissing reports whether err positively says the set does not
// exist on Telegram's side.
//
// Classification is positive-only, and deliberately so: this is the signal
// that authorises *creating* a set. A network blip, a 429, or a context
// cancelled by SIGTERM must never be read as "the pack is gone", or a routine
// failure would turn into an attempt to create a set that already exists.
func isStickerSetMissing(err error) bool {
	return errors.Is(err, bot.ErrorBadRequest) &&
		strings.Contains(err.Error(), "STICKERSET_INVALID")
}

// isPackNameOccupied reports whether err says the short name is already taken.
//
// Reaching this after isStickerSetMissing is the one proof available that a set
// exists under the configured name which this bot cannot write to. Kept as its
// own predicate rather than folded into apiRefusal, because that function's job
// is wording and this one's is a control-flow decision.
func isPackNameOccupied(err error) bool {
	return err != nil && contains(err.Error(), "PACK_SHORT_NAME_OCCUPIED", "already occupied")
}

// userError carries text meant to be shown to the user verbatim.
//
// There are two kinds of failure here and they must never be confused: a
// refusal the user can act on ("that image is too large"), and an internal
// failure that must not reach a reply at all — a transport error's text can
// embed the bot token. Wrapping the first kind in a distinct type makes "is
// this safe to echo?" a type question instead of a judgement call per site.
type userError struct{ msg string }

func (e userError) Error() string { return e.msg }

// refuse builds a userError. Its text is replied verbatim, so write it as a
// sentence addressed to the user.
func refuse(msg string) error { return userError{msg: msg} }

const genericFailure = "Something went wrong. Try again in a moment."

// stickerHandlerContext applies the /addsticker deadline for the path the
// replied source will take.
func stickerHandlerContext(ctx context.Context, transcoding bool) (context.Context, context.CancelFunc) {
	if transcoding {
		return context.WithTimeout(ctx, stickerVideoHandlerTimeout)
	}
	return context.WithTimeout(ctx, stickerHandlerTimeout)
}

// mediaContext bounds the download-and-upload leg, reserving the tail of the
// parent's budget for the reply that follows.
//
// The photo path spends most of its deadline before it has anything to say.
// Run on the bare handler context, a slow link exhausted the whole 10s inside
// the media leg, and the reply — including the error reply explaining what went
// wrong — was then sent on a dead context, so the user saw nothing at all.
func mediaContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return chathelper.FetchContext(ctx)
}

// replyErr turns a handler error into a reply.
//
// A userError is shown verbatim — it was written for the user. Anything else is
// logged and replaced with a generic line: internal errors can carry a download
// URL with the bot token in it, and that must never be echoed.
func replyErr(ctx context.Context, b *bot.Bot, msg *models.Message, op string, err error) error {
	var ue userError
	if errors.As(err, &ue) {
		return chathelper.Reply(ctx, b, msg, ue.msg)
	}
	log.Error(op, "err", err)
	return chathelper.Reply(ctx, b, msg, genericFailure)
}

// replyMisconfigured answers a bad pack configuration.
//
// Distinct from replyErr because these failures need both halves: the operator
// needs the precise cause in the log, and the user needs a sentence that tells
// them to go tell the owner. A userError would give the second without the
// first; a plain error would give the first and a useless generic reply.
func replyMisconfigured(ctx context.Context, b *bot.Bot, msg *models.Message, op string, err error, text string) error {
	log.Error(op, "err", err)
	return chathelper.Reply(ctx, b, msg, text)
}

// apiRefusal maps a Telegram API error to user-facing text, or returns ok=false
// when the error has no specific meaning and should be treated as a failure.
//
// Matching is on MTProto code substrings rather than prose. The Bot API server
// rewrites only some of these into English (STICKER_EMOJI_INVALID); the rest
// arrive as "Bad Request: <CODE>", and the prose could change without notice.
// Both forms are matched where they differ.
func apiRefusal(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	text := err.Error()
	switch {
	case contains(text, "PACK_SHORT_NAME_INVALID", "invalid sticker set name"):
		return "Telegram rejected the shared pack's name. Ask the bot owner to check its configuration.", true
	case contains(text, "PACK_TITLE_INVALID"):
		return "Telegram rejected the shared pack's title. Ask the bot owner to check its configuration.", true
	case contains(text, "STICKERS_TOO_MUCH"):
		return "The shared pack is full (" + strconv.Itoa(maxStickersPerPack) + " stickers).", true
	case contains(text, "STICKER_EMOJI_INVALID", "invalid sticker emojis"):
		return "Telegram rejected those emoji. Try different ones.", true
	case contains(text, "too many emoji specified"):
		return "At most 20 emoji per sticker.", true
	case contains(text, "STICKER_PNG_DIMENSIONS", "STICKER_DIMENSIONS_INVALID"):
		return "Telegram rejected that image's dimensions.", true
	}
	return "", false
}

// replyAPIError converts a Telegram API error into a reply. Errors with no
// specific mapping are logged and answered generically — the raw error never
// reaches the user.
func replyAPIError(ctx context.Context, b *bot.Bot, msg *models.Message, op string, err error) error {
	if text, ok := apiRefusal(err); ok {
		return chathelper.Reply(ctx, b, msg, text)
	}
	log.Error(op, "err", err)
	return chathelper.Reply(ctx, b, msg, genericFailure)
}

func contains(text string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(text, n) {
			return true
		}
	}
	return false
}
