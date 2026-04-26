package kitsune

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMemoryIdempotencyStore_AddFirstTimeThenDuplicate(t *testing.T) {
	s := newMemoryIdempotencyStore()
	ctx := context.Background()

	first, err := s.Add(ctx, "k1")
	if err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if !first {
		t.Errorf("first Add returned firstTime=false, want true")
	}

	second, err := s.Add(ctx, "k1")
	if err != nil {
		t.Fatalf("second Add: %v", err)
	}
	if second {
		t.Errorf("second Add returned firstTime=true, want false")
	}

	other, err := s.Add(ctx, "k2")
	if err != nil {
		t.Fatalf("Add k2: %v", err)
	}
	if !other {
		t.Errorf("Add k2 returned firstTime=false, want true")
	}
}

func TestMemoryIdempotencyStore_RaceFreeUnderConcurrency(t *testing.T) {
	s := newMemoryIdempotencyStore()
	ctx := context.Background()

	const workers = 16
	const adds = 100
	var firstCount atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < adds; i++ {
				first, err := s.Add(ctx, "shared-key")
				if err != nil {
					t.Errorf("Add: %v", err)
					return
				}
				if first {
					firstCount.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	if got := firstCount.Load(); got != 1 {
		t.Errorf("firstCount=%d, want 1 (Add must atomically claim the key exactly once)", got)
	}
}
