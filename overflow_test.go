package kitsune_test

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// dropCountHook implements kitsune.Hook + kitsune.OverflowHook, counting drops.
type dropCountHook struct {
	kitsune.Hook
	drops atomic.Int64
}

func (h *dropCountHook) OnDrop(_ context.Context, _ string, _ any) { h.drops.Add(1) }

// ---------------------------------------------------------------------------
// Overflow: default (block)
// ---------------------------------------------------------------------------

func TestOverflowDefaultIsBlock(t *testing.T) {
	// Without Overflow(), all 50 items must be delivered (regression).
	const n = 50
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	results, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v, nil
	}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != n {
		t.Fatalf("want %d results, got %d", n, len(results))
	}
}

// ---------------------------------------------------------------------------
// Overflow: DropNewest
// ---------------------------------------------------------------------------

func TestOverflowDropNewest(t *testing.T) {
	// Fast source+map, slow terminal → buffer fills → DropNewest drops incoming items.
	const n = 200
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	var received []int
	_, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v, nil
	}, kitsune.Buffer(2), kitsune.Overflow(kitsune.DropNewest)).
		ForEach(func(_ context.Context, v int) error {
			time.Sleep(time.Millisecond) // slow consumer causes buffer to fill
			received = append(received, v)
			return nil
		}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(received) >= n {
		t.Fatalf("expected drops with DropNewest, but got all %d items", n)
	}
	// Surviving items must be in ascending order.
	for i := 1; i < len(received); i++ {
		if received[i] <= received[i-1] {
			t.Fatalf("order violation at index %d: %d not > %d", i, received[i], received[i-1])
		}
	}
}

// ---------------------------------------------------------------------------
// Overflow: DropOldest
// ---------------------------------------------------------------------------

func TestOverflowDropOldest(t *testing.T) {
	const n = 100
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	var received []int
	_, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v, nil
	}, kitsune.Buffer(2), kitsune.Overflow(kitsune.DropOldest)).
		ForEach(func(_ context.Context, v int) error {
			time.Sleep(2 * time.Millisecond)
			received = append(received, v)
			return nil
		}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(received) >= n {
		t.Fatalf("expected drops with DropOldest, but got all %d items", n)
	}
	// The last item (n-1) should be present: it's the newest and survives DropOldest.
	last := received[len(received)-1]
	if last != n-1 {
		t.Fatalf("expected last surviving item to be %d (newest), got %d", n-1, last)
	}
}

// ---------------------------------------------------------------------------
// Overflow: concurrent stages
// ---------------------------------------------------------------------------

func TestOverflowDropNewestConcurrent(t *testing.T) {
	// Concurrent stage with DropNewest must complete without deadlock or panic.
	const n = 500
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	results, err := kitsune.Map(kitsune.FromSlice(items), func(_ context.Context, v int) (int, error) {
		return v, nil
	}, kitsune.Concurrency(4), kitsune.Buffer(2), kitsune.Overflow(kitsune.DropNewest)).
		Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected some results")
	}
}

func TestOverflowDropOldestConcurrent(t *testing.T) {
	// Concurrent stage with DropOldest must have no races (run with -race).
	const n = 500
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	results, err := kitsune.Map(kitsune.FromSlice(items), func(_ context.Context, v int) (int, error) {
		return v, nil
	}, kitsune.Concurrency(4), kitsune.Buffer(2), kitsune.Overflow(kitsune.DropOldest)).
		Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected some results")
	}
}

// TestOverflowDropOldestShardedStress exercises the sharded DropOldest outbox
// under sustained backpressure with Concurrency(8) and a tiny Buffer(2), which
// keeps the slow path hot. Run with -race to catch any regression in
// cross-shard ordering: the sharded slow path is only safe because each shard
// owns its own mutex, the underlying channel tolerates concurrent send + recv,
// and the shared dropped counter is atomic.
func TestOverflowDropOldestShardedStress(t *testing.T) {
	const n = 20_000
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}

	h := &dropCountHook{Hook: kitsune.LogHook(slog.New(slog.NewTextHandler(io.Discard, nil)))}
	var received atomic.Int64

	_, err := kitsune.Map(
		kitsune.FromSlice(items),
		func(_ context.Context, v int) (int, error) { return v, nil },
		kitsune.Concurrency(8),
		kitsune.Buffer(2),
		kitsune.Overflow(kitsune.DropOldest),
	).ForEach(func(_ context.Context, _ int) error {
		// No sleep: maximum throughput. The sharded outbox should still
		// make forward progress without deadlock or races.
		received.Add(1)
		return nil
	}).Run(context.Background(), kitsune.WithHook(h))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if received.Load() == 0 {
		t.Fatal("expected some items to be received")
	}
	// Sanity: received + dropped must not exceed n (no phantom items).
	if received.Load()+h.drops.Load() > int64(n) {
		t.Fatalf("received(%d) + dropped(%d) > %d", received.Load(), h.drops.Load(), n)
	}
}

// ---------------------------------------------------------------------------
// Overflow hook
// ---------------------------------------------------------------------------

func TestOverflowHookCalled(t *testing.T) {
	// OverflowHook.OnDrop is called for each dropped item.
	h := &dropCountHook{
		Hook: kitsune.LogHook(slog.New(slog.NewTextHandler(io.Discard, nil))),
	}

	const n = 200
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	var received atomic.Int64
	_, err := kitsune.Map(kitsune.FromSlice(items), func(_ context.Context, v int) (int, error) {
		return v, nil
	}, kitsune.Buffer(2), kitsune.Overflow(kitsune.DropNewest)).
		ForEach(func(_ context.Context, _ int) error {
			time.Sleep(time.Millisecond) // slow consumer
			received.Add(1)
			return nil
		}).Run(context.Background(), kitsune.WithHook(h))
	if err != nil {
		t.Fatal(err)
	}
	if received.Load() >= int64(n) {
		t.Fatalf("expected some drops, got all %d items", n)
	}
	// OnDrop must have been called at least once.
	if h.drops.Load() == 0 {
		t.Fatal("expected OnDrop to be called, got 0 calls")
	}
	if received.Load()+h.drops.Load() > int64(n) {
		t.Fatalf("received(%d) + dropped(%d) > %d", received.Load(), h.drops.Load(), n)
	}
}

// ---------------------------------------------------------------------------
// Overflow with Broadcast
// ---------------------------------------------------------------------------

func TestOverflowBroadcast(t *testing.T) {
	// One fast consumer, one slow consumer via Broadcast.
	// The slow consumer's channel drops items; the fast consumer gets all.
	const n = 50
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}

	p := kitsune.FromSlice(items)
	outs := kitsune.Broadcast(p, 2)
	fast, slow := outs[0], outs[1]

	var fastCount, slowCount atomic.Int64
	r1 := fast.ForEach(func(_ context.Context, _ int) error {
		fastCount.Add(1)
		return nil
	}).Build()

	r2 := kitsune.Map(slow, func(_ context.Context, v int) (int, error) {
		return v, nil
	}, kitsune.Buffer(2), kitsune.Overflow(kitsune.DropNewest)).
		ForEach(func(_ context.Context, _ int) error {
			time.Sleep(2 * time.Millisecond) // slow consumer causes buffer to fill
			slowCount.Add(1)
			return nil
		}).Build()

	merged, err := kitsune.MergeRunners(r1, r2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := merged.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fastCount.Load() != int64(n) {
		t.Fatalf("fast consumer: want %d, got %d", n, fastCount.Load())
	}
	if slowCount.Load() >= int64(n) {
		t.Fatalf("slow consumer should have dropped some items, got all %d", n)
	}
}

// ---------------------------------------------------------------------------
// Overflow under load
// ---------------------------------------------------------------------------

func TestOverflowUnderLoad(t *testing.T) {
	// High-throughput stress: no deadlock, drops occur with both strategies.
	const n = 10_000

	items := make([]int, n)
	for i := range items {
		items[i] = i
	}

	for _, strategy := range []kitsune.OverflowStrategy{kitsune.DropNewest, kitsune.DropOldest} {
		h := &dropCountHook{Hook: kitsune.LogHook(slog.New(slog.NewTextHandler(io.Discard, nil)))}
		var received atomic.Int64

		_, err := kitsune.Map(
			kitsune.FromSlice(items),
			func(_ context.Context, v int) (int, error) { return v, nil },
			kitsune.Concurrency(8),
			kitsune.Buffer(4),
			kitsune.Overflow(strategy),
		).ForEach(func(_ context.Context, _ int) error {
			received.Add(1)
			return nil
		}).Run(context.Background(), kitsune.WithHook(h))
		if err != nil {
			t.Fatalf("strategy %v: unexpected error: %v", strategy, err)
		}
		if received.Load() == 0 {
			t.Fatalf("strategy %v: no items received", strategy)
		}
	}
}
