package stock

import (
	"context"
	"errors"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/systemstate"
)

func legacyCursor(value int64) *int64 { return &value }

func legacyPreservedDividend() DividendRecord {
	return DividendRecord{
		Kind:        DividendKindCash,
		PublishedAt: 1,
		RecordDate:  2,
		VNDPerShare: 1_500,
		Title:       "Cash dividend",
		SourceURL:   "https://example.test/event/2612974",
	}
}

func TestInitStoreRemovesLegacyDividendFields(t *testing.T) {
	ctx := context.Background()
	provider := storage.NewMemoryProvider()
	portfolioColl := provider.Collection(CollectionName)
	systemColl := provider.Collection(systemstate.CollectionName)
	docs := storage.Typed[legacyDividendPortfolio](portfolioColl)

	legacy := legacyDividendPortfolio{
		VND: 50_000,
		Assets: map[string]legacyDividendAssetPosition{
			"TCB": {Quantity: 100, Base: 3_000_000, DividendCheckedAt: legacyCursor(123), OpenedAt: 99},
		},
		Dividends: map[string]map[string]DividendRecord{
			"TCB": {"2612974": legacyPreservedDividend()},
		},
		AppliedDividendEvents: map[string]int64{"old-hash": 456},
		Meta:                  PortfolioMeta{Invested: 3_000_000, CreatedAt: 1},
	}
	if err := docs.Put(ctx, "user:7", legacy); err != nil {
		t.Fatal(err)
	}
	if err := docs.Put(ctx, "pending-dividend:leave-me", legacy); err != nil {
		t.Fatal(err)
	}

	if err := InitStore(ctx, portfolioColl, systemColl); err != nil {
		t.Fatalf("InitStore: %v", err)
	}

	got, _, err := docs.Get(ctx, "user:7")
	if err != nil {
		t.Fatal(err)
	}
	if got.AppliedDividendEvents != nil || got.Assets["TCB"].DividendCheckedAt != nil {
		t.Fatalf("legacy fields remain: %+v", got)
	}
	if got.VND != legacy.VND || got.Assets["TCB"].Quantity != 100 || got.Assets["TCB"].Base != 3_000_000 || got.Assets["TCB"].OpenedAt != 99 || got.Meta != legacy.Meta {
		t.Fatalf("portfolio data changed: got=%+v want=%+v", got, legacy)
	}
	if event, ok := got.Dividends["TCB"]["2612974"]; !ok || event != legacyPreservedDividend() {
		t.Fatalf("dividend history was discarded: %+v", got.Dividends)
	}

	pending, _, err := docs.Get(ctx, "pending-dividend:leave-me")
	if err != nil {
		t.Fatal(err)
	}
	if pending.AppliedDividendEvents == nil || pending.Assets["TCB"].DividendCheckedAt == nil {
		t.Fatalf("non-user document was rewritten: %+v", pending)
	}

	marker, exists, err := systemstate.New(systemColl).Get(ctx, dividendHistoryMigrationMarkerKey)
	if err != nil || !exists || marker.Status != "completed" || marker.Count != 1 {
		t.Fatalf("marker=%+v exists=%v err=%v", marker, exists, err)
	}

	if err := InitStore(ctx, portfolioColl, systemColl); err != nil {
		t.Fatalf("second InitStore: %v", err)
	}
	marker, _, _ = systemstate.New(systemColl).Get(ctx, dividendHistoryMigrationMarkerKey)
	if marker.Count != 1 {
		t.Fatalf("idempotent marker count=%d, want 1", marker.Count)
	}
}

type conflictOnceDividendMigrationStore struct {
	storage.DocStore[legacyDividendPortfolio]
	conflicted bool
}

func (s *conflictOnceDividendMigrationStore) PutVersioned(ctx context.Context, key string, version int64, value legacyDividendPortfolio) error {
	if !s.conflicted {
		s.conflicted = true
		return storage.ErrConflict
	}
	return s.DocStore.PutVersioned(ctx, key, version, value)
}

func TestDividendHistoryMigrationRetriesVersionConflict(t *testing.T) {
	ctx := context.Background()
	provider := storage.NewMemoryProvider()
	docs := storage.Typed[legacyDividendPortfolio](provider.Collection(CollectionName))
	if err := docs.Put(ctx, "user:7", legacyDividendPortfolio{
		Assets: map[string]legacyDividendAssetPosition{
			"TCB": {Quantity: 10, Base: 300_000, DividendCheckedAt: legacyCursor(123), OpenedAt: 99},
		},
	}); err != nil {
		t.Fatal(err)
	}
	store := &conflictOnceDividendMigrationStore{DocStore: docs}
	changed, err := migrateDividendHistorySchema(ctx, store, "user:7")
	if err != nil || !changed || !store.conflicted {
		t.Fatalf("changed=%v conflicted=%v err=%v", changed, store.conflicted, err)
	}
}

type alwaysConflictDividendMigrationStore struct {
	storage.DocStore[legacyDividendPortfolio]
}

func (s alwaysConflictDividendMigrationStore) PutVersioned(context.Context, string, int64, legacyDividendPortfolio) error {
	return storage.ErrConflict
}

func TestDividendHistoryMigrationReturnsExhaustedConflict(t *testing.T) {
	ctx := context.Background()
	provider := storage.NewMemoryProvider()
	docs := storage.Typed[legacyDividendPortfolio](provider.Collection(CollectionName))
	if err := docs.Put(ctx, "user:7", legacyDividendPortfolio{
		Assets: map[string]legacyDividendAssetPosition{
			"TCB": {Quantity: 10, Base: 300_000, DividendCheckedAt: legacyCursor(123), OpenedAt: 99},
		},
	}); err != nil {
		t.Fatal(err)
	}
	changed, err := migrateDividendHistorySchema(ctx, alwaysConflictDividendMigrationStore{docs}, "user:7")
	if changed || !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("changed=%v err=%v, want wrapped conflict", changed, err)
	}
}

func TestInitStoreDoesNotMarkFailedMigrationComplete(t *testing.T) {
	ctx := context.Background()
	provider := storage.NewMemoryProvider()
	// A legacy portfolio with an invalid position must fail validation before
	// its retired fields are removed.
	if err := storage.Typed[legacyDividendPortfolio](provider.Collection(CollectionName)).Put(ctx, "user:7", legacyDividendPortfolio{
		Assets: map[string]legacyDividendAssetPosition{
			"TCB": {Quantity: 10, Base: -1, DividendCheckedAt: legacyCursor(123), OpenedAt: 99},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := InitStore(ctx, provider.Collection(CollectionName), provider.Collection(systemstate.CollectionName)); err == nil {
		t.Fatal("InitStore accepted invalid legacy portfolio")
	}
	_, exists, err := systemstate.New(provider.Collection(systemstate.CollectionName)).Get(ctx, dividendHistoryMigrationMarkerKey)
	if err != nil || exists {
		t.Fatalf("completion marker exists=%v after failure, err=%v", exists, err)
	}
}
