package wordledaily

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil/mongotest"
)

var mongoTests mongotest.Manager

func TestMain(m *testing.M) {
	os.Exit(mongoTests.Run(m))
}

// The migration runs against MongoDB in production, where documents go
// through BSON rather than JSON.
func TestInitStore_MongoCarriesLegacyWordlePlayersOnce(t *testing.T) {
	uri := mongoTests.URI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	client, err := storage.NewMongoClient(ctx, uri)
	if err != nil {
		t.Fatalf("NewMongoClient: %v", err)
	}
	db := client.Database(fmt.Sprintf("tiennm99bot_wordledaily_test_%d", time.Now().UnixNano()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = db.Drop(ctx)
		_ = client.Disconnect(ctx)
	})
	assertLegacyMigration(t, storage.NewMongoProvider(db))
}
