package kitsune

import (
	"context"
	"errors"
	"iter"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zenbaku/go-kitsune/engine"
)

// From creates a Pipeline that reads from an existing channel.
// The pipeline completes when the channel is closed.
func From[T any](ch <-chan T) *Pipeline[T] {
	g := engine.New()
	fn := func(ctx context.Context, yield func(any) bool) error {
		for {
			select {
			case item, ok := <-ch:
				if !ok {
					return nil
				}
				if !yield(item) {
					return nil
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	id := g.AddNode(&engine.Node{Kind: engine.Source, Fn: fn})
	return &Pipeline[T]{g: g, node: id}
}

// FromSlice creates a Pipeline that emits each element of the slice.
func FromSlice[T any](items []T) *Pipeline[T] {
	g := engine.New()
	fn := func(ctx context.Context, yield func(any) bool) error {
		for _, item := range items {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !yield(item) {
				return nil
			}
		}
		return nil
	}
	id := g.AddNode(&engine.Node{Kind: engine.Source, Fn: fn})
	return &Pipeline[T]{g: g, node: id}
}

// Generate creates a Pipeline from a push-based source function.
// Call yield for each item. yield returns false if the pipeline is done
// (cancelled or downstream signalled completion). Generate handles
// backpressure internally — yield blocks when downstream is full.
//
//	kitsune.Generate(func(ctx context.Context, yield func(Record) bool) error {
//	    for cursor := ""; ; {
//	        page, next, err := api.Fetch(ctx, cursor)
//	        if err != nil { return err }
//	        for _, r := range page {
//	            if !yield(r) { return nil }
//	        }
//	        if next == "" { return nil }
//	        cursor = next
//	    }
//	})
func Generate[T any](fn func(ctx context.Context, yield func(T) bool) error) *Pipeline[T] {
	g := engine.New()
	wrapped := func(ctx context.Context, yield func(any) bool) error {
		return fn(ctx, func(item T) bool { return yield(item) })
	}
	id := g.AddNode(&engine.Node{Kind: engine.Source, Fn: wrapped})
	return &Pipeline[T]{g: g, node: id}
}

// FromIter creates a Pipeline from a Go iterator ([iter.Seq]).
//
//	p := kitsune.FromIter(slices.Values(items))
func FromIter[T any](seq iter.Seq[T]) *Pipeline[T] {
	return Generate(func(ctx context.Context, yield func(T) bool) error {
		for item := range seq {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !yield(item) {
				return nil
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Channel[T] — push-based source
// ---------------------------------------------------------------------------

// ErrChannelClosed is returned by [Channel.Send] when the channel has been closed.
var ErrChannelClosed = errors.New("kitsune: channel closed")

// Channel[T] is a push-based source that lets external code feed items into a
// pipeline. Create with [NewChannel], connect to a pipeline with [Channel.Source],
// then push items with [Channel.Send] or [Channel.TrySend]. Call [Channel.Close]
// when no more items will be sent so the pipeline can drain and exit cleanly.
//
// Channel is safe for concurrent use — multiple goroutines may call Send and
// TrySend simultaneously. Close is idempotent and may be called from any goroutine.
type Channel[T any] struct {
	ch      chan T
	once    sync.Once
	closed  atomic.Bool
	mu      sync.RWMutex
	sourced atomic.Bool
}

// NewChannel creates a push-based source with an internal buffer of the given size.
// The buffer decouples producers from the pipeline's processing rate.
// A buffer of 0 creates an unbuffered channel — Send blocks until the pipeline
// consumes the item, and TrySend always returns false unless a consumer is ready.
func NewChannel[T any](buffer int) *Channel[T] {
	return &Channel[T]{ch: make(chan T, buffer)}
}

// Source returns the [*Pipeline] for this channel. Panics if called more than once
// (single-consumer rule — use [Broadcast] if multiple consumers are needed).
func (c *Channel[T]) Source() *Pipeline[T] {
	if !c.sourced.CompareAndSwap(false, true) {
		panic("kitsune: Channel.Source called more than once")
	}
	return From((<-chan T)(c.ch))
}

// Send pushes an item into the channel. It blocks if the buffer is full (backpressure).
// Returns [ErrChannelClosed] if the channel has been closed, or ctx.Err() if the
// context is cancelled while waiting for buffer space.
func (c *Channel[T]) Send(ctx context.Context, item T) error {
	if c.closed.Load() {
		return ErrChannelClosed
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed.Load() {
		return ErrChannelClosed
	}
	select {
	case c.ch <- item:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TrySend pushes an item without blocking.
// Returns false if the buffer is full or the channel is already closed.
func (c *Channel[T]) TrySend(item T) bool {
	if c.closed.Load() {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed.Load() {
		return false
	}
	select {
	case c.ch <- item:
		return true
	default:
		return false
	}
}

// Close signals that no more items will be sent. Safe to call multiple times
// (idempotent). The pipeline drains remaining buffered items and exits cleanly.
func (c *Channel[T]) Close() {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed.Store(true)
		close(c.ch)
		c.mu.Unlock()
	})
}

// ---------------------------------------------------------------------------
// Scheduled sources
// ---------------------------------------------------------------------------

// Ticker emits the current [time.Time] at regular intervals.
// The first tick fires after d. The pipeline runs until the context is cancelled.
// Pass [WithClock] to use a deterministic clock for testing.
//
//	p := kitsune.Ticker(5 * time.Second)
//	p.Take(10) // collect 10 ticks then stop
func Ticker(d time.Duration, opts ...StageOption) *Pipeline[time.Time] {
	cfg := buildStageConfig(opts)
	clk := cfg.clock
	if clk == nil {
		clk = engine.RealClock{}
	}
	return Generate(func(ctx context.Context, yield func(time.Time) bool) error {
		ticker := clk.NewTicker(d)
		defer ticker.Stop()
		for {
			select {
			case t := <-ticker.C():
				if !yield(t) {
					return nil
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
}

// Interval emits a monotonically increasing int64 (0, 1, 2, …) at regular intervals.
// The first value fires after d. The pipeline runs until the context is cancelled.
// Pass [WithClock] to use a deterministic clock for testing.
//
//	p := kitsune.Interval(time.Second)
//	p.Take(5) // → 0, 1, 2, 3, 4
func Interval(d time.Duration, opts ...StageOption) *Pipeline[int64] {
	cfg := buildStageConfig(opts)
	clk := cfg.clock
	if clk == nil {
		clk = engine.RealClock{}
	}
	return Generate(func(ctx context.Context, yield func(int64) bool) error {
		ticker := clk.NewTicker(d)
		defer ticker.Stop()
		var i int64
		for {
			select {
			case <-ticker.C():
				if !yield(i) {
					return nil
				}
				i++
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Unfold / Iterate / Repeatedly / Cycle
// ---------------------------------------------------------------------------

// Unfold generates a stream by repeatedly applying fn to a seed value.
// fn receives the current accumulator and returns (value, nextAcc, stop).
// When stop is true, the stream ends without emitting value.
//
//	// Fibonacci sequence
//	kitsune.Unfold([2]int{0, 1}, func(s [2]int) (int, [2]int, bool) {
//	    return s[0], [2]int{s[1], s[0] + s[1]}, false
//	}).Take(8)
//	// → 0, 1, 1, 2, 3, 5, 8, 13
func Unfold[S, T any](seed S, fn func(S) (T, S, bool)) *Pipeline[T] {
	return Generate(func(_ context.Context, yield func(T) bool) error {
		acc := seed
		for {
			val, next, stop := fn(acc)
			if stop {
				return nil
			}
			if !yield(val) {
				return nil
			}
			acc = next
		}
	})
}

// Iterate creates an infinite stream starting with seed where each subsequent
// value is produced by applying fn to the previous value.
// Use [Pipeline.Take] or [TakeWhile] to bound the output.
//
//	kitsune.Iterate(1, func(n int) int { return n * 2 }).Take(5)
//	// → 1, 2, 4, 8, 16
func Iterate[T any](seed T, fn func(T) T) *Pipeline[T] {
	return Generate(func(_ context.Context, yield func(T) bool) error {
		cur := seed
		for {
			if !yield(cur) {
				return nil
			}
			cur = fn(cur)
		}
	})
}

// Repeatedly creates an infinite stream by calling fn on each iteration.
// Use [Pipeline.Take] or [TakeWhile] to bound the output.
//
//	// Emit a random number on every tick.
//	kitsune.Repeatedly(rand.Int).Take(5)
func Repeatedly[T any](fn func() T) *Pipeline[T] {
	return Generate(func(_ context.Context, yield func(T) bool) error {
		for {
			if !yield(fn()) {
				return nil
			}
		}
	})
}

// Cycle creates an infinite stream that repeatedly loops over items.
// Panics if items is empty.
//
//	kitsune.Cycle([]string{"a","b","c"}).Take(7)
//	// → "a","b","c","a","b","c","a"
func Cycle[T any](items []T) *Pipeline[T] {
	if len(items) == 0 {
		panic("kitsune: Cycle requires a non-empty slice")
	}
	return Generate(func(ctx context.Context, yield func(T) bool) error {
		for {
			for _, item := range items {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if !yield(item) {
					return nil
				}
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Timer
// ---------------------------------------------------------------------------

// Timer emits a single value after delay by calling fn, then closes.
// The pipeline produces exactly one item unless the context is cancelled first.
// Pass [WithClock] to use a deterministic clock for testing.
//
//	// Emit a heartbeat message after 5 seconds.
//	kitsune.Timer(5*time.Second, func() string { return "ping" })
func Timer[T any](delay time.Duration, fn func() T, opts ...StageOption) *Pipeline[T] {
	cfg := buildStageConfig(opts)
	clk := cfg.clock
	if clk == nil {
		clk = engine.RealClock{}
	}
	return Generate(func(ctx context.Context, yield func(T) bool) error {
		select {
		case <-clk.After(delay):
			yield(fn())
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}

// ---------------------------------------------------------------------------
// Concat
// ---------------------------------------------------------------------------

// Concat runs each pipeline factory sequentially, forwarding all items from
// each before starting the next. Because each factory creates its own graph,
// the sources are expressed as factory functions rather than *Pipeline values.
//
//	kitsune.Concat(
//	    func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{1, 2}) },
//	    func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{3, 4}) },
//	)
//	// → 1, 2, 3, 4
func Concat[T any](factories ...func() *Pipeline[T]) *Pipeline[T] {
	return Generate(func(ctx context.Context, yield func(T) bool) error {
		for _, factory := range factories {
			innerCtx, cancel := context.WithCancel(ctx)
			stopped := false
			err := factory().ForEach(func(_ context.Context, item T) error {
				if !yield(item) {
					stopped = true
					cancel()
				}
				return nil
			}).Run(innerCtx)
			cancel()
			if stopped {
				return nil
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		return nil
	})
}
