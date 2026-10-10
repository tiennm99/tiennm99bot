package guessgame

import (
	"context"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/keylock"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/subscription"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// TelegramAPI is the subset of *bot.Bot the summary and the daily push use.
type TelegramAPI interface {
	SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error)
	EditMessageText(ctx context.Context, params *bot.EditMessageTextParams) (*models.Message, error)
	SendGame(ctx context.Context, params *bot.SendGameParams) (*models.Message, error)
}

// ScoreReporter records a won game's score on the card it was played from.
type ScoreReporter interface {
	Report(ctx context.Context, a htmlgame.Address, userID int64, score int) error
}

// BotReporter reports scores through the bot with setGameScore.
type BotReporter struct{ B *bot.Bot }

// Report implements ScoreReporter.
func (r BotReporter) Report(ctx context.Context, a htmlgame.Address, userID int64, score int) error {
	return htmlgame.ReportScore(ctx, r.B, a, userID, score)
}

// DailyConfig is one game's daily mode.
type DailyConfig struct {
	Rules     Rules
	Answers   []string // the answer pool; each cycle walks a keyed permutation of it
	AnswerKey []byte   // keys the answer order
	Store     storage.Collection
	// StatsPrefix is the key prefix of players' daily stats; "stats:" when
	// empty. A game whose collection already uses "stats:" sets another.
	StatsPrefix string
	Epoch       time.Time // puzzle #1 starts here; zero means Epoch
	Now         func() time.Time
	Schedule    func(d time.Duration, f func()) // runs the summary flush; time.AfterFunc by default
	Async       func(f func())                  // runs a score report; a goroutine by default
	Reporter    ScoreReporter                   // nil skips score reports
	API         TelegramAPI                     // nil skips the live group summary
	// GameShortName is the BotFather game the 07:00 push sends.
	GameShortName string
	// LogName prefixes log lines, e.g. "wordledaily".
	LogName string
	// AnswerPrefix starts the recap's answer line; "Answer: " when empty.
	AnswerPrefix string
}

// Daily is a game's daily mode: everyone plays the same answer, a new one
// every day at 07:00 ICT. Its stores are exported for the game's own tests
// and migrations.
type Daily struct {
	cfg   DailyConfig
	epoch time.Time

	Puzzles     storage.DocStore[PuzzleDoc]
	Plays       storage.DocStore[Progress]
	Stats       storage.DocStore[UserStats]
	ChatDays    storage.DocStore[ChatDay]
	Streaks     storage.DocStore[ChatStreak]
	Subscribers subscription.Store
	PushDate    subscription.DayStore

	SubscribersMu sync.Mutex
	// Locks serialises one player's daily requests and one chat topic's
	// summary updates.
	Locks keylock.Map

	perms permCache
	cache puzzleCache
	flush flusher
}

// NewDaily builds a game's daily mode. Without a store it only answers the
// questions that need none; the game reports itself disabled.
func NewDaily(cfg DailyConfig) *Daily {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Schedule == nil {
		cfg.Schedule = func(d time.Duration, f func()) { time.AfterFunc(d, f) }
	}
	if cfg.StatsPrefix == "" {
		cfg.StatsPrefix = "stats:"
	}
	if cfg.AnswerPrefix == "" {
		cfg.AnswerPrefix = "Answer: "
	}
	d := &Daily{cfg: cfg, epoch: Epoch}
	if !cfg.Epoch.IsZero() {
		d.epoch = cfg.Epoch
	}
	if cfg.Store != nil {
		d.Puzzles = storage.Typed[PuzzleDoc](cfg.Store)
		d.Plays = storage.Typed[Progress](cfg.Store)
		d.Stats = storage.Typed[UserStats](cfg.Store)
		d.ChatDays = storage.Typed[ChatDay](cfg.Store)
		d.Streaks = storage.Typed[ChatStreak](cfg.Store)
		d.Subscribers = storage.Typed[subscription.Doc](cfg.Store)
		d.PushDate = storage.Typed[subscription.DayDoc](cfg.Store)
	}
	return d
}

// Rules returns the game's rules.
func (d *Daily) Rules() Rules { return d.cfg.Rules }

// MaxGuesses is the daily guess budget.
func (d *Daily) MaxGuesses() int { return d.cfg.Rules.MaxGuesses() }

// Score is what a daily win reports with setGameScore: fewer guesses rank
// higher.
func (d *Daily) Score(guesses int) int { return d.MaxGuesses() + 1 - guesses }

func (d *Daily) async(f func()) {
	if d.cfg.Async != nil {
		d.cfg.Async(f)
		return
	}
	go f()
}
