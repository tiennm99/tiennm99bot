package modules

import (
	"context"
	"net/http"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// Visibility classifies who may invoke a command. The dispatcher enforces
// this at command-handler entry: Public is unrestricted; Protected requires
// the sender to be in Auth.AdminUserIDs (or be the bot owner); Private
// requires the sender to be Auth.BotOwnerID; Unlisted is unrestricted like
// Public but never advertised. /help and the Telegram command menu list
// Public commands only.
type Visibility int

const (
	VisibilityPublic Visibility = iota
	VisibilityProtected
	VisibilityPrivate
	VisibilityUnlisted
)

// CommandHandler runs in response to a Telegram command. Returning an error
// causes the dispatcher to log the failure and count it in metrics. Updates
// arrive by long polling and are consumed either way — Telegram never
// redelivers on a handler error — so the error return is purely for
// logging/metrics, not flow control.
type CommandHandler func(ctx context.Context, b *bot.Bot, update *models.Update) error

// CallbackHandler runs in response to inline-keyboard callback data. Callback
// payloads are not commands and therefore do not participate in command stats.
type CallbackHandler func(ctx context.Context, b *bot.Bot, update *models.Update) error

// Callback registers an inline-keyboard callback-data prefix owned by a module.
// Prefixes must be globally non-overlapping so one callback reaches one owner.
type Callback struct {
	Prefix     string
	Visibility Visibility
	Handler    CallbackHandler
}

// Game registers the handler for a BotFather HTML5 game's Play button.
//
// Telegram delivers a Play press as a callback query that carries
// game_short_name instead of callback data, so a Data-prefix Callback can never
// see it. The handler must answer the query — with the game URL, or with an
// alert explaining why the game cannot open — or the client keeps spinning.
type Game struct {
	ShortName  string // BotFather short name: ^[A-Za-z0-9_]{3,64}$, unique across modules
	Visibility Visibility
	Handler    CallbackHandler
}

// Route is an HTTP handler a module serves on the bot's HTTP server.
//
// Pattern is a net/http ServeMux path pattern without a method or host, and
// must live under /games/<module>/ so a module can never shadow the health
// route or another module's paths. A module that has nothing to serve (for
// example because its public URL is not configured) returns no routes.
type Route struct {
	Pattern string
	Handler http.Handler
}

// CronHandler runs when a cron fires, driven by the in-process scheduler
// (internal/cron). The handler receives the owning module's Deps — the same
// bundle its Factory got, including its own storage Collection.
type CronHandler func(ctx context.Context, deps Deps) error

// Command is a single Telegram bot command exposed by a module.
type Command struct {
	Name        string         // ^[a-z0-9_]{1,32}$ — Telegram BotFather rules
	Visibility  Visibility     // public/protected/private
	Description string         // concise summary shown in command discovery (required, non-empty)
	Parameters  string         // optional syntax after the command, e.g. "<quantity> <ticker>"
	Handler     CommandHandler // required
}

// Cron is a single scheduled job exposed by a module.
type Cron struct {
	Schedule string      // 5-field cron expr (UTC); the in-process scheduler fires the handler on it
	Name     string      // unique within module
	Handler  CronHandler // required
}

// Module is a self-contained feature unit: a name plus zero or more commands
// and crons. Modules are constructed by Factory functions that capture their
// per-module Deps via closure.
//
// Module.Name is overridden by the registry to its catalog key; factories may
// leave it blank.
type Module struct {
	Name        string
	Commands    []Command
	Callbacks   []Callback
	Crons       []Cron
	Games       []Game                                                        // optional; BotFather games whose Play button this module answers
	HTTP        []Route                                                       // optional; routes served on the bot's HTTP server
	CommandHook func(ctx context.Context, name string, update *models.Update) // optional; called by dispatcher after each authorized command invocation. update carries the originating Telegram update so hooks can attribute usage to a user.
	Fallback    *CommandFallback                                              // optional; handles a /command no module registered. At most one across all modules.
	Inline      *InlineQuery                                                  // optional; handles inline-mode queries. At most one across all modules.
}

// CommandFallback handles a /command that no module registered.
//
// Install registers it after every Command, and the bot library returns the
// *first* matching handler, so a registered command can never reach here. That
// ordering is the whole mechanism: code always wins over anything resolved at
// runtime, including an alias that shares a command's name.
//
// Name is the parsed command, lowercased with any @botname suffix stripped —
// the same normalisation matchCommand applies — so a fallback never re-parses
// the entity itself.
type CommandFallback struct {
	Visibility Visibility
	Handler    func(ctx context.Context, b *bot.Bot, name string, update *models.Update) error
}

// InlineQuery handles inline-mode queries ("@botname <text>" typed in any chat).
//
// The bot library has no HandlerType for inline queries, so Install matches
// update.InlineQuery itself. Inline mode must also be enabled for the bot in
// BotFather; without that Telegram never delivers these updates and the handler
// is simply never called.
type InlineQuery struct {
	Visibility Visibility
	Handler    func(ctx context.Context, b *bot.Bot, update *models.Update) error
}

// Deps is the dependency bundle a Factory receives.
//
// Deps.Registry is a pointer to the Registry being built. At factory call
// time the Registry is partially populated (only modules earlier in the
// MODULES env order); by the time any handler runs, it is fully populated.
// Modules that need to introspect commands (e.g. /help) capture this pointer
// in their handler closures.
type Deps struct {
	Store    storage.Collection // the module's own collection; build typed views with storage.Typed[T]
	Registry *Registry          // populated by Build; safe to capture but read-only at module use
	Bot      *bot.Bot           // nil-safe: only crons that fan-out (lol daily push) need it
	// BotUsername is the bot's Telegram username without "@", from BOT_USERNAME
	// or getMe at startup. Empty when neither was available; consumers must
	// cope (sticker resolves it lazily, lol omits it from its User-Agent).
	BotUsername string
}

// Factory constructs a Module from its Deps. Deps are passed directly (instead
// of a separate Init step) so handler closures can capture them — idiomatic Go
// and removes a lifecycle ordering trap.
type Factory func(deps Deps) Module
