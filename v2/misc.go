package kitsune

import (
	"context"
	"sort"
	"time"

	"github.com/zenbaku/go-kitsune/v2/internal"
)

// ---------------------------------------------------------------------------
// LiftPure / LiftFallible
// ---------------------------------------------------------------------------

// LiftPure wraps a pure, context-free, infallible function into the signature
// expected by [Map]. This is the simplest way to adapt a plain transform:
//
//	doubled := kitsune.Map(p, kitsune.LiftPure(func(n int) int { return n * 2 }))
func LiftPure[I, O any](fn func(I) O) func(context.Context, I) (O, error) {
	return func(_ context.Context, v I) (O, error) {
		return fn(v), nil
	}
}

// LiftFallible wraps a context-free fallible function into the [Map] signature.
func LiftFallible[I, O any](fn func(I) (O, error)) func(context.Context, I) (O, error) {
	return func(_ context.Context, v I) (O, error) {
		return fn(v)
	}
}

// ---------------------------------------------------------------------------
// StartWith / DefaultIfEmpty
// ---------------------------------------------------------------------------

// StartWith prepends one or more items to a pipeline before forwarding all
// items from p.
func StartWith[T any](p *Pipeline[T], items ...T) *Pipeline[T] {
	if len(items) == 0 {
		return p
	}
	prefix := FromSlice(items)
	return Merge(prefix, p)
}

// DefaultIfEmpty emits defaultVal if p completes without emitting any items;
// otherwise forwards all items from p unchanged.
func DefaultIfEmpty[T any](p *Pipeline[T], defaultVal T, opts ...StageOption) *Pipeline[T] {
	cfg := buildStageConfig(opts)
	ch := make(chan T, cfg.buffer)
	meta := stageMeta{
		kind:       "default_if_empty",
		name:       orDefault(cfg.name, "default_if_empty"),
		buffer:     cfg.buffer,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	stage := func(ctx context.Context) error {
		defer close(ch)
		defer func() { go internal.DrainChan(p.ch) }()

		outbox := internal.NewBlockingOutbox(ch)
		emitted := false

		for {
			select {
			case item, ok := <-p.ch:
				if !ok {
					if !emitted {
						return outbox.Send(ctx, defaultVal)
					}
					return nil
				}
				emitted = true
				if err := outbox.Send(ctx, item); err != nil {
					return err
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	id := p.sl.add(stage, meta)
	return newPipeline(ch, p.sl, id)
}

// ---------------------------------------------------------------------------
// Timestamp / TimeInterval
// ---------------------------------------------------------------------------

// Timestamped pairs an item with the wall-clock time it was observed.
type Timestamped[T any] struct {
	Value T
	Time  time.Time
}

// Timestamp tags each item with the time it exits this stage.
// Respects [WithClock] for deterministic tests.
func Timestamp[T any](p *Pipeline[T], opts ...StageOption) *Pipeline[Timestamped[T]] {
	cfg := buildStageConfig(opts)
	ch := make(chan Timestamped[T], cfg.buffer)
	meta := stageMeta{
		kind:       "timestamp",
		name:       orDefault(cfg.name, "timestamp"),
		buffer:     cfg.buffer,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	stage := func(ctx context.Context) error {
		defer close(ch)
		defer func() { go internal.DrainChan(p.ch) }()

		clk := cfg.clock
		if clk == nil {
			clk = internal.RealClock{}
		}
		outbox := internal.NewBlockingOutbox(ch)

		for {
			select {
			case item, ok := <-p.ch:
				if !ok {
					return nil
				}
				ts := Timestamped[T]{Value: item, Time: clk.Now()}
				if err := outbox.Send(ctx, ts); err != nil {
					return err
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	id := p.sl.add(stage, meta)
	return newPipeline(ch, p.sl, id)
}

// TimedInterval pairs an item with the elapsed time since the previous item.
// The first item always has Elapsed == 0. Runs at Concurrency(1).
type TimedInterval[T any] struct {
	Value   T
	Elapsed time.Duration
}

// TimeInterval tags each item with the duration elapsed since the previous item.
// Respects [WithClock] for deterministic tests.
func TimeInterval[T any](p *Pipeline[T], opts ...StageOption) *Pipeline[TimedInterval[T]] {
	cfg := buildStageConfig(opts)
	ch := make(chan TimedInterval[T], cfg.buffer)
	meta := stageMeta{
		kind:       "time_interval",
		name:       orDefault(cfg.name, "time_interval"),
		buffer:     cfg.buffer,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	stage := func(ctx context.Context) error {
		defer close(ch)
		defer func() { go internal.DrainChan(p.ch) }()

		clk := cfg.clock
		if clk == nil {
			clk = internal.RealClock{}
		}
		outbox := internal.NewBlockingOutbox(ch)
		var last time.Time
		first := true

		for {
			select {
			case item, ok := <-p.ch:
				if !ok {
					return nil
				}
				now := clk.Now()
				var elapsed time.Duration
				if !first {
					elapsed = now.Sub(last)
				}
				first = false
				last = now
				ti := TimedInterval[T]{Value: item, Elapsed: elapsed}
				if err := outbox.Send(ctx, ti); err != nil {
					return err
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	id := p.sl.add(stage, meta)
	return newPipeline(ch, p.sl, id)
}

// ---------------------------------------------------------------------------
// Sort / SortBy
// ---------------------------------------------------------------------------

// Sort collects all items, sorts them using less, and emits them in sorted order.
// The pipeline must be finite. Runs at Concurrency(1).
func Sort[T any](p *Pipeline[T], less func(a, b T) bool, opts ...StageOption) *Pipeline[T] {
	cfg := buildStageConfig(opts)
	ch := make(chan T, cfg.buffer)
	meta := stageMeta{
		kind:       "sort",
		name:       orDefault(cfg.name, "sort"),
		buffer:     cfg.buffer,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	stage := func(ctx context.Context) error {
		defer close(ch)
		defer func() { go internal.DrainChan(p.ch) }()

		outbox := internal.NewBlockingOutbox(ch)
		var buf []T

		// Collect all items.
		for {
			select {
			case item, ok := <-p.ch:
				if !ok {
					goto sortAndEmit
				}
				buf = append(buf, item)
			case <-ctx.Done():
				return ctx.Err()
			}
		}

	sortAndEmit:
		sort.Slice(buf, func(i, j int) bool { return less(buf[i], buf[j]) })
		for _, item := range buf {
			if err := outbox.Send(ctx, item); err != nil {
				return err
			}
		}
		return nil
	}

	id := p.sl.add(stage, meta)
	return newPipeline(ch, p.sl, id)
}

// SortBy sorts items by their key K using the natural ordering of K.
func SortBy[T any, K interface{ ~int | ~int64 | ~float64 | ~string }](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[T] {
	return Sort(p, func(a, b T) bool { return keyFn(a) < keyFn(b) }, opts...)
}

// ---------------------------------------------------------------------------
// Unzip
// ---------------------------------------------------------------------------

// Unzip splits a pipeline of [Pair][A, B] into two separate pipelines.
// Both output pipelines must be consumed (e.g., via [MergeRunners]).
func Unzip[A, B any](p *Pipeline[Pair[A, B]], opts ...StageOption) (*Pipeline[A], *Pipeline[B]) {
	cfg := buildStageConfig(opts)
	aCh := make(chan A, cfg.buffer)
	bCh := make(chan B, cfg.buffer)

	meta := stageMeta{
		kind:       "unzip",
		name:       orDefault(cfg.name, "unzip"),
		buffer:     cfg.buffer,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(aCh) },
		getChanCap: func() int { return cap(aCh) },
	}

	stage := func(ctx context.Context) error {
		defer close(aCh)
		defer close(bCh)
		defer func() { go internal.DrainChan(p.ch) }()

		aBox := internal.NewBlockingOutbox(aCh)
		bBox := internal.NewBlockingOutbox(bCh)

		for {
			select {
			case pair, ok := <-p.ch:
				if !ok {
					return nil
				}
				if err := aBox.Send(ctx, pair.Left); err != nil {
					return err
				}
				if err := bBox.Send(ctx, pair.Right); err != nil {
					return err
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	id := p.sl.add(stage, meta)
	return newPipeline(aCh, p.sl, id), newPipeline(bCh, p.sl, id)
}

// ---------------------------------------------------------------------------
// Contains (terminal shorthand)
// ---------------------------------------------------------------------------

// Contains returns true if any item in the pipeline equals value.
// Processing stops early on the first match.
func Contains[T comparable](ctx context.Context, p *Pipeline[T], value T, opts ...RunOption) (bool, error) {
	return Any(ctx, p, func(v T) bool { return v == value }, opts...)
}

// ElementAt returns the item at the given 0-based index.
// Returns (zero, false, nil) if the pipeline has fewer than index+1 items.
func ElementAt[T any](ctx context.Context, p *Pipeline[T], index int, opts ...RunOption) (T, bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var result T
	found := false
	i := 0
	_ = p.ForEach(func(_ context.Context, v T) error {
		if i == index {
			result = v
			found = true
			cancel()
			return context.Canceled
		}
		i++
		return nil
	}).Run(ctx, opts...)

	return result, found, nil
}
