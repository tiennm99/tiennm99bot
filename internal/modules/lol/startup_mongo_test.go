package lol

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil/mongotest"
)

var mongoTests mongotest.Manager

func TestMain(m *testing.M) {
	os.Exit(mongoTests.Run(m))
}

func TestInitStore_MongoCreatesMatchCacheTTLIndex(t *testing.T) {
	uri := mongoTests.URI(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := storage.NewMongoClient(ctx, uri)
	if err != nil {
		t.Fatalf("NewMongoClient: %v", err)
	}
	dbName := fmt.Sprintf("tiennm99bot_lol_test_%d", time.Now().UnixNano())
	db := client.Database(dbName)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = db.Drop(cleanupCtx)
		_ = client.Disconnect(cleanupCtx)
	}()

	provider := storage.NewMongoProvider(db)
	lolColl := provider.Collection(CollectionName)
	rawLolColl, ok := storage.MongoCollection(lolColl)
	if !ok {
		t.Fatal("lol collection is not Mongo-backed")
	}

	if err := InitStore(ctx, lolColl); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if err := InitStore(ctx, lolColl); err != nil {
		t.Fatalf("InitStore second run: %v", err)
	}

	cur, err := rawLolColl.Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	var found bson.M
	for cur.Next(ctx) {
		var doc bson.M
		if err := cur.Decode(&doc); err != nil {
			t.Fatalf("decode index: %v", err)
		}
		if doc["name"] == matchCacheTTLIndexName {
			found = doc
			break
		}
	}
	if err := cur.Err(); err != nil {
		t.Fatalf("index cursor: %v", err)
	}
	if found == nil {
		t.Fatalf("missing index %s", matchCacheTTLIndexName)
	}
	if got, ok := int64FromBSON(found["expireAfterSeconds"]); !ok || got != int64(matchCacheTTL/time.Second) {
		t.Fatalf("expireAfterSeconds = %v, want %d", found["expireAfterSeconds"], int64(matchCacheTTL/time.Second))
	}

	keyDoc, ok := bsonDoc(found["key"])
	if !ok {
		t.Fatalf("index key is not a document: %#v", found["key"])
	}
	if got, _ := int64FromBSON(keyDoc[matchCacheTTLField]); got != 1 {
		t.Fatalf("index key %s = %v, want 1", matchCacheTTLField, keyDoc[matchCacheTTLField])
	}

	partial, ok := bsonDoc(found["partialFilterExpression"])
	if !ok {
		t.Fatalf("partialFilterExpression is not a document: %#v", found["partialFilterExpression"])
	}
	idFilter, ok := bsonDoc(partial["_id"])
	if !ok {
		t.Fatalf("partial _id filter is not a document: %#v", partial["_id"])
	}
	if idFilter["$gte"] != matchCacheKeyPrefix || idFilter["$lt"] != matchCacheKeyUpper {
		t.Fatalf("partial _id filter = %#v, want [%q, %q)", idFilter, matchCacheKeyPrefix, matchCacheKeyUpper)
	}
}

func bsonDoc(v any) (bson.M, bool) {
	switch d := v.(type) {
	case bson.M:
		return d, true
	case map[string]any:
		return bson.M(d), true
	case bson.D:
		out := bson.M{}
		for _, e := range d {
			out[e.Key] = e.Value
		}
		return out, true
	default:
		return nil, false
	}
}

func int64FromBSON(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	default:
		return 0, false
	}
}
