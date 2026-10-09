// Package wordledaily is the Wordle Daily Telegram HTML5 game: one shared
// English five-letter puzzle a day, like NYT Wordle. /wordledaily sends the
// BotFather game; Play opens a board page this bot serves. A new puzzle
// starts every day at 07:00 ICT (00:00 UTC).
//
// The server owns the game. The page only sends guesses; the answer, the
// colours, each player's progress and stats live server-side, and the answer
// reaches the page only once that player's game is over. In a group the bot
// keeps one live results message per chat and day with spoiler-free colour
// grids, and subscribed chats get the day's card plus yesterday's group
// results at 07:00 ICT. See docs/wordledaily.md.
package wordledaily

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/keylock"
	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/subscription"
	"github.com/tiennm99/tiennm99bot/internal/modules/wordle/wordlist"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	// ShortName is the module's catalog key and command name, so its routes
	// live under /games/wordledaily/.
	ShortName = "wordledaily"
	// GameShortName is the BotFather game the cards send and Play answers.
	GameShortName = "wordle"

	subscribeCommand   = ShortName + "_subscribe"
	unsubscribeCommand = ShortName + "_unsubscribe"

	secretEnv = "WORDLEDAILY_GAME_SECRET" //nolint:gosec // G101: an env var name, not a credential

	// minSecretBytes is the shortest WORDLEDAILY_GAME_SECRET accepted.
	minSecretBytes = 32
	// tokenKeyLabel and answerKeyLabel derive the two keys from the root
	// secret. Their own labels keep a noitu token from verifying here and
	// keep the answer order independent of the link-signing key.
	tokenKeyLabel  = "tiennm99bot/wordledaily/token/v1"  //nolint:gosec // G101: a public derivation label
	answerKeyLabel = "tiennm99bot/wordledaily/answer/v1" //nolint:gosec // G101: a public derivation label

	routePrefix = "/games/" + ShortName + "/"
)

// telegramAPI is the subset of *bot.Bot the summary and the daily push use.
type telegramAPI interface {
	SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error)
	EditMessageText(ctx context.Context, params *bot.EditMessageTextParams) (*models.Message, error)
	SendGame(ctx context.Context, params *bot.SendGameParams) (*models.Message, error)
}

// scoreReporter records a won game's score on the card it was played from.
type scoreReporter interface {
	Report(ctx context.Context, a htmlgame.Address, userID int64, score int) error
}

type botReporter struct{ b *bot.Bot }

func (r botReporter) Report(ctx context.Context, a htmlgame.Address, userID int64, score int) error {
	return htmlgame.ReportScore(ctx, r.b, a, userID, score)
}

// config is everything New reads from the environment, injectable in tests.
type config struct {
	baseURL  string // public https base without trailing slash; "" disables
	rootKey  []byte // derives the token and answer keys; empty disables
	now      func() time.Time
	store    storage.Collection              // nil disables
	reporter scoreReporter                   // nil disables
	api      telegramAPI                     // nil skips the live group summary
	schedule func(d time.Duration, f func()) // runs the summary flush; time.AfterFunc by default
	async    func(f func())                  // runs a score report; a goroutine by default
	answers  []string
	dict     map[string]struct{}
	epoch    time.Time // puzzle #1 starts here; zero means the package epoch
}

// New builds the module from the environment. The game is enabled only when
// GAME_BASE_URL is a valid https URL, a key is available and storage exists.
func New(deps modules.Deps) modules.Module {
	_, dict := wordlist.Load()
	cfg := config{
		baseURL:  htmlgame.ParseBaseURL(os.Getenv(htmlgame.BaseURLEnv), ShortName),
		rootKey:  rootKey(os.Getenv(secretEnv), deps),
		now:      time.Now,
		store:    deps.Store,
		schedule: func(d time.Duration, f func()) { time.AfterFunc(d, f) },
		answers:  wordlist.Answers(),
		dict:     dict,
	}
	if deps.Bot != nil {
		cfg.reporter = botReporter{b: deps.Bot}
		cfg.api = deps.Bot
	}
	return newService(cfg).module()
}

// rootKey returns WORDLEDAILY_GAME_SECRET when set, otherwise the bot token.
// A secret that is set but too short disables the game rather than silently
// falling back.
func rootKey(secret string, deps modules.Deps) []byte {
	if secret != "" {
		if len(secret) < minSecretBytes {
			log.Warn("WORDLEDAILY_GAME_SECRET is shorter than 32 bytes; wordledaily game disabled")
			return nil
		}
		return []byte(secret)
	}
	if deps.Bot == nil || deps.Bot.Token() == "" {
		return nil
	}
	return []byte(deps.Bot.Token())
}

// service is the module's runtime state.
type service struct {
	cfg       config
	tokenKey  []byte
	answerKey []byte
	epoch     time.Time

	puzzles     storage.DocStore[puzzleDoc]
	plays       storage.DocStore[progress]
	stats       storage.DocStore[userStats]
	chatDays    storage.DocStore[chatDay]
	streaks     storage.DocStore[chatStreak]
	subscribers subscription.Store
	pushDate    subscription.DayStore

	subscribersMu sync.Mutex
	locks         keylock.Map
	limiter       *htmlgame.Limiter
	perms         permCache
	cache         puzzleCache
	flush         flusher
}

func newService(cfg config) *service {
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.schedule == nil {
		cfg.schedule = func(d time.Duration, f func()) { time.AfterFunc(d, f) }
	}
	svc := &service{
		cfg:       cfg,
		tokenKey:  htmlgame.DeriveKey(cfg.rootKey, tokenKeyLabel),
		answerKey: htmlgame.DeriveKey(cfg.rootKey, answerKeyLabel),
		epoch:     epoch,
		limiter:   &htmlgame.Limiter{Per: requestsPerMinute, Window: time.Minute},
	}
	if !cfg.epoch.IsZero() {
		svc.epoch = cfg.epoch
	}
	if cfg.store != nil {
		svc.puzzles = storage.Typed[puzzleDoc](cfg.store)
		svc.plays = storage.Typed[progress](cfg.store)
		svc.stats = storage.Typed[userStats](cfg.store)
		svc.chatDays = storage.Typed[chatDay](cfg.store)
		svc.streaks = storage.Typed[chatStreak](cfg.store)
		svc.subscribers = storage.Typed[subscription.Doc](cfg.store)
		svc.pushDate = storage.Typed[subscription.DayDoc](cfg.store)
	}
	if svc.enabled() {
		log.Info("wordledaily game enabled", "answers", len(cfg.answers), "words", len(cfg.dict))
	}
	return svc
}

// enabled reports whether Play can open the game.
func (s *service) enabled() bool {
	return s.cfg.baseURL != "" && len(s.tokenKey) > 0 && len(s.answerKey) > 0 &&
		s.cfg.store != nil && s.cfg.reporter != nil && len(s.cfg.answers) > 0 && len(s.cfg.dict) > 0
}

// module describes the service to the registry. Routes and the daily push
// exist only when the game is enabled, so a disabled game exposes nothing
// over HTTP and pushes nothing.
func (s *service) module() modules.Module {
	mod := modules.Module{
		Commands: []modules.Command{{
			Name:        ShortName,
			Visibility:  modules.VisibilityPublic,
			Description: "Play today's Wordle Daily puzzle",
			Handler:     s.handleCommand,
		}, {
			Name:        subscribeCommand,
			Visibility:  modules.VisibilityPublic,
			Description: "Get the Wordle Daily puzzle and group results at 07:00 ICT",
			Handler:     s.handleSubscribe,
		}, {
			Name:        unsubscribeCommand,
			Visibility:  modules.VisibilityPublic,
			Description: "Stop the daily Wordle Daily push",
			Handler:     s.handleUnsubscribe,
		}},
		Games: []modules.Game{{
			ShortName:  GameShortName,
			Visibility: modules.VisibilityPublic,
			Handler:    s.handlePlay,
		}},
	}
	if s.enabled() {
		mod.HTTP = []modules.Route{{Pattern: routePrefix, Handler: s.handler()}}
		mod.Crons = []modules.Cron{{Name: dailyPushCronName, Schedule: dailyPushSchedule, Handler: s.dailyPushHandler}}
	}
	return mod
}
