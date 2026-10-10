package loldle

import (
	"context"
	"fmt"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// legacyMigrationKey marks, in the system collection, that the in-chat
// game's players were carried into unlimited mode.
const legacyMigrationKey = "loldle:legacy-unlimited"

// legacyGame and legacyStats are the documents the in-chat game kept before
// unlimited mode, under "game:<subject>" and "stats:<subject>" in this
// module's collection. A subject is a user id in a private chat and a
// group's chat id (negative) in a group, where the whole group shared one
// round. A game was deleted when it ended, so every stored one is active.
type legacyGame struct {
	Target     string   `json:"target" bson:"target"`
	Guesses    []string `json:"guesses" bson:"guesses"`
	StartedAt  *int64   `json:"startedAt" bson:"startedAt"`
	MaxGuesses int      `json:"maxGuesses,omitempty" bson:"maxGuesses,omitempty"`
}

type legacyStats struct {
	Played     int `json:"played" bson:"played"`
	Wins       int `json:"wins" bson:"wins"`
	Streak     int `json:"streak" bson:"streak"`
	BestStreak int `json:"bestStreak" bson:"bestStreak"`
}

// InitStore carries the in-chat game's players into unlimited mode, once:
// each player's stats become their unlimited stats and their round in play
// becomes their current round, with its marks rebuilt from today's data.
// Group rounds and group stats belong to no single player and are left
// alone. Legacy documents are never changed or deleted and a target that
// already exists is never overwritten, so a rerun after a failed boot
// changes nothing that was carried already; a completed marker in the
// system collection makes later boots a no-op.
func InitStore(ctx context.Context, coll, systemColl storage.Collection) error {
	champions := loadChampions()
	return guessgame.RunOnce(ctx, systemColl, legacyMigrationKey, func() (int, error) {
		stats, err := migrateLegacyStats(ctx, coll)
		if err != nil {
			return 0, err
		}
		rounds, err := migrateLegacyRounds(ctx, coll, champions)
		if err != nil {
			return 0, err
		}
		log.Info("loldle migrated legacy players", "stats", stats, "rounds", rounds)
		return stats + rounds, nil
	})
}

func migrateLegacyStats(ctx context.Context, coll storage.Collection) (int, error) {
	from := storage.Typed[legacyStats](coll)
	to := storage.Typed[guessgame.RoundStats](coll)
	keys, err := from.List(ctx, "stats:")
	if err != nil {
		return 0, fmt.Errorf("loldle migration list stats: %w", err)
	}
	n := 0
	for _, key := range keys {
		uid, ok := guessgame.LegacyUserID(key, "stats:")
		if !ok {
			continue
		}
		old, _, err := from.Get(ctx, key)
		if err != nil {
			return n, fmt.Errorf("loldle migration get %s: %w", key, err)
		}
		st := guessgame.RoundStats{Played: old.Played, Wins: old.Wins, CurStreak: old.Streak, MaxStreak: old.BestStreak,
			Dist: make([]int, MaxGuessesCap)}
		added, err := guessgame.PutIfMissing(ctx, to, guessgame.RoundStatsKey(uid), st)
		if err != nil {
			return n, fmt.Errorf("loldle migration put stats %d: %w", uid, err)
		}
		if added {
			n++
		}
	}
	return n, nil
}

func migrateLegacyRounds(ctx context.Context, coll storage.Collection, champions []Champion) (int, error) {
	from := storage.Typed[legacyGame](coll)
	to := storage.Typed[guessgame.Round](coll)
	keys, err := from.List(ctx, "game:")
	if err != nil {
		return 0, fmt.Errorf("loldle migration list games: %w", err)
	}
	n := 0
	for _, key := range keys {
		uid, ok := guessgame.LegacyUserID(key, "game:")
		if !ok {
			continue
		}
		old, _, err := from.Get(ctx, key)
		if err != nil {
			return n, fmt.Errorf("loldle migration get %s: %w", key, err)
		}
		target := findChampionByExactName(champions, old.Target)
		maxGuesses := normalizeMaxGuesses(old.MaxGuesses)
		// A round whose answer left the data cannot be scored, and an
		// exhausted one is over; the in-chat game started afresh on both.
		if target == nil || len(old.Guesses) >= maxGuesses {
			continue
		}
		rd := guessgame.Round{Seq: 1, Target: target.ChampionName, Guesses: []guessgame.Guess{}, Status: guessgame.StatusPlaying,
			MaxGuesses: maxGuesses}
		for _, name := range old.Guesses {
			// A guess naming a champion since removed is skipped, as the
			// in-chat board did.
			if g := findChampionByExactName(champions, name); g != nil {
				rd.Guesses = append(rd.Guesses, guessgame.Guess{Word: g.ChampionName, Marks: marksOf(CompareChampions(g, target))})
			}
		}
		if old.StartedAt != nil && len(rd.Guesses) > 0 {
			rd.StartedAt = *old.StartedAt
		}
		added, err := guessgame.PutIfMissing(ctx, to, guessgame.RoundKey(uid), rd)
		if err != nil {
			return n, fmt.Errorf("loldle migration put round %d: %w", uid, err)
		}
		if added {
			n++
		}
	}
	return n, nil
}
