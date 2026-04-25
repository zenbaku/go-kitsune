package kitsune

import (
	"context"
	"errors"
	"iter"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// sourceStage builds a stageFunc that closes ch on exit and respects cancellation.
// itemFn is called to produce items; it receives:
//   - a send helper that delivers an item to ch (blocking, respects ctx, gate, and drainCh)
//   - the run context
//
// This helper exists so every source shares the same close logic.
func sourceStage[T any](ch chan T, gate *internal.Gate, drainCh <-chan struct{}, itemFn func(ctx context.Context, send func(T) error) error) stageFunc {
	return func(ctx context.Context) error {
		defer close(ch)

		send := func(item T) error {
			if gate != nil {
				if err := gate.Wait(ctx); err != nil {
					return err
				}
			}
			select {
			case ch <- item:
				return nil
			case <-drainCh:
				return errDrained
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		err := itemFn(ctx, send)
		if err == errDrained {
			return nil
		}
		return err
	}
}

// ---------------------------------------------------------------------------
// FromSlice
// ---------------------------------------------------------------------------

// FromSlice creates a Pipeline that emits each element of the slice.
func FromSlice[T any](items []T) *Pipeline[T] {
	cfg := buildStageConfig(nil)
	id := nextPipelineID()
	meta := stageMeta{
		id:          id,
		kind:        "source",
		name:        "from_slice",
		concurrency: 1,
		buffer:      cfg.buffer,
	}
	var out *Pipeline[T]
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		rc.initDrainNotify(id, out.consumerCount.Load())
		drainCh := rc.drainCh(id)
		gate := rc.gate
		var stage stageFunc
		if internal.IsNoopHook(rc.hook) && gate == nil {
			stage = func(ctx context.Context) error {
				defer close(ch)
				for _, item := range items {
					select {
					case ch <- item:
					case <-drainCh:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			}
		} else {
			stage = sourceStage(ch, gate, drainCh, func(ctx context.Context, send func(T) error) error {
				for _, item := range items {
					if err := send(item); err != nil {
						return err
					}
				}
				return nil
			})
		}
		rc.add(stage, m)
		return ch
	}
	out = newPipeline(id, meta, build)
	return out
}

// ---------------------------------------------------------------------------
// From
// ---------------------------------------------------------------------------

// From creates a Pipeline that reads from an existing channel.
// The pipeline completes when the channel is closed.
func From[T any](src <-chan T) *Pipeline[T] {
	cfg := buildStageConfig(nil)
	id := nextPipelineID()
	meta := stageMeta{
		id:          id,
		kind:        "source",
		name:        "from",
		concurrency: 1,
		buffer:      cfg.buffer,
	}
	var out *Pipeline[T]
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		rc.initDrainNotify(id, out.consumerCount.Load())
		drainCh := rc.drainCh(id)
		stage := sourceStage(ch, rc.gate, drainCh, func(ctx context.Context, send func(T) error) error {
			for {
				select {
				case item, ok := <-src:
					if !ok {
						return nil
					}
					if err := send(item); err != nil {
						return err
					}
				case <-drainCh:
					return errDrained
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		})
		rc.add(stage, m)
		return ch
	}
	out = newPipeline(id, meta, build)
	return out
}

// ---------------------------------------------------------------------------
// Generate
// ---------------------------------------------------------------------------

// Generate creates a Pipeline from a push-based source function.
// Call yield for each item. yield returns false if the pipeline is done
// (cancelled or downstream signalled completion). Generate handles
// backpressure internally: yield blocks when downstream is full.
//
// When to use Generate vs [NewChannel]: prefer Generate when the producer
// is a loop you can express inline (paginated APIs, cursors, polling).
// Prefer [NewChannel] when items arrive from external goroutines that the
// pipeline does not own (HTTP handlers, gRPC streams, callbacks).
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
	cfg := buildStageConfig(nil)
	id := nextPipelineID()
	meta := stageMeta{
		id:          id,
		kind:        "source",
		name:        "generate",
		concurrency: 1,
		buffer:      cfg.buffer,
	}
	var out *Pipeline[T]
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		rc.initDrainNotify(id, out.consumerCount.Load())
		drainCh := rc.drainCh(id)
		gate := rc.gate
		var stage stageFunc
		done := rc.done
		if internal.IsNoopHook(rc.hook) && gate == nil {
			// Fast path: yield selects between the output channel and the pipeline
			// done signal. The done channel is closed by early-exit stages (Take,
			// TakeWhile) so infinite sources (Ticker, Repeatedly, …) stop cleanly.
			//
			// A derived stageCtx is also cancelled when done closes, so blocking
			// external calls inside the generator (e.g. long-poll RPCs) are
			// interrupted even if the generator is not currently inside yield.
			//
			// If fn returns context.Canceled because stageCtx was cancelled by the
			// done signal (not by the parent ctx), we suppress it so it is not
			// mistaken for a pipeline error.
			stage = func(ctx context.Context) error {
				stageCtx, cancelStage := context.WithCancel(ctx)
				defer cancelStage()
				go func() {
					select {
					case <-done:
						cancelStage()
					case <-drainCh:
						cancelStage()
					case <-stageCtx.Done():
					}
				}()
				defer close(ch)
				err := fn(stageCtx, func(item T) bool {
					select {
					case ch <- item:
						return stageCtx.Err() == nil
					case <-drainCh:
						return false
					case <-done:
						return false
					}
				})
				if errors.Is(err, context.Canceled) && ctx.Err() == nil {
					return nil // cancelled by done signal or drain, not by parent context
				}
				return err
			}
		} else {
			stage = func(ctx context.Context) error {
				stageCtx, cancelStage := context.WithCancel(ctx)
				defer cancelStage()
				go func() {
					select {
					case <-done:
						cancelStage()
					case <-drainCh:
						cancelStage()
					case <-stageCtx.Done():
					}
				}()
				defer close(ch)
				err := fn(stageCtx, func(item T) bool {
					if gate != nil {
						if err := gate.Wait(stageCtx); err != nil {
							return false
						}
					}
					select {
					case ch <- item:
						return true
					case <-drainCh:
						return false
					case <-stageCtx.Done():
						return false
					case <-done:
						return false
					}
				})
				if errors.Is(err, context.Canceled) && ctx.Err() == nil {
					return nil // cancelled by done signal or drain, not by parent context
				}
				return err
			}
		}
		rc.add(stage, m)
		return ch
	}
	out = newPipeline(id, meta, build)
	return out
}

// ---------------------------------------------------------------------------
// FromIter
// ---------------------------------------------------------------------------

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
// Channel is safe for concurrent use: multiple goroutines may call Send and
// TrySend simultaneously. Close is idempotent and may be called from any goroutine.
//
// When to use Channel vs [Generate]: prefer Channel when items arrive from
// external goroutines you do not control (HTTP handlers, gRPC streams,
// callbacks). Prefer [Generate] when the producer is a loop you can express
// inline (paginated APIs, cursors, polling).
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
// Ticker
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
		clk = internal.RealClock{}
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
		clk = internal.RealClock{}
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

// Empty returns a Pipeline that completes immediately with no items.
// It is the identity element for pipeline composition: Merge(Empty[T](), p)
// behaves identically to p for any pipeline p.
// Useful as a base case in tests and as a placeholder in pipeline algebra.
func Empty[T any]() *Pipeline[T] {
	return Generate(func(_ context.Context, _ func(T) bool) error {
		return nil
	})
}

// Never returns a Pipeline that never emits any items and never completes
// until the context is cancelled. It is the absorbing element for Amb:
// Amb(Never[T](), p) emits whatever p emits.
// Useful as a placeholder in tests that assert on other branches, and as
// the identity element for Merge with respect to liveness.
func Never[T any]() *Pipeline[T] {
	return Generate(func(ctx context.Context, _ func(T) bool) error {
		<-ctx.Done()
		return ctx.Err()
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
			_, err := factory().ForEach(func(_ context.Context, item T) error {
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

// ---------------------------------------------------------------------------
// Amb
// ---------------------------------------------------------------------------

// Amb subscribes to all pipeline factories concurrently and forwards items
// exclusively from whichever factory emits first, cancelling all others.
// If no factory emits before ctx is cancelled, Amb produces no items.
// Each factory must create a new independent pipeline graph.
//
//	kitsune.Amb(
//	    func() *Pipeline[Result] { return fetchFromPrimaryDB(ctx) },
//	    func() *Pipeline[Result] { return fetchFromReplicaDB(ctx) },
//	)
func Amb[T any](factories ...func() *Pipeline[T]) *Pipeline[T] {
	if len(factories) == 0 {
		return Generate(func(_ context.Context, _ func(T) bool) error { return nil })
	}
	if len(factories) == 1 {
		return Generate(func(ctx context.Context, yield func(T) bool) error {
			_, err := factories[0]().ForEach(func(_ context.Context, item T) error {
				if !yield(item) {
					return context.Canceled
				}
				return nil
			}).Run(ctx)
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		})
	}
	return Generate(func(ctx context.Context, yield func(T) bool) error {
		innerCtx, innerCancel := context.WithCancel(ctx)
		defer innerCancel()

		type ambItem struct {
			val     T
			factory int
		}

		// Per-factory item channels and per-factory contexts so that
		// cancelling a non-winner does not interrupt the winner's goroutine.
		chs := make([]chan ambItem, len(factories))
		factoryCtxs := make([]context.Context, len(factories))
		factoryCancels := make([]context.CancelFunc, len(factories))
		for i := range factories {
			chs[i] = make(chan ambItem, internal.DefaultBuffer)
			factoryCtxs[i], factoryCancels[i] = context.WithCancel(innerCtx)
		}
		defer func() {
			for _, cancel := range factoryCancels {
				cancel()
			}
		}()

		var (
			wg       sync.WaitGroup
			errOnce  sync.Once
			firstErr error
		)

		// Start each factory in its own goroutine using its own context,
		// so only non-winners are cancelled when a winner is determined.
		for i, factory := range factories {
			i, factory := i, factory
			wg.Add(1)
			go func() {
				defer wg.Done()
				fCtx := factoryCtxs[i]
				_, err := factory().ForEach(func(_ context.Context, v T) error {
					select {
					case chs[i] <- ambItem{val: v, factory: i}:
						return nil
					case <-fCtx.Done():
						return fCtx.Err()
					}
				}).Run(fCtx)
				if err != nil && !errors.Is(err, context.Canceled) {
					errOnce.Do(func() { firstErr = err })
					innerCancel()
				}
				close(chs[i])
			}()
		}

		// Fan all per-factory channels into a merged channel. Each fanner
		// uses its factory's context so cancelled factories stop forwarding.
		merged := make(chan ambItem, internal.DefaultBuffer)
		go func() {
			var fanWg sync.WaitGroup
			for i, ch := range chs {
				i, ch := i, ch
				fanWg.Add(1)
				go func() {
					defer fanWg.Done()
					for it := range ch {
						select {
						case merged <- it:
						case <-factoryCtxs[i].Done():
							return
						}
					}
				}()
			}
			fanWg.Wait()
			close(merged)
		}()

		// Determine winner: first factory to emit. Cancel all non-winners
		// while keeping the winner's context alive so it drains completely.
		winner := -1
		for it := range merged {
			if winner == -1 {
				winner = it.factory
				for j, cancel := range factoryCancels {
					if j != winner {
						cancel()
					}
				}
			}
			if it.factory != winner {
				continue
			}
			if !yield(it.val) {
				return nil
			}
		}

		wg.Wait()
		return firstErr
	})
}

// ---------------------------------------------------------------------------
// Catch
// ---------------------------------------------------------------------------

// Catch provides stream-level error recovery. The primary pipeline p runs
// normally; if its execution returns a non-nil, non-context error, fn is
// called with that error and its returned fallback pipeline is subscribed
// instead — items already emitted by p are kept and the fallback's items
// follow them.
//
// If p completes without error the fallback is never started.
// If the context is cancelled during p the fallback is also skipped.
//
// p is run in an isolated sub-execution; callers may safely pass a pipeline
// that has not yet been consumed by any other stage.
//
//	kitsune.Catch(riskyPipeline, func(err error) *kitsune.Pipeline[Event] {
//	    log.Printf("primary failed (%v); switching to backup", err)
//	    return backupSource()
//	})
func Catch[T any](p *Pipeline[T], fn func(error) *Pipeline[T]) *Pipeline[T] {
	return Generate(func(ctx context.Context, yield func(T) bool) error {
		innerCtx, cancel := context.WithCancel(ctx)
		stopped := false
		_, err := p.ForEach(func(_ context.Context, item T) error {
			if !yield(item) {
				stopped = true
				cancel()
			}
			return nil
		}).Run(innerCtx)
		cancel()

		if stopped || err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Primary errored — subscribe to the fallback.
		fallback := fn(err)
		innerCtx2, cancel2 := context.WithCancel(ctx)
		_, err2 := fallback.ForEach(func(_ context.Context, item T) error {
			if !yield(item) {
				stopped = true
				cancel2()
			}
			return nil
		}).Run(innerCtx2)
		cancel2()

		if stopped {
			return nil
		}
		if errors.Is(err2, context.Canceled) && ctx.Err() == nil {
			return nil
		}
		return err2
	})
}

// Using acquires a resource, builds a pipeline from it, and guarantees the
// resource is released when the pipeline exits — regardless of whether it
// completes, errors, or is cancelled. It is the pipeline equivalent of a
// try-with-resources or defer pattern for resource-bound sources.
//
//	conn, runner := db.Acquire(ctx)
//	// becomes:
//	p := kitsune.Using(
//	    func(ctx context.Context) (*sql.Rows, error) { return db.QueryContext(ctx, q) },
//	    func(rows *sql.Rows) *kitsune.Pipeline[Row] { return kitsune.Unfold(rows, scanRow) },
//	    func(rows *sql.Rows) { rows.Close() },
//	)
//
// If acquire returns an error no items are emitted and release is not called.
// Otherwise release is always called exactly once.
func Using[T, R any](
	acquire func(context.Context) (R, error),
	build func(R) *Pipeline[T],
	release func(R),
) *Pipeline[T] {
	return Generate(func(ctx context.Context, yield func(T) bool) error {
		res, err := acquire(ctx)
		if err != nil {
			return err
		}
		defer release(res)

		innerCtx, cancel := context.WithCancel(ctx)
		stopped := false
		_, err = build(res).ForEach(func(_ context.Context, item T) error {
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
		return err
	})
}
