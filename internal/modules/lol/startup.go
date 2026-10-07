package lol

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	matchCacheTTLIndexName = "lol_match_cache_ttl"
	matchCacheTTLField     = "updatedAt"
	matchCacheKeyPrefix    = "matches:"
	matchCacheKeyUpper     = "matches;"
	matchCacheTTL          = 30 * 24 * time.Hour
)

// InitStore performs LoL collection startup maintenance. It is safe to call
// every boot: MongoDB indexes are created idempotently and memory storage is a
// no-op.
func InitStore(ctx context.Context, lolColl storage.Collection) error {
	if mongoColl, ok := storage.MongoCollection(lolColl); ok {
		if err := ensureMatchCacheTTLIndex(ctx, mongoColl); err != nil {
			return err
		}
	}
	return nil
}

func ensureMatchCacheTTLIndex(ctx context.Context, coll *mongo.Collection) error {
	model := mongo.IndexModel{
		Keys: bson.D{{Key: matchCacheTTLField, Value: 1}},
		Options: options.Index().
			SetName(matchCacheTTLIndexName).
			SetExpireAfterSeconds(int32(matchCacheTTL / time.Second)).
			SetPartialFilterExpression(bson.D{{Key: "_id", Value: matchCacheIDRange()}}),
	}
	if _, err := coll.Indexes().CreateOne(ctx, model); err != nil {
		return fmt.Errorf("lol match cache ttl index: %w", err)
	}
	return nil
}

func matchCacheIDRange() bson.D {
	return bson.D{
		{Key: "$gte", Value: matchCacheKeyPrefix},
		{Key: "$lt", Value: matchCacheKeyUpper},
	}
}
