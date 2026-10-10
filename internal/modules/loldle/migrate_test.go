package loldle

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil/mongotest"
)

var mongoTests mongotest.Manager

func TestMain(m *testing.M) {
	os.Exit(mongoTests.Run(m))
}

// legacyLoldle seeds the in-chat game's documents.
func legacyLoldle(t *testing.T, coll storage.Collection) {
	t.Helper()
	ctx := context.Background()
	stats := storage.Typed[legacyStats](coll)
	for key, st := range map[string]legacyStats{
		"stats:1":    {Played: 9, Wins: 6, Streak: 2, BestStreak: 4},
		"stats:2":    {Played: 3},
		"stats:-100": {Played: 30, Wins: 20},
	} {
		if err := stats.Put(ctx, key, st); err != nil {
			t.Fatal(err)
		}
	}
	at := int64(1_700_000_000_000)
	games := storage.Typed[legacyGame](coll)
	for key, g := range map[string]legacyGame{
		"game:1":    {Target: "Ahri", Guesses: []string{"Akali", "Removed", "Kai'Sa"}, StartedAt: &at, MaxGuesses: 5},
		"game:3":    {Target: "Gone", Guesses: []string{"Akali"}},
		"game:4":    {Target: "Ahri", Guesses: []string{}},
		"game:5":    {Target: "Ahri", Guesses: []string{"Akali", "Jinx"}, MaxGuesses: 2},
		"game:-100": {Target: "Ahri", Guesses: []string{"Akali"}},
	} {
		if err := games.Put(ctx, key, g); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.Typed[roundConfig](coll).Put(ctx, configKey("-100"), roundConfig{MaxGuesses: 4}); err != nil {
		t.Fatal(err)
	}
}

// assertLegacyMigration runs the migration twice over provider and checks
// what it carried over.
func assertLegacyMigration(t *testing.T, provider storage.Provider) {
	t.Helper()
	ctx := context.Background()
	coll, system := provider.Collection(ShortName), provider.Collection("system")
	legacyLoldle(t, coll)
	ustats := storage.Typed[guessgame.RoundStats](coll)
	if err := ustats.Put(ctx, guessgame.RoundStatsKey(2), guessgame.RoundStats{Played: 1, Wins: 1, LastSeq: 1}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := InitStore(ctx, coll, system); err != nil {
			t.Fatal(err)
		}
	}
	st, _, err := ustats.Get(ctx, guessgame.RoundStatsKey(1))
	if err != nil || st.Played != 9 || st.Wins != 6 || st.CurStreak != 2 || st.MaxStreak != 4 || len(st.Dist) != MaxGuessesCap {
		t.Fatalf("player 1 stats = %+v, %v", st, err)
	}
	if st, _, _ := ustats.Get(ctx, guessgame.RoundStatsKey(2)); st.Played != 1 {
		t.Fatalf("existing stats overwritten: %+v", st)
	}
	rounds := storage.Typed[guessgame.Round](coll)
	keys, _ := rounds.List(ctx, "u")
	if got := strings.Join(keys, " "); got != "uround:1 uround:4 ustats:1 ustats:2" {
		t.Fatalf("migrated keys = %s", got)
	}
	rd, _, err := rounds.Get(ctx, guessgame.RoundKey(1))
	if err != nil || rd.Seq != 1 || rd.Target != "Ahri" || rd.MaxGuesses != 5 || rd.StartedAt != 1_700_000_000_000 || rd.Status != guessgame.StatusPlaying ||
		len(rd.Guesses) != 2 || rd.Guesses[0] != (guessgame.Guess{Word: "Akali", Marks: "cwwwccu"}) || rd.Guesses[1].Marks != "cwccwwd" {
		t.Fatalf("player 1 round = %+v, %v", rd, err)
	}
	if rd, _, _ := rounds.Get(ctx, guessgame.RoundKey(4)); rd.MaxGuesses != MaxGuesses || rd.StartedAt != 0 {
		t.Fatalf("legacy default round = %+v", rd)
	}
	if rec, _, err := storage.Typed[struct {
		Status string `json:"status" bson:"status"`
		Count  int64  `json:"count" bson:"count"`
	}](system).Get(ctx, legacyMigrationKey); err != nil || rec.Status != "complete" || rec.Count != 3 {
		t.Fatalf("marker = %+v, %v", rec, err)
	}
	// Legacy documents and the setmax config are kept as they were.
	if keys, _ := storage.Typed[legacyStats](coll).List(ctx, "stats:"); len(keys) != 3 {
		t.Fatalf("legacy stats = %v", keys)
	}
	if keys, _ := storage.Typed[legacyGame](coll).List(ctx, "game:"); len(keys) != 5 {
		t.Fatalf("legacy games = %v", keys)
	}
	if n, _ := getMaxGuesses(ctx, storage.Typed[roundConfig](coll), "-100"); n != 4 {
		t.Fatalf("setmax = %d", n)
	}
}

func TestInitStore_CarriesLegacyPlayersOnce(t *testing.T) {
	assertLegacyMigration(t, storage.NewMemoryProvider())
}

// The migrated round and stats carry on through the page.
func TestInitStore_MigratedRoundPlaysOn(t *testing.T) {
	h := newHarness(t)
	legacyLoldle(t, h.coll)
	if err := InitStore(context.Background(), h.coll, storage.NewMemoryProvider().Collection("system")); err != nil {
		t.Fatal(err)
	}
	tok := h.token(1, 1, 5, "A", true)
	if r := h.state(tok); len(r.v.Guesses) != 2 || r.v.Max != 5 {
		t.Fatalf("migrated round = %s", r.body)
	}
	r := h.roundGuess(tok, 1, "Ahri")
	if s := r.v.Stats; s == nil || s.Played != 10 || s.Cur != 3 || s.Max != 4 || s.Dist[2] != 1 {
		t.Fatalf("after the migrated round = %s", r.body)
	}
	// Daily stats never read the legacy documents.
	if st, err := h.svc.LoadStats(context.Background(), 1); err != nil || st.Played != 0 {
		t.Fatalf("daily stats = %+v, %v", st, err)
	}
}

// The migration runs against MongoDB in production, where documents go
// through BSON rather than JSON.
func TestInitStore_MongoCarriesLegacyPlayersOnce(t *testing.T) {
	uri := mongoTests.URI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	client, err := storage.NewMongoClient(ctx, uri)
	if err != nil {
		t.Fatalf("NewMongoClient: %v", err)
	}
	db := client.Database(fmt.Sprintf("tiennm99bot_loldle_test_%d", time.Now().UnixNano()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = db.Drop(ctx)
		_ = client.Disconnect(ctx)
	})
	assertLegacyMigration(t, storage.NewMongoProvider(db))
}
