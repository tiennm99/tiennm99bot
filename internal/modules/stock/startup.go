package stock

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/systemstate"
)

const (
	dividendHistoryMigrationMarkerKey = "migration:stock-dividend-history-v1"
	dividendHistoryMigrationRetries   = 5
)

// legacyDividendAssetPosition contains the retired discovery cursor so a
// whole-document replacement can physically remove it from MongoDB.
type legacyDividendAssetPosition struct {
	Quantity          int64   `json:"quantity" bson:"quantity"`
	Base              float64 `json:"base" bson:"base"`
	DividendCheckedAt *int64  `json:"dividendCheckedAt,omitempty" bson:"dividendCheckedAt,omitempty"`
	OpenedAt          int64   `json:"openedAt,omitempty" bson:"openedAt,omitempty"`
}

// legacyDividendPortfolio mirrors both the current portfolio fields and the
// retired applied-event ledger. Keeping Dividends here is important: migration
// must never discard history already written by the new runtime.
type legacyDividendPortfolio struct {
	VND                   float64                                `json:"vnd" bson:"vnd"`
	Assets                map[string]legacyDividendAssetPosition `json:"assets" bson:"assets"`
	Dividends             map[string]map[string]DividendRecord   `json:"dividends,omitempty" bson:"dividends,omitempty"`
	AppliedDividendEvents map[string]int64                       `json:"appliedDividendEvents,omitempty" bson:"appliedDividendEvents,omitempty"`
	Meta                  PortfolioMeta                          `json:"meta" bson:"meta"`
}

// InitStore removes the cursor-era dividend fields from stock user portfolios.
// The completion marker makes the one-time migration idempotent; it is written
// only after every listed user document has been handled successfully.
func InitStore(ctx context.Context, portfolioColl, systemColl storage.Collection) error {
	system := systemstate.New(systemColl)
	marker, exists, err := system.Get(ctx, dividendHistoryMigrationMarkerKey)
	if err != nil {
		return fmt.Errorf("stock dividend history migration: read marker: %w", err)
	}
	if exists && marker.Status == "completed" {
		return nil
	}

	docs := storage.Typed[legacyDividendPortfolio](portfolioColl)
	keys, err := docs.List(ctx, "user:")
	if err != nil {
		return fmt.Errorf("stock dividend history migration: list portfolios: %w", err)
	}

	var migrated int64
	for index, key := range keys {
		changed, err := migrateDividendHistorySchema(ctx, docs, key)
		if err != nil {
			return err
		}
		if changed {
			migrated++
			log.Info("stock dividend history migrated", "portfolio", index+1, "total", len(keys))
		}
	}

	marker = completedDividendHistoryMigration(marker, exists, migrated, time.Now().UnixMilli())
	if err := system.Put(ctx, dividendHistoryMigrationMarkerKey, marker); err != nil {
		return fmt.Errorf("stock dividend history migration: write marker: %w", err)
	}
	return nil
}

func completedDividendHistoryMigration(marker systemstate.Record, exists bool, migrated, now int64) systemstate.Record {
	if !exists {
		marker = systemstate.Record{
			Kind: "migration",
			Name: "stock dividend history v1",
		}
	}
	if marker.CompletedAt == 0 {
		marker.CompletedAt = now
	}
	marker.Status = "completed"
	marker.Count += migrated
	marker.UpdatedAt = now
	return marker
}

func migrateDividendHistorySchema(ctx context.Context, docs storage.DocStore[legacyDividendPortfolio], key string) (bool, error) {
	for attempt := 0; attempt < dividendHistoryMigrationRetries; attempt++ {
		doc, version, err := docs.Get(ctx, key)
		if err != nil {
			return false, fmt.Errorf("stock dividend history migration: read %s: %w", key, err)
		}
		if !doc.hasLegacyDividendFields() {
			return false, nil
		}
		current := doc.currentPortfolio()
		if err := current.Validate(); err != nil {
			return false, fmt.Errorf("stock dividend history migration: validate %s: %w", key, err)
		}
		err = docs.PutVersioned(ctx, key, version, doc.withoutLegacyDividendFields())
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, storage.ErrConflict) {
			return false, fmt.Errorf("stock dividend history migration: write %s: %w", key, err)
		}
	}
	return false, fmt.Errorf("stock dividend history migration: write %s: %w", key, storage.ErrConflict)
}

func (p legacyDividendPortfolio) hasLegacyDividendFields() bool {
	if p.AppliedDividendEvents != nil {
		return true
	}
	for _, position := range p.Assets {
		if position.DividendCheckedAt != nil {
			return true
		}
	}
	return false
}

func (p legacyDividendPortfolio) withoutLegacyDividendFields() legacyDividendPortfolio {
	p.AppliedDividendEvents = nil
	for ticker, position := range p.Assets {
		position.DividendCheckedAt = nil
		p.Assets[ticker] = position
	}
	return p
}

func (p legacyDividendPortfolio) currentPortfolio() Portfolio {
	assets := make(map[string]AssetPosition, len(p.Assets))
	for ticker, position := range p.Assets {
		assets[ticker] = AssetPosition{
			Quantity: position.Quantity,
			Base:     position.Base,
			OpenedAt: position.OpenedAt,
		}
	}
	return Portfolio{VND: p.VND, Assets: assets, Dividends: p.Dividends, Meta: p.Meta}
}
