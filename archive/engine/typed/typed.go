// Package typed is an experimental generic pipeline engine.
//
// It exists to validate two hypotheses about a potential v2 engine:
//
//  1. Using chan T instead of chan any eliminates the ~2 allocs/item cost from
//     interface boxing that the current engine pays at every stage boundary.
//  2. The append-stages design (each operator appends a typed goroutine closure
//     and returns a new Pipeline[O]) composes cleanly without a separate graph
//     compilation step.
//
// This package is NOT part of the public kitsune API. It may change or be
// removed at any time. The kitsune root package never imports it.
//
// # Design
//
// A Pipeline[T] is just a pair of (output channel, list of stage functions).
// Operators prepend a stage that reads from the previous output channel and
// writes to a new typed output channel, then return a new Pipeline[O]. Run
// launches all stages concurrently and waits for them to finish.
//
// # Limitations compared to the production engine
//
//   - No per-item error handling, retries, or dead-letter routing.
//   - No hooks, metrics, or sampling.
//   - No ordered-concurrent mode or supervision.
//   - Concurrency is always 1 per stage (sufficient to measure boxing overhead).
//
// These are intentional omissions — the goal is to measure the boxing overhead
// in isolation. Error handling and hooks can be layered on without changing the
// fundamental chan T transport.
package typed

import (
	"context"
	"errors"
	"sync"
)

const defaultBuffer = 16

// Pipeline is a lazily-evaluated sequence of typed stages. The zero value is
// not useful; construct one with FromSlice or FromChan.
type Pipeline[T any] struct {
	ch     chan T
	stages []func(ctx context.Context) error
}

// FromSlice creates a Pipeline whose source emits each item in items.
func FromSlice[T any](items []T) Pipeline[T] {
	ch := make(chan T, defaultBuffer)
	stage := func(ctx context.Context) error {
		defer close(ch)
		for _, item := range items {
			select {
			case ch <- item:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	return Pipeline[T]{ch: ch, stages: []func(context.Context) error{stage}}
}

// Map applies fn to each item, returning a new Pipeline[O].
func Map[I, O any](p Pipeline[I], fn func(I) O) Pipeline[O] {
	outCh := make(chan O, defaultBuffer)
	stage := func(ctx context.Context) error {
		defer close(outCh)
		for item := range p.ch {
			select {
			case outCh <- fn(item):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return ctx.Err()
	}
	return Pipeline[O]{
		ch:     outCh,
		stages: append(p.stages, stage),
	}
}

// Filter keeps only items for which keep returns true.
func Filter[T any](p Pipeline[T], keep func(T) bool) Pipeline[T] {
	outCh := make(chan T, defaultBuffer)
	stage := func(ctx context.Context) error {
		defer close(outCh)
		for item := range p.ch {
			if keep(item) {
				select {
				case outCh <- item:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return ctx.Err()
	}
	return Pipeline[T]{
		ch:     outCh,
		stages: append(p.stages, stage),
	}
}

// Drain runs the pipeline to completion, discarding all output items.
// Returns the first non-nil error from any stage.
func (p Pipeline[T]) Drain(ctx context.Context) error {
	drain := func(ctx context.Context) error {
		for range p.ch {
		}
		return ctx.Err()
	}
	return run(ctx, append(p.stages, drain))
}

// Collect runs the pipeline and returns all output items.
func (p Pipeline[T]) Collect(ctx context.Context) ([]T, error) {
	var out []T
	collect := func(ctx context.Context) error {
		for item := range p.ch {
			out = append(out, item)
		}
		return ctx.Err()
	}
	return out, run(ctx, append(p.stages, collect))
}

// run launches all stage functions concurrently, waits for completion, and
// returns the first error encountered (cancelling the context for the rest).
func run(ctx context.Context, stages []func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg      sync.WaitGroup
		once    sync.Once
		firstErr error
	)
	for _, s := range stages {
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s(ctx); err != nil && !errors.Is(err, context.Canceled) {
				once.Do(func() {
					firstErr = err
					cancel()
				})
			}
		}()
	}
	wg.Wait()
	return firstErr
}
