package kitsune

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
)

// forEachFastPath is the drain-protocol + micro-batching fast path for ForEach/Drain.
// Conditions: Concurrency(1), DefaultHandler, OverflowBlock, no timeout, NoopHook.
// Skips hook calls, time.Now, and the per-item ctx.Done select.
func forEachFastPath[T any](inCh chan T, fn func(context.Context, T) error) stageFunc {
	return func(ctx context.Context) error {
		defer func() { go internal.DrainChan((<-chan T)(inCh)) }()

		var buf [internal.ReceiveBatchSize]T
		for {
			item, ok := <-inCh
			if !ok {
				return nil
			}
			buf[0] = item
			n := 1
			closed := false
		fillSink:
			for n < internal.ReceiveBatchSize {
				select {
				case v, ok2 := <-inCh:
					if !ok2 {
						closed = true
						break fillSink
					}
					buf[n] = v
					n++
				default:
					break fillSink
				}
			}
			for i := range n {
				it := buf[i]
				var zero T
				buf[i] = zero
				if err := fn(ctx, it); err != nil {
					return err
				}
			}
			if closed {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
}

// forEachSerial is the full-featured serial path: OnError, Supervise, hooks.
func forEachSerial[T any](inCh chan T, fn func(context.Context, T) error, cfg stageConfig, hook internal.Hook) stageFunc {
	adaptedFn := func(ctx context.Context, item T) (struct{}, error) {
		return struct{}{}, fn(ctx, item)
	}
	return func(ctx context.Context) error {
		defer func() { go internal.DrainChan((<-chan T)(inCh)) }()

		hook.OnStageStart(ctx, cfg.name)
		var processed, errs int64
		defer func() { hook.OnStageDone(ctx, cfg.name, processed, errs) }()

		inner := func() error {
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					itemCtx, cancelItem := itemContext(ctx, cfg)
					start := time.Now()
					_, err, attempt := internal.ProcessItem(itemCtx, adaptedFn, item, cfg.errorHandler)
					dur := time.Since(start)
					cancelItem()
					if err == internal.ErrSkipped {
						errs++
						hook.OnItem(ctx, cfg.name, dur, err)
						continue
					}
					if err != nil {
						errs++
						hook.OnItem(ctx, cfg.name, dur, err)
						return internal.WrapStageErr(cfg.name, err, attempt)
					}
					processed++
					hook.OnItem(ctx, cfg.name, dur, nil)
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

// forEachConcurrent runs n goroutines in parallel, each reading from the shared
// inCh and calling fn. There is no output channel — fn is the side effect.
func forEachConcurrent[T any](inCh chan T, fn func(context.Context, T) error, cfg stageConfig, hook internal.Hook) stageFunc {
	adaptedFn := func(ctx context.Context, item T) (struct{}, error) {
		return struct{}{}, fn(ctx, item)
	}
	return func(ctx context.Context) error {
		defer func() { go internal.DrainChan((<-chan T)(inCh)) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			sem := make(chan struct{}, cfg.concurrency)
			errCh := make(chan error, 1)
			var wg sync.WaitGroup

			func() {
				for {
					select {
					case item, ok := <-inCh:
						if !ok {
							return
						}
						select {
						case sem <- struct{}{}:
						case <-innerCtx.Done():
							return
						}
						wg.Add(1)
						go func(it T) {
							defer wg.Done()
							defer func() { <-sem }()

							itemCtx, cancelItem := itemContext(innerCtx, cfg)
							start := time.Now()
							_, err, attempt := internal.ProcessItem(itemCtx, adaptedFn, it, cfg.errorHandler)
							dur := time.Since(start)
							cancelItem()
							if err == internal.ErrSkipped {
								errCount.Add(1)
								hook.OnItem(ctx, cfg.name, dur, err)
								return
							}
							if err != nil {
								errCount.Add(1)
								hook.OnItem(ctx, cfg.name, dur, err)
								reportErr(errCh, internal.WrapStageErr(cfg.name, err, attempt))
								cancel()
								return
							}
							procCount.Add(1)
							hook.OnItem(ctx, cfg.name, dur, nil)
						}(item)
					case <-innerCtx.Done():
						return
					}
				}
			}()

			wg.Wait()
			select {
			case err := <-errCh:
				return err
			default:
				return nil
			}
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

// forEachOrdered runs n goroutines in parallel but calls fn for each item in
// input order. Workers execute fn concurrently; the drainer reads results in
// insertion order and reports errors deterministically.
func forEachOrdered[T any](inCh chan T, fn func(context.Context, T) error, cfg stageConfig, hook internal.Hook) stageFunc {
	adaptedFn := func(ctx context.Context, item T) (struct{}, error) {
		return struct{}{}, fn(ctx, item)
	}
	type result struct {
		dur time.Duration
		err error
		att int
	}
	return func(ctx context.Context) error {
		defer func() { go internal.DrainChan((<-chan T)(inCh)) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			sem := make(chan struct{}, cfg.concurrency)
			slots := make(chan chan result, cfg.concurrency*2)
			drainErrs := make(chan error, 1)

			// Drainer: reads slots in insertion order, reports errors in sequence.
			go func() {
				for slotCh := range slots {
					r := <-slotCh
					if r.err == internal.ErrSkipped {
						continue
					}
					if r.err != nil {
						errCount.Add(1)
						hook.OnItem(ctx, cfg.name, r.dur, r.err)
						cancel()
						go func() {
							for s := range slots {
								<-s
							}
						}()
						drainErrs <- internal.WrapStageErr(cfg.name, r.err, r.att)
						return
					}
					procCount.Add(1)
					hook.OnItem(ctx, cfg.name, r.dur, nil)
				}
				drainErrs <- nil
			}()

			// Dispatcher: reads input, assigns each item to an ordered slot.
			func() {
				for {
					var item T
					var ok bool
					select {
					case item, ok = <-inCh:
						if !ok {
							return
						}
					case <-innerCtx.Done():
						return
					}
					select {
					case sem <- struct{}{}:
					case <-innerCtx.Done():
						return
					}
					slotCh := make(chan result, 1)
					select {
					case slots <- slotCh:
					case <-innerCtx.Done():
						<-sem
						return
					}
					go func(it T, sc chan result) {
						defer func() { <-sem }()
						itemCtx, cancelItem := itemContext(innerCtx, cfg)
						start := time.Now()
						_, err, att := internal.ProcessItem(itemCtx, adaptedFn, it, cfg.errorHandler)
						dur := time.Since(start)
						cancelItem()
						sc <- result{dur: dur, err: err, att: att}
					}(item, slotCh)
				}
			}()

			for i := 0; i < cfg.concurrency; i++ {
				sem <- struct{}{}
			}
			close(slots)
			return <-drainErrs
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

// ---------------------------------------------------------------------------
// ForEach — terminal stage
// ---------------------------------------------------------------------------

// ForEachRunner is a terminal stage that consumes all items from a pipeline
// by calling fn for each one. It is created by [Pipeline.ForEach].
type ForEachRunner[T any] struct {
	runner *Runner
}

// ForEach returns a [ForEachRunner] that calls fn for every item in the pipeline.
// No processing occurs until [ForEachRunner.Run] is called.
//
// With Concurrency(n) > 1, n goroutines run fn in parallel. Add Ordered() to
// ensure fn is called in input order even with concurrency (workers execute in
// parallel; results are acknowledged in sequence).
//
// Use OnError to control what happens when fn returns an error (default: Halt).
// Skip drops the item and continues; Retry re-calls fn up to the configured limit.
// Use Supervise to restart the stage on error or panic.
func (p *Pipeline[T]) ForEach(fn func(context.Context, T) error, opts ...StageOption) *ForEachRunner[T] {
	track(p)
	cfg := buildStageConfig(opts)
	n := max(1, cfg.concurrency)
	meta := stageMeta{
		id:          nextPipelineID(),
		kind:        "sink",
		name:        orDefault(cfg.name, "for_each"),
		concurrency: n,
		inputs:      []int{p.id},
		hasSuperv:   cfg.supervision.HasSupervision(),
	}

	terminal := func(rc *runCtx) {
		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}

		// Typed build-time fusion: if the upstream set a fusionEntry AND is our sole
		// consumer AND cfg + hook satisfy fast-path conditions, compose everything into
		// one goroutine with zero inter-stage channel hops and zero boxing.
		// Fusion is only eligible in serial mode (no concurrency, no OnError, etc.).
		if p.fusionEntry != nil && p.consumerCount.Load() == 1 &&
			isFastPathEligibleCfg(cfg) && internal.IsNoopHook(hook) {
			stage := p.fusionEntry(rc, fn)
			rc.add(stage, meta)
			return
		}

		inCh := p.build(rc)
		var stage stageFunc
		switch {
		case n > 1 && cfg.ordered:
			stage = forEachOrdered(inCh, fn, cfg, hook)
		case n > 1:
			stage = forEachConcurrent(inCh, fn, cfg, hook)
		case isFastPathEligible(cfg, hook):
			stage = forEachFastPath(inCh, fn)
		default:
			stage = forEachSerial(inCh, fn, cfg, hook)
		}
		rc.add(stage, meta)
	}

	return &ForEachRunner[T]{runner: &Runner{terminal: terminal}}
}

// Build returns a [Runner] that can be combined with other runners via [MergeRunners].
// Use this when the pipeline forks (e.g., [Partition], [Broadcast]) and you need
// to run multiple terminal stages together.
//
//	evens, odds := kitsune.Partition(p, isEven)
//	r1 := evens.ForEach(storeEven).Build()
//	r2 := odds.ForEach(logOdd).Build()
//	runner, _ := kitsune.MergeRunners(r1, r2)
//	err := runner.Run(ctx)
//
// Returns [ErrNoRunners] if called with no arguments.
func (r *ForEachRunner[T]) Build() *Runner {
	return r.runner
}

// Run executes the pipeline, blocking until completion.
func (r *ForEachRunner[T]) Run(ctx context.Context, opts ...RunOption) error {
	return r.runner.Run(ctx, opts...)
}

// ---------------------------------------------------------------------------
// Drain — terminal stage (discard all items)
// ---------------------------------------------------------------------------

// DrainRunner is a terminal stage that discards all items from a pipeline.
// It is created by [Pipeline.Drain].
type DrainRunner[T any] struct {
	p *Pipeline[T]
}

// Drain returns a [DrainRunner] that discards every item in the pipeline.
// No processing occurs until [DrainRunner.Run] is called.
func (p *Pipeline[T]) Drain() *DrainRunner[T] {
	return &DrainRunner[T]{p: p}
}

// Build returns a [Runner] that can be combined with other runners via [MergeRunners].
func (r *DrainRunner[T]) Build() *Runner {
	return r.p.ForEach(func(_ context.Context, _ T) error { return nil }).Build()
}

// Run registers the Drain terminal stage and executes the pipeline.
func (r *DrainRunner[T]) Run(ctx context.Context, opts ...RunOption) error {
	return r.Build().Run(ctx, opts...)
}
