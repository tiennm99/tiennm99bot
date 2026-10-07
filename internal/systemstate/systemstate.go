// Package systemstate stores app-level process metadata — chiefly completion
// markers for one-time startup tasks such as data migrations — in a shared
// "system" collection that belongs to no feature module.
package systemstate

import (
	"context"
	"errors"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// CollectionName is the app-level collection for startup tasks and other
// process metadata that does not belong to a feature module.
const CollectionName = "system"

// Record is intentionally small and generic. Stable keys carry the meaning;
// optional fields let startup tasks record counts and completion state.
type Record struct {
	Kind        string `json:"kind" bson:"kind"`
	Name        string `json:"name" bson:"name"`
	Status      string `json:"status,omitempty" bson:"status,omitempty"`
	Count       int64  `json:"count,omitempty" bson:"count,omitempty"`
	CompletedAt int64  `json:"completed_at,omitempty" bson:"completed_at,omitempty"`
	UpdatedAt   int64  `json:"updated_at" bson:"updated_at"`
}

// Store is a typed view over the system collection, keyed by a stable,
// caller-chosen string per task.
type Store struct {
	docs storage.DocStore[Record]
}

// New returns a Store over coll, normally the collection named CollectionName.
func New(coll storage.Collection) Store {
	return Store{docs: storage.Typed[Record](coll)}
}

// Get returns the record stored under key. A missing key is not an error: it
// reports false with a zero Record.
func (s Store) Get(ctx context.Context, key string) (Record, bool, error) {
	rec, _, err := s.docs.Get(ctx, key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return Record{}, false, nil
		}
		return Record{}, false, err
	}
	return rec, true, nil
}

// Put overwrites the record stored under key.
func (s Store) Put(ctx context.Context, key string, rec Record) error {
	return s.docs.Put(ctx, key, rec)
}
