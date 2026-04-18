package kitsune

import (
	"context"
	"math/rand"
	"sort"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
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

// FilterFunc wraps a simple predicate into the signature expected by [Filter].
// Use this when migrating code from a context-free predicate:
//
//	kitsune.Filter(p, kitsune.FilterFunc(func(n int) bool { return n > 0 }))
func FilterFunc[T any](fn func(T) bool) func(context.Context, T) (bool, error) {
	return func(_ context.Context, v T) (bool, error) {
		return fn(v), nil
	}
}

// RejectFunc wraps a simple predicate into the signature expected by [Reject].
func RejectFunc[T any](fn func(T) bool) func(context.Context, T) (bool, error) {
	return FilterFunc(fn)
}

// TapFunc wraps a simple side-effect function into the signature expected by [Tap].
func TapFunc[T any](fn func(T)) func(context.Context, T) error {
	return func(_ context.Context, v T) error {
		fn(v)
		return nil
	}
}

// TapErrorFunc wraps a simple error-observer function into the signature
// expected by [TapError].
func TapErrorFunc(fn func(error)) func(context.Context, error) {
	return func(_ context.Context, err error) {
		fn(err)
	}
}

// FinallyFunc wraps a simple cleanup function into the signature expected
// by [Finally].
func FinallyFunc(fn func(error)) func(context.Context, error) {
	return func(_ context.Context, err error) {
		fn(err)
	}
}

// ExpandMapFunc wraps a context-free child-factory function into the
// signature expected by [ExpandMap].
func ExpandMapFunc[T any](fn func(T) *Pipeline[T]) func(context.Context, T) *Pipeline[T] {
	return func(_ context.Context, item T) *Pipeline[T] {
		return fn(item)
	}
}

// ---------------------------------------------------------------------------
// StartWith / DefaultIfEmpty
// ---------------------------------------------------------------------------

// StartWith prepends one or more items to a pipeline before forwarding all
// items from p. Prefix items are always emitted before any item from p.
func StartWith[T any](p *Pipeline[T], items ...T) *Pipeline[T] {
	track(p)
	if len(items) == 0 {
		return p
	}
	// Use the factory-based Concat so the prefix runs to completion before p
	// starts, guaranteeing strict ordering.
	itemsCopy := items // capture for closure
	return Concat(
		func() *Pipeline[T] { return FromSlice(itemsCopy) },
		func() *Pipeline[T] { return p },
	)
}

// DefaultIfEmpty emits defaultVal if p completes without emitting any items;
// otherwise forwards all items from p unchanged.
func DefaultIfEmpty[T any](p *Pipeline[T], defaultVal T, opts ...StageOption) *Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "default_if_empty",
		name:   orDefault(cfg.name, "default_if_empty"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	var defaultIfEmptyOut *Pipeline[T]
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		rc.initDrainNotify(id, defaultIfEmptyOut.consumerCount.Load())
		drainCh := rc.drainCh(id)
		stage := func(ctx context.Context) error {
			defer close(ch)
			cooperativeDrain := false
			defer func() {
				if !cooperativeDrain {
					go internal.DrainChan(inCh)
				}
			}()
			defer func() { rc.signalDrain(p.id) }()

			outbox := internal.NewBlockingOutbox(ch)
			emitted := false

			for {
				select {
				case item, ok := <-inCh:
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
				case <-drainCh:
					cooperativeDrain = true
					return nil
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	defaultIfEmptyOut = newPipeline(id, meta, build)
	return defaultIfEmptyOut
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
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "timestamp",
		name:   orDefault(cfg.name, "timestamp"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	var timestampOut *Pipeline[Timestamped[T]]
	build := func(rc *runCtx) chan Timestamped[T] {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan Timestamped[T])
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan Timestamped[T], buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		rc.initDrainNotify(id, timestampOut.consumerCount.Load())
		drainCh := rc.drainCh(id)
		stage := func(ctx context.Context) error {
			defer close(ch)
			cooperativeDrain := false
			defer func() {
				if !cooperativeDrain {
					go internal.DrainChan(inCh)
				}
			}()
			defer func() { rc.signalDrain(p.id) }()

			clk := cfg.clock
			if clk == nil {
				clk = internal.RealClock{}
			}
			outbox := internal.NewBlockingOutbox(ch)

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					ts := Timestamped[T]{Value: item, Time: clk.Now()}
					if err := outbox.Send(ctx, ts); err != nil {
						return err
					}
				case <-ctx.Done():
					return ctx.Err()
				case <-drainCh:
					cooperativeDrain = true
					return nil
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	timestampOut = newPipeline(id, meta, build)
	return timestampOut
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
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "time_interval",
		name:   orDefault(cfg.name, "time_interval"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	var timeIntervalOut *Pipeline[TimedInterval[T]]
	build := func(rc *runCtx) chan TimedInterval[T] {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan TimedInterval[T])
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan TimedInterval[T], buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		rc.initDrainNotify(id, timeIntervalOut.consumerCount.Load())
		drainCh := rc.drainCh(id)
		stage := func(ctx context.Context) error {
			defer close(ch)
			cooperativeDrain := false
			defer func() {
				if !cooperativeDrain {
					go internal.DrainChan(inCh)
				}
			}()
			defer func() { rc.signalDrain(p.id) }()

			clk := cfg.clock
			if clk == nil {
				clk = internal.RealClock{}
			}
			outbox := internal.NewBlockingOutbox(ch)
			var last time.Time
			first := true

			for {
				select {
				case item, ok := <-inCh:
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
				case <-drainCh:
					cooperativeDrain = true
					return nil
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	timeIntervalOut = newPipeline(id, meta, build)
	return timeIntervalOut
}

// ---------------------------------------------------------------------------
// Sort / SortBy
// ---------------------------------------------------------------------------

// Sort collects all items, sorts them using less, and emits them in sorted order.
// The pipeline must be finite. Runs at Concurrency(1).
func Sort[T any](p *Pipeline[T], less func(a, b T) bool, opts ...StageOption) *Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "sort",
		name:   orDefault(cfg.name, "sort"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	var sortOut *Pipeline[T]
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		rc.initDrainNotify(id, sortOut.consumerCount.Load())
		drainCh := rc.drainCh(id)
		stage := func(ctx context.Context) error {
			defer close(ch)
			cooperativeDrain := false
			defer func() {
				if !cooperativeDrain {
					go internal.DrainChan(inCh)
				}
			}()
			defer func() { rc.signalDrain(p.id) }()

			outbox := internal.NewBlockingOutbox(ch)

			// Collect all items.
			collected, err := func() ([]T, error) {
				var out []T
				for {
					select {
					case item, ok := <-inCh:
						if !ok {
							return out, nil
						}
						out = append(out, item)
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-drainCh:
						cooperativeDrain = true
						return nil, nil
					}
				}
			}()
			if err != nil {
				return err
			}
			if cooperativeDrain {
				return nil
			}

			sort.Slice(collected, func(i, j int) bool { return less(collected[i], collected[j]) })
			for _, item := range collected {
				if err := outbox.Send(ctx, item); err != nil {
					return err
				}
			}
			return nil
		}
		rc.add(stage, m)
		return ch
	}
	sortOut = newPipeline(id, meta, build)
	return sortOut
}

// SortBy sorts items by their key K using the provided less function.
func SortBy[T any, K any](p *Pipeline[T], keyFn func(T) K, less func(a, b K) bool, opts ...StageOption) *Pipeline[T] {
	return Sort(p, func(a, b T) bool { return less(keyFn(a), keyFn(b)) }, opts...)
}

// ---------------------------------------------------------------------------
// Unzip
// ---------------------------------------------------------------------------

// Unzip splits a pipeline of [Pair][A, B] into two separate pipelines.
// Both output pipelines must be consumed (e.g., via [MergeRunners]).
func Unzip[A, B any](p *Pipeline[Pair[A, B]], opts ...StageOption) (*Pipeline[A], *Pipeline[B]) {
	track(p)
	cfg := buildStageConfig(opts)
	aID := nextPipelineID()
	bID := nextPipelineID()

	aMeta := stageMeta{
		id:     aID,
		kind:   "unzip",
		name:   orDefault(cfg.name, "unzip"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	bMeta := stageMeta{
		id:     bID,
		kind:   "unzip",
		name:   orDefault(cfg.name, "unzip") + "_b",
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}

	// sharedBuild creates both channels and the stage on the first call.
	sharedBuild := func(rc *runCtx) (chan A, chan B) {
		if existing := rc.getChan(aID); existing != nil {
			return existing.(chan A), rc.getChan(bID).(chan B)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		aCh := make(chan A, buf)
		bCh := make(chan B, buf)
		m := aMeta
		m.buffer = buf
		m.getChanLen = func() int { return len(aCh) }
		m.getChanCap = func() int { return cap(aCh) }
		rc.setChan(aID, aCh)
		rc.setChan(bID, bCh)
		stage := func(ctx context.Context) error {
			defer close(aCh)
			defer close(bCh)
			defer func() { go internal.DrainChan(inCh) }()

			aBox := internal.NewBlockingOutbox(aCh)
			bBox := internal.NewBlockingOutbox(bCh)

			for {
				select {
				case pair, ok := <-inCh:
					if !ok {
						return nil
					}
					if err := aBox.Send(ctx, pair.First); err != nil {
						return err
					}
					if err := bBox.Send(ctx, pair.Second); err != nil {
						return err
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return aCh, bCh
	}

	aP := newPipeline(aID, aMeta, func(rc *runCtx) chan A {
		a, _ := sharedBuild(rc)
		return a
	})
	bP := newPipeline(bID, bMeta, func(rc *runCtx) chan B {
		_, b := sharedBuild(rc)
		return b
	})
	return aP, bP
}

// ---------------------------------------------------------------------------
// Contains (terminal shorthand)
// ---------------------------------------------------------------------------

// Contains returns true if any item in the pipeline equals value.
// Processing stops early on the first match.
func Contains[T comparable](ctx context.Context, p *Pipeline[T], value T, opts ...RunOption) (bool, error) {
	return Any(ctx, p, func(v T) bool { return v == value }, opts...)
}

// ---------------------------------------------------------------------------
// MapIntersperse
// ---------------------------------------------------------------------------

// MapIntersperse applies fn to each item and inserts sep between consecutive
// mapped outputs (not before the first or after the last).
//
//	kitsune.MapIntersperse(words, ",", strings.ToUpper)
//	// "hello", "world" → "HELLO", ",", "WORLD"
func MapIntersperse[T, O any](p *Pipeline[T], sep O, fn func(context.Context, T) (O, error), opts ...StageOption) *Pipeline[O] {
	first := true
	return FlatMap(p, func(ctx context.Context, item T, yield func(O) error) error {
		out, err := fn(ctx, item)
		if err != nil {
			return err
		}
		if first {
			first = false
			return yield(out)
		}
		if err := yield(sep); err != nil {
			return err
		}
		return yield(out)
	}, opts...)
}

// ---------------------------------------------------------------------------
// RandomSample
// ---------------------------------------------------------------------------

// RandomSample passes each item with independent probability rate (0.0-1.0).
// Unlike [TakeRandom], which buffers the entire stream for reservoir
// sampling, RandomSample makes a per-item decision as items arrive. It is
// a streaming (non-barrier) operator.
func RandomSample[T any](p *Pipeline[T], rate float64, opts ...StageOption) *Pipeline[T] {
	return Filter(p, func(_ context.Context, _ T) (bool, error) {
		return rand.Float64() < rate, nil
	}, opts...)
}

// ---------------------------------------------------------------------------
// EndWith
// ---------------------------------------------------------------------------

// EndWith appends one or more items to p after it closes. Suffix items are
// always emitted after all items from p, in the order given.
//
//	kitsune.EndWith(kitsune.FromSlice([]int{1, 2, 3}), 4, 5)
//	// emits: 1, 2, 3, 4, 5
func EndWith[T any](p *Pipeline[T], items ...T) *Pipeline[T] {
	track(p)
	if len(items) == 0 {
		return p
	}
	itemsCopy := items
	return Concat(
		func() *Pipeline[T] { return p },
		func() *Pipeline[T] { return FromSlice(itemsCopy) },
	)
}
