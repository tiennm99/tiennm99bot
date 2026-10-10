package guessgame

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/systemstate"
)

// RunOnce runs a one-time startup migration guarded by a marker under key
// in the system collection: a completed marker makes it a no-op, and the
// marker is written only once migrate succeeds, recording how many
// documents it wrote. migrate must itself be safe to run again, since a
// failure before the marker reruns it on the next boot.
func RunOnce(ctx context.Context, systemColl storage.Collection, key string, migrate func() (int, error)) error {
	state := systemstate.New(systemColl)
	if rec, ok, err := state.Get(ctx, key); err != nil {
		return fmt.Errorf("migration marker %s: %w", key, err)
	} else if ok && rec.Status == "complete" {
		return nil
	}
	n, err := migrate()
	if err != nil {
		return err
	}
	now := time.Now().UTC().UnixMilli()
	if err := state.Put(ctx, key, systemstate.Record{
		Kind:        "migration",
		Name:        key,
		Status:      "complete",
		Count:       int64(n),
		CompletedAt: now,
		UpdatedAt:   now,
	}); err != nil {
		return fmt.Errorf("migration marker put %s: %w", key, err)
	}
	return nil
}

// LegacyUserID reads the subject of a legacy in-chat game key
// "<prefix><subject>". Those games keyed a private chat by the user id and
// a group by its (negative) chat id, a round the whole group shared; only a
// positive subject is a player.
func LegacyUserID(key, prefix string) (int64, bool) {
	uid, err := strconv.ParseInt(strings.TrimPrefix(key, prefix), 10, 64)
	return uid, err == nil && uid > 0
}

// PutIfMissing writes v under key unless a document is already there, and
// reports whether it wrote.
func PutIfMissing[T any](ctx context.Context, store storage.DocStore[T], key string, v T) (bool, error) {
	_, _, err := store.Get(ctx, key)
	switch {
	case err == nil:
		return false, nil
	case !errors.Is(err, storage.ErrNotFound):
		return false, err
	}
	if err := store.PutVersioned(ctx, key, 0, v); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
