package kitsune

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"math/rand"
	"sync"
	"sync/atomic"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// Collect — gather all items into a slice
// ---------------------------------------------------------------------------

// Collect runs the pipeline and returns all emitted items as a slice.
// It is equivalent to:
//
//	var out []T
//	err := p.ForEach(func(_ context.Context, v T) error {
//	    out = append(out, v)
//	    return nil
//	}).Run(ctx)
func Collect[T any](ctx context.Context, p *Pipeline[T], opts ...RunOption) ([]T, error) {
	var out []T
	err := p.ForEach(func(_ context.Context, v T) error {
		out = append(out, v)
		return nil
	}).Run(ctx, opts...)
	return out, err
}

// ---------------------------------------------------------------------------
// First / Last
// ---------------------------------------------------------------------------

// First returns the first item emitted by the pipeline and cancels processing
// immediately after. Returns (zero, false, nil) if the pipeline emits no items.
func First[T any](ctx context.Context, p *Pipeline[T], opts ...RunOption) (T, bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var result T
	var found bool
	_ = p.ForEach(func(_ context.Context, v T) error {
		result = v
		found = true
		cancel()
		return context.Canceled
	}).Run(ctx, opts...)

	if !found {
		var zero T
		return zero, false, nil
	}
	return result, true, nil
}

// Last returns the last item emitted by the pipeline.
// Returns (zero, false, nil) if the pipeline emits no items.
func Last[T any](ctx context.Context, p *Pipeline[T], opts ...RunOption) (T, bool, error) {
	var result T
	var found bool
	err := p.ForEach(func(_ context.Context, v T) error {
		result = v
		found = true
		return nil
	}).Run(ctx, opts...)
	if err != nil {
		var zero T
		return zero, false, err
	}
	return result, found, nil
}

// ---------------------------------------------------------------------------
// Single
// ---------------------------------------------------------------------------

// SingleOption configures [Single] behaviour for empty pipelines.
type SingleOption func(*singleConfig)

type singleConfig struct {
	hasDefault bool
	defaultVal any
}

// OrDefault returns v when the pipeline emits no items, instead of an error.
func OrDefault[T any](v T) SingleOption {
	return func(cfg *singleConfig) {
		cfg.hasDefault = true
		cfg.defaultVal = v
	}
}

// OrZero returns the zero value of T when the pipeline emits no items,
// instead of an error. Equivalent to [OrDefault] with the zero value.
func OrZero[T any]() SingleOption {
	var zero T
	return OrDefault[T](zero)
}

// Single drains p and returns the single item it emits. It returns an error
// if the pipeline emits zero items (unless [OrDefault] or [OrZero] is
// provided) or more than one item (always an error).
func Single[T any](ctx context.Context, p *Pipeline[T], opts ...SingleOption) (T, error) {
	cfg := &singleConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	var result T
	count := 0
	err := p.ForEach(func(_ context.Context, v T) error {
		count++
		if count > 1 {
			return fmt.Errorf("kitsune: Single: pipeline emitted more than one item")
		}
		result = v
		return nil
	}).Run(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	if count == 0 {
		if cfg.hasDefault {
			v, ok := cfg.defaultVal.(T)
			if !ok {
				var zero T
				return zero, fmt.Errorf("kitsune: Single: OrDefault value type does not match Single[T] type parameter")
			}
			return v, nil
		}
		var zero T
		return zero, fmt.Errorf("kitsune: Single: pipeline emitted no items")
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Count
// ---------------------------------------------------------------------------

// Count returns the number of items emitted by the pipeline.
func Count[T any](ctx context.Context, p *Pipeline[T], opts ...RunOption) (int64, error) {
	var n int64
	err := p.ForEach(func(_ context.Context, _ T) error {
		n++
		return nil
	}).Run(ctx, opts...)
	return n, err
}

// ---------------------------------------------------------------------------
// Any / All
// ---------------------------------------------------------------------------

// Any returns true if at least one item satisfies pred. Processing stops
// as soon as the first matching item is found.
func Any[T any](ctx context.Context, p *Pipeline[T], pred func(T) bool, opts ...RunOption) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	found := false
	_ = p.ForEach(func(_ context.Context, v T) error {
		if pred(v) {
			found = true
			cancel()
			return context.Canceled
		}
		return nil
	}).Run(ctx, opts...)
	return found, nil
}

// All returns true if every item satisfies pred. Processing stops as soon as
// a non-matching item is found.
func All[T any](ctx context.Context, p *Pipeline[T], pred func(T) bool, opts ...RunOption) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	allMatch := true
	_ = p.ForEach(func(_ context.Context, v T) error {
		if !pred(v) {
			allMatch = false
			cancel()
			return context.Canceled
		}
		return nil
	}).Run(ctx, opts...)
	return allMatch, nil
}

// ---------------------------------------------------------------------------
// Find
// ---------------------------------------------------------------------------

// Find returns the first item satisfying pred, or (zero, false, nil) if none.
func Find[T any](ctx context.Context, p *Pipeline[T], pred func(T) bool, opts ...RunOption) (T, bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var result T
	found := false
	_ = p.ForEach(func(_ context.Context, v T) error {
		if pred(v) {
			result = v
			found = true
			cancel()
			return context.Canceled
		}
		return nil
	}).Run(ctx, opts...)
	return result, found, nil
}

// ---------------------------------------------------------------------------
// Sum / Min / Max / MinMax
// ---------------------------------------------------------------------------

// Numeric is the set of integer and floating-point types supported by
// [Sum], [Min], [Max], and related operators.
type Numeric interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr |
		~float32 | ~float64
}

// Sum returns the sum of all items. Returns 0 if the pipeline is empty.
func Sum[T Numeric](ctx context.Context, p *Pipeline[T], opts ...RunOption) (T, error) {
	var sum T
	err := p.ForEach(func(_ context.Context, v T) error {
		sum += v
		return nil
	}).Run(ctx, opts...)
	return sum, err
}

// Min returns the minimum item.
// Returns (zero, false, nil) if the pipeline emits no items.
func Min[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (T, bool, error) {
	var result T
	found := false
	err := p.ForEach(func(_ context.Context, v T) error {
		if !found || less(v, result) {
			result = v
			found = true
		}
		return nil
	}).Run(ctx, opts...)
	if err != nil {
		var zero T
		return zero, false, err
	}
	return result, found, nil
}

// Max returns the maximum item.
// Returns (zero, false, nil) if the pipeline emits no items.
func Max[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (T, bool, error) {
	return Min(ctx, p, func(a, b T) bool { return less(b, a) }, opts...)
}

// MinMaxResult holds the minimum and maximum items observed by [MinMax] in a single pass.
type MinMaxResult[T any] struct {
	Min T
	Max T
}

// MinMax returns both the minimum and maximum items in a single pass.
// Returns (zero, false, nil) if the pipeline emits no items.
func MinMax[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (MinMaxResult[T], bool, error) {
	var result MinMaxResult[T]
	found := false
	err := p.ForEach(func(_ context.Context, v T) error {
		if !found {
			result.Min = v
			result.Max = v
			found = true
			return nil
		}
		if less(v, result.Min) {
			result.Min = v
		}
		if less(result.Max, v) {
			result.Max = v
		}
		return nil
	}).Run(ctx, opts...)
	if err != nil {
		return MinMaxResult[T]{}, false, err
	}
	return result, found, nil
}

// MinBy returns the item with the smallest key returned by keyFn.
// Returns (zero, false, nil) if the pipeline emits no items.
func MinBy[T any, K any](ctx context.Context, p *Pipeline[T], keyFn func(T) K, less func(a, b K) bool, opts ...RunOption) (T, bool, error) {
	return Min(ctx, p, func(a, b T) bool { return less(keyFn(a), keyFn(b)) }, opts...)
}

// MaxBy returns the item with the largest key returned by keyFn.
// Returns (zero, false, nil) if the pipeline emits no items.
func MaxBy[T any, K any](ctx context.Context, p *Pipeline[T], keyFn func(T) K, less func(a, b K) bool, opts ...RunOption) (T, bool, error) {
	return Max(ctx, p, func(a, b T) bool { return less(keyFn(a), keyFn(b)) }, opts...)
}

// ---------------------------------------------------------------------------
// ReduceWhile
// ---------------------------------------------------------------------------

// ReduceWhile folds items into a single value using fn until fn signals stop.
// fn returns (newState, continueReducing). When continueReducing is false,
// the current state is returned immediately without consuming further items.
// If the source emits no items, initial is returned.
func ReduceWhile[T, S any](ctx context.Context, p *Pipeline[T], initial S, fn func(S, T) (S, bool), opts ...RunOption) (S, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	state := initial
	_ = p.ForEach(func(_ context.Context, v T) error {
		next, cont := fn(state, v)
		state = next
		if !cont {
			cancel()
			return context.Canceled
		}
		return nil
	}).Run(ctx, opts...)
	return state, nil
}

// ---------------------------------------------------------------------------
// TakeRandom — reservoir sampling
// ---------------------------------------------------------------------------

// TakeRandom returns a pipeline that buffers all items from p and emits a
// single []T containing a random sample of up to n items, selected using
// reservoir sampling (Algorithm R). Each item has an equal probability of
// being selected. The emitted slice has min(n, sourceSize) items. Order is
// not guaranteed. Use [Single] to collect the result.
func TakeRandom[T any](p *Pipeline[T], n int, opts ...StageOption) *Pipeline[[]T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "take_random",
		name:   orDefault(cfg.name, "take_random"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan []T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan []T)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan []T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()
			outbox := internal.NewBlockingOutbox(ch)

			if n <= 0 {
				return outbox.Send(ctx, []T{})
			}

			reservoir := make([]T, 0, n)
			i := 0
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						result := make([]T, len(reservoir))
						copy(result, reservoir)
						return outbox.Send(ctx, result)
					}
					i++
					if len(reservoir) < n {
						reservoir = append(reservoir, item)
					} else {
						j := rand.Intn(i)
						if j < n {
							reservoir[j] = item
						}
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
// ToMap
// ---------------------------------------------------------------------------

// ToMap collects items into a map using keyFn and valueFn.
// If two items produce the same key, the last one wins.
func ToMap[T any, K comparable, V any](ctx context.Context, p *Pipeline[T], keyFn func(T) K, valueFn func(T) V, opts ...RunOption) (map[K]V, error) {
	m := make(map[K]V)
	err := p.ForEach(func(_ context.Context, v T) error {
		m[keyFn(v)] = valueFn(v)
		return nil
	}).Run(ctx, opts...)
	return m, err
}

// ---------------------------------------------------------------------------
// SequenceEqual
// ---------------------------------------------------------------------------

// SequenceEqual returns true if a and b emit the same items in the same order
// and have the same length.
func SequenceEqual[T comparable](ctx context.Context, a, b *Pipeline[T], opts ...RunOption) (bool, error) {
	track(a)
	track(b)
	// Build a combined runner that reads from both channels in lockstep.
	// Reading a then b sequentially per pair is correct because SequenceEqual
	// requires positional equality; no parallelism is needed.
	equal := true
	inputs := []int64{a.id, b.id}

	terminal := func(rc *runCtx) {
		aCh := a.build(rc)
		bCh := b.build(rc)
		stage := func(stageCtx context.Context) error {
			defer func() {
				go internal.DrainChan(aCh)
				go internal.DrainChan(bCh)
			}()
			for {
				var av T
				var aok bool
				select {
				case av, aok = <-aCh:
				case <-stageCtx.Done():
					return stageCtx.Err()
				}

				var bv T
				var bok bool
				select {
				case bv, bok = <-bCh:
				case <-stageCtx.Done():
					return stageCtx.Err()
				}

				if !aok && !bok {
					return nil // both exhausted simultaneously — lengths match
				}
				if !aok || !bok || av != bv {
					equal = false
					return nil
				}
			}
		}
		rc.add(stage, stageMeta{kind: "sequence_equal", name: "sequence_equal", inputs: inputs})
	}

	runner := &Runner{terminal: terminal}
	err := runner.Run(ctx, opts...)
	if err != nil && !errors.Is(err, context.Canceled) {
		return false, err
	}
	return equal, nil
}

// ---------------------------------------------------------------------------
// Iter — Go 1.23 range-over-func
// ---------------------------------------------------------------------------

// Iter returns an iterator over all items emitted by the pipeline.
// Iter returns a pull-based iterator over the pipeline's output items and an
// error function. The iterator is suitable for use with range-over-func
// (Go 1.23+).
//
// The error function must be called after iteration completes — or after
// breaking out of the loop — to retrieve any pipeline execution error. It
// blocks until the pipeline finishes and is safe to call multiple times.
//
// If the caller breaks out of the loop early, the pipeline context is
// cancelled and the error function returns nil (the context.Canceled caused
// by the break is suppressed). If the caller's own context is cancelled, the
// error function returns context.Canceled.
//
//	seq, errFn := kitsune.Iter(ctx, p)
//	for item := range seq {
//	    process(item)
//	}
//	if err := errFn(); err != nil {
//	    log.Fatal(err)
//	}
func Iter[T any](ctx context.Context, p *Pipeline[T], opts ...RunOption) (iter.Seq[T], func() error) {
	ch := make(chan T, internal.DefaultBuffer)
	// pipelineCtx is intentionally NOT derived from ctx. If it were, cancelling
	// ctx would immediately cancel the errgroup context, which would cancel the
	// generator's stageCtx, causing the generator to exit and close its output
	// channel before the ForEach callback ever observes ctx.Done(). That race
	// makes the pipeline return nil instead of ctx.Err(). By keeping pipelineCtx
	// independent, the only way cancellation propagates into the pipeline is
	// through the ForEach callback's explicit ctx.Done() check below.
	pipelineCtx, pipelineCancel := context.WithCancel(context.Background())
	var callerBroke atomic.Bool

	handle := p.ForEach(func(_ context.Context, item T) error {
		select {
		case ch <- item:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-pipelineCtx.Done():
			return nil // stopped internally (caller broke or natural seq exit)
		}
	}).Build().RunAsync(pipelineCtx, opts...)

	go func() {
		<-handle.Done()
		close(ch)
	}()

	seq := iter.Seq[T](func(yield func(T) bool) {
		defer func() {
			pipelineCancel()
			for range ch { //nolint:revive
			}
		}()
		for item := range ch {
			if !yield(item) {
				callerBroke.Store(true)
				return
			}
		}
	})

	var (
		errOnce sync.Once
		errVal  error
	)
	errFn := func() error {
		errOnce.Do(func() {
			errVal = handle.Wait()
			if callerBroke.Load() && errors.Is(errVal, context.Canceled) {
				errVal = nil
			}
		})
		return errVal
	}

	return seq, errFn
}
