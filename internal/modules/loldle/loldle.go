// Package loldle is LoLdle, the League of Legends champion guessing game, in
// two modes behind the one BotFather game "loldle":
//
//   - unlimited: /loldle sends a card that opens the player's own round
//     with a random champion; the page's New game button starts another.
//     /loldle <champion>, /loldle_giveup and /loldle_stats play and read
//     the same round from the chat, with stickers and flavour lines, and
//     /loldle_setmax sets a chat's round length.
//   - LoLdle Daily: one shared champion a day from 07:00 ICT, like Wordle
//     Daily: /loldledaily, the live spoiler-free group results, the 07:00
//     recap and push, and setGameScore on a win.
//
// Each guess is scored on seven attributes (gender, species, range type,
// resource, regions, positions, release year). Play picks the mode from the
// card: /loldle cards are recorded, and any other card plays daily. The
// server owns the game; the answer reaches the page only once that game is
// over. See docs/loldle.md.
package loldle

import (
	"cmp"
	"encoding/json"
	"math/rand/v2"
	"os"
	"slices"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	// ShortName is the module's catalog key and the unlimited command name,
	// so its routes live under /games/loldle/.
	ShortName = "loldle"
	// GameShortName is the BotFather game the cards send and Play answers.
	GameShortName = "loldle"

	dailyCommand       = "loldledaily"
	subscribeCommand   = dailyCommand + "_subscribe"
	unsubscribeCommand = dailyCommand + "_unsubscribe"

	secretEnv = "LOLDLE_GAME_SECRET" //nolint:gosec // G101: an env var name, not a credential

	// minSecretBytes is the shortest LOLDLE_GAME_SECRET accepted.
	minSecretBytes = 32
	// tokenKeyLabel and answerKeyLabel derive the two keys from the root
	// secret, apart from every other game's.
	tokenKeyLabel  = "tiennm99bot/loldle/token/v1"  //nolint:gosec // G101: a public derivation label
	answerKeyLabel = "tiennm99bot/loldle/answer/v1" //nolint:gosec // G101: a public derivation label

	routePrefix = "/games/" + ShortName + "/"

	// dailyStatsPrefix keeps daily stats apart from the in-chat game's
	// legacy "stats:<subject>" documents in the same collection.
	dailyStatsPrefix = "dstats:"

	// dailyPushCronName and cardsCronName must be unique across all
	// modules' crons.
	dailyPushCronName = "loldledaily_daily_push"
	cardsCronName     = "loldle_unlimited_cards"
	// cardsSchedule runs the unlimited card cleanup at 03:40 ICT.
	cardsSchedule = "40 20 * * *"

	// ddragonOrigin serves the champion icons the page shows.
	ddragonOrigin = "https://ddragon.leagueoflegends.com"
)

// config is everything New reads from the environment, injectable in tests.
type config struct {
	baseURL   string // public https base without trailing slash; "" disables
	rootKey   []byte // derives the token and answer keys; empty disables
	now       func() time.Time
	store     storage.Collection              // nil disables
	reporter  guessgame.ScoreReporter         // nil disables
	api       guessgame.TelegramAPI           // nil skips the live group summary
	schedule  func(d time.Duration, f func()) // runs the summary flush; time.AfterFunc by default
	async     func(f func())                  // runs a score report; a goroutine by default
	champions []Champion
	epoch     time.Time                // daily puzzle #1 starts here; zero means the shared epoch
	pick      func(prev string) string // an unlimited round's champion; random by default
}

// New builds the module from the environment. The web game is enabled only
// when GAME_BASE_URL is a valid https URL, a key is available and storage
// exists; the in-chat /loldle commands need storage only.
func New(deps modules.Deps) modules.Module {
	cfg := config{
		baseURL:   htmlgame.ParseBaseURL(os.Getenv(htmlgame.BaseURLEnv), ShortName),
		rootKey:   rootKey(os.Getenv(secretEnv), deps),
		now:       time.Now,
		store:     deps.Store,
		schedule:  func(d time.Duration, f func()) { time.AfterFunc(d, f) },
		champions: loadChampions(),
	}
	if deps.Bot != nil {
		cfg.reporter = guessgame.BotReporter{B: deps.Bot}
		cfg.api = deps.Bot
	}
	return newService(cfg).module()
}

// rootKey returns LOLDLE_GAME_SECRET when set, otherwise the bot token. A
// secret that is set but too short disables the game rather than silently
// falling back.
func rootKey(secret string, deps modules.Deps) []byte {
	if secret != "" {
		if len(secret) < minSecretBytes {
			log.Warn("LOLDLE_GAME_SECRET is shorter than 32 bytes; loldle game disabled")
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
	settings  ConfigStore       // nil without storage
	limiter   *htmlgame.Limiter
	// championList is the public champion list the page searches: names
	// and icon ids only, never an attribute.
	championList []byte
}

func newService(cfg config) *service {
	if cfg.now == nil {
		cfg.now = time.Now
	}
	names := make([]string, len(cfg.champions))
	for i, c := range cfg.champions {
		names[i] = c.ChampionName
	}
	slices.Sort(names)
	if cfg.pick == nil {
		cfg.pick = randomPick(names)
	}
	svc := &service{
		cfg:          cfg,
		tokenKey:     htmlgame.DeriveKey(cfg.rootKey, tokenKeyLabel),
		answerKey:    htmlgame.DeriveKey(cfg.rootKey, answerKeyLabel),
		rules:        rules{champions: cfg.champions},
		limiter:      &htmlgame.Limiter{Per: requestsPerMinute, Window: time.Minute},
		championList: championList(cfg.champions),
	}
	// The daily answers are the champions sorted by name, so the order
	// depends on the key alone; adding a champion changes only days that
	// have not been pinned yet.
	svc.Daily = guessgame.NewDaily(guessgame.DailyConfig{
		Rules:         rules{champions: cfg.champions, keepGone: true},
		Answers:       names,
		AnswerKey:     svc.answerKey,
		Store:         cfg.store,
		StatsPrefix:   dailyStatsPrefix,
		Epoch:         cfg.epoch,
		Now:           cfg.now,
		Schedule:      cfg.schedule,
		Async:         cfg.async,
		Reporter:      cfg.reporter,
		API:           cfg.api,
		GameShortName: GameShortName,
		LogName:       ShortName,
		AnswerPrefix:  "Champion: ",
	})
	if len(names) > 0 {
		svc.rounds = guessgame.NewRounds(guessgame.RoundsConfig{
			Store:   cfg.store,
			Rules:   svc.rules,
			Pick:    cfg.pick,
			DistLen: MaxGuessesCap,
			Now:     cfg.now,
		})
	}
	svc.cards = guessgame.NewCards(cfg.store, cfg.now, ShortName)
	if cfg.store != nil {
		svc.settings = storage.Typed[roundConfig](cfg.store)
	}
	if svc.enabled() {
		log.Info("loldle game enabled", "champions", len(names))
	}
	return svc
}

// randomPick draws an unlimited round's champion, never the champion of the
// round it replaces.
func randomPick(names []string) func(prev string) string {
	return func(prev string) string {
		for {
			n := names[rand.IntN(len(names))]
			if n != prev || len(names) < 2 {
				return n
			}
		}
	}
}

// championEntry is one champion in the page's search list.
type championEntry struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

func championList(champions []Champion) []byte {
	list := make([]championEntry, len(champions))
	for i, c := range champions {
		list[i] = championEntry{Name: c.ChampionName, ID: c.ID}
	}
	slices.SortFunc(list, func(a, b championEntry) int { return cmp.Compare(a.Name, b.Name) })
	raw, err := json.Marshal(list)
	if err != nil {
		panic("loldle: encode champion list: " + err.Error())
	}
	return raw
}

// enabled reports whether Play can open the game.
func (s *service) enabled() bool {
	return s.cfg.baseURL != "" && len(s.tokenKey) > 0 && len(s.answerKey) > 0 &&
		s.cfg.store != nil && s.cfg.reporter != nil && len(s.cfg.champions) > 0
}

// module describes the service to the registry. Routes and crons exist only
// when the game is enabled, so a disabled game exposes nothing over HTTP and
// pushes nothing.
func (s *service) module() modules.Module {
	daily := guessgame.DailyCommands{
		Daily:   s.Daily,
		Enabled: s.enabled,
		Texts:   guessgame.DailyTexts{Channel: msgDailyChannel, Disabled: msgDisabled, SendFail: msgDailySendFail},
	}
	mod := modules.Module{
		Commands: []modules.Command{{
			Name:        ShortName,
			Visibility:  modules.VisibilityPublic,
			Description: "Play unlimited LoLdle; with a champion, guess in your round",
			Parameters:  "[champion]",
			Handler:     s.handleLoldle,
		}, {
			Name:        "loldle_giveup",
			Visibility:  modules.VisibilityPublic,
			Description: "Reveal the answer of your unlimited LoLdle round",
			Handler:     s.handleGiveup,
		}, {
			Name:        "loldle_stats",
			Visibility:  modules.VisibilityPublic,
			Description: "Show your unlimited LoLdle stats",
			Handler:     s.handleStats,
		}, {
			Name:        "loldle_setmax",
			Visibility:  modules.VisibilityPrivate,
			Description: "Set max guesses (1-10) for unlimited rounds started in this chat",
			Handler:     s.handleSetMax,
		}, {
			Name:        dailyCommand,
			Visibility:  modules.VisibilityPublic,
			Description: "Play today's LoLdle Daily champion",
			Handler:     daily.HandleCard,
		}, {
			Name:        subscribeCommand,
			Visibility:  modules.VisibilityPublic,
			Description: "Get the LoLdle Daily puzzle and group results at 07:00 ICT",
			Handler:     daily.HandleSubscribe,
		}, {
			Name:        unsubscribeCommand,
			Visibility:  modules.VisibilityPublic,
			Description: "Stop the daily LoLdle Daily push",
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
