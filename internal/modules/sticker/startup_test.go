package sticker

import (
	"context"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/systemstate"
)

// legacyShape stands in for the retired records: a pack keyed by owner ID, a
// "slug:" reservation, and a "pending-delete:" confirmation. Only the keys
// matter to the cleanup, so one loose shape covers all three.
type legacyShape struct {
	Slug    string `bson:"slug"`
	OwnerID int64  `bson:"ownerId"`
}

func seedLegacy(t *testing.T, coll storage.Collection, keys ...string) {
	t.Helper()
	docs := storage.Typed[legacyShape](coll)
	for _, k := range keys {
		if err := docs.Put(context.Background(), k, legacyShape{Slug: "old", OwnerID: 42}); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
}

func remainingKeys(t *testing.T, coll storage.Collection) []string {
	t.Helper()
	keys, err := storage.Typed[legacyShape](coll).List(context.Background(), "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return keys
}

// All three retired key spaces go, in one pass.
func TestInitStore_RemovesEveryLegacyKeySpace(t *testing.T) {
	provider := storage.NewMemoryProvider()
	stickerColl := provider.Collection(CollectionName)
	systemColl := provider.Collection(systemstate.CollectionName)

	seedLegacy(t, stickerColl,
		"123456789",              // pack record, keyed by owner ID
		"987654321",              // another owner's pack
		"slug:mypack",            // name reservation
		"pending-delete:1234567", // /delpack confirmation
	)

	if err := InitStore(context.Background(), stickerColl, systemColl); err != nil {
		t.Fatalf("InitStore: %v", err)
	}

	if got := remainingKeys(t, stickerColl); len(got) != 0 {
		t.Errorf("collection still holds %v, want it emptied", got)
	}
}

// The marker records how many were removed, so the count is auditable after
// the fact.
func TestInitStore_MarksCompletionWithCount(t *testing.T) {
	provider := storage.NewMemoryProvider()
	stickerColl := provider.Collection(CollectionName)
	systemColl := provider.Collection(systemstate.CollectionName)
	seedLegacy(t, stickerColl, "1", "2", "slug:x")

	if err := InitStore(context.Background(), stickerColl, systemColl); err != nil {
		t.Fatalf("InitStore: %v", err)
	}

	rec, found, err := systemstate.New(systemColl).Get(context.Background(), legacyPackCleanupMarkerKey)
	if err != nil || !found {
		t.Fatalf("marker: found=%v err=%v", found, err)
	}
	if rec.Status != "completed" {
		t.Errorf("status = %q, want completed", rec.Status)
	}
	if rec.Count != 3 {
		t.Errorf("count = %d, want 3", rec.Count)
	}
	if rec.CompletedAt == 0 || rec.UpdatedAt == 0 {
		t.Errorf("timestamps unset: %+v", rec)
	}
}

// Once marked, the scan must not run again — and specifically must not delete
// anything written to this collection later.
func TestInitStore_MarkerStopsASecondPass(t *testing.T) {
	provider := storage.NewMemoryProvider()
	stickerColl := provider.Collection(CollectionName)
	systemColl := provider.Collection(systemstate.CollectionName)
	seedLegacy(t, stickerColl, "1")

	if err := InitStore(context.Background(), stickerColl, systemColl); err != nil {
		t.Fatalf("first InitStore: %v", err)
	}

	// Whatever a future version of this module might store.
	seedLegacy(t, stickerColl, "something-new")

	if err := InitStore(context.Background(), stickerColl, systemColl); err != nil {
		t.Fatalf("second InitStore: %v", err)
	}
	got := remainingKeys(t, stickerColl)
	if len(got) != 1 || got[0] != "something-new" {
		t.Errorf("remaining = %v, want only the newly written key", got)
	}
}

// An already-clean database is the normal case on a fresh deploy, and on the
// memory backend where the collection never held anything.
func TestInitStore_EmptyCollectionIsFine(t *testing.T) {
	provider := storage.NewMemoryProvider()
	stickerColl := provider.Collection(CollectionName)
	systemColl := provider.Collection(systemstate.CollectionName)

	if err := InitStore(context.Background(), stickerColl, systemColl); err != nil {
		t.Fatalf("InitStore on an empty collection: %v", err)
	}

	rec, found, err := systemstate.New(systemColl).Get(context.Background(), legacyPackCleanupMarkerKey)
	if err != nil || !found {
		t.Fatalf("marker: found=%v err=%v", found, err)
	}
	if rec.Count != 0 {
		t.Errorf("count = %d, want 0", rec.Count)
	}
}

// The module itself must keep using none of this: if a future edit gives the
// factory a store, the cleanup above would start deleting live data.
func TestNew_UsesNoStorage(t *testing.T) {
	provider := storage.NewMemoryProvider()
	coll := provider.Collection(CollectionName)
	seedLegacy(t, coll, "sentinel")

	mod := New(modules.Deps{Store: coll})
	if len(mod.Commands) != 1 || mod.Commands[0].Name != "addsticker" {
		t.Fatalf("module commands = %+v, want only /addsticker", mod.Commands)
	}
	if got := remainingKeys(t, coll); len(got) != 1 {
		t.Errorf("factory touched storage; remaining = %v", got)
	}
}
