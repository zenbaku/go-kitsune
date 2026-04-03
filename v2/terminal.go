package kitsune

import (
	"context"

	"github.com/zenbaku/go-kitsune/v2/internal"
)

// ---------------------------------------------------------------------------
// ForEach — terminal stage
// ---------------------------------------------------------------------------

// ForEachRunner is a terminal stage that consumes all items from a pipeline
// by calling fn for each one. It is created by [Pipeline.ForEach].
type ForEachRunner[T any] struct {
	p  *Pipeline[T]
	fn func(context.Context, T) error
}

// ForEach returns a [ForEachRunner] that calls fn for every item in the pipeline.
// No processing occurs until [ForEachRunner.Run] is called.
func (p *Pipeline[T]) ForEach(fn func(context.Context, T) error, opts ...StageOption) *ForEachRunner[T] {
	return &ForEachRunner[T]{p: p, fn: fn}
}

// Build registers the ForEach terminal stage into the pipeline's stageList and
// returns a [Runner] that can be combined with other runners via [MergeRunners].
// Use this when the pipeline forks (e.g., [Partition], [Broadcast]) and you need
// to run multiple terminal stages together.
//
//	evens, odds := kitsune.Partition(p, isEven)
//	r1 := evens.ForEach(storeEven).Build()
//	r2 := odds.ForEach(logOdd).Build()
//	runner, _ := kitsune.MergeRunners(r1, r2)
//	runner.Run(ctx)
func (r *ForEachRunner[T]) Build() *Runner {
	p := r.p
	fn := r.fn
	inCh := p.ch

	meta := stageMeta{
		kind:        "sink",
		name:        "for_each",
		concurrency: 1,
		inputs:      []int{p.id},
	}
	termStage := func(runCtx context.Context) error {
		defer func() { go internal.DrainChan((<-chan T)(inCh)) }()
		for {
			select {
			case item, ok := <-inCh:
				if !ok {
					return nil
				}
				if err := fn(runCtx, item); err != nil {
					return err
				}
			case <-runCtx.Done():
				return runCtx.Err()
			}
		}
	}
	p.sl.add(termStage, meta)
	return &Runner{sl: p.sl}
}

// Run registers the ForEach terminal stage into the pipeline's stageList and
// executes the pipeline, blocking until completion.
func (r *ForEachRunner[T]) Run(ctx context.Context, opts ...RunOption) error {
	p := r.p
	fn := r.fn
	inCh := p.ch

	// Register the terminal stage into the shared stageList.
	meta := stageMeta{
		kind:        "sink",
		name:        "for_each",
		concurrency: 1,
		inputs:      []int{p.id},
	}
	termStage := func(runCtx context.Context) error {
		// On early exit, drain the input channel in a background goroutine
		// so that the upstream source is never left blocked trying to send.
		defer func() { go internal.DrainChan((<-chan T)(inCh)) }()
		for {
			select {
			case item, ok := <-inCh:
				if !ok {
					return nil
				}
				if err := fn(runCtx, item); err != nil {
					return err
				}
			case <-runCtx.Done():
				return runCtx.Err()
			}
		}
	}

	p.sl.add(termStage, meta)

	runner := &Runner{sl: p.sl}
	return runner.Run(ctx, opts...)
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
