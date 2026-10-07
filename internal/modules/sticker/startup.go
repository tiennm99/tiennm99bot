package sticker

import (
	"context"
	"fmt"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/systemstate"
)

const legacyPackCleanupMarkerKey = "migration:sticker-drop-legacy-packs-v1"

// legacyRecord is a placeholder type, not a schema.
//
// The retired design wrote three different shapes into this collection — pack
// records keyed by owner ID, "slug:" name reservations, and "pending-delete:"
// confirmations. Cleanup only lists keys and deletes them, and neither
// operation decodes a document, so one empty type serves for all three rather
// than resurrecting structs whose only remaining purpose would be deletion.
type legacyRecord struct{}

// InitStore removes the per-user sticker pack records the retired pack
// commands left behind.
//
// The module no longer stores anything: /addsticker writes to one shared,
// env-configured set and takes the set owner's user ID from OWNER_ID, so there
// is nothing per-user to key. Its factory ignores the collection entirely.
// These documents are therefore unreachable by any code path — not stale data
// that some handler might still read, but orphans.
//
// Guarded by a completion marker so the scan runs once per database rather than
// on every boot, matching the stock and stats migrations. Safe to run against a
// collection that is already empty, and safe on the memory backend where the
// collection never had anything in it.
func InitStore(ctx context.Context, stickerColl, systemColl storage.Collection) error {
	system := systemstate.New(systemColl)
	marker, exists, err := system.Get(ctx, legacyPackCleanupMarkerKey)
	if err != nil {
		return fmt.Errorf("sticker legacy pack cleanup: read marker: %w", err)
	}
	if exists && marker.Status == "completed" {
		return nil
	}

	docs := storage.Typed[legacyRecord](stickerColl)
	// Empty prefix: the retired design used three disjoint key spaces (bare
	// owner IDs, "slug:", "pending-delete:") and all of them are dead, so
	// listing everything is both correct and cheaper than three scans.
	keys, err := docs.List(ctx, "")
	if err != nil {
		return fmt.Errorf("sticker legacy pack cleanup: list records: %w", err)
	}

	var deleted int64
	for _, key := range keys {
		if err := docs.Delete(ctx, key); err != nil {
			// Abort without writing the marker, so the next boot retries the
			// rest. Deletes are idempotent, so a partial run is safe to repeat.
			return fmt.Errorf("sticker legacy pack cleanup: delete %s: %w", key, err)
		}
		deleted++
	}
	if deleted > 0 {
		log.Info("sticker legacy pack records removed", "count", deleted)
	}

	marker = completedLegacyPackCleanup(marker, exists, deleted, time.Now().UnixMilli())
	if err := system.Put(ctx, legacyPackCleanupMarkerKey, marker); err != nil {
		return fmt.Errorf("sticker legacy pack cleanup: write marker: %w", err)
	}
	return nil
}

func completedLegacyPackCleanup(marker systemstate.Record, exists bool, deleted, now int64) systemstate.Record {
	if !exists {
		marker = systemstate.Record{
			Kind: "migration",
			Name: "sticker drop legacy packs v1",
		}
	}
	if marker.CompletedAt == 0 {
		marker.CompletedAt = now
	}
	marker.Status = "completed"
	marker.Count += deleted
	marker.UpdatedAt = now
	return marker
}
