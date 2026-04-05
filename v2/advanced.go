package kitsune

import (
	"context"
	"fmt"
	"sync"

	"github.com/zenbaku/go-kitsune/v2/internal"
)

// ---------------------------------------------------------------------------
// SwitchMap
// ---------------------------------------------------------------------------

// SwitchMap transforms each input item into a sub-stream using fn (which calls
// yield for each output). When a new input item arrives, the current sub-stream
// is cancelled immediately and the new sub-stream begins.
// Only the outputs of the most-recently-started sub-stream are forwarded.
func SwitchMap[I, O any](p *Pipeline[I], fn func(context.Context, I, func(O) error) error, opts ...StageOption) *Pipeline[O] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:          id,
		kind:        "switch_map",
		name:        orDefault(cfg.name, "switch_map"),
		concurrency: cfg.concurrency,
		buffer:      cfg.buffer,
		inputs:      []int{p.id},
	}
	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}
		inCh := p.build(rc)
		ch := make(chan O, cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			errCh := make(chan error, 1)

			var (
				mu          sync.Mutex
				innerCancel context.CancelFunc
			)
			// wg tracks ALL inner goroutines so we can wait for them all before
			// closing the output channel (even "cancelled" goroutines may still
			// be in flight).
			var wg sync.WaitGroup

			cancelCurrent := func() {
				mu.Lock()
				if innerCancel != nil {
					innerCancel()
					innerCancel = nil
				}
				mu.Unlock()
			}

			// naturalEnd tracks whether the outer input exhausted normally.
			// When true, we must NOT cancel the last sub-stream — it should be
			// allowed to complete. When false (error or ctx cancellation), we cancel.
			naturalEnd := func() bool {
				for {
					select {
					case item, ok := <-inCh:
						if !ok {
							return true
						}
						// Cancel the previous sub-stream.
						mu.Lock()
						if innerCancel != nil {
							innerCancel()
						}
						ic, cancel := context.WithCancel(ctx)
						innerCancel = cancel
						mu.Unlock()

						wg.Add(1)
						go func(it I, ic context.Context, c context.CancelFunc) {
							defer wg.Done()
							defer c()
							// Use ic for sends so a cancelled goroutine doesn't
							// block on a full channel or write to a closed one.
							send := func(v O) error { return outbox.Send(ic, v) }
							err, _ := internal.ProcessFlatMapItem(ic, fn, it, cfg.errorHandler, send)
							if err != nil && err != internal.ErrSkipped && ic.Err() == nil {
								reportErr(errCh, internal.WrapStageErr(cfg.name, err, 0))
							}
						}(item, ic, cancel)

					case err := <-errCh:
						cancelCurrent()
						_ = err
						return false
					case <-ctx.Done():
						cancelCurrent()
						return false
					}
				}
			}()

			if !naturalEnd {
				cancelCurrent()
			}
			wg.Wait() // wait for all goroutines before defer close(ch) runs

			select {
			case err := <-errCh:
				return err
			default:
				return nil
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}

// ---------------------------------------------------------------------------
// ExhaustMap
// ---------------------------------------------------------------------------

// ExhaustMap transforms each input item into a sub-stream using fn.
// While a sub-stream is in progress, new input items are dropped.
// Only when the current sub-stream finishes is the next item processed.
func ExhaustMap[I, O any](p *Pipeline[I], fn func(context.Context, I, func(O) error) error, opts ...StageOption) *Pipeline[O] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:          id,
		kind:        "exhaust_map",
		name:        orDefault(cfg.name, "exhaust_map"),
		concurrency: cfg.concurrency,
		buffer:      cfg.buffer,
		inputs:      []int{p.id},
	}
	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}
		inCh := p.build(rc)
		ch := make(chan O, cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			errCh := make(chan error, 1)

			var wg sync.WaitGroup
			// semaphore of 1: tracks whether an inner goroutine is active.
			sem := make(chan struct{}, 1)
			sem <- struct{}{} // initially available

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						wg.Wait() // wait for any active inner goroutine before closing ch
						select {
						case err := <-errCh:
							return err
						default:
							return nil
						}
					}
					// Non-blocking: only launch if no inner goroutine is running.
					select {
					case <-sem:
						wg.Add(1)
						go func(it I) {
							defer wg.Done()
							defer func() { sem <- struct{}{} }()
							send := func(v O) error { return outbox.Send(ctx, v) }
							err, _ := internal.ProcessFlatMapItem(ctx, fn, it, cfg.errorHandler, send)
							if err != nil && err != internal.ErrSkipped && ctx.Err() == nil {
								reportErr(errCh, internal.WrapStageErr(cfg.name, err, 0))
							}
						}(item)
					default:
						// Inner goroutine busy — drop item.
					}
				case err := <-errCh:
					wg.Wait()
					return err
				case <-ctx.Done():
					wg.Wait()
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}

// ---------------------------------------------------------------------------
// ConcatMap
// ---------------------------------------------------------------------------

// ConcatMap transforms each input item into a sub-stream using fn, processing
// sub-streams sequentially: the next item is only processed after the current
// sub-stream has completed. Output order is fully preserved.
// This is equivalent to FlatMap with Concurrency(1).
func ConcatMap[I, O any](p *Pipeline[I], fn func(context.Context, I, func(O) error) error, opts ...StageOption) *Pipeline[O] {
	// ConcatMap = serial FlatMap; enforce single concurrency.
	opts = append([]StageOption{Concurrency(1)}, opts...)
	return FlatMap(p, fn, opts...)
}

// ---------------------------------------------------------------------------
// MapResult
// ---------------------------------------------------------------------------

// Result holds either a successful value or an error from a fallible operation.
// Use [MapResult] to propagate errors as values without halting the pipeline.
type Result[T any] struct {
	Value T
	Err   error
}

// MapResult applies fn to each item and wraps the outcome in a [Result].
// Errors from fn are captured as Result.Err rather than halting the pipeline.
func MapResult[I, O any](p *Pipeline[I], fn func(context.Context, I) (O, error), opts ...StageOption) *Pipeline[Result[O]] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "map_result",
		name:   orDefault(cfg.name, "map_result"),
		buffer: cfg.buffer,
		inputs: []int{p.id},
	}
	build := func(rc *runCtx) chan Result[O] {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan Result[O])
		}
		inCh := p.build(rc)
		ch := make(chan Result[O], cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					itemCtx, cancelItem := itemContext(ctx, cfg)
					val, err := fn(itemCtx, item)
					cancelItem()
					r := Result[O]{Value: val, Err: err}
					if sendErr := outbox.Send(ctx, r); sendErr != nil {
						return sendErr
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}

// ---------------------------------------------------------------------------
// MapRecover
// ---------------------------------------------------------------------------

// MapRecover applies fn to each item, wrapping the result and any error in a
// [Result], including recovering from panics in fn. Panics are converted to
// errors and surfaced as Result.Err.
func MapRecover[I, O any](p *Pipeline[I], fn func(context.Context, I) (O, error), opts ...StageOption) *Pipeline[Result[O]] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "map_recover",
		name:   orDefault(cfg.name, "map_recover"),
		buffer: cfg.buffer,
		inputs: []int{p.id},
	}
	build := func(rc *runCtx) chan Result[O] {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan Result[O])
		}
		inCh := p.build(rc)
		ch := make(chan Result[O], cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					r := callRecover(ctx, fn, item, cfg)
					if sendErr := outbox.Send(ctx, r); sendErr != nil {
						return sendErr
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}

// callRecover calls fn(ctx, item) and recovers from panics.
func callRecover[I, O any](ctx context.Context, fn func(context.Context, I) (O, error), item I, cfg stageConfig) (r Result[O]) {
	defer func() {
		if p := recover(); p != nil {
			switch e := p.(type) {
			case error:
				r.Err = e
			default:
				r.Err = panicError{p}
			}
		}
	}()
	itemCtx, cancel := itemContext(ctx, cfg)
	defer cancel()
	val, err := fn(itemCtx, item)
	return Result[O]{Value: val, Err: err}
}

// panicError wraps a panic value as an error.
type panicError struct{ val any }

func (e panicError) Error() string {
	return fmt.Sprintf("panic: %v", e.val)
}
