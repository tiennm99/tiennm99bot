package keylock

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestMap_DistinctKeysDoNotBlock asserts the per-key fan-out: two acquires on
// different keys must run in parallel even while one is held. We arrange a
// goroutine that holds key "a" for a beat, then a second goroutine acquires
// "b" — if Acquire blocked across keys, the second goroutine couldn't finish
// before the first releases. We give it a generous timeout to avoid flakes
// on slow CI hardware while still failing if locks are global.
func TestMap_DistinctKeysDoNotBlock(t *testing.T) {
	var m Map
	holdA := make(chan struct{})
	releasedA := make(chan struct{})
	bDone := make(chan struct{})

	go func() {
		release := m.Acquire("a")
		close(holdA)
		<-time.After(50 * time.Millisecond)
		release()
		close(releasedA)
	}()

	go func() {
		<-holdA
		release := m.Acquire("b")
		release()
		close(bDone)
	}()

	select {
	case <-bDone:
		// Good: b acquired while a was held.
	case <-time.After(40 * time.Millisecond):
		t.Fatal("Acquire(\"b\") blocked while Acquire(\"a\") was held — keys are not independent")
	}
	<-releasedA
}

// TestMap_SameKeySerialises asserts mutual exclusion on a shared key under
// concurrent contention. If acquires interleaved, the counter would race.
func TestMap_SameKeySerialises(t *testing.T) {
	var m Map
	var counter int64
	const goroutines = 32
	const itersEach = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < itersEach; j++ {
				release := m.Acquire("shared")
				// Read-modify-write that would race without serialisation.
				v := atomic.LoadInt64(&counter)
				atomic.StoreInt64(&counter, v+1)
				release()
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&counter); got != int64(goroutines*itersEach) {
		t.Errorf("counter = %d, want %d (lost updates → mutex didn't serialise)", got, goroutines*itersEach)
	}
}

func TestMap_LenCountsDistinctKeys(t *testing.T) {
	var m Map
	if m.Len() != 0 {
		t.Fatalf("empty Len = %d", m.Len())
	}
	m.Acquire("a")()
	m.Acquire("a")()
	m.Acquire("b")()
	if m.Len() != 2 {
		t.Fatalf("Len = %d, want 2", m.Len())
	}
}
