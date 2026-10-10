// Package wordledaily is the Wordle Telegram HTML5 game in both of its
// modes, behind the one BotFather game "wordle":
//
//   - Wordle Daily: one shared English five-letter puzzle a day, like NYT
//     Wordle. /wordledaily sends the card; a new puzzle starts every day at
//     07:00 ICT (00:00 UTC). In a group the bot keeps one live results
//     message per chat and day with spoiler-free colour grids, and
//     subscribed chats get the day's card plus yesterday's group results
//     at 07:00 ICT.
//   - Unlimited: /wordle sends a card that opens the player's own round
//     with a random word; the page's New game button starts another.
//     /wordle <word>, /wordle_new, /wordle_giveup and /wordle_stats play
//     and read the same round from the chat.
//
// Play picks the mode from the card: /wordle cards are recorded, and any
// other card plays daily. The server owns the game. The page only sends
// guesses; the answer, the colours, each player's progress and stats live
// server-side, and the answer reaches the page only once that game is over.
// See docs/wordledaily.md and docs/wordle.md.
package wordledaily

import (
	"math/rand/v2"
	"os"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
	"github.com/tiennm99/tiennm99bot/internal/modules/wordle/wordlist"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	// ShortName is the module's catalog key and the daily command name, so
	// its routes live under /games/wordledaily/.
	ShortName = "wordledaily"
	// LegacyShortName is the catalog key of the retired in-chat wordle
	// module, whose commands and data this module took over.
	LegacyShortName = "wordle"
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

	// maxGuesses is the guess budget of both modes.
	maxGuesses = 6

	// dailyPushCronName and cardsCronName must be unique across all
	// modules' crons.
	dailyPushCronName = "wordledaily_daily_push"
	cardsCronName     = "wordle_unlimited_cards"
	// cardsSchedule runs the unlimited card cleanup at 03:35 ICT.
	cardsSchedule = "35 20 * * *"
)

// config is everything New reads from the environment, injectable in tests.
type config struct {
	baseURL  string // public https base without trailing slash; "" disables
	rootKey  []byte // derives the token and answer keys; empty disables
	now      func() time.Time
	store    storage.Collection              // nil disables
	reporter guessgame.ScoreReporter         // nil disables
	api      guessgame.TelegramAPI           // nil skips the live group summary
	schedule func(d time.Duration, f func()) // runs the summary flush; time.AfterFunc by default
	async    func(f func())                  // runs a score report; a goroutine by default
	answers  []string
	dict     map[string]struct{}
	epoch    time.Time                // puzzle #1 starts here; zero means the shared epoch
	pick     func(prev string) string // an unlimited round's word; random from answers by default
}

// New builds the module from the environment. The web game is enabled only
// when GAME_BASE_URL is a valid https URL, a key is available and storage
// exists; the in-chat /wordle commands need storage only.
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
		cfg.reporter = guessgame.BotReporter{B: deps.Bot}
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

// service is the module's runtime state. The daily mode is embedded, so
// its stores and methods read as the service's own.
type service struct {
	*guessgame.Daily
	cfg       config
	tokenKey  []byte
	answerKey []byte
	rules     rules
	rounds    *guessgame.Rounds // nil without storage
	cards     *guessgame.Cards  // nil without storage
	limiter   *htmlgame.Limiter
}

func newService(cfg config) *service {
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.pick == nil {
		cfg.pick = randomPick(cfg.answers)
	}
	svc := &service{
		cfg:       cfg,
		tokenKey:  htmlgame.DeriveKey(cfg.rootKey, tokenKeyLabel),
		answerKey: htmlgame.DeriveKey(cfg.rootKey, answerKeyLabel),
		rules:     rules{dict: cfg.dict},
		limiter:   &htmlgame.Limiter{Per: requestsPerMinute, Window: time.Minute},
	}
	svc.Daily = guessgame.NewDaily(guessgame.DailyConfig{
		Rules:         svc.rules,
		Answers:       cfg.answers,
		AnswerKey:     svc.answerKey,
		Store:         cfg.store,
		Epoch:         cfg.epoch,
		Now:           cfg.now,
		Schedule:      cfg.schedule,
		Async:         cfg.async,
		Reporter:      cfg.reporter,
		API:           cfg.api,
		GameShortName: GameShortName,
		LogName:       ShortName,
	})
	if len(cfg.answers) > 0 {
		svc.rounds = guessgame.NewRounds(guessgame.RoundsConfig{
			Store:   cfg.store,
			Rules:   svc.rules,
			Pick:    cfg.pick,
			DistLen: maxGuesses,
			Now:     cfg.now,
		})
	}
	svc.cards = guessgame.NewCards(cfg.store, cfg.now, ShortName)
	if svc.enabled() {
		log.Info("wordledaily game enabled", "answers", len(cfg.answers), "words", len(cfg.dict))
	}
	return svc
}

// randomPick draws an unlimited round's word from answers, never the word
// of the round it replaces.
func randomPick(answers []string) func(prev string) string {
	return func(prev string) string {
		for {
			w := answers[rand.IntN(len(answers))]
			if w != prev || len(answers) < 2 {
				return w
			}
		}
	}
}

// enabled reports whether Play can open the game.
func (s *service) enabled() bool {
	return s.cfg.baseURL != "" && len(s.tokenKey) > 0 && len(s.answerKey) > 0 &&
		s.cfg.store != nil && s.cfg.reporter != nil && len(s.cfg.answers) > 0 && len(s.cfg.dict) > 0
}

// module describes the service to the registry. Routes and crons exist only
// when the game is enabled, so a disabled game exposes nothing over HTTP and
// pushes nothing.
func (s *service) module() modules.Module {
	daily := guessgame.DailyCommands{
		Daily:   s.Daily,
		Enabled: s.enabled,
		Texts:   guessgame.DailyTexts{Channel: msgChannel, Disabled: msgDisabled, SendFail: msgSendGameFail},
	}
	mod := modules.Module{
		Commands: []modules.Command{{
			Name:        LegacyShortName,
			Visibility:  modules.VisibilityPublic,
			Description: "Play unlimited Wordle; with a word, guess in your round",
			Parameters:  "[word]",
			Handler:     s.handleWordle,
		}, {
			Name:        "wordle_new",
			Visibility:  modules.VisibilityPublic,
			Description: "Start a new unlimited Wordle round (gives up the current one)",
			Handler:     s.handleNew,
		}, {
			Name:        "wordle_giveup",
			Visibility:  modules.VisibilityPublic,
			Description: "Reveal the answer of your unlimited Wordle round",
			Handler:     s.handleGiveup,
		}, {
			Name:        "wordle_stats",
			Visibility:  modules.VisibilityPublic,
			Description: "Show your unlimited Wordle stats",
			Handler:     s.handleStats,
		}, {
			Name:        ShortName,
			Visibility:  modules.VisibilityPublic,
			Description: "Play today's Wordle Daily puzzle",
			Handler:     daily.HandleCard,
		}, {
			Name:        subscribeCommand,
			Visibility:  modules.VisibilityPublic,
			Description: "Get the Wordle Daily puzzle and group results at 07:00 ICT",
			Handler:     daily.HandleSubscribe,
		}, {
			Name:        unsubscribeCommand,
			Visibility:  modules.VisibilityPublic,
			Description: "Stop the daily Wordle Daily push",
			Handler:     daily.HandleUnsubscribe,
		}},
		Games: []modules.Game{{
			ShortName:  GameShortName,
			Visibility: modules.VisibilityPublic,
			Handler:    s.handlePlay,
		}},
	}
	if s.enabled() {
		mod.HTTP = []modules.Route{{Pattern: routePrefix, Handler: s.handler()}}
		mod.Crons = []modules.Cron{
			{Name: dailyPushCronName, Schedule: guessgame.DailyPushSchedule, Handler: s.PushHandler},
			{Name: cardsCronName, Schedule: cardsSchedule, Handler: s.cards.Cleanup},
		}
	}
	return mod
}
