package engine

import (
	"context"
	"sync"
	"sync/atomic"
)

// Outbox is the send side of a stage output channel.
// Consumers still read from the underlying chan any unchanged.
// The default blockingOutbox is a zero-overhead pass-through of the existing
// select pattern; DropNewest and DropOldest are only constructed when
// explicitly requested.
type Outbox interface {
	// Send delivers item to the output channel.
	// Returns ctx.Err() if the context is cancelled (blocking mode only).
	// Drop modes never return an error.
	Send(ctx context.Context, item any) error
	// Dropped returns the number of items dropped since creation.
	Dropped() int64
}

// ---------------------------------------------------------------------------
// Block (default)
// ---------------------------------------------------------------------------

type blockingOutbox struct {
	ch chan any
}

func (o *blockingOutbox) Send(ctx context.Context, item any) error {
	select {
	case o.ch <- item:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *blockingOutbox) Dropped() int64 { return 0 }

// ---------------------------------------------------------------------------
// DropNewest — non-blocking send; incoming item is discarded when full
// ---------------------------------------------------------------------------

type dropNewestOutbox struct {
	ch      chan any
	hook    Hook
	name    string
	dropped atomic.Int64
}

func (o *dropNewestOutbox) Send(ctx context.Context, item any) error {
	select {
	case o.ch <- item:
	default:
		n := o.dropped.Add(1)
		_ = n
		if oh, ok := o.hook.(OverflowHook); ok {
			oh.OnDrop(ctx, o.name, item)
		}
	}
	return nil
}

func (o *dropNewestOutbox) Dropped() int64 { return o.dropped.Load() }

// ---------------------------------------------------------------------------
// DropOldest — evict oldest buffered item to make room; mutex-protected
// ---------------------------------------------------------------------------

// dropOldestOutbox is safe for concurrent senders. The mutex serialises the
// "drain oldest + send new" pair so no two goroutines can interleave their
// drain/send steps and corrupt buffer ordering.
// The mutex is only held when the buffer is full, keeping the fast path
// (buffer has space) entirely lock-free.
type dropOldestOutbox struct {
	ch      chan any
	mu      sync.Mutex
	hook    Hook
	name    string
	dropped atomic.Int64
}

func (o *dropOldestOutbox) Send(ctx context.Context, item any) error {
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
		_ = old
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

func (o *dropOldestOutbox) Dropped() int64 { return o.dropped.Load() }

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

// NewOutbox constructs the appropriate Outbox for the given overflow strategy.
func NewOutbox(ch chan any, overflow int, hook Hook, name string) Outbox {
	switch overflow {
	case OverflowDropNewest:
		return &dropNewestOutbox{ch: ch, hook: hook, name: name}
	case OverflowDropOldest:
		return &dropOldestOutbox{ch: ch, hook: hook, name: name}
	default:
		return &blockingOutbox{ch: ch}
	}
}
