package stock

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/systemstate"
	"github.com/tiennm99/tiennm99bot/internal/testutil/mongotest"
)

var stockMongoTests mongotest.Manager

func TestMain(m *testing.M) {
	os.Exit(stockMongoTests.Run(m))
}

func TestInitStorePhysicallyDropsLegacyDividendFieldsInMongoDB(t *testing.T) {
	ctx, portfolioColl, systemColl := setupMongoStockMigrationTest(t)
	docs := storage.Typed[legacyDividendPortfolio](portfolioColl)
	if err := docs.Put(ctx, "user:7", legacyDividendPortfolio{
		VND: 50_000,
		Assets: map[string]legacyDividendAssetPosition{
			"TCB": {Quantity: 100, Base: 3_000_000, DividendCheckedAt: legacyCursor(123), OpenedAt: 99},
		},
		Dividends: map[string]map[string]DividendRecord{
			"TCB": {"2612974": legacyPreservedDividend()},
		},
		AppliedDividendEvents: map[string]int64{"old-hash": 456},
		Meta:                  PortfolioMeta{Invested: 3_000_000, CreatedAt: 1},
	}); err != nil {
		t.Fatal(err)
	}

	if err := InitStore(ctx, portfolioColl, systemColl); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if err := InitStore(ctx, portfolioColl, systemColl); err != nil {
		t.Fatalf("second InitStore: %v", err)
	}

	rawColl, ok := storage.MongoCollection(portfolioColl)
	if !ok {
		t.Fatal("portfolio collection is not MongoDB-backed")
	}
	var raw bson.Raw
	if err := rawColl.FindOne(ctx, bson.M{"_id": "user:7"}).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.LookupErr("appliedDividendEvents"); err == nil {
		t.Fatalf("root legacy ledger remains in %v", raw)
	}
	position := raw.Lookup("assets").Document().Lookup("TCB").Document()
	if _, err := position.LookupErr("dividendCheckedAt"); err == nil {
		t.Fatalf("asset legacy cursor remains in %v", position)
	}
	if position.Lookup("openedAt").Int64() != 99 || raw.Lookup("vnd").Double() != 50_000 {
		t.Fatalf("current portfolio fields changed: %v", raw)
	}
	if _, err := raw.LookupErr("dividends"); err != nil {
		t.Fatalf("new dividend history was discarded: %v", raw)
	}
}

func setupMongoStockMigrationTest(t *testing.T) (context.Context, storage.Collection, storage.Collection) {
	t.Helper()
	uri := stockMongoTests.URI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	client, err := storage.NewMongoClient(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	db := client.Database(fmt.Sprintf("tiennm99bot_stock_migration_test_%d", time.Now().UnixNano()))
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = db.Drop(cleanupCtx)
		_ = client.Disconnect(cleanupCtx)
	})
	provider := storage.NewMongoProvider(db)
	return ctx, provider.Collection(CollectionName), provider.Collection(systemstate.CollectionName)
}
