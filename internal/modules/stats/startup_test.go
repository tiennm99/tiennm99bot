package stats

import (
	"context"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/systemstate"
)

func TestInitStoreMarksStockDividendStatsDeletedAndRetainsHistory(t *testing.T) {
	ctx := context.Background()
	provider := storage.NewMemoryProvider()
	statsColl := provider.Collection("stats")
	systemColl := provider.Collection(systemstate.CollectionName)
	docs := storage.Typed[usageEntry](statsColl)

	seed := map[string]usageEntry{
		"stock_dividend":        {Cmd: "stock_dividend", N: 12},
		"stock_dividend:7":      {Cmd: "stock_dividend", UserID: 7, Username: "alice", N: 5, Deleted: true},
		"stock_dividend_extra":  {Cmd: "stock_dividend_extra", N: 3},
		"stock_cash_dividend:7": {Cmd: "stock_cash_dividend", UserID: 7, Username: "alice", N: 2},
	}
	for key, entry := range seed {
		if err := docs.Put(ctx, key, entry); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}

	if err := InitStore(ctx, statsColl, systemColl); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if err := InitStore(ctx, statsColl, systemColl); err != nil {
		t.Fatalf("InitStore second run: %v", err)
	}

	for _, key := range []string{"stock_dividend", "stock_dividend:7"} {
		entry, _, err := docs.Get(ctx, key)
		if err != nil {
			t.Fatalf("get retained %s: %v", key, err)
		}
		if !entry.Deleted || entry.N != seed[key].N || entry.Username != seed[key].Username {
			t.Fatalf("retained %s = %+v, want deleted history %+v", key, entry, seed[key])
		}
	}
	for _, key := range []string{"stock_dividend_extra", "stock_cash_dividend:7"} {
		entry, _, err := docs.Get(ctx, key)
		if err != nil || entry.Deleted {
			t.Fatalf("unrelated %s = %+v, err=%v", key, entry, err)
		}
	}

	marker, exists, err := systemstate.New(systemColl).Get(ctx, deletedStockDividendMarkerKey)
	if err != nil || !exists || marker.Status != "completed" || marker.Count != 2 {
		t.Fatalf("marker=%+v exists=%v err=%v", marker, exists, err)
	}

	rows, err := newUsageStore(statsColl).TopCommands(ctx, 10)
	if err != nil {
		t.Fatalf("TopCommands: %v", err)
	}
	for _, row := range rows {
		if row.display == "/stock_dividend" {
			t.Fatalf("retired command remains visible: %+v", rows)
		}
	}

	// Simulate a legacy instance writing after the first startup completed.
	legacy, _, err := docs.Get(ctx, "stock_dividend:7")
	if err != nil {
		t.Fatal(err)
	}
	legacy.Deleted = false
	legacy.N++
	if err := docs.Put(ctx, "stock_dividend:7", legacy); err != nil {
		t.Fatal(err)
	}
	store := newUsageStore(statsColl)
	if err := store.Increment(ctx, "stock_dividend", usageUser{ID: 7, Username: "alice"}, true); err != nil {
		t.Fatalf("retired Increment: %v", err)
	}
	if rows, err := store.UsersByCommand(ctx, "stock_dividend", 10); err != nil || len(rows) != 0 {
		t.Fatalf("retired users = %+v, err=%v", rows, err)
	}
	if rows, err := store.TopUsers(ctx, 10); err != nil || len(rows) != 1 || rows[0].display != "@alice" || rows[0].n != 2 {
		t.Fatalf("top users after legacy write = %+v, err=%v", rows, err)
	}
	if rows, found, err := store.CommandsByUser(ctx, "alice", 10); err != nil || !found || len(rows) != 1 || rows[0].display != "/stock_cash_dividend" {
		t.Fatalf("commands after legacy write = %+v, found=%v err=%v", rows, found, err)
	}
	if err := InitStore(ctx, statsColl, systemColl); err != nil {
		t.Fatalf("InitStore reconciliation: %v", err)
	}
	legacy, _, err = docs.Get(ctx, "stock_dividend:7")
	if err != nil || !legacy.Deleted || legacy.N != 6 {
		t.Fatalf("reconciled legacy entry = %+v, err=%v", legacy, err)
	}
}
