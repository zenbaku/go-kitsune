package internal

import (
	"context"
	"sync"
	"sync/atomic"
)

// Outbox[T] abstracts sending a typed item downstream with overflow handling.
type Outbox[T any] interface {
	// Send delivers item to the output channel.
	// Returns ctx.Err() if the context is cancelled (blocking mode only).
	// Drop modes never return an error.
	Send(ctx context.Context, item T) error
	// Dropped returns the number of items dropped since creation.
	Dropped() int64
}

// ---------------------------------------------------------------------------
// Block (default)
// ---------------------------------------------------------------------------

type blockingOutbox[T any] struct {
	ch chan T
}

func (o *blockingOutbox[T]) Send(ctx context.Context, item T) error {
	select {
	case o.ch <- item:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *blockingOutbox[T]) Dropped() int64 { return 0 }

// ---------------------------------------------------------------------------
// DropNewest — non-blocking send; incoming item is discarded when full
// ---------------------------------------------------------------------------

type dropNewestOutbox[T any] struct {
	ch      chan T
	hook    Hook
	name    string
	dropped atomic.Int64
}

func (o *dropNewestOutbox[T]) Send(ctx context.Context, item T) error {
	select {
	case o.ch <- item:
	default:
		o.dropped.Add(1)
		if oh, ok := o.hook.(OverflowHook); ok {
			oh.OnDrop(ctx, o.name, item)
		}
	}
	return nil
}

func (o *dropNewestOutbox[T]) Dropped() int64 { return o.dropped.Load() }

// ---------------------------------------------------------------------------
// DropOldest — evict oldest buffered item to make room; mutex-protected
// ---------------------------------------------------------------------------

// dropOldestOutbox is safe for concurrent senders. The mutex serialises the
// "drain oldest + send new" pair so no two goroutines can interleave their
// drain/send steps and corrupt buffer ordering.
// The mutex is only held when the buffer is full, keeping the fast path
// (buffer has space) entirely lock-free.
type dropOldestOutbox[T any] struct {
	ch      chan T
	mu      sync.Mutex
	hook    Hook
	name    string
	dropped atomic.Int64
}

func (o *dropOldestOutbox[T]) Send(ctx context.Context, item T) error {
	// Fast path: buffer has space — no lock needed.
	select {
	case o.ch <- item:
		return nil
	default:
	}

	// Slow path: buffer is full — hold the lock while we drain + resend.
	o.mu.Lock()
	defer o.mu.Unlock()

	select {
	case o.ch <- item:
		// Another goroutine drained between the fast-path check and us
		// acquiring the lock.
		return nil
	default:
	}

	// Evict the oldest item.
	select {
	case old := <-o.ch:
		o.dropped.Add(1)
		if oh, ok := o.hook.(OverflowHook); ok {
			oh.OnDrop(ctx, o.name, old)
		}
	default:
		// Should not happen while holding the lock (we just saw the channel
		// full), but be defensive.
	}

	// Now there is guaranteed space (we hold the lock, no other sender
	// can fill it before us).
	select {
	case o.ch <- item:
	default:
		// Truly should not happen; drop rather than block.
		o.dropped.Add(1)
		if oh, ok := o.hook.(OverflowHook); ok {
			oh.OnDrop(ctx, o.name, item)
		}
	}
	return nil
}

func (o *dropOldestOutbox[T]) Dropped() int64 { return o.dropped.Load() }

// ---------------------------------------------------------------------------
// DropOldest — sharded variant for concurrent workers
// ---------------------------------------------------------------------------

// dropOldestShard is one shard of a sharded DropOldest outbox. Each shard owns
// its own sync.Mutex so concurrent senders routed to different shards never
// contend on the slow path. All shards share the same underlying channel and
// a single atomic dropped counter, so Dropped() reports the aggregate across
// shards regardless of which shard is queried.
//
// Send logic is identical to dropOldestOutbox: fast path is a lock-free
// non-blocking send; slow path holds this shard's mutex while evicting the
// oldest buffered item and enqueueing the new one.
type dropOldestShard[T any] struct {
	ch      chan T
	mu      sync.Mutex
	hook    Hook
	name    string
	dropped *atomic.Int64 // shared across all shards
}

func (o *dropOldestShard[T]) Send(ctx context.Context, item T) error {
	// Fast path: buffer has space — no lock needed.
	select {
	case o.ch <- item:
		return nil
	default:
	}

	// Slow path: buffer is full — hold this shard's lock while we drain + resend.
	o.mu.Lock()
	defer o.mu.Unlock()

	select {
	case o.ch <- item:
		// A concurrent drain (by a downstream reader or a sibling shard) freed
		// space between the fast-path check and us acquiring the lock.
		return nil
	default:
	}

	// Evict the oldest item.
	select {
	case old := <-o.ch:
		o.dropped.Add(1)
		if oh, ok := o.hook.(OverflowHook); ok {
			oh.OnDrop(ctx, o.name, old)
		}
	default:
		// Defensive: the channel appeared full but is now empty. A sibling
		// shard or downstream reader drained it. Fall through to the send.
	}

	// Now there should be space. A sibling shard holding its own lock may
	// race to fill the slot; if so, drop this item rather than block.
	select {
	case o.ch <- item:
	default:
		o.dropped.Add(1)
		if oh, ok := o.hook.(OverflowHook); ok {
			oh.OnDrop(ctx, o.name, item)
		}
	}
	return nil
}

func (o *dropOldestShard[T]) Dropped() int64 { return o.dropped.Load() }

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

// NewBlockingOutbox creates a blocking outbox backed by ch.
func NewBlockingOutbox[T any](ch chan T) Outbox[T] {
	return &blockingOutbox[T]{ch: ch}
}

// NewOutbox creates the appropriate Outbox for the given overflow strategy.
// For concurrent DropOldest stages, prefer NewShardedDropOldestOutbox which
// eliminates cross-worker contention on the slow path.
func NewOutbox[T any](ch chan T, overflow Overflow, hook Hook, name string) Outbox[T] {
	switch overflow {
	case OverflowDropNewest:
		return &dropNewestOutbox[T]{ch: ch, hook: hook, name: name}
	case OverflowDropOldest:
		return &dropOldestOutbox[T]{ch: ch, hook: hook, name: name}
	default:
		return &blockingOutbox[T]{ch: ch}
	}
}

// NewShardedDropOldestOutbox creates n DropOldest shards that share the same
// underlying channel and a single atomic dropped counter. Each shard has its
// own sync.Mutex, so workers routed to different shards never contend on the
// slow path.
//
// Callers must route each worker to exactly one shard (typically by
// worker-index modulo n). n must be >= 1; if n < 1 a single shard is returned.
func NewShardedDropOldestOutbox[T any](ch chan T, n int, hook Hook, name string) []Outbox[T] {
	if n < 1 {
		n = 1
	}
	dropped := new(atomic.Int64)
	boxes := make([]Outbox[T], n)
	for i := 0; i < n; i++ {
		boxes[i] = &dropOldestShard[T]{
			ch:      ch,
			hook:    hook,
			name:    name,
			dropped: dropped,
		}
	}
	return boxes
}
