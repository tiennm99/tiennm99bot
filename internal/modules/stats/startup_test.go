package stats

import (
	"context"
	"errors"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/systemstate"
)

func TestInitStore_SwapsNoituCommandStats(t *testing.T) {
	assertNoituCommandSwap(t, storage.NewMemoryProvider())
}

// assertNoituCommandSwap seeds the solo game's history under "noitu" and the
// group game's under "noitupvp", runs the startup migration twice, and checks
// that the solo history landed on "noitubot" and the group history on
// "noitu" without the two mixing.
func assertNoituCommandSwap(t *testing.T, provider storage.Provider) {
	t.Helper()
	ctx := context.Background()
	statsColl := provider.Collection("stats")
	systemColl := provider.Collection(systemstate.CollectionName)
	docs := storage.Typed[usageEntry](statsColl)

	seeds := map[string]usageEntry{
		usageKey("noitu", 0):    {Cmd: "noitu", N: 5},
		usageKey("noitu", 7):    {Cmd: "noitu", UserID: 7, Username: "alice", N: 2},
		usageKey("noitupvp", 0): {Cmd: "noitupvp", N: 3},
		usageKey("noitupvp", 7): {Cmd: "noitupvp", UserID: 7, Username: "alice", N: 4},
		usageKey("noitupvp", 8): {Cmd: "noitupvp", UserID: 8, Username: "bob", N: 6},
		// A command that only shares the prefix stays where it is.
		usageKey("noitutop", 7): {Cmd: "noitutop", UserID: 7, Username: "alice", N: 1},
	}
	for key, entry := range seeds {
		if err := docs.Put(ctx, key, entry); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}

	for run := range 2 {
		if err := InitStore(ctx, statsColl, systemColl); err != nil {
			t.Fatalf("InitStore run %d: %v", run+1, err)
		}
	}

	want := map[string]usageEntry{
		usageKey("noitubot", 0): {Cmd: "noitubot", N: 5},
		usageKey("noitubot", 7): {Cmd: "noitubot", UserID: 7, Username: "alice", N: 2},
		usageKey("noitu", 0):    {Cmd: "noitu", N: 3},
		usageKey("noitu", 7):    {Cmd: "noitu", UserID: 7, Username: "alice", N: 4},
		usageKey("noitu", 8):    {Cmd: "noitu", UserID: 8, Username: "bob", N: 6},
		usageKey("noitutop", 7): {Cmd: "noitutop", UserID: 7, Username: "alice", N: 1},
	}
	for key, w := range want {
		got, _, err := docs.Get(ctx, key)
		if err != nil {
			t.Fatalf("get %s: %v", key, err)
		}
		if got != w {
			t.Errorf("%s = %+v, want %+v", key, got, w)
		}
	}
	for _, key := range []string{usageKey("noitupvp", 0), usageKey("noitupvp", 7), usageKey("noitupvp", 8)} {
		if _, _, err := docs.Get(ctx, key); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("old key %s err = %v, want ErrNotFound", key, err)
		}
	}

	state := systemstate.New(systemColl)
	for key, count := range map[string]int64{
		"stats:command-rename:noitu-to-noitubot": 7,
		"stats:command-rename:noitupvp-to-noitu": 13,
	} {
		rec, ok, err := state.Get(ctx, key)
		if err != nil || !ok || rec.Status != "complete" || rec.Count != count {
			t.Errorf("marker %s = %+v ok=%v err=%v, want complete count %d", key, rec, ok, err, count)
		}
	}
}

// A boot that stopped after the first rename resumes with the second only:
// the rows already under the new "noitu" name are group history and must not
// move to noitubot again.
func TestInitStore_NoituSwapResumesAfterFirstStep(t *testing.T) {
	ctx := context.Background()
	provider := storage.NewMemoryProvider()
	statsColl := provider.Collection("stats")
	systemColl := provider.Collection(systemstate.CollectionName)
	docs := storage.Typed[usageEntry](statsColl)

	if err := migrateCommandRename(ctx, statsColl, systemColl, commandRenames[0]); err != nil {
		t.Fatalf("first step: %v", err)
	}
	for key, entry := range map[string]usageEntry{
		usageKey("noitubot", 7): {Cmd: "noitubot", UserID: 7, Username: "alice", N: 2},
		// Moved by an interrupted second step before the restart.
		usageKey("noitu", 7):    {Cmd: "noitu", UserID: 7, Username: "alice", N: 4},
		usageKey("noitupvp", 8): {Cmd: "noitupvp", UserID: 8, Username: "bob", N: 6},
	} {
		if err := docs.Put(ctx, key, entry); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}

	if err := InitStore(ctx, statsColl, systemColl); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	for key, n := range map[string]int64{
		usageKey("noitubot", 7): 2,
		usageKey("noitu", 7):    4,
		usageKey("noitu", 8):    6,
	} {
		got, _, err := docs.Get(ctx, key)
		if err != nil || got.N != n {
			t.Errorf("%s = %+v err=%v, want n %d", key, got, err, n)
		}
	}
}

// A rename merges into rows already under the new name and keeps a row that
// was only ever deleted history marked deleted.
func TestMigrateCommandRename_MergesAndKeepsDeletedFlag(t *testing.T) {
	ctx := context.Background()
	provider := storage.NewMemoryProvider()
	statsColl := provider.Collection("stats")
	systemColl := provider.Collection(systemstate.CollectionName)
	docs := storage.Typed[usageEntry](statsColl)
	for key, entry := range map[string]usageEntry{
		usageKey("old", 0): {Cmd: "old", N: 2, Deleted: true},
		usageKey("old", 7): {Cmd: "old", UserID: 7, Username: "alice", N: 3},
		usageKey("new", 7): {Cmd: "new", UserID: 7, Username: "al", N: 5},
	} {
		if err := docs.Put(ctx, key, entry); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	if err := migrateCommandRename(ctx, statsColl, systemColl, commandRename{oldCmd: "old", newCmd: "new", markerKey: "test-rename"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if got, _, _ := docs.Get(ctx, usageKey("new", 0)); got != (usageEntry{Cmd: "new", N: 2, Deleted: true}) {
		t.Errorf("anonymous = %+v", got)
	}
	if got, _, _ := docs.Get(ctx, usageKey("new", 7)); got != (usageEntry{Cmd: "new", UserID: 7, Username: "alice", N: 8}) {
		t.Errorf("user = %+v", got)
	}
}
