package kitsune

import (
	"context"

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
		Buffer:       scfg.buffer,
		ErrorHandler: scfg.errorHandler,
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
