package kitsune

import (
	"context"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
)

// forEachFastPath is the drain-protocol + micro-batching fast path for ForEach/Drain.
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
func (p *Pipeline[T]) ForEach(fn func(context.Context, T) error, opts ...StageOption) *ForEachRunner[T] {
	track(p)
	cfg := buildStageConfig(opts)
	meta := stageMeta{
		id:          nextPipelineID(),
		kind:        "sink",
		name:        orDefault(cfg.name, "for_each"),
		concurrency: 1,
		inputs:      []int{p.id},
	}

	terminal := func(rc *runCtx) {
		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}

		// Typed build-time fusion: if the upstream set a fusionEntry AND is our sole
		// consumer AND cfg + hook satisfy fast-path conditions, compose everything into
		// one goroutine with zero inter-stage channel hops and zero boxing.
		if p.fusionEntry != nil && p.consumerCount.Load() == 1 &&
			isFastPathEligibleCfg(cfg) && internal.IsNoopHook(hook) {
			stage := p.fusionEntry(rc, fn)
			rc.add(stage, meta)
			return
		}

		inCh := p.build(rc)
		var stage stageFunc
		if isFastPathEligible(cfg, hook) {
			stage = forEachFastPath(inCh, fn)
		} else {
			stage = func(ctx context.Context) error {
				defer func() { go internal.DrainChan((<-chan T)(inCh)) }()
				hook.OnStageStart(ctx, meta.name)
				var processed, errs int64
				defer func() { hook.OnStageDone(ctx, meta.name, processed, errs) }()
				inner := func() error {
					for {
						select {
						case item, ok := <-inCh:
							if !ok {
								return nil
							}
							start := time.Now()
							err := fn(ctx, item)
							dur := time.Since(start)
							if err != nil {
								errs++
								hook.OnItem(ctx, meta.name, dur, err)
								return err
							}
							processed++
							hook.OnItem(ctx, meta.name, dur, nil)
						case <-ctx.Done():
							return ctx.Err()
						}
					}
				}
				return internal.Supervise(ctx, cfg.supervision, hook, meta.name, inner)
			}
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
//	runner.Run(ctx)
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

// Run registers the Drain terminal stage and executes the pipeline.
func (r *DrainRunner[T]) Run(ctx context.Context, opts ...RunOption) error {
	return r.p.ForEach(func(_ context.Context, _ T) error { return nil }).Run(ctx, opts...)
}
