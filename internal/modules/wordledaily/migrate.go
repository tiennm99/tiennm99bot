package wordledaily

import (
	"context"
	"fmt"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
	"github.com/tiennm99/tiennm99bot/internal/modules/wordle/wordlist"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// legacyMigrationKey marks, in the system collection, that the in-chat
// wordle module's players were carried into unlimited mode.
const legacyMigrationKey = "wordledaily:legacy-wordle-unlimited"

// legacyGame and legacyStats are the documents the retired in-chat wordle
// module kept in its "wordle" collection, under "game:<subject>" and
// "stats:<subject>". A subject is a user id in a private chat and a group's
// chat id (negative) in a group, where the whole group shared one round.
type legacyGame struct {
	Target    string        `json:"target" bson:"target"`
	Guesses   []legacyGuess `json:"guesses" bson:"guesses"`
	Solved    bool          `json:"solved" bson:"solved"`
	Giveup    bool          `json:"giveup" bson:"giveup"`
	StartedAt int64         `json:"startedAt" bson:"startedAt"` // unix ms
}

type legacyGuess struct {
	Word    string                 `json:"word" bson:"word"`
	Results []wordlist.LetterScore `json:"results" bson:"results"`
}

type legacyStats struct {
	Played       int    `json:"played" bson:"played"`
	Wins         int    `json:"wins" bson:"wins"`
	Streak       int    `json:"streak" bson:"streak"`
	BestStreak   int    `json:"bestStreak" bson:"bestStreak"`
	LastResultAt *int64 `json:"lastResultAt" bson:"lastResultAt"`
}

// InitStore carries the in-chat wordle module's players into unlimited
// mode, once: each player's stats become their unlimited stats and an
// unfinished round becomes their current round. Group rounds and group
// stats belong to no single player and are left alone. Legacy documents
// are never changed or deleted and a target that already exists is never
// overwritten, so a rerun after a failed boot changes nothing that was
// carried already; a completed marker in the system collection makes later
// boots a no-op.
func InitStore(ctx context.Context, legacyColl, coll, systemColl storage.Collection) error {
	return guessgame.RunOnce(ctx, systemColl, legacyMigrationKey, func() (int, error) {
		stats, err := migrateLegacyStats(ctx, legacyColl, coll)
		if err != nil {
			return 0, err
		}
		rounds, err := migrateLegacyRounds(ctx, legacyColl, coll)
		if err != nil {
			return 0, err
		}
		log.Info("wordledaily migrated legacy wordle players", "stats", stats, "rounds", rounds)
		return stats + rounds, nil
	})
}

func migrateLegacyStats(ctx context.Context, legacyColl, coll storage.Collection) (int, error) {
	from := storage.Typed[legacyStats](legacyColl)
	to := storage.Typed[guessgame.RoundStats](coll)
	keys, err := from.List(ctx, "stats:")
	if err != nil {
		return 0, fmt.Errorf("wordledaily migration list stats: %w", err)
	}
	n := 0
	for _, key := range keys {
		uid, ok := guessgame.LegacyUserID(key, "stats:")
		if !ok {
			continue
		}
		old, _, err := from.Get(ctx, key)
		if err != nil {
			return n, fmt.Errorf("wordledaily migration get %s: %w", key, err)
		}
		st := guessgame.RoundStats{
			Played:    old.Played,
			Wins:      old.Wins,
			CurStreak: old.Streak,
			MaxStreak: old.BestStreak,
			Dist:      make([]int, maxGuesses),
		}
		if old.LastResultAt != nil {
			st.LastAt = *old.LastResultAt
		}
		added, err := guessgame.PutIfMissing(ctx, to, guessgame.RoundStatsKey(uid), st)
		if err != nil {
			return n, fmt.Errorf("wordledaily migration put stats %d: %w", uid, err)
		}
		if added {
			n++
		}
	}
	return n, nil
}

func migrateLegacyRounds(ctx context.Context, legacyColl, coll storage.Collection) (int, error) {
	from := storage.Typed[legacyGame](legacyColl)
	to := storage.Typed[guessgame.Round](coll)
	keys, err := from.List(ctx, "game:")
	if err != nil {
		return 0, fmt.Errorf("wordledaily migration list games: %w", err)
	}
	n := 0
	for _, key := range keys {
		uid, ok := guessgame.LegacyUserID(key, "game:")
		if !ok {
			continue
		}
		old, _, err := from.Get(ctx, key)
		if err != nil {
			return n, fmt.Errorf("wordledaily migration get %s: %w", key, err)
		}
		// A finished round already counted in the stats; only one still
		// being played carries over.
		if old.Solved || old.Giveup || len(old.Guesses) >= maxGuesses || old.Target == "" {
			continue
		}
		rd := guessgame.Round{Seq: 1, Target: old.Target, Guesses: []guessgame.Guess{}, Status: guessgame.StatusPlaying, MaxGuesses: maxGuesses}
		if len(old.Guesses) > 0 {
			// A round's clock starts at its first guess.
			rd.StartedAt = old.StartedAt
		}
		for _, g := range old.Guesses {
			rd.Guesses = append(rd.Guesses, guessgame.Guess{Word: g.Word, Marks: marksOf(g.Results)})
		}
		added, err := guessgame.PutIfMissing(ctx, to, guessgame.RoundKey(uid), rd)
		if err != nil {
			return n, fmt.Errorf("wordledaily migration put round %d: %w", uid, err)
		}
		if added {
			n++
		}
	}
	return n, nil
}
