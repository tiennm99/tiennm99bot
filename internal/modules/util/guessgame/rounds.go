package guessgame

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/keylock"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// Unlimited mode: every player has their own current round with a random
// answer, kept server-side under "uround:<uid>", and their own stats under
// "ustats:<uid>". A round is replaced, never appended to, so one document a
// player is all it stores.

// Round is a player's current unlimited round.
type Round struct {
	Seq        int     `json:"seq" bson:"seq"` // 1 for the player's first round, then one more each round
	Target     string  `json:"target" bson:"target"`
	Guesses    []Guess `json:"guesses" bson:"guesses"`
	Status     string  `json:"status" bson:"status"`
	GaveUp     bool    `json:"gave_up,omitempty" bson:"gave_up,omitempty"`
	MaxGuesses int     `json:"max_guesses" bson:"max_guesses"`                   // frozen when the round starts
	StartedAt  int64   `json:"started_at,omitempty" bson:"started_at,omitempty"` // unix ms of the first guess
	FinishedAt int64   `json:"finished_at,omitempty" bson:"finished_at,omitempty"`
}

// Finished reports whether the round is won or lost.
func (r *Round) Finished() bool { return r.Status == StatusWon || r.Status == StatusLost }

// RoundStats is a player's unlimited record. A streak counts consecutive
// wins. LastSeq is the last round folded in, so a finished round counts
// once even when its stats write is retried.
type RoundStats struct {
	Played    int   `json:"played" bson:"played"`
	Wins      int   `json:"wins" bson:"wins"`
	CurStreak int   `json:"cur_streak" bson:"cur_streak"`
	MaxStreak int   `json:"max_streak" bson:"max_streak"`
	Dist      []int `json:"dist" bson:"dist"` // wins by guess count, index 0 = 1 guess
	LastAt    int64 `json:"last_at,omitempty" bson:"last_at,omitempty"`
	LastSeq   int   `json:"last_seq" bson:"last_seq"`
}

// RoundsConfig is one game's unlimited mode.
type RoundsConfig struct {
	Store storage.Collection
	Rules Rules // Judge scores the guesses
	// Pick returns a random answer for a new round; prev is the answer of
	// the round it replaces, "" for a player's first.
	Pick func(prev string) string
	// DistLen sizes the stats' guess distribution: the largest guess
	// budget a round can have.
	DistLen int
	Now     func() time.Time
}

// RoundState is a player's round after a request.
type RoundState struct {
	Round Round
	Stats RoundStats
	// Ended reports that this request finished the round.
	Ended bool
	// Abandoned is the round a New request gave up, set only when that
	// counted as a loss.
	Abandoned *Round
}

// Rounds is a game's unlimited mode. Every method holds the player's lock,
// so the page and the chat commands never interleave on one round.
type Rounds struct {
	cfg    RoundsConfig
	rounds storage.DocStore[Round]
	stats  storage.DocStore[RoundStats]
	locks  keylock.Map
}

// NewRounds builds a game's unlimited mode, or nil without storage.
func NewRounds(cfg RoundsConfig) *Rounds {
	if cfg.Store == nil {
		return nil
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Rounds{
		cfg:    cfg,
		rounds: storage.Typed[Round](cfg.Store),
		stats:  storage.Typed[RoundStats](cfg.Store),
	}
}

// RoundKey is the key of a player's current round.
func RoundKey(userID int64) string { return "uround:" + strconv.FormatInt(userID, 10) }

// RoundStatsKey is the key of a player's unlimited stats.
func RoundStatsKey(userID int64) string { return "ustats:" + strconv.FormatInt(userID, 10) }

func roundLockKey(userID int64) string { return "r:" + strconv.FormatInt(userID, 10) }

// load returns the player's round, found=false when they never had one.
func (r *Rounds) load(ctx context.Context, userID int64) (Round, bool, error) {
	rd, _, err := r.rounds.Get(ctx, RoundKey(userID))
	switch {
	case err == nil:
		return rd, true, nil
	case errors.Is(err, storage.ErrNotFound):
		return Round{}, false, nil
	default:
		return Round{}, false, err
	}
}

// LoadStats returns the player's unlimited stats, zero when they never
// finished a round.
func (r *Rounds) LoadStats(ctx context.Context, userID int64) (RoundStats, error) {
	st, _, err := r.stats.Get(ctx, RoundStatsKey(userID))
	if errors.Is(err, storage.ErrNotFound) {
		st, err = RoundStats{}, nil
	}
	if len(st.Dist) != r.cfg.DistLen {
		st.Dist = append(st.Dist, make([]int, r.cfg.DistLen)...)[:r.cfg.DistLen]
	}
	return st, err
}

// start writes a fresh round after prev (zero for the first) and returns it.
func (r *Rounds) start(ctx context.Context, userID int64, prev Round, maxGuesses int) (Round, error) {
	rd := Round{Seq: prev.Seq + 1, Target: r.cfg.Pick(prev.Target), Guesses: []Guess{}, Status: StatusPlaying, MaxGuesses: maxGuesses}
	if err := r.rounds.Put(ctx, RoundKey(userID), rd); err != nil {
		return Round{}, err
	}
	return rd, nil
}

// current returns the player's round, starting their first one when there
// is none, with their stats. A finished round whose stats write failed is
// folded in now. Caller holds the player's lock.
func (r *Rounds) current(ctx context.Context, userID int64, maxGuesses int) (RoundState, error) {
	rd, found, err := r.load(ctx, userID)
	if err != nil {
		return RoundState{}, err
	}
	if !found {
		if rd, err = r.start(ctx, userID, Round{}, maxGuesses); err != nil {
			return RoundState{}, err
		}
	}
	st, err := r.settle(ctx, userID, rd)
	if err != nil {
		return RoundState{}, err
	}
	return RoundState{Round: rd, Stats: st}, nil
}

// settle folds a finished round into the stats, once. Caller holds the
// player's lock.
func (r *Rounds) settle(ctx context.Context, userID int64, rd Round) (RoundStats, error) {
	st, err := r.LoadStats(ctx, userID)
	if err != nil || !rd.Finished() || st.LastSeq >= rd.Seq {
		return st, err
	}
	st.Played++
	if rd.Status == StatusWon {
		st.Wins++
		if n := len(rd.Guesses); n >= 1 && n <= len(st.Dist) {
			st.Dist[n-1]++
		}
		st.CurStreak++
		st.MaxStreak = max(st.MaxStreak, st.CurStreak)
	} else {
		st.CurStreak = 0
	}
	st.LastSeq, st.LastAt = rd.Seq, rd.FinishedAt
	if err := r.stats.Put(ctx, RoundStatsKey(userID), st); err != nil {
		return RoundStats{}, err
	}
	return st, nil
}

// finish ends the round as status and saves it, then folds it into the
// stats. The round is saved first and is the record: a failed stats write
// is repaired by the next request.
func (r *Rounds) finish(ctx context.Context, userID int64, rd *Round, status string) (RoundStats, error) {
	rd.Status, rd.FinishedAt = status, r.cfg.Now().UnixMilli()
	if err := r.rounds.Put(ctx, RoundKey(userID), *rd); err != nil {
		return RoundStats{}, err
	}
	return r.settle(ctx, userID, *rd)
}

// Load returns the player's current round, finished or not, starting their
// first one with maxGuesses when they have none.
func (r *Rounds) Load(ctx context.Context, userID int64, maxGuesses int) (RoundState, error) {
	defer r.locks.Acquire(roundLockKey(userID))()
	return r.current(ctx, userID, maxGuesses)
}

// Guess plays input on the player's current round. seq is the round the
// caller shows; a stale one is refused so a board never mixes two rounds,
// and 0 plays whatever round is current. A player without a round gets one
// with maxGuesses. Rules.Judge errors pass through unchanged.
func (r *Rounds) Guess(ctx context.Context, userID int64, seq int, input string, maxGuesses int) (RoundState, error) {
	defer r.locks.Acquire(roundLockKey(userID))()
	s, err := r.current(ctx, userID, maxGuesses)
	if err != nil {
		return RoundState{}, err
	}
	rd := s.Round
	if seq != 0 && seq != rd.Seq {
		return s, ErrNewRound
	}
	if rd.Finished() {
		return s, ErrFinished
	}
	g, err := r.cfg.Rules.Judge(input, rd.Target, rd.Guesses)
	if err != nil {
		return s, err
	}
	now := r.cfg.Now().UnixMilli()
	if rd.StartedAt == 0 {
		rd.StartedAt = now
	}
	rd.Guesses = append(rd.Guesses, g)
	if g.Word == rd.Target || len(rd.Guesses) >= rd.MaxGuesses {
		status := StatusLost
		if g.Word == rd.Target {
			status = StatusWon
		}
		st, err := r.finish(ctx, userID, &rd, status)
		if err != nil {
			return RoundState{}, err
		}
		return RoundState{Round: rd, Stats: st, Ended: true}, nil
	}
	if err := r.rounds.Put(ctx, RoundKey(userID), rd); err != nil {
		return RoundState{}, err
	}
	return RoundState{Round: rd, Stats: s.Stats}, nil
}

// New starts the player's next round with maxGuesses. seq is the round the
// caller shows: a stale one returns the current round unchanged, so a
// double click starts one round, and 0 always starts one. A round still
// being played is given up first; that counts as a loss once it has a
// guess, and a round nobody guessed on is just replaced.
func (r *Rounds) New(ctx context.Context, userID int64, seq, maxGuesses int) (RoundState, error) {
	defer r.locks.Acquire(roundLockKey(userID))()
	s, err := r.current(ctx, userID, maxGuesses)
	if err != nil {
		return RoundState{}, err
	}
	prev := s.Round
	if seq != 0 && seq != prev.Seq {
		return s, nil
	}
	var abandoned *Round
	if !prev.Finished() && len(prev.Guesses) > 0 {
		prev.GaveUp = true
		if s.Stats, err = r.finish(ctx, userID, &prev, StatusLost); err != nil {
			return RoundState{}, err
		}
		abandoned = &prev
	}
	rd, err := r.start(ctx, userID, prev, maxGuesses)
	if err != nil {
		return RoundState{}, err
	}
	return RoundState{Round: rd, Stats: s.Stats, Abandoned: abandoned}, nil
}

// GiveUp ends the player's round being played as a loss. On a finished
// round it changes nothing and reports Ended false; without a round it
// returns ErrNoRound.
func (r *Rounds) GiveUp(ctx context.Context, userID int64) (RoundState, error) {
	defer r.locks.Acquire(roundLockKey(userID))()
	rd, found, err := r.load(ctx, userID)
	if err != nil {
		return RoundState{}, err
	}
	if !found {
		return RoundState{}, ErrNoRound
	}
	st, err := r.settle(ctx, userID, rd)
	if err != nil {
		return RoundState{}, err
	}
	if rd.Finished() {
		return RoundState{Round: rd, Stats: st}, nil
	}
	rd.GaveUp = true
	if st, err = r.finish(ctx, userID, &rd, StatusLost); err != nil {
		return RoundState{}, err
	}
	return RoundState{Round: rd, Stats: st, Ended: true}, nil
}

// Replace starts the player's next round without counting the current one,
// for a round that can no longer be played, such as one whose answer left
// the game's data.
func (r *Rounds) Replace(ctx context.Context, userID int64, maxGuesses int) (RoundState, error) {
	defer r.locks.Acquire(roundLockKey(userID))()
	prev, _, err := r.load(ctx, userID)
	if err != nil {
		return RoundState{}, err
	}
	rd, err := r.start(ctx, userID, prev, maxGuesses)
	if err != nil {
		return RoundState{}, err
	}
	st, err := r.LoadStats(ctx, userID)
	if err != nil {
		return RoundState{}, err
	}
	return RoundState{Round: rd, Stats: st}, nil
}
