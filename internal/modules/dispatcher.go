package modules

import (
	"context"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/metrics"
)

// Auth gates Protected/Private commands by sender Telegram user ID. Public
// commands are always allowed. A zero BotOwnerID + empty AdminUserIDs means
// every Protected/Private command is denied — the safe default for an
// unconfigured deployment.
type Auth struct {
	BotOwnerID   int64          // owner is implicitly an admin; receives Private + Protected
	AdminUserIDs map[int64]bool // additional users allowed to run Protected commands
}

// Permits reports whether the sender of update may run a command of visibility v.
// Denies are silent — callers must NOT reply to denied requests, otherwise the
// existence of a Protected/Private command is leaked to unprivileged users.
func (a Auth) Permits(v Visibility, update *models.Update) bool {
	if v == VisibilityPublic || v == VisibilityUnlisted {
		return true
	}
	if update == nil {
		return false
	}
	var senderID int64
	if update.Message != nil && update.Message.From != nil {
		senderID = update.Message.From.ID
	} else if update.CallbackQuery != nil {
		senderID = update.CallbackQuery.From.ID
	} else if update.InlineQuery != nil {
		senderID = update.InlineQuery.From.ID
	}
	if senderID == 0 {
		return false
	}
	switch v {
	case VisibilityPrivate:
		return a.BotOwnerID != 0 && senderID == a.BotOwnerID
	case VisibilityProtected:
		if a.BotOwnerID != 0 && senderID == a.BotOwnerID {
			return true
		}
		return a.AdminUserIDs[senderID]
	}
	return false
}

// Install registers every command in the registry with the Telegram bot.
//
// Uses RegisterHandlerMatchFunc with a local matcher rather than the library's
// bot.MatchTypeCommand because the library compares the full bot_command
// entity bytes for equality. In groups, Telegram clients send /cmd@botname,
// so the entity bytes are "cmd@botname" — never equal to the registered
// command name "cmd". The matcher below strips the @suffix before comparing,
// and ignores a command whose suffix names another bot.
//
// auth gates Protected/Private commands; pass a zero-value Auth to deny all
// Protected/Private commands (the right answer for a misconfigured deploy).
func Install(b *bot.Bot, reg *Registry, auth Auth) {
	for name, cmd := range reg.AllCommands {
		cmdCopy := cmd // capture by value for the closure
		nameCopy := name
		b.RegisterHandlerMatchFunc(
			func(update *models.Update) bool {
				return matchCommand(nameCopy, reg.botUsername, update)
			},
			func(ctx context.Context, b *bot.Bot, update *models.Update) {
				defer recoverHandler("command", cmdCopy.Name, nil)
				if !auth.Permits(cmdCopy.Visibility, update) {
					return // silent — do not leak existence of gated commands
				}
				metrics.IncCommand(cmdCopy.Name)
				// context.Background is intentional: the hook must outlive the request
				// context so stats writes complete even after the handler returns.
				go func() { //nolint:gosec // G118: goroutine intentionally detached from request context
					// This goroutine is outside the handler's barrier above, so
					// it needs its own: a panicking hook on its own goroutine
					// still terminates the process.
					defer recoverHandler("command hook", cmdCopy.Name, nil)
					hookCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					reg.RunCommandHooks(hookCtx, cmdCopy.Name, update)
				}()
				err := cmdCopy.Handler(ctx, b, update)
				if err != nil {
					metrics.IncError("handler-error")
				}
				logCommand(cmdCopy.Name, update, err)
			},
		)
	}
	for prefix, callback := range reg.callbacks {
		callbackCopy := callback
		prefixCopy := prefix
		b.RegisterHandler(bot.HandlerTypeCallbackQueryData, prefixCopy, bot.MatchTypePrefix,
			func(ctx context.Context, b *bot.Bot, update *models.Update) {
				defer recoverHandler("callback", prefixCopy, func() {
					if update != nil && update.CallbackQuery != nil {
						_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: update.CallbackQuery.ID})
					}
				})
				if !auth.Permits(callbackCopy.Visibility, update) {
					if update != nil && update.CallbackQuery != nil {
						_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: update.CallbackQuery.ID})
					}
					return
				}
				err := callbackCopy.Handler(ctx, b, update)
				if err != nil {
					metrics.IncError("callback-handler-error")
					log.Error("callback", "prefix", prefixCopy, "err", err)
				}
			})
	}

	// Registered LAST, and that placement is load-bearing: the bot library
	// returns the first handler whose matcher accepts an update, so every
	// Command above out-ranks the fallback. A name defined in code therefore
	// always beats one resolved at runtime — including an alias that shadows a
	// command added in a later build.
	if fb := reg.Fallback(); fb != nil {
		fbCopy := *fb
		b.RegisterHandlerMatchFunc(
			func(update *models.Update) bool { return commandName(reg.botUsername, update) != "" },
			func(ctx context.Context, b *bot.Bot, update *models.Update) {
				name := commandName(reg.botUsername, update)
				defer recoverHandler("fallback", name, nil)
				if !auth.Permits(fbCopy.Visibility, update) {
					return // silent — same rule the command path follows
				}
				// Deliberately not counted in metrics.IncCommand or the command
				// hooks: an un-registered name is not a command, and feeding
				// arbitrary user text to the stats module would let anyone mint
				// unbounded metric labels.
				if err := fbCopy.Handler(ctx, b, name, update); err != nil {
					metrics.IncError("fallback-handler-error")
					log.Error("fallback", "name", name, "err", err)
				}
			},
		)
	}

	if inline := reg.Inline(); inline != nil {
		inlineCopy := *inline
		b.RegisterHandlerMatchFunc(
			func(update *models.Update) bool { return update != nil && update.InlineQuery != nil },
			func(ctx context.Context, b *bot.Bot, update *models.Update) {
				defer recoverHandler("inline", "inline_query", nil)
				if !auth.Permits(inlineCopy.Visibility, update) {
					return
				}
				if err := inlineCopy.Handler(ctx, b, update); err != nil {
					metrics.IncError("inline-handler-error")
					log.Error("inline", "err", err)
				}
			},
		)
	}
}

// commandName returns the normalised bot_command in update, or "" when the
// update carries none.
//
// Shares matchCommand's parsing rules (see commandToken) so a fallback sees
// exactly the name a registered command would have matched on.
func commandName(botUsername string, update *models.Update) string {
	if update == nil || update.Message == nil {
		return ""
	}
	text := update.Message.Text
	for _, e := range update.Message.Entities {
		if e.Type != models.MessageEntityTypeBotCommand {
			continue
		}
		end := e.Offset + e.Length
		if e.Offset < 0 || end > len(text) || e.Length < 1 {
			continue
		}
		if tok, ok := commandToken(text[e.Offset+1:end], botUsername); ok {
			return strings.ToLower(tok)
		}
	}
	return ""
}

// commandToken strips the @botname suffix from a bot_command entity body (the
// text after the leading '/'). ok is false when the suffix names another bot.
//
// Telegram delivers /cmd@otherbot to every bot that sees all group messages —
// privacy mode off, or admin rights — so the suffix must be checked against
// our own username. Usernames are case-insensitive. An empty botUsername
// (startup getMe failed) accepts any suffix: refusing every /cmd@botname would
// break the bot in groups, which is worse than answering a stray command.
func commandToken(tok, botUsername string) (string, bool) {
	i := strings.IndexByte(tok, '@')
	if i < 0 {
		return tok, true
	}
	if botUsername != "" && !strings.EqualFold(tok[i+1:], botUsername) {
		return "", false
	}
	return tok[:i], true
}

// recoverHandler contains a panic raised by a module handler. The bot runs with
// bot.WithNotAsyncHandlers() and a single worker, so the handler executes inline
// on the polling goroutine: without this barrier one panicking handler ends the
// process for every user. Mirrors the barrier the cron scheduler already puts
// around its handlers (internal/cron/scheduler.go).
//
// The panic is logged at ERROR with a full stack and counted under a distinct
// handler-panic metric, so a handler that panics on every call is loud rather
// than quietly failing per-request.
//
// onPanic, when non-nil, runs after logging — the callback path uses it to
// answer the query so the caller's client stops showing a spinner. It is itself
// guarded, because a panic raised inside the recovery path would have no
// remaining barrier.
func recoverHandler(kind, name string, onPanic func()) {
	rec := recover()
	if rec == nil {
		return
	}
	metrics.IncError("handler-panic")
	log.Error(kind+" panic", kind, name, "panic", rec, "stack", string(debug.Stack()))
	if onPanic == nil {
		return
	}
	defer func() { _ = recover() }()
	onPanic()
}

// logCommand emits one structured line per authorized command invocation: what
// was typed (input), who sent it (user id + @username), where (DM vs group, with
// chat id and — for groups — the title), and the outcome. The result is kept
// simple — "ok" via INFO, or the handler error via ERROR — because handler
// return values can be arbitrarily complex; the error plus context is the useful
// part. msg is non-nil here: matchCommand only matches updates with a Message.
func logCommand(name string, update *models.Update, err error) {
	msg := update.Message
	fields := []any{
		"command", name,
		"input", msg.Text,
		"chat_type", string(msg.Chat.Type),
		"chat_id", msg.Chat.ID,
	}
	if msg.Chat.Title != "" {
		fields = append(fields, "chat_title", msg.Chat.Title)
	}
	if from := msg.From; from != nil {
		fields = append(fields, "user_id", from.ID)
		if from.Username != "" {
			fields = append(fields, "username", from.Username)
		}
	}
	if err != nil {
		log.Error("command", append(fields, "err", err)...)
		return
	}
	log.Info("command", fields...)
}

// matchCommand reports whether update is a text message whose bot_command
// entity (after stripping any @botname suffix) equals name. Mirrors the
// library's HandlerTypeMessageText + MatchTypeCommand semantics but tolerates
// the group-form /cmd@botname that the library rejects.
//
// A /cmd@otherbot addressed to another bot never matches (see commandToken).
//
// Matching is case-insensitive so /PING and /Ping reach the same handler as
// /ping — mobile keyboards autocapitalize, and a typed command that silently
// does nothing reads as the bot being broken. Registered names are lowercase by
// validateCommand, so folding case can never make two commands collide, and the
// canonical Command.Name still drives stats, metrics, hooks, and logs.
func matchCommand(name, botUsername string, update *models.Update) bool {
	if update == nil || update.Message == nil {
		return false
	}
	text := update.Message.Text
	for _, e := range update.Message.Entities {
		if e.Type != models.MessageEntityTypeBotCommand {
			continue
		}
		// Bounds check: defensive against malformed entities from a future
		// API revision. The library's match func omits it, and a matcher runs
		// outside Install's panic barrier, so a bad entity would panic the
		// polling goroutine with nothing to catch it.
		end := e.Offset + e.Length
		if e.Offset < 0 || end > len(text) || e.Length < 1 {
			continue
		}
		tok, ok := commandToken(text[e.Offset+1:end], botUsername) // drop leading '/'
		if ok && strings.EqualFold(tok, name) {
			return true
		}
	}
	return false
}
