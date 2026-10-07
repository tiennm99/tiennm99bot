package gold

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"

	stockmod "github.com/tiennm99/tiennm99bot/internal/modules/stock"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// newGoldStore returns a fresh in-memory typed portfolio store for tests.
func newGoldStore() PortfolioStore {
	return storage.Typed[Portfolio](storage.NewMemoryProvider().Collection("gold"))
}

func TestLoadPortfolio_FirstTimeUser(t *testing.T) {
	p, err := LoadPortfolio(context.Background(), newGoldStore(), 42, 123)
	if err != nil {
		t.Fatalf("LoadPortfolio: %v", err)
	}
	if p.VND != 0 || p.Luong != 0 || p.Meta.CreatedAt != 123 {
		t.Fatalf("portfolio: got %+v", p)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	store := newGoldStore()
	p := NewPortfolio(1)
	p.AddVND(5_000_000)
	p.AddLuong(1.25)
	p.Meta.Invested = 5_000_000
	if err := SavePortfolio(context.Background(), store, 42, p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadPortfolio(context.Background(), store, 42, 999)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.VND != 5_000_000 || got.Luong != 1.25 || got.Meta.CreatedAt != 1 {
		t.Fatalf("round trip: got %+v", got)
	}
}

func TestDeductLuongDustCleanup(t *testing.T) {
	p := NewPortfolio(0)
	p.AddLuong(0.1)
	p.AddLuong(0.2)
	ok, held := p.DeductLuong(0.3)
	if !ok {
		t.Fatalf("deduct: ok=false held=%v", held)
	}
	if p.Luong != 0 {
		t.Fatalf("dust not cleaned: got %.20f", p.Luong)
	}
}

func TestDeductVNDInsufficient(t *testing.T) {
	p := NewPortfolio(0)
	p.AddVND(1000)
	ok, bal := p.DeductVND(1500)
	if ok || bal != 1000 || p.VND != 1000 {
		t.Fatalf("deduct: ok=%v bal=%v p=%+v", ok, bal, p)
	}
}

func TestNormalizeAmountSpecialValues(t *testing.T) {
	for _, n := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), goldDustEpsilon / 2} {
		if got := normalizeAmount(n); got != 0 {
			t.Fatalf("normalizeAmount(%v) = %v, want 0", n, got)
		}
	}
}

// conflictOnceStore wraps a real typed store and forces exactly one write
// conflict (after committing a competing value) before delegating, to exercise
// UpdatePortfolio's optimistic-lock retry.
type conflictOnceStore struct {
	PortfolioStore
	conflicted bool
}

func (s *conflictOnceStore) PutVersioned(ctx context.Context, key string, expectedVersion int64, val Portfolio) error {
	if !s.conflicted {
		s.conflicted = true
		competing := NewPortfolio(1)
		competing.AddVND(10)
		if err := s.Put(ctx, key, competing); err != nil {
			return err
		}
		return storage.ErrConflict
	}
	return s.PortfolioStore.PutVersioned(ctx, key, expectedVersion, val)
}

func TestUpdatePortfolioRetriesAfterWriteConflict(t *testing.T) {
	ctx := context.Background()
	store := &conflictOnceStore{PortfolioStore: newGoldStore()}
	got, err := UpdatePortfolio(ctx, store, 7, 1, func(p *Portfolio) error {
		p.AddVND(5)
		return nil
	})
	if err != nil {
		t.Fatalf("UpdatePortfolio: %v", err)
	}
	if got.VND != 15 {
		t.Fatalf("updated portfolio VND = %v, want 15", got.VND)
	}
	loaded, err := LoadPortfolio(ctx, store, 7, 1)
	if err != nil {
		t.Fatalf("LoadPortfolio: %v", err)
	}
	if loaded.VND != 15 {
		t.Fatalf("stored portfolio VND = %v, want 15", loaded.VND)
	}
}

// alwaysConflictStore always returns ErrConflict on PutVersioned to exhaust retries.
type alwaysConflictStore struct {
	PortfolioStore
	attempts int
}

func (s *alwaysConflictStore) PutVersioned(_ context.Context, _ string, _ int64, _ Portfolio) error {
	s.attempts++
	return storage.ErrConflict
}

func TestUpdatePortfolioReturnsConflictAfterExhaustingRetries(t *testing.T) {
	ctx := context.Background()
	store := &alwaysConflictStore{PortfolioStore: newGoldStore()}
	_, err := UpdatePortfolio(ctx, store, 7, 1, func(p *Portfolio) error {
		p.AddVND(5)
		return nil
	})
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("UpdatePortfolio: got %v, want wrapped ErrConflict", err)
	}
	if store.attempts != portfolioUpdateAttempts {
		t.Errorf("CAS attempts = %d, want %d", store.attempts, portfolioUpdateAttempts)
	}
	if _, _, err := store.Get(ctx, "user:7"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("exhausted update must not persist anything, Get = %v", err)
	}
}

// countingCASStore counts PutVersioned calls and delegates to the underlying store.
type countingCASStore struct {
	PortfolioStore
	casCalls int
}

func (s *countingCASStore) PutVersioned(ctx context.Context, key string, expectedVersion int64, val Portfolio) error {
	s.casCalls++
	return s.PortfolioStore.PutVersioned(ctx, key, expectedVersion, val)
}

func TestUpdatePortfolioMutateErrorDoesNotRetryOrPersist(t *testing.T) {
	ctx := context.Background()
	store := &countingCASStore{PortfolioStore: newGoldStore()}
	mutateCalls := 0
	_, err := UpdatePortfolio(ctx, store, 7, 1, func(p *Portfolio) error {
		mutateCalls++
		return errInsufficientVND
	})
	if !errors.Is(err, errInsufficientVND) {
		t.Fatalf("UpdatePortfolio: got %v, want errInsufficientVND", err)
	}
	if mutateCalls != 1 {
		t.Errorf("mutate calls = %d, want 1 (business errors must not retry)", mutateCalls)
	}
	if store.casCalls != 0 {
		t.Errorf("CAS calls = %d, want 0 (failed mutate must not write)", store.casCalls)
	}
	if _, _, err := store.Get(ctx, "user:7"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("failed mutate must not persist anything, Get = %v", err)
	}
}

func TestUpdatePortfolioConcurrentIncrementsLoseNoUpdates(t *testing.T) {
	ctx := context.Background()
	store := newGoldStore()
	const goroutines = 16
	var successes atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := UpdatePortfolio(ctx, store, 7, 1, func(p *Portfolio) error {
				p.AddVND(1000)
				return nil
			})
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, storage.ErrConflict) {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("UpdatePortfolio: %v", err)
	}
	// Retry exhaustion under heavy contention is acceptable; a lost update is not:
	// the stored balance must equal exactly 1000 per successful update.
	if successes.Load() == 0 {
		t.Fatal("no update succeeded")
	}
	loaded, err := LoadPortfolio(ctx, store, 7, 1)
	if err != nil {
		t.Fatalf("LoadPortfolio: %v", err)
	}
	if want := float64(successes.Load()) * 1000; loaded.VND != want {
		t.Errorf("VND = %v, want %v (%d successful updates)", loaded.VND, want, successes.Load())
	}
}

func TestStockAndGoldPortfolioKeysDoNotCollide(t *testing.T) {
	ctx := context.Background()
	provider := storage.NewMemoryProvider()

	goldStore := storage.Typed[Portfolio](provider.Collection("gold"))
	goldPortfolio := NewPortfolio(1)
	goldPortfolio.AddLuong(2)
	if err := SavePortfolio(ctx, goldStore, 7, goldPortfolio); err != nil {
		t.Fatalf("save gold: %v", err)
	}

	stockStore := storage.Typed[stockmod.Portfolio](provider.Collection("stock"))
	stockPortfolio := stockmod.NewPortfolio(1)
	if err := stockPortfolio.BuyTicker("TCB", 100, 3_000_000, 1); err != nil {
		t.Fatalf("buy stock: %v", err)
	}
	if err := stockmod.SavePortfolio(ctx, stockStore, 7, stockPortfolio); err != nil {
		t.Fatalf("save stock: %v", err)
	}

	// Both stores share the same provider but different collections (module
	// isolation); verify each module's key is visible only in its own collection.
	goldKeys, err := goldStore.List(ctx, "")
	if err != nil {
		t.Fatalf("list gold: %v", err)
	}
	stockKeys, err := stockStore.List(ctx, "")
	if err != nil {
		t.Fatalf("list stock: %v", err)
	}
	if len(goldKeys) != 1 || goldKeys[0] != "user:7" {
		t.Fatalf("gold keys: got %v, want [user:7]", goldKeys)
	}
	if len(stockKeys) != 1 || stockKeys[0] != "user:7" {
		t.Fatalf("stock keys: got %v, want [user:7]", stockKeys)
	}
}
