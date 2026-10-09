package noitu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/dict"
	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/engine"
	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/opponent"
)

const (
	turnLimit       = 30 * time.Second
	turnGrace       = 2 * time.Second
	minOpeningOut   = 20
	maxSessions     = 2000
	maxUserSessions = 3
	idleTTL         = 15 * time.Minute
	finishedTTL     = 2 * time.Minute
	startsPerMinute = 10
	moveInterval    = 300 * time.Millisecond
	maxChain        = 300
	maxSuggestions  = 3
	reportTimeout   = 10 * time.Second
)

// Score report states shown on the page.
const (
	reportNone    = ""
	reportPending = "pending"
	reportOK      = "ok"
	reportFailed  = "failed"
	reportSkipped = "skipped"
)

var errBusy = errors.New("noitu: too many sessions")

// chainEntry is one word of the chain as the page shows it.
type chainEntry struct {
	Word     string       `json:"word"`
	By       string       `json:"by"` // "bot" or "player"
	Points   int          `json:"points"`
	Meanings []dict.Sense `json:"meanings"`
}

// session is one live game. Its mutex guards every field below it; the
// store's mutex is never taken while holding it.
type session struct {
	id         string
	claims     claims
	difficulty opponent.Difficulty

	mu          sync.Mutex
	eng         *engine.Engine
	strategy    opponent.Strategy
	chain       []chainEntry
	suggestions []string
	lastMove    time.Time
	lastSeen    time.Time
	endedAt     time.Time
	report      string
}

// expired reports whether the session can be dropped: idle too long, or
// finished long enough ago that its result screen has been read.
func (s *session) expired(now time.Time) bool {
	if s.eng.Over() {
		return now.Sub(s.endedAt) > finishedTTL
	}
	return now.Sub(s.lastSeen) > idleTTL
}

// sessionStore indexes live sessions by id, by owner (player + game
// message) and by user, and counts recent starts per user.
type sessionStore struct {
	mu      sync.Mutex
	byID    map[string]*session
	byOwner map[string]*session
	byUser  map[int64][]*session
	starts  map[int64][]time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{
		byID:    map[string]*session{},
		byOwner: map[string]*session{},
		byUser:  map[int64][]*session{},
		starts:  map[int64][]time.Time{},
	}
}

func (st *sessionStore) get(id string) *session {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.byID[id]
}

// allowStart records a start for user and reports whether it is within
// startsPerMinute.
func (st *sessionStore) allowStart(user int64, now time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	recent := st.starts[user][:0]
	for _, t := range st.starts[user] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= startsPerMinute {
		st.starts[user] = recent
		return false
	}
	st.starts[user] = append(recent, now)
	return true
}

// add registers s and returns the session it displaced, if any: the owner's
// previous session on the same game message, or else the user's oldest
// session once they hold maxUserSessions. The caller must settle the
// displaced session (see service.settle) so a game left mid-play still gets
// its score reported. Nothing changes when add fails.
func (st *sessionStore) add(s *session) (*session, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	user := s.claims.UserID
	displaced := st.byOwner[s.claims.ownerKey()]
	if displaced == nil && len(st.byUser[user]) >= maxUserSessions {
		displaced = st.byUser[user][0] // kept oldest first
	}
	if displaced == nil && len(st.byID) >= maxSessions {
		return nil, errBusy
	}
	if displaced != nil {
		st.removeLocked(displaced)
	}
	st.byID[s.id] = s
	st.byOwner[s.claims.ownerKey()] = s
	st.byUser[user] = append(st.byUser[user], s)
	return displaced, nil
}

func (st *sessionStore) remove(s *session) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.removeLocked(s)
}

func (st *sessionStore) removeLocked(s *session) {
	if st.byID[s.id] == s {
		delete(st.byID, s.id)
	}
	if key := s.claims.ownerKey(); st.byOwner[key] == s {
		delete(st.byOwner, key)
	}
	user := s.claims.UserID
	if live := slices.DeleteFunc(st.byUser[user], func(o *session) bool { return o == s }); len(live) > 0 {
		st.byUser[user] = live
	} else {
		delete(st.byUser, user)
	}
}

// all snapshots the live sessions and drops start records older than a
// minute.
func (st *sessionStore) all(now time.Time) []*session {
	st.mu.Lock()
	defer st.mu.Unlock()
	for user, ts := range st.starts {
		if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= time.Minute {
			delete(st.starts, user)
		}
	}
	out := make([]*session, 0, len(st.byID))
	for _, s := range st.byID {
		out = append(out, s)
	}
	return out
}

func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// service is the module's runtime: config, the word store and the sessions.
type service struct {
	cfg      config
	words    *dict.Store // nil when the game is disabled
	sessions *sessionStore
}

func (s *service) enabled() bool { return s.words != nil }

// sweep settles turns that ran out with nobody calling the API (so their
// scores are still reported) and drops expired sessions.
func (s *service) sweep(now time.Time) {
	for _, sess := range s.sessions.all(now) {
		sess.mu.Lock()
		if sess.eng.Timeout(now) {
			s.finish(sess, now)
		}
		drop := sess.expired(now)
		sess.mu.Unlock()
		if drop {
			s.sessions.remove(sess)
		}
	}
}

func (s *service) sweepCron(context.Context, modules.Deps) error {
	s.sweep(s.cfg.now())
	return nil
}

// settle ends a game the player walked away from and reports its score, as
// the sweep would on timeout. A dead-end position ends as no legal move
// rather than a resignation. Caller must not hold sess.mu.
func (s *service) settle(sess *session, now time.Time) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if s.resign(sess) {
		s.finish(sess, now)
	}
}

// resign ends the human's game: as no legal move when the position is a dead
// end, otherwise as giving up. Reports whether it ended the game. Caller
// holds sess.mu.
func (s *service) resign(sess *session) bool {
	return sess.eng.NoMove() || sess.eng.GiveUp()
}

// finish records the end of a game and reports a positive score once.
// Caller holds sess.mu.
func (s *service) finish(sess *session, now time.Time) {
	sess.endedAt = now
	if sess.eng.Winner() == engine.Bot {
		sess.suggestions = sess.eng.Suggestions(maxSuggestions)
	}
	score := sess.eng.Score()
	if score <= 0 || s.cfg.reporter == nil {
		sess.report = reportSkipped
		return
	}
	sess.report = reportPending
	go s.report(sess, score) //nolint:gosec // G118: the report must outlive the request that ended the game
}

func (s *service) report(sess *session, score int) {
	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()
	err := s.cfg.reporter.Report(ctx, sess.claims, score)
	status := reportOK
	if err != nil && !scoreNotModified(err) {
		status = reportFailed
		log.Warn("noitu setGameScore failed", "err", err)
	}
	sess.mu.Lock()
	sess.report = status
	sess.mu.Unlock()
}
