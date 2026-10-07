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
	ctx, statsColl, systemColl := setupMongoStatsTest(t)

	rawStatsColl, ok := storage.MongoCollection(statsColl)
	if !ok {
		t.Fatal("stats collection is not Mongo-backed")
	}

	docs := storage.Typed[usageEntry](statsColl)
	if err := docs.Put(ctx, "stock_dividend", usageEntry{Cmd: "stock_dividend", N: 9}); err != nil {
		t.Fatalf("seed anonymous stats: %v", err)
	}
	if err := docs.Put(ctx, "stock_dividend:7", usageEntry{Cmd: "stock_dividend", UserID: 7, Username: "alice", N: 4}); err != nil {
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
	for _, key := range []string{"stock_dividend", "stock_dividend:7"} {
		entry, _, err := docs.Get(ctx, key)
		if err != nil || !entry.Deleted {
			t.Fatalf("retained %s = %+v, err=%v", key, entry, err)
		}
	}
	prefixEntry, _, err := docs.Get(ctx, "stock_dividend_extra")
	if err != nil || prefixEntry.Deleted {
		t.Fatalf("prefix entry = %+v, err=%v", prefixEntry, err)
	}
	marker, exists, err := systemstate.New(systemColl).Get(ctx, deletedStockDividendMarkerKey)
	if err != nil || !exists || marker.Status != "completed" || marker.Count != 2 {
		t.Fatalf("marker=%+v exists=%v err=%v", marker, exists, err)
	}

	legacy, _, err := docs.Get(ctx, "stock_dividend:7")
	if err != nil {
		t.Fatal(err)
	}
	legacy.Deleted = false
	legacy.N++
	if err := docs.Put(ctx, "stock_dividend:7", legacy); err != nil {
		t.Fatal(err)
	}
	store := newUsageStore(statsColl)
	if err := store.Increment(ctx, "stock_dividend", usageUser{ID: 7, Username: "alice"}, true); err != nil {
		t.Fatalf("retired Increment: %v", err)
	}
	if rows, err := store.TopCommands(ctx, 10); err != nil || len(rows) != 1 || rows[0].display != "/stock_dividend_extra" || rows[0].n != 3 {
		t.Fatalf("top commands after legacy write = %+v, err=%v", rows, err)
	}
	if rows, err := store.TopUsers(ctx, 10); err != nil || len(rows) != 0 {
		t.Fatalf("retired top users = %+v, err=%v", rows, err)
	}
	if rows, err := store.UsersByCommand(ctx, "stock_dividend", 10); err != nil || len(rows) != 0 {
		t.Fatalf("retired users = %+v, err=%v", rows, err)
	}
	if err := InitStore(ctx, statsColl, systemColl); err != nil {
		t.Fatalf("InitStore reconciliation: %v", err)
	}
	legacy, _, err = docs.Get(ctx, "stock_dividend:7")
	if err != nil || !legacy.Deleted || legacy.N != 5 {
		t.Fatalf("reconciled legacy entry = %+v, err=%v", legacy, err)
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

func setupMongoStatsTest(t *testing.T) (context.Context, storage.Collection, storage.Collection) {
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

	provider := storage.NewMongoProvider(db)
	return ctx, provider.Collection("stats"), provider.Collection(systemstate.CollectionName)
}

func TestInc_MongoUsernameMoveClearsOldHolder(t *testing.T) {
	_, statsColl, _ := setupMongoStatsTest(t)
	assertUsernameMoveClearsOldHolder(t, statsColl)
}
