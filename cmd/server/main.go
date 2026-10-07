// Command server runs tiennm99bot: it loads configuration from the environment,
// opens the storage backend, builds the module registry, and then serves
// Telegram updates by long polling while an in-process scheduler fires module
// crons. A small HTTP server answers the container health check.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-telegram/bot"

	"github.com/tiennm99/tiennm99bot/internal/cron"
	"github.com/tiennm99/tiennm99bot/internal/deploynotify"
	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/metrics"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/alias"
	"github.com/tiennm99/tiennm99bot/internal/modules/amlich"
	"github.com/tiennm99/tiennm99bot/internal/modules/blacklist"
	"github.com/tiennm99/tiennm99bot/internal/modules/coin"
	"github.com/tiennm99/tiennm99bot/internal/modules/gold"
	"github.com/tiennm99/tiennm99bot/internal/modules/lol"
	"github.com/tiennm99/tiennm99bot/internal/modules/loldle"
	"github.com/tiennm99/tiennm99bot/internal/modules/misc"
	"github.com/tiennm99/tiennm99bot/internal/modules/monkeyd"
	"github.com/tiennm99/tiennm99bot/internal/modules/random"
	"github.com/tiennm99/tiennm99bot/internal/modules/stats"
	"github.com/tiennm99/tiennm99bot/internal/modules/sticker"
	"github.com/tiennm99/tiennm99bot/internal/modules/stock"
	"github.com/tiennm99/tiennm99bot/internal/modules/util"
	"github.com/tiennm99/tiennm99bot/internal/modules/weather"
	"github.com/tiennm99/tiennm99bot/internal/modules/wordle"
	"github.com/tiennm99/tiennm99bot/internal/server"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/systemstate"
	"github.com/tiennm99/tiennm99bot/internal/telegram"
)

// gitSHA is the local-build fallback read from the VCS metadata that go build
// embeds automatically. On Coolify the commit comes from SOURCE_COMMIT instead
// (resolveCommitSHA prefers it).
var gitSHA = buildCommitSHA()

// buildCommitSHA returns the short vcs.revision embedded by go build, or ""
// when the binary carries no VCS metadata.
func buildCommitSHA() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return shortCommitSHA(setting.Value)
		}
	}
	return ""
}

func shortCommitSHA(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 7 {
		return value[:7]
	}
	return value
}

// resolveCommitSHA returns the commit identifier for the deploy notification.
// Coolify injects SOURCE_COMMIT into the container environment at runtime, so
// prefer it; fall back to Go's embedded VCS revision for local builds; and when
// neither is set, report "unknown" so the owner still gets the startup DM.
func resolveCommitSHA(envSourceCommit string) string {
	if s := strings.TrimSpace(envSourceCommit); s != "" {
		return s
	}
	if gitSHA != "" {
		return gitSHA
	}
	return "unknown"
}

// factories is the static module catalog. Adding a new module is a one-line
// change here. Lives in main rather than the modules package to avoid an
// import cycle (modules → util → modules).
func factories() map[string]modules.Factory {
	return map[string]modules.Factory{
		"util":                 util.New,
		"misc":                 misc.New,
		"random":               random.New,
		"amlich":               amlich.New,
		"monkeyd":              monkeyd.New,
		"wordle":               wordle.New,
		"loldle":               loldle.New,
		lol.CollectionName:     lol.New,
		coin.CollectionName:    coin.New,
		"gold":                 gold.New,
		stock.CollectionName:   stock.New,
		"stats":                stats.New,
		sticker.CollectionName: sticker.New,
		"alias":                alias.New,
		"blacklist":            blacklist.New,
		weather.CollectionName: weather.New,
	}
}

// mongodbInitTimeout caps MongoDB connect+ping at startup (and Disconnect at
// shutdown). Atlas SRV DNS + TLS handshake can take a couple seconds on a cold
// container; 10s leaves headroom without hiding a wedged cluster.
const mongodbInitTimeout = 10 * time.Second

// stockMigrationTimeout bounds the one-time scan of persisted stock
// portfolios without tying it to the shorter MongoDB connection timeout.
const stockMigrationTimeout = 2 * time.Minute

func main() {
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := loadConfig()
	if cfg.TelegramBotToken == "" {
		log.Fatal("missing required env", "key", "TELEGRAM_BOT_TOKEN")
	}

	// Periodic metrics flush. Cancels with rootCtx; shutdown below waits for it
	// and flushes once more after polling stops, so the trailing window and the
	// last in-flight update are not lost when main returns.
	metricsDone := make(chan struct{})
	go func() {
		metrics.Run(rootCtx)
		close(metricsDone)
	}()

	provider, closeProvider, err := buildProvider(rootCtx, cfg)
	if err != nil {
		log.Fatal("storage init failed", "err", err)
	}
	defer closeProvider()

	if err := initStatsStore(rootCtx, provider); err != nil {
		log.Fatal("stats storage init failed", "err", err)
	}
	if err := lol.InitStore(rootCtx, provider.Collection(lol.CollectionName)); err != nil {
		log.Fatal("lol storage init failed", "err", err)
	}
	if err := initStickerStore(rootCtx, provider); err != nil {
		log.Fatal("sticker storage init failed", "err", err)
	}
	migrationCtx, cancelMigration := context.WithTimeout(rootCtx, stockMigrationTimeout)
	if err := initStockStore(migrationCtx, provider); err != nil {
		cancelMigration()
		log.Fatal("stock storage init failed", "err", err)
	}
	cancelMigration()

	b, err := telegram.NewBot(cfg.TelegramBotToken)
	if err != nil {
		log.Fatal("telegram bot init failed", "err", err)
	}

	botUsername := resolveBotUsername(rootCtx, b, cfg.BotUsername)
	reg, err := modules.Build(cfg.Modules, factories(), provider, modules.BuildOptions{
		Bot:         b,
		BotUsername: botUsername,
	})
	if err != nil {
		log.Fatal("module registry build failed", "err", err)
	}
	auth := modules.Auth{BotOwnerID: cfg.BotOwnerID, AdminUserIDs: cfg.AdminUserIDs}
	modules.Install(b, reg, auth)
	log.Info("modules loaded",
		"modules", len(reg.Modules),
		"commands", len(reg.AllCommands),
		"crons", len(reg.Crons()))
	if n, err := registerCommandMenu(rootCtx, b, reg); err != nil {
		log.Warn("telegram command menu registration failed", "commands", n, "err", err)
	} else {
		log.Info("telegram command menu registered", "commands", n)
	}

	// In-process cron scheduler runs unconditionally so the long-lived container
	// fires module crons (e.g. lol daily push) on their Schedule.
	stopCron, err := cron.Run(rootCtx, reg)
	if err != nil {
		log.Fatal("cron scheduler init failed", "err", err)
	}
	defer stopCron()

	if cfg.BotOwnerID == 0 {
		log.Warn("OWNER_ID unset; Private (owner-only) commands will be denied; Protected commands still work for ADMIN_IDS")
	}

	// Clear any existing webhook at startup before the owner DM and before
	// polling. getUpdates returns HTTP 409 while a webhook is set, so a stuck
	// webhook silently breaks the bot. Best-effort, one shot: a real failure
	// here is logged, not retried.
	if err := telegram.DeleteWebhook(rootCtx, cfg.TelegramBotToken); err != nil {
		log.Warn("deleteWebhook failed; getUpdates may 409 if a webhook is set", "err", err)
	} else {
		log.Info("webhook cleared")
	}

	deploynotify.Run(rootCtx, deploynotify.Config{
		Bot:     b,
		OwnerID: cfg.BotOwnerID,
		GitSHA:  resolveCommitSHA(cfg.SourceCommit),
	})

	handler := server.New()

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// The only route is GET / (health). It responds instantly, so this
		// write deadline is ample and bounds any slow-loris write.
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Info("server listening", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("server crashed", "err", err)
		}
	}()

	// Long polling is the sole Telegram transport (no webhook, no public
	// ingress). Telegram permits exactly one getUpdates consumer per bot token,
	// so deploy exactly one replica. The webhook was cleared at startup above.
	pollingDone := make(chan struct{})
	go func() {
		defer close(pollingDone)
		log.Info("telegram long polling started")
		b.Start(rootCtx) // returns when rootCtx is cancelled
		log.Info("telegram long polling stopped")
	}()

	<-rootCtx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
	}
	select {
	case <-pollingDone:
	case <-shutdownCtx.Done():
		log.Warn("telegram long polling did not stop before shutdown timeout")
	}
	<-metricsDone
	metrics.Flush()
}

func initStockStore(ctx context.Context, provider storage.Provider) error {
	return initStockStoreWith(ctx, provider, stock.InitStore)
}

func initStatsStore(ctx context.Context, provider storage.Provider) error {
	return initStatsStoreWith(ctx, provider, stats.InitStore)
}

type statsStoreInitializer func(context.Context, storage.Collection, storage.Collection) error

func initStatsStoreWith(ctx context.Context, provider storage.Provider, init statsStoreInitializer) error {
	return init(
		ctx,
		provider.Collection("stats"),
		provider.Collection(systemstate.CollectionName),
	)
}

func initStickerStore(ctx context.Context, provider storage.Provider) error {
	return initStickerStoreWith(ctx, provider, sticker.InitStore)
}

type stickerStoreInitializer func(context.Context, storage.Collection, storage.Collection) error

func initStickerStoreWith(ctx context.Context, provider storage.Provider, init stickerStoreInitializer) error {
	return init(
		ctx,
		provider.Collection(sticker.CollectionName),
		provider.Collection(systemstate.CollectionName),
	)
}

type stockStoreInitializer func(context.Context, storage.Collection, storage.Collection) error

func initStockStoreWith(ctx context.Context, provider storage.Provider, init stockStoreInitializer) error {
	return init(
		ctx,
		provider.Collection(stock.CollectionName),
		provider.Collection(systemstate.CollectionName),
	)
}

// buildProvider picks the storage backend. Selection order:
//  1. Explicit KV_PROVIDER env (memory|mongodb) wins.
//  2. Auto-detect: MONGO_URL set → mongodb; otherwise memory.
//
// The self-host default is mongodb (just set MONGO_URL + MONGO_DATABASE — no
// KV_PROVIDER needed). The memory backend is for tests and local no-database
// runs (MODULES=).
//
// Returned closer is always non-nil and safe to call exactly once.
func buildProvider(ctx context.Context, cfg config) (storage.Provider, func(), error) {
	backend := strings.ToLower(strings.TrimSpace(cfg.KVProvider))
	if backend == "" {
		if cfg.MongoURL != "" {
			backend = "mongodb"
		} else {
			backend = "memory"
		}
	}

	switch backend {
	case "memory":
		log.Warn("storage backend: in-memory (data lost on restart)")
		return storage.NewMemoryProvider(), func() {}, nil

	case "mongodb":
		if cfg.MongoURL == "" || cfg.MongoDatabase == "" {
			return nil, func() {}, errors.New("KV_PROVIDER=mongodb requires MONGO_URL and MONGO_DATABASE")
		}
		initCtx, cancel := context.WithTimeout(ctx, mongodbInitTimeout)
		defer cancel()
		client, err := storage.NewMongoClient(initCtx, cfg.MongoURL)
		if err != nil {
			return nil, func() {}, err
		}
		db, err := storage.NewMongoDatabase(client, cfg.MongoDatabase)
		if err != nil {
			_ = client.Disconnect(context.Background())
			return nil, func() {}, err
		}
		closer := func() {
			discCtx, cancel := context.WithTimeout(context.Background(), mongodbInitTimeout)
			defer cancel()
			if err := client.Disconnect(discCtx); err != nil {
				log.Error("mongo disconnect failed", "err", err)
			}
		}
		// NEVER log MONGO_URL — it is mongodb+srv://user:pass@host and is a
		// credential. Log only the (non-secret) database name.
		log.Info("storage backend", "backend", "mongodb", "database", cfg.MongoDatabase)
		return storage.NewMongoProvider(db), closer, nil

	default:
		return nil, func() {}, fmt.Errorf("unknown KV_PROVIDER %q (want memory|mongodb)", backend)
	}
}

// botUsernameTimeout bounds the startup getMe. It is best-effort, so a slow
// Telegram must not hold up the rest of startup for long.
const botUsernameTimeout = 10 * time.Second

// resolveBotUsername returns the bot's username: BOT_USERNAME when set,
// otherwise one getMe call. A failed getMe is logged and yields "" rather than
// stopping startup; modules that need the username then learn it lazily or
// do without.
func resolveBotUsername(ctx context.Context, b *bot.Bot, configured string) string {
	if configured != "" {
		log.Info("bot username", "username", configured, "source", "BOT_USERNAME")
		return configured
	}
	ctx, cancel := context.WithTimeout(ctx, botUsernameTimeout)
	defer cancel()
	me, err := b.GetMe(ctx)
	if err != nil || me == nil || me.Username == "" {
		log.Warn("getMe failed; bot username unknown until a module asks again", "err", err)
		return ""
	}
	log.Info("bot username", "username", me.Username, "source", "getMe")
	return me.Username
}

// config is the process configuration read from the environment by loadConfig.
type config struct {
	Port             string
	TelegramBotToken string
	SourceCommit     string // Coolify-injected commit SHA (runtime env) for deploynotify
	BotUsername      string // optional; empty = ask Telegram via getMe at startup
	Modules          []string
	BotOwnerID       int64
	AdminUserIDs     map[int64]bool
	KVProvider       string // empty = auto-detect; or "memory"|"mongodb"
	MongoURL         string // required when KVProvider=mongodb (Atlas SRV connection string; SECRET — never log)
	MongoDatabase    string // required when KVProvider=mongodb
}

// loadConfig reads config from the environment. PORT defaults to 8080 and an
// invalid PORT is fatal; malformed OWNER_ID / ADMIN_IDS entries are logged and
// ignored.
func loadConfig() config {
	envMap := make(map[string]string, len(os.Environ()))
	for _, kv := range os.Environ() {
		if eq := strings.IndexByte(kv, '='); eq >= 0 {
			envMap[kv[:eq]] = kv[eq+1:]
		}
	}
	port := envMap["PORT"]
	if port == "" {
		port = "8080"
	}
	// PORT must be a number in 0..65535. http.Server uses ":<port>" verbatim,
	// so a junk value would otherwise surface only at ListenAndServe time;
	// fail fast here instead.
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		log.Fatal("invalid PORT", "value", port)
	}
	return config{
		Port:             port,
		TelegramBotToken: envMap["TELEGRAM_BOT_TOKEN"],
		SourceCommit:     envMap["SOURCE_COMMIT"],
		BotUsername:      strings.TrimPrefix(strings.TrimSpace(envMap["BOT_USERNAME"]), "@"),
		Modules:          splitCSV(envMap["MODULES"]),
		BotOwnerID:       parseInt64(envMap["OWNER_ID"]),
		AdminUserIDs:     parseInt64Set(envMap["ADMIN_IDS"]),
		KVProvider:       envMap["KV_PROVIDER"],
		MongoURL:         envMap["MONGO_URL"],
		MongoDatabase:    envMap["MONGO_DATABASE"],
	}
}

// splitCSV splits a comma-separated list, trimming entries and dropping empty ones.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// parseInt64 returns 0 (the "unset" sentinel) when s is empty or invalid.
// Telegram user IDs are positive int64 so 0 is unambiguously "no value".
func parseInt64(s string) int64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		log.Warn("invalid int64 in env", "value", s, "err", err)
		return 0
	}
	return n
}

// parseInt64Set parses a comma-separated list of int64 IDs into a set. Bad
// entries are logged and skipped — one malformed admin ID does not deny the
// rest.
func parseInt64Set(s string) map[int64]bool {
	if s == "" {
		return nil
	}
	out := map[int64]bool{}
	for _, p := range strings.Split(s, ",") {
		t := strings.TrimSpace(p)
		if t == "" {
			continue
		}
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			log.Warn("invalid admin id", "value", t, "err", err)
			continue
		}
		out[n] = true
	}
	return out
}
