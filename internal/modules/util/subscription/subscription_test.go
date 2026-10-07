package subscription

import (
	"context"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

func newStore() Store {
	return storage.Typed[Doc](storage.NewMemoryProvider().Collection("test"))
}

func TestSubscribers_AddRemoveListIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newStore()

	got, _ := List(ctx, store)
	if len(got) != 0 {
		t.Errorf("empty list = %v, want []", got)
	}

	added, err := Add(ctx, store, 42, 0)
	if err != nil || !added {
		t.Fatalf("first add: added=%v err=%v", added, err)
	}
	added, _ = Add(ctx, store, 42, 0)
	if added {
		t.Errorf("re-add should be no-op")
	}

	got, _ = List(ctx, store)
	if len(got) != 1 || got[0] != (Subscriber{ChatID: 42}) {
		t.Errorf("after add(42,0): %v, want [{42 0}]", got)
	}

	if _, err := Add(ctx, store, 7, 0); err != nil {
		t.Fatal(err)
	}
	got, _ = List(ctx, store)
	if len(got) != 2 {
		t.Errorf("after add(7,0): %v, want len 2", got)
	}

	removed, err := Remove(ctx, store, 42, 0)
	if err != nil || !removed {
		t.Fatalf("remove(42,0): removed=%v err=%v", removed, err)
	}
	removed, _ = Remove(ctx, store, 42, 0)
	if removed {
		t.Errorf("re-remove should be no-op")
	}

	got, _ = List(ctx, store)
	if len(got) != 1 || got[0].ChatID != 7 {
		t.Errorf("after remove(42,0): %v, want [{7 0}]", got)
	}
}

// TestSubscribers_TopicsAreDistinct locks in the forum-topic fix: the same
// chat can subscribe in multiple topics independently, and removing one
// topic must not affect the others.
func TestSubscribers_TopicsAreDistinct(t *testing.T) {
	ctx := context.Background()
	store := newStore()

	for _, tid := range []int{0, 5, 9} {
		added, err := Add(ctx, store, 100, tid)
		if err != nil || !added {
			t.Fatalf("add(100,%d): added=%v err=%v", tid, added, err)
		}
	}
	// Same (chat, thread) is rejected as duplicate.
	if added, _ := Add(ctx, store, 100, 5); added {
		t.Errorf("duplicate (100,5) should be no-op")
	}

	got, _ := List(ctx, store)
	if len(got) != 3 {
		t.Fatalf("after 3 adds: %v, want len 3", got)
	}

	// Removing one topic leaves the other two intact.
	if removed, _ := Remove(ctx, store, 100, 5); !removed {
		t.Error("remove(100,5) should report removed=true")
	}
	got, _ = List(ctx, store)
	if len(got) != 2 {
		t.Errorf("after remove(100,5): %v, want len 2", got)
	}
	for _, s := range got {
		if s.ChatID == 100 && s.ThreadID == 5 {
			t.Errorf("(100,5) still present: %v", got)
		}
	}
}

// TestSubscribers_RemoveAllForChat covers the chat-wide prune used when a
// terminal error means every topic in that chat is dead too.
func TestSubscribers_RemoveAllForChat(t *testing.T) {
	ctx := context.Background()
	store := newStore()

	for _, tid := range []int{0, 5, 9} {
		if _, err := Add(ctx, store, 100, tid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Add(ctx, store, 200, 0); err != nil {
		t.Fatal(err)
	}

	n, err := RemoveAllForChat(ctx, store, 100)
	if err != nil {
		t.Fatalf("RemoveAllForChat(100): %v", err)
	}
	if n != 3 {
		t.Errorf("RemoveAllForChat(100) returned %d, want 3", n)
	}

	got, _ := List(ctx, store)
	if len(got) != 1 || got[0].ChatID != 200 {
		t.Errorf("after wipe(100): %v, want only chat 200", got)
	}

	// Idempotent: second call removes nothing.
	if n, _ := RemoveAllForChat(ctx, store, 100); n != 0 {
		t.Errorf("second RemoveAllForChat(100) = %d, want 0", n)
	}
}

func TestSubscribers_CurrentShapeRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newStore()

	// Write the current shape directly via Put so we can confirm round-trip.
	currentSubs := []Subscriber{{ChatID: 11}, {ChatID: 22}, {ChatID: 33}}
	if err := store.Put(ctx, Key, Doc{Subscribers: currentSubs}); err != nil {
		t.Fatal(err)
	}

	got, err := List(ctx, store)
	if err != nil {
		t.Fatalf("listSubscribers: %v", err)
	}
	want := []Subscriber{{ChatID: 11}, {ChatID: 22}, {ChatID: 33}}
	if len(got) != len(want) {
		t.Fatalf("decode: got %v, want %v", got, want)
	}
	for i, s := range got {
		if s != want[i] {
			t.Errorf("[%d]: got %v, want %v", i, s, want[i])
		}
	}

	// Next mutation verifies subscribers wrap correctly.
	if _, err := Add(ctx, store, 44, 7); err != nil {
		t.Fatal(err)
	}
	doc, _, _ := store.Get(ctx, Key)
	if doc.Subscribers == nil {
		t.Error("expected non-nil Subscribers field after add")
	}
	if len(doc.Subscribers) != 4 {
		t.Errorf("after add: doc.Subscribers len = %d, want 4", len(doc.Subscribers))
	}
}
