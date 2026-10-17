package stats

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/systemstate"
)

const (
	statsCommandUsersIndexName   = "stats_cmd_n_user"
	statsUserCommandsIndexName   = "stats_uid_n_cmd"
	statsUsernameLookupIndexName = "stats_user_uid"
)

// commandRename moves one command's usage history to its new name. Each
// rename has its own completion marker in the system collection.
type commandRename struct {
	oldCmd, newCmd, markerKey string
}

// commandRenames run in order, each at most once. The noitu renames swap two
// names, so /noitu (the solo game, now /noitubot) must move out before
// /noitupvp (the group game, now /noitu) moves in; the reverse order would
// merge both histories into noitubot.
var commandRenames = []commandRename{
	{oldCmd: "noitu", newCmd: "noitubot", markerKey: "stats:command-rename:noitu-to-noitubot"},
	{oldCmd: "noitupvp", newCmd: "noitu", markerKey: "stats:command-rename:noitupvp-to-noitu"},
}

// commandRetirement marks every usage row of the listed commands deleted
// once, recorded under markerKey in the system collection.
type commandRetirement struct {
	cmds      []string
	markerKey string
}

// commandRetirements run in order after the renames, each at most once. The
// commands must also be in retiredCommands, which keeps any row the marker
// missed out of the views.
var commandRetirements = []commandRetirement{
	{cmds: []string{"monkeyd_crawl", "monkeyd_tags"}, markerKey: "migration:stats-delete-monkeyd-v1"},
}

// deletedCommandMigrationRetries bounds the optimistic-lock retries when one
// row is marked deleted on the document store.
const deletedCommandMigrationRetries = 5

// InitStore performs stats collection startup maintenance. It is safe to call
// every boot: MongoDB indexes are created idempotently, and one-time command
// rename and retirement migrations are guarded by markers in the shared
// system collection.
func InitStore(ctx context.Context, statsColl, systemColl storage.Collection) error {
	if mongoColl, ok := storage.MongoCollection(statsColl); ok {
		if err := ensureUsageIndexes(ctx, mongoColl); err != nil {
			return err
		}
	}
	for _, r := range commandRenames {
		if err := migrateCommandRename(ctx, statsColl, systemColl, r); err != nil {
			return err
		}
	}
	for _, r := range commandRetirements {
		if err := markDeletedCommands(ctx, statsColl, systemColl, r); err != nil {
			return err
		}
	}
	return nil
}

// markDeletedCommands sets deleted: true on every usage row of r.cmds, the
// anonymous totals and the per-user rows, and records completion with the
// number of rows it marked. The rows themselves are kept as history. A
// completed marker makes it a no-op.
func markDeletedCommands(ctx context.Context, statsColl, systemColl storage.Collection, r commandRetirement) error {
	state := systemstate.New(systemColl)
	if rec, ok, err := state.Get(ctx, r.markerKey); err != nil {
		return fmt.Errorf("stats command retire marker %s: %w", r.markerKey, err)
	} else if ok && rec.Status == "complete" {
		return nil
	}

	var marked int64
	for _, cmd := range r.cmds {
		var (
			n   int64
			err error
		)
		if mongoColl, ok := storage.MongoCollection(statsColl); ok {
			n, err = markMongoUsageEntriesDeleted(ctx, mongoColl, cmd)
		} else {
			n, err = markDocUsageEntriesDeleted(ctx, storage.Typed[usageEntry](statsColl), cmd)
		}
		if err != nil {
			return err
		}
		marked += n
	}

	now := time.Now().UTC().UnixMilli()
	if err := state.Put(ctx, r.markerKey, systemstate.Record{
		Kind:        "migration",
		Name:        r.markerKey,
		Status:      "complete",
		Count:       marked,
		CompletedAt: now,
		UpdatedAt:   now,
	}); err != nil {
		return fmt.Errorf("stats command retire marker put %s: %w", r.markerKey, err)
	}
	return nil
}

func markMongoUsageEntriesDeleted(ctx context.Context, coll *mongo.Collection, cmd string) (int64, error) {
	res, err := coll.UpdateMany(ctx,
		bson.M{"cmd": cmd, "deleted": bson.M{"$ne": true}},
		bson.M{
			"$set":         bson.M{"deleted": true},
			"$inc":         bson.M{"version": int64(1)},
			"$currentDate": bson.M{"updatedAt": true},
		},
	)
	if err != nil {
		return 0, fmt.Errorf("stats command retire update %s: %w", cmd, err)
	}
	return res.ModifiedCount, nil
}

func markDocUsageEntriesDeleted(ctx context.Context, docs storage.DocStore[usageEntry], cmd string) (int64, error) {
	keys, err := docs.List(ctx, cmd)
	if err != nil {
		return 0, fmt.Errorf("stats command retire list %s: %w", cmd, err)
	}
	var marked int64
	for _, key := range keys {
		// The key prefix also matches longer command names.
		if key != cmd && !strings.HasPrefix(key, cmd+":") {
			continue
		}
		changed, err := markUsageEntryDeleted(ctx, docs, key, cmd)
		if err != nil {
			return 0, err
		}
		if changed {
			marked++
		}
	}
	return marked, nil
}

// markUsageEntryDeleted sets the deleted flag on one row with a versioned
// write, retrying when a concurrent increment wins the race. It reports
// whether the row changed.
func markUsageEntryDeleted(ctx context.Context, docs storage.DocStore[usageEntry], key, cmd string) (bool, error) {
	for range deletedCommandMigrationRetries {
		entry, version, err := docs.Get(ctx, key)
		if errors.Is(err, storage.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("stats command retire get %s: %w", key, err)
		}
		if entry.Cmd != cmd || entry.Deleted {
			return false, nil
		}
		entry.Deleted = true
		err = docs.PutVersioned(ctx, key, version, entry)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, storage.ErrConflict) {
			return false, fmt.Errorf("stats command retire put %s: %w", key, err)
		}
	}
	return false, fmt.Errorf("stats command retire put %s: %w", key, storage.ErrConflict)
}

// migrateCommandRename merges every usage row of r.oldCmd, the anonymous
// total and the per-user rows, into the matching r.newCmd row, deletes the
// old row, and records completion. A completed marker makes it a no-op, so a
// later rename that reuses r.oldCmd as its new name is never moved again.
func migrateCommandRename(ctx context.Context, statsColl, systemColl storage.Collection, r commandRename) error {
	state := systemstate.New(systemColl)
	if rec, ok, err := state.Get(ctx, r.markerKey); err != nil {
		return fmt.Errorf("stats command rename marker %s: %w", r.markerKey, err)
	} else if ok && rec.Status == "complete" {
		return nil
	}

	docs := storage.Typed[usageEntry](statsColl)
	keys, err := docs.List(ctx, r.oldCmd)
	if err != nil {
		return fmt.Errorf("stats command rename list %s: %w", r.oldCmd, err)
	}

	var moved int64
	for _, key := range keys {
		// The key prefix also matches longer command names.
		if key != r.oldCmd && !strings.HasPrefix(key, r.oldCmd+":") {
			continue
		}
		entry, _, err := docs.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("stats command rename get %s: %w", key, err)
		}
		if entry.Cmd != r.oldCmd {
			continue
		}

		targetKey := usageKey(r.newCmd, entry.UserID)
		target, _, err := docs.Get(ctx, targetKey)
		missingTarget := errors.Is(err, storage.ErrNotFound)
		if err != nil && !missingTarget {
			return fmt.Errorf("stats command rename get target %s: %w", targetKey, err)
		}
		if missingTarget {
			target = usageEntry{Deleted: entry.Deleted}
		}
		target.Cmd = r.newCmd
		target.UserID = entry.UserID
		if entry.UserID == 0 {
			target.Username = ""
		} else if entry.Username != "" {
			target.Username = entry.Username
		}
		target.N += entry.N
		if !entry.Deleted {
			target.Deleted = false
		}
		if err := docs.Put(ctx, targetKey, target); err != nil {
			return fmt.Errorf("stats command rename put target %s: %w", targetKey, err)
		}
		if err := docs.Delete(ctx, key); err != nil {
			return fmt.Errorf("stats command rename delete old %s: %w", key, err)
		}
		moved += entry.N
	}

	now := time.Now().UTC().UnixMilli()
	if err := state.Put(ctx, r.markerKey, systemstate.Record{
		Kind:        "migration",
		Name:        r.markerKey,
		Status:      "complete",
		Count:       moved,
		CompletedAt: now,
		UpdatedAt:   now,
	}); err != nil {
		return fmt.Errorf("stats command rename marker put %s: %w", r.markerKey, err)
	}
	return nil
}

func ensureUsageIndexes(ctx context.Context, coll *mongo.Collection) error {
	models := []mongo.IndexModel{
		{
			Keys:    bson.D{bsonField("cmd", 1), bsonField("n", -1), bsonField("user", 1)},
			Options: options.Index().SetName(statsCommandUsersIndexName),
		},
		{
			Keys:    bson.D{bsonField("uid", 1), bsonField("n", -1), bsonField("cmd", 1)},
			Options: options.Index().SetName(statsUserCommandsIndexName),
		},
		{
			Keys:    bson.D{bsonField("user", 1), bsonField("uid", 1)},
			Options: options.Index().SetName(statsUsernameLookupIndexName),
		},
	}
	if _, err := coll.Indexes().CreateMany(ctx, models); err != nil {
		return fmt.Errorf("stats indexes: %w", err)
	}
	return nil
}
