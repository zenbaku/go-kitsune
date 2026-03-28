package kitsune

import (
	"context"
	"sync/atomic"

	"github.com/jonathan/go-kitsune/internal/engine"
)

// ForEach creates a terminal stage that processes each item.
// Returns a [Runner] for deferred execution.
func (p *Pipeline[T]) ForEach(fn func(context.Context, T) error, opts ...StageOption) *Runner {
	wrapped := func(ctx context.Context, in any) error {
		return fn(ctx, in.(T))
	}
	scfg := buildStageConfig(opts)
	p.g.AddNode(&engine.Node{
		Kind:         engine.Sink,
		Name:         scfg.name,
		Fn:           wrapped,
		Inputs:       []engine.InputRef{{Node: p.node, Port: p.port}},
		Concurrency:  scfg.concurrency,
		Ordered:      scfg.ordered,
		Buffer:       scfg.buffer,
		Overflow:     scfg.overflow,
		ErrorHandler: scfg.errorHandler,
		Supervision:  scfg.supervision,
	})
	return &Runner{g: p.g}
}

// Drain creates a terminal that consumes and discards all items.
// Useful when side effects happen in [Pipeline.Tap] or [Map] stages.
func (p *Pipeline[T]) Drain() *Runner {
	p.g.AddNode(&engine.Node{
		Kind:         engine.Sink,
		Fn:           func(context.Context, any) error { return nil },
		Inputs:       []engine.InputRef{{Node: p.node, Port: p.port}},
		Concurrency:  1,
		Buffer:       engine.DefaultBuffer,
		ErrorHandler: engine.DefaultHandler{},
	})
	return &Runner{g: p.g}
}

// Collect runs the pipeline and materializes all output items into a slice.
func (p *Pipeline[T]) Collect(ctx context.Context, opts ...RunOption) ([]T, error) {
	var results []T
	runner := p.ForEach(func(_ context.Context, item T) error {
		results = append(results, item)
		return nil
	})
	if err := runner.Run(ctx, opts...); err != nil {
		return nil, err
	}
	return results, nil
}

// First runs the pipeline and returns the first item emitted.
// The second return value is false if the pipeline produced no items.
// The pipeline is stopped early as soon as the first item is received.
func (p *Pipeline[T]) First(ctx context.Context, opts ...RunOption) (T, bool, error) {
	results, err := p.Take(1).Collect(ctx, opts...)
	if err != nil {
		var zero T
		return zero, false, err
	}
	if len(results) == 0 {
		var zero T
		return zero, false, nil
	}
	return results[0], true, nil
}

// Last runs the pipeline and returns the final item emitted.
// The second return value is false if the pipeline produced no items.
func (p *Pipeline[T]) Last(ctx context.Context, opts ...RunOption) (T, bool, error) {
	results, err := p.Collect(ctx, opts...)
	if err != nil {
		var zero T
		return zero, false, err
	}
	if len(results) == 0 {
		var zero T
		return zero, false, nil
	}
	return results[len(results)-1], true, nil
}

// Count runs the pipeline and returns the total number of items emitted.
func (p *Pipeline[T]) Count(ctx context.Context, opts ...RunOption) (int64, error) {
	var n atomic.Int64
	runner := p.ForEach(func(_ context.Context, _ T) error {
		n.Add(1)
		return nil
	})
	if err := runner.Run(ctx, opts...); err != nil {
		return 0, err
	}
	return n.Load(), nil
}

// Any runs the pipeline and returns true if at least one item satisfies fn.
// The pipeline is stopped early as soon as a matching item is found.
func (p *Pipeline[T]) Any(ctx context.Context, fn func(T) bool, opts ...RunOption) (bool, error) {
	results, err := p.Filter(fn).Take(1).Collect(ctx, opts...)
	if err != nil {
		return false, err
	}
	return len(results) > 0, nil
}

// All runs the pipeline and returns true if every item satisfies fn.
// The pipeline is stopped early as soon as a non-matching item is found.
func (p *Pipeline[T]) All(ctx context.Context, fn func(T) bool, opts ...RunOption) (bool, error) {
	results, err := p.Filter(func(item T) bool { return !fn(item) }).Take(1).Collect(ctx, opts...)
	if err != nil {
		return false, err
	}
	return len(results) == 0, nil
}
