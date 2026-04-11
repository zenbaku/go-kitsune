package kitsune

import (
	"context"
	"fmt"
	"sync"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// SwitchMap
// ---------------------------------------------------------------------------

// SwitchMap transforms each input item into a sub-stream using fn (which calls
// yield for each output). When a new input item arrives, the current sub-stream
// is cancelled immediately and the new sub-stream begins.
// Only the outputs of the most-recently-started sub-stream are forwarded.
//
// Use Timeout(d) to impose a per-item deadline on fn; the context passed to fn
// is cancelled after d. Use Supervise to restart the stage loop on panic or
// unrecoverable error. Use Overflow to control the drop policy when the output
// channel is full.
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
		overflow:    cfg.overflow,
		timeout:     cfg.timeout,
		hasSuperv:   cfg.supervision.HasSupervision(),
		inputs:      []int{p.id},
	}
	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan O, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		cfg := cfg // local copy; resolve pipeline-level default handler
		cfg.errorHandler = resolveHandler(cfg, rc)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			inner := func() error {
				outbox := internal.NewOutbox(ch, cfg.overflow, hook, cfg.name)
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
				var loopErr error
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
								itemCtx, cancelItem := itemContext(ic, cfg)
								defer cancelItem()
								// Use ic for sends so a cancelled goroutine doesn't
								// block on a full channel or write to a closed one.
								send := func(v O) error { return outbox.Send(ic, v) }
								err, _ := internal.ProcessFlatMapItem(itemCtx, fn, it, cfg.errorHandler, send)
								if err != nil && err != internal.ErrSkipped && ic.Err() == nil {
									reportErr(errCh, internal.WrapStageErr(cfg.name, err, 0))
								}
							}(item, ic, cancel)

						case err := <-errCh:
							loopErr = err
							cancelCurrent()
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

				if loopErr != nil {
					return loopErr
				}
				select {
				case err := <-errCh:
					return err
				default:
					return nil
				}
			}
			return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
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
//
// Use Timeout(d) to impose a per-item deadline on fn; the context passed to fn
// is cancelled after d. Use Supervise to restart the stage loop on panic or
// unrecoverable error. Use Overflow to control the drop policy when the output
// channel is full.
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
		overflow:    cfg.overflow,
		timeout:     cfg.timeout,
		hasSuperv:   cfg.supervision.HasSupervision(),
		inputs:      []int{p.id},
	}
	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan O, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		cfg := cfg // local copy; resolve pipeline-level default handler
		cfg.errorHandler = resolveHandler(cfg, rc)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			inner := func() error {
				outbox := internal.NewOutbox(ch, cfg.overflow, hook, cfg.name)
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
								itemCtx, cancelItem := itemContext(ctx, cfg)
								defer cancelItem()
								send := func(v O) error { return outbox.Send(ctx, v) }
								err, _ := internal.ProcessFlatMapItem(itemCtx, fn, it, cfg.errorHandler, send)
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
			return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
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
	// Append last so this overrides any caller-supplied Concurrency option.
	opts = append(opts, Concurrency(1))
	return FlatMap(p, fn, opts...)
}

// ---------------------------------------------------------------------------
// MapResult
// ---------------------------------------------------------------------------

// ErrItem holds an input item alongside the error returned by its processing
// function. Produced by [MapResult] on the failed output pipeline.
type ErrItem[I any] struct {
	Item I
	Err  error
}

// MapResult applies fn to each item and routes results by outcome:
// successful outputs go to the first (ok) pipeline; failures go to the second
// (failed) pipeline as [ErrItem] values containing the original input and error.
//
//	ok, failed := kitsune.MapResult(p, fetchUser)
//	// ok:     *Pipeline[User]        — successful lookups
//	// failed: *Pipeline[ErrItem[ID]] — items that errored, with original input
//
// Both output pipelines must be consumed (same rule as [Partition]).
func MapResult[I, O any](p *Pipeline[I], fn func(context.Context, I) (O, error), opts ...StageOption) (*Pipeline[O], *Pipeline[ErrItem[I]]) {
	track(p)
	cfg := buildStageConfig(opts)
	okID := nextPipelineID()
	errID := nextPipelineID()

	okMeta := stageMeta{
		id:     okID,
		kind:   "map_result",
		name:   orDefault(cfg.name, "map_result"),
		buffer: cfg.buffer,
		inputs: []int{p.id},
	}
	errMeta := stageMeta{
		id:     errID,
		kind:   "map_result_err",
		name:   orDefault(cfg.name, "map_result") + "_err",
		buffer: cfg.buffer,
		inputs: []int{p.id},
	}

	sharedBuild := func(rc *runCtx) (chan O, chan ErrItem[I]) {
		if existing := rc.getChan(okID); existing != nil {
			return existing.(chan O), rc.getChan(errID).(chan ErrItem[I])
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		okC := make(chan O, buf)
		errC := make(chan ErrItem[I], buf)
		m := okMeta
		m.buffer = buf
		m.getChanLen = func() int { return len(okC) }
		m.getChanCap = func() int { return cap(okC) }
		rc.setChan(okID, okC)
		rc.setChan(errID, errC)
		okBox := internal.NewBlockingOutbox(okC)
		errBox := internal.NewBlockingOutbox(errC)
		stage := func(ctx context.Context) error {
			defer close(okC)
			defer close(errC)
			defer func() { go internal.DrainChan(inCh) }()
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					itemCtx, cancelItem := itemContext(ctx, cfg)
					val, err := fn(itemCtx, item)
					cancelItem()
					if err != nil {
						if sendErr := errBox.Send(ctx, ErrItem[I]{Item: item, Err: err}); sendErr != nil {
							return sendErr
						}
					} else {
						if sendErr := okBox.Send(ctx, val); sendErr != nil {
							return sendErr
						}
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return okC, errC
	}

	okP := newPipeline(okID, okMeta, func(rc *runCtx) chan O {
		o, _ := sharedBuild(rc)
		return o
	})
	errP := newPipeline(errID, errMeta, func(rc *runCtx) chan ErrItem[I] {
		_, e := sharedBuild(rc)
		return e
	})
	return okP, errP
}

// ---------------------------------------------------------------------------
// MapRecover
// ---------------------------------------------------------------------------

// MapRecover applies fn to each item. If fn returns an error or panics,
// recover is called with the original input and the error to produce a
// fallback output value. The output pipeline always emits one item per input.
func MapRecover[I, O any](p *Pipeline[I], fn func(context.Context, I) (O, error), recover func(context.Context, I, error) O, opts ...StageOption) *Pipeline[O] {
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
	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan O, buf)
		m := meta
		m.buffer = buf
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
					val, err := callRecover(ctx, fn, item, cfg)
					if err != nil {
						val = recover(ctx, item, err)
					}
					if sendErr := outbox.Send(ctx, val); sendErr != nil {
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

// callRecover calls fn(ctx, item) and recovers from panics, returning the
// value and error (panic values are converted to errors).
func callRecover[I, O any](ctx context.Context, fn func(context.Context, I) (O, error), item I, cfg stageConfig) (val O, err error) {
	defer func() {
		if p := recover(); p != nil {
			switch e := p.(type) {
			case error:
				err = e
			default:
				err = panicError{p}
			}
		}
	}()
	itemCtx, cancel := itemContext(ctx, cfg)
	defer cancel()
	return fn(itemCtx, item)
}

// panicError wraps a panic value as an error.
type panicError struct{ val any }

func (e panicError) Error() string {
	return fmt.Sprintf("panic: %v", e.val)
}
