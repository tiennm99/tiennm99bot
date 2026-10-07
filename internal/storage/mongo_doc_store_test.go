package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/tiennm99/tiennm99bot/internal/testutil/mongotest"
)

var mongoTests mongotest.Manager

func TestMain(m *testing.M) {
	os.Exit(mongoTests.Run(m))
}

// mongoLocalSetup connects to the shared test MongoDB and returns a fresh,
// uniquely named database plus cleanup.
func mongoLocalSetup(t *testing.T) (*mongo.Database, func()) {
	t.Helper()
	uri := mongoTests.URI(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := NewMongoClient(ctx, uri)
	if err != nil {
		t.Fatalf("NewMongoClient: %v", err)
	}
	dbName := fmt.Sprintf("tiennm99bot_test_%d", time.Now().UnixNano())
	if len(dbName) > 63 {
		dbName = dbName[:63]
	}
	db := client.Database(dbName)

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = db.Drop(ctx)
		_ = client.Disconnect(ctx)
	}
	return db, cleanup
}

type portfolioLike struct {
	USD    float64            `json:"usd" bson:"usd"`
	Assets map[string]float64 `json:"assets" bson:"assets"`
}

func mongoStore[T any](t *testing.T, module string) (DocStore[T], *mongo.Collection, func()) {
	t.Helper()
	db, cleanup := mongoLocalSetup(t)
	coll := db.Collection(module)
	return Typed[T](NewMongoProvider(db).Collection(module)), coll, cleanup
}

// rawDoc fetches the on-disk BSON document for assertions about its shape.
func rawDoc(t *testing.T, coll *mongo.Collection, id string) bson.M {
	t.Helper()
	var doc bson.M
	if err := coll.FindOne(context.Background(), bson.M{"_id": id}).Decode(&doc); err != nil {
		t.Fatalf("raw FindOne %s: %v", id, err)
	}
	return doc
}

func TestMongoDocStore_RootShape(t *testing.T) {
	store, coll, cleanup := mongoStore[portfolioLike](t, "coin")
	defer cleanup()
	ctx := context.Background()

	if err := store.Put(ctx, "user:7", portfolioLike{USD: 1000.25, Assets: map[string]float64{"BTC": 1}}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	doc := rawDoc(t, coll, "user:7")

	if _, ok := doc["value"]; ok {
		t.Error("doc has legacy 'value' envelope field")
	}
	if _, ok := doc["_payload"]; ok {
		t.Error("doc has '_payload' fallback field")
	}
	if _, ok := doc["usd"]; !ok {
		t.Error("payload field 'usd' not hoisted to root")
	}
	if _, ok := doc["assets"]; !ok {
		t.Error("payload field 'assets' not hoisted to root")
	}
	if _, ok := doc["updatedAt"].(bson.DateTime); !ok {
		t.Errorf("updatedAt is %T, want BSON Date", doc["updatedAt"])
	}

	got, version, err := store.Get(ctx, "user:7")
	if err != nil || version != 1 || got.USD != 1000.25 || got.Assets["BTC"] != 1 {
		t.Fatalf("Get round-trip: %+v v=%d err=%v", got, version, err)
	}
}

func TestMongoDocStore_OverwriteRemovesStaleFields(t *testing.T) {
	store, coll, cleanup := mongoStore[map[string]int](t, "misc")
	defer cleanup()
	ctx := context.Background()

	if err := store.Put(ctx, "k", map[string]int{"a": 1, "b": 2}); err != nil {
		t.Fatalf("Put A: %v", err)
	}
	if err := store.Put(ctx, "k", map[string]int{"a": 3}); err != nil {
		t.Fatalf("Put B: %v", err)
	}
	doc := rawDoc(t, coll, "k")
	if _, ok := doc["b"]; ok {
		t.Error("stale field 'b' survived overwrite")
	}
	if doc["a"] != int32(3) && doc["a"] != int64(3) {
		t.Errorf("a = %v (%T), want 3", doc["a"], doc["a"])
	}
}

func TestMongoDocStore_PutVersionedConcurrentCreate(t *testing.T) {
	store, _, cleanup := mongoStore[portfolioLike](t, "coin")
	defer cleanup()
	ctx := context.Background()

	const n = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if err := store.PutVersioned(ctx, "race", 0, portfolioLike{USD: 1}); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("unexpected err: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("concurrent create winners = %d, want exactly 1", wins)
	}
}

func TestMongoDocStore_PutVersionedStaleConflict(t *testing.T) {
	store, _, cleanup := mongoStore[portfolioLike](t, "coin")
	defer cleanup()
	ctx := context.Background()

	_ = store.PutVersioned(ctx, "k", 0, portfolioLike{USD: 1})
	if err := store.PutVersioned(ctx, "k", 99, portfolioLike{USD: 2}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS = %v, want ErrConflict", err)
	}
	if err := store.PutVersioned(ctx, "k", 1, portfolioLike{USD: 2}); err != nil {
		t.Fatalf("fresh CAS: %v", err)
	}
}

// wrappedScalar / wrappedArray prove non-object values become named root fields
// (the lol pattern) rather than an envelope or fallback.
type wrappedScalar struct {
	Date string `json:"date" bson:"date"`
}
type wrappedArray struct {
	Subscribers []int `json:"subscribers" bson:"subscribers"`
}

func TestMongoDocStore_WrappedScalarAndArray(t *testing.T) {
	db, cleanup := mongoLocalSetup(t)
	defer cleanup()
	ctx := context.Background()
	p := NewMongoProvider(db)

	scalar := Typed[wrappedScalar](p.Collection("lol"))
	if err := scalar.Put(ctx, "last_push_date", wrappedScalar{Date: "2026-06-28"}); err != nil {
		t.Fatalf("scalar Put: %v", err)
	}
	doc := rawDoc(t, db.Collection("lol"), "last_push_date")
	if doc["date"] != "2026-06-28" {
		t.Errorf("scalar root field date = %v", doc["date"])
	}

	arr := Typed[wrappedArray](p.Collection("lol"))
	if err := arr.Put(ctx, "subscribers", wrappedArray{Subscribers: []int{1, 2, 3}}); err != nil {
		t.Fatalf("array Put: %v", err)
	}
	doc = rawDoc(t, db.Collection("lol"), "subscribers")
	if _, ok := doc["subscribers"].(bson.A); !ok {
		t.Errorf("array root field subscribers = %T, want bson.A", doc["subscribers"])
	}
}

// Scan reads a whole prefix in one query, sorted by _id, with the payload
// fields decoded from the document root.
func TestMongoDocStore_ScanReturnsPayloadsInKeyOrder(t *testing.T) {
	store, _, cleanup := mongoStore[portfolioLike](t, "coin")
	defer cleanup()
	ctx := context.Background()

	if err := store.Put(ctx, "user:2", portfolioLike{USD: 2}); err != nil {
		t.Fatalf("Put user:2: %v", err)
	}
	if err := store.Put(ctx, "user:1", portfolioLike{USD: 1}); err != nil {
		t.Fatalf("Put user:1: %v", err)
	}
	if err := store.Put(ctx, "other", portfolioLike{USD: 9}); err != nil {
		t.Fatalf("Put other: %v", err)
	}

	docs, err := store.Scan(ctx, "user:")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("Scan user: returned %d docs, want 2: %+v", len(docs), docs)
	}
	if docs[0].ID != "user:1" || docs[1].ID != "user:2" {
		t.Errorf("Scan out of key order: %s, %s", docs[0].ID, docs[1].ID)
	}
	if docs[0].Val.USD != 1 || docs[1].Val.USD != 2 {
		t.Errorf("Scan lost hoisted payload fields: %+v", docs)
	}

	all, err := store.Scan(ctx, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("Scan all = %d docs, err %v", len(all), err)
	}
}

// List returns keys in key order, like the memory store, so callers behave the
// same on both backends. Insertion order is deliberately reversed here.
func TestMongoDocStore_ListReturnsKeysInKeyOrder(t *testing.T) {
	store, _, cleanup := mongoStore[portfolioLike](t, "coin")
	defer cleanup()
	ctx := context.Background()

	for _, key := range []string{"user:3", "user:1", "other", "user:2"} {
		if err := store.Put(ctx, key, portfolioLike{}); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}

	keys, err := store.List(ctx, "user:")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := []string{"user:1", "user:2", "user:3"}; !slices.Equal(keys, want) {
		t.Errorf("List = %v, want %v", keys, want)
	}
}
