// Package noitu is the "nối từ" Telegram HTML5 game: the /noitubot command
// sends the BotFather game, the Play button opens a page this bot serves, and
// the player chains Vietnamese words against the bot opponent there. /noitu
// sends a card to a group whose Play button opens a room where the chat's
// members chain words against each other, and /noitutop shows the group's
// leaderboard across those room games.
//
// The server owns the game. The page only sends words; validation, the turn
// timer, the bot's replies and the score all happen here, and the score is
// reported with setGameScore so Telegram's in-chat high-score table works.
// See docs/noitu-game.md.
package noitu

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	mrand "math/rand/v2"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/dict"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	// ShortName is the game's BotFather short name.
	ShortName = "noitu"

	baseURLEnv = "GAME_BASE_URL"
	secretEnv  = "NOITU_GAME_SECRET" //nolint:gosec // G101: an env var name, not a credential

	// minSecretBytes is the shortest NOITU_GAME_SECRET accepted.
	minSecretBytes = 32
	// tokenKeyLabel derives the token key from the bot token when no
	// dedicated secret is set.
	tokenKeyLabel = "tiennm99bot/noitu/token/v1" //nolint:gosec // G101: a public derivation label; the key is the bot token

	routePrefix = "/games/" + ShortName + "/"
)

// config is everything New reads from the environment, injectable in tests.
type config struct {
	baseURL   string // public https base without trailing slash; "" disables
	key       []byte // token HMAC key; empty disables
	now       func() time.Time
	newRNG    func() *mrand.Rand
	reporter  scoreReporter // nil disables
	announcer announcer     // nil skips room result messages
	cards     storage.DocStore[pvpCard]
	top       storage.DocStore[topEntry] // nil disables the group leaderboard
	loadDict  func() (*dict.Store, error)
}

// New builds the module from the environment. The game is enabled only when
// GAME_BASE_URL is a valid https URL and a token key is available; otherwise
// /noitu and /noitubot still send the game and Play explains it is not
// configured.
func New(deps modules.Deps) modules.Module {
	cfg := config{
		baseURL:  parseBaseURL(os.Getenv(baseURLEnv)),
		key:      tokenKey(os.Getenv(secretEnv), deps),
		now:      time.Now,
		newRNG:   newSessionRNG,
		loadDict: dict.Default,
	}
	if deps.Bot != nil {
		cfg.reporter = botReporter{b: deps.Bot}
		cfg.announcer = botReporter{b: deps.Bot}
	}
	if deps.Store != nil {
		cfg.cards = storage.Typed[pvpCard](deps.Store)
		cfg.top = storage.Typed[topEntry](deps.Store)
	}
	return newWithConfig(cfg)
}

// newWithConfig assembles the module from an explicit config.
func newWithConfig(cfg config) modules.Module { return newService(cfg).module() }

// newService loads the dictionary when the game is configured. A load failure
// leaves the game disabled rather than failing startup for every module.
func newService(cfg config) *service {
	svc := &service{cfg: cfg, sessions: newSessionStore(), rooms: newRoomStore()}
	if cfg.baseURL != "" && len(cfg.key) > 0 && cfg.reporter != nil {
		words, err := cfg.loadDict()
		if err != nil {
			log.Error("noitu dictionary load failed; game disabled", "err", err)
		} else {
			svc.words = words
			log.Info("noitu game enabled", "words", words.Len(), "dictionary_sha256", words.SourceSHA256)
		}
	}
	return svc
}

// module describes the service to the registry. Routes and the sweep cron
// exist only when the game is enabled, so a disabled game exposes nothing
// over HTTP. The card cleanup runs whenever cards can be stored, because
// /noitu registers cards even while the game is disabled.
func (svc *service) module() modules.Module {
	mod := modules.Module{
		Commands: []modules.Command{{
			Name:        pvpCommand,
			Visibility:  modules.VisibilityPublic,
			Description: "Chơi nối từ với các thành viên trong nhóm (chỉ trong nhóm)",
			Handler:     svc.handlePvPCommand,
		}, {
			Name:        soloCommand,
			Visibility:  modules.VisibilityPublic,
			Description: "Chơi nối từ với bot",
			Handler:     svc.handleCommand,
		}, {
			Name:        topCommand,
			Visibility:  modules.VisibilityPublic,
			Description: "Bảng xếp hạng nối từ của nhóm",
			Handler:     svc.handleTopCommand,
		}},
		Games: []modules.Game{{
			ShortName:  ShortName,
			Visibility: modules.VisibilityPublic,
			Handler:    svc.handlePlay,
		}},
	}
	if svc.enabled() {
		mod.HTTP = []modules.Route{{Pattern: routePrefix, Handler: svc.handler()}}
		mod.Crons = []modules.Cron{{Schedule: "* * * * *", Name: "noitu_sweep", Handler: svc.sweepCron}}
	}
	if svc.cfg.cards != nil {
		mod.Crons = append(mod.Crons, modules.Cron{Schedule: cardCleanupSchedule, Name: "noitu_pvp_cards", Handler: svc.cleanupCards})
	}
	return mod
}

// parseBaseURL accepts https://host[/path] and returns it without a trailing
// slash. Anything else is logged and disables the game.
func parseBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		log.Warn("GAME_BASE_URL must be https://host[/path]; noitu game disabled")
		return ""
	}
	return strings.TrimRight(u.String(), "/")
}

// tokenKey returns NOITU_GAME_SECRET when set, otherwise a key derived from
// the bot token. A secret that is set but too short disables the game rather
// than silently falling back.
func tokenKey(secret string, deps modules.Deps) []byte {
	if secret != "" {
		if len(secret) < minSecretBytes {
			log.Warn("NOITU_GAME_SECRET is shorter than 32 bytes; noitu game disabled")
			return nil
		}
		return []byte(secret)
	}
	if deps.Bot == nil {
		return nil
	}
	return deriveKey(deps.Bot.Token())
}

// deriveKey is HMAC-SHA256(key=bot token, tokenKeyLabel). Rotating the bot
// token therefore invalidates every open game link.
func deriveKey(botToken string) []byte {
	if botToken == "" {
		return nil
	}
	mac := hmac.New(sha256.New, []byte(botToken))
	mac.Write([]byte(tokenKeyLabel))
	return mac.Sum(nil)
}

// newSessionRNG seeds a per-game generator for the opening word and the bot's
// choices. Gameplay randomness, not security; crypto/rand only seeds it.
func newSessionRNG() *mrand.Rand {
	var seed [16]byte
	_, _ = rand.Read(seed[:])
	return mrand.New(mrand.NewPCG(binary.LittleEndian.Uint64(seed[:8]), binary.LittleEndian.Uint64(seed[8:])))
}
