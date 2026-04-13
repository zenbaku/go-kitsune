package internal

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// TestShardedDropOldestOutboxBasic verifies that a single shard behaves like
// the non-sharded dropOldestOutbox: the fast path sends without blocking, and
// the slow path evicts the oldest buffered item when full.
func TestShardedDropOldestOutboxBasic(t *testing.T) {
	ch := make(chan int, 2)
	boxes := NewShardedDropOldestOutbox[int](ch, 1, nil, "t")
	if len(boxes) != 1 {
		t.Fatalf("want 1 shard, got %d", len(boxes))
	}
	ctx := context.Background()
	// Fill the buffer.
	if err := boxes[0].Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := boxes[0].Send(ctx, 2); err != nil {
		t.Fatal(err)
	}
	// Third send must evict 1 and enqueue 3.
	if err := boxes[0].Send(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if got := <-ch; got != 2 {
		t.Fatalf("want 2 (oldest after eviction), got %d", got)
	}
	if got := <-ch; got != 3 {
		t.Fatalf("want 3 (newest), got %d", got)
	}
	if boxes[0].Dropped() != 1 {
		t.Fatalf("want 1 drop, got %d", boxes[0].Dropped())
	}
}

// TestShardedDropOldestOutboxSharedCounter verifies that drops from different
// shards accumulate in the shared counter reported by Dropped() on any shard.
func TestShardedDropOldestOutboxSharedCounter(t *testing.T) {
	ch := make(chan int, 1)
	boxes := NewShardedDropOldestOutbox[int](ch, 2, nil, "t")
	if len(boxes) != 2 {
		t.Fatalf("want 2 shards, got %d", len(boxes))
	}
	ctx := context.Background()
	// Fill the shared channel via shard 0.
	if err := boxes[0].Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
	// Shard 1 must evict (buffer full) and then report the drop in the
	// shared counter.
	if err := boxes[1].Send(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if got := <-ch; got != 2 {
		t.Fatalf("want 2 (newest), got %d", got)
	}
	// Both shards must see the same drop count because they share the counter.
	if boxes[0].Dropped() != 1 {
		t.Fatalf("shard 0: want 1 drop, got %d", boxes[0].Dropped())
	}
	if boxes[1].Dropped() != 1 {
		t.Fatalf("shard 1: want 1 drop, got %d", boxes[1].Dropped())
	}
}

// TestShardedDropOldestOutboxIndependentMutexes verifies that a contended
// drain on one shard does not block another shard's send. Run with -race to
// catch ordering bugs.
func TestShardedDropOldestOutboxIndependentMutexes(t *testing.T) {
	ch := make(chan int, 4)
	boxes := NewShardedDropOldestOutbox[int](ch, 4, nil, "t")
	ctx := context.Background()

	const perShard = 200
	var wg sync.WaitGroup
	var totalSends atomic.Int64
	for i := 0; i < len(boxes); i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < perShard; j++ {
				if err := boxes[idx].Send(ctx, idx*perShard+j); err != nil {
					t.Errorf("shard %d send: %v", idx, err)
					return
				}
				totalSends.Add(1)
			}
		}(i)
	}

	// Drain in parallel so senders can make progress.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		count := 0
		for range ch {
			count++
			if count == perShard*4 {
				return
			}
		}
	}()

	wg.Wait()
	close(ch)
	<-drained

	if totalSends.Load() != int64(len(boxes))*perShard {
		t.Fatalf("want %d sends, got %d", int64(len(boxes))*perShard, totalSends.Load())
	}
}
