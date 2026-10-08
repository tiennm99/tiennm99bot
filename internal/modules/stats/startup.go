package stats

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	statsCommandUsersIndexName   = "stats_cmd_n_user"
	statsUserCommandsIndexName   = "stats_uid_n_cmd"
	statsUsernameLookupIndexName = "stats_user_uid"
)

// InitStore creates the stats collection's MongoDB indexes. Index creation is
// idempotent, so it runs on every boot; the memory backend needs none.
func InitStore(ctx context.Context, statsColl storage.Collection) error {
	mongoColl, ok := storage.MongoCollection(statsColl)
	if !ok {
		return nil
	}
	return ensureUsageIndexes(ctx, mongoColl)
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
