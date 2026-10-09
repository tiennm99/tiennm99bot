package stats

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/systemstate"
	"github.com/tiennm99/tiennm99bot/internal/testutil/mongotest"
)

var mongoTests mongotest.Manager

func TestMain(m *testing.M) {
	os.Exit(mongoTests.Run(m))
}

func TestInitStore_MongoCreatesIndexes(t *testing.T) {
	ctx, provider := setupMongoStatsProvider(t)
	statsColl := provider.Collection("stats")
	systemColl := provider.Collection(systemstate.CollectionName)

	rawStatsColl, ok := storage.MongoCollection(statsColl)
	if !ok {
		t.Fatal("stats collection is not Mongo-backed")
	}

	docs := storage.Typed[usageEntry](statsColl)
	if err := docs.Put(ctx, "stock_dividend", usageEntry{Cmd: "stock_dividend", N: 9, Deleted: true}); err != nil {
		t.Fatalf("seed anonymous stats: %v", err)
	}
	if err := docs.Put(ctx, "stock_dividend:7", usageEntry{Cmd: "stock_dividend", UserID: 7, Username: "alice", N: 4, Deleted: true}); err != nil {
		t.Fatalf("seed user stats: %v", err)
	}
	if err := docs.Put(ctx, "stock_dividend_extra", usageEntry{Cmd: "stock_dividend_extra", N: 3}); err != nil {
		t.Fatalf("seed prefix stats: %v", err)
	}

	if err := InitStore(ctx, statsColl, systemColl); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if err := InitStore(ctx, statsColl, systemColl); err != nil {
		t.Fatalf("InitStore second run: %v", err)
	}

	store := newUsageStore(statsColl)
	if rows, err := store.TopCommands(ctx, 10); err != nil || len(rows) != 1 || rows[0].display != "/stock_dividend_extra" || rows[0].n != 3 {
		t.Fatalf("top commands with deleted rows = %+v, err=%v", rows, err)
	}
	if rows, err := store.TopUsers(ctx, 10); err != nil || len(rows) != 0 {
		t.Fatalf("deleted top users = %+v, err=%v", rows, err)
	}
	if rows, err := store.UsersByCommand(ctx, "stock_dividend", 10); err != nil || len(rows) != 0 {
		t.Fatalf("deleted users = %+v, err=%v", rows, err)
	}

	cur, err := rawStatsColl.Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	found := map[string]bool{}
	for cur.Next(ctx) {
		var doc struct {
			Name string `bson:"name"`
		}
		if err := cur.Decode(&doc); err != nil {
			t.Fatalf("decode index: %v", err)
		}
		found[doc.Name] = true
	}
	if err := cur.Err(); err != nil {
		t.Fatalf("index cursor: %v", err)
	}
	for _, name := range []string{statsCommandUsersIndexName, statsUserCommandsIndexName, statsUsernameLookupIndexName} {
		if !found[name] {
			t.Fatalf("missing index %s; indexes=%v", name, found)
		}
	}
}

func setupMongoStatsTest(t *testing.T) (context.Context, storage.Collection) {
	t.Helper()
	ctx, provider := setupMongoStatsProvider(t)
	return ctx, provider.Collection("stats")
}

func setupMongoStatsProvider(t *testing.T) (context.Context, storage.Provider) {
	t.Helper()

	uri := mongoTests.URI(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	client, err := storage.NewMongoClient(ctx, uri)
	if err != nil {
		t.Fatalf("NewMongoClient: %v", err)
	}
	dbName := fmt.Sprintf("tiennm99bot_stats_test_%d", time.Now().UnixNano())
	db := client.Database(dbName)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = db.Drop(cleanupCtx)
		_ = client.Disconnect(cleanupCtx)
	})

	return ctx, storage.NewMongoProvider(db)
}

func TestInc_MongoUsernameMoveClearsOldHolder(t *testing.T) {
	_, statsColl := setupMongoStatsTest(t)
	assertUsernameMoveClearsOldHolder(t, statsColl)
}

func TestInitStore_MongoSwapsNoituCommandStats(t *testing.T) {
	_, provider := setupMongoStatsProvider(t)
	assertNoituCommandSwap(t, provider)
}
