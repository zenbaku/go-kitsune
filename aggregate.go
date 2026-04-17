package kitsune

import (
	"context"
	"fmt"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// Scan
// ---------------------------------------------------------------------------

// Scan accumulates state across items using fn, emitting the running state
// after each item. The first emission is fn(initial, firstItem).
func Scan[T, S any](p *Pipeline[T], initial S, fn func(S, T) S, opts ...StageOption) *Pipeline[S] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "scan",
		name:   orDefault(cfg.name, "scan"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan S {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan S)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan S, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			state := initial

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					state = fn(state, item)
					if err := outbox.Send(ctx, state); err != nil {
						return err
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
// Reduce
// ---------------------------------------------------------------------------

// Reduce folds all items into a single value using fn. The result is emitted
// once when the source completes. If the source emits no items, initial is emitted.
func Reduce[T, S any](p *Pipeline[T], initial S, fn func(S, T) S, opts ...StageOption) *Pipeline[S] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "reduce",
		name:   orDefault(cfg.name, "reduce"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan S {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan S)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan S, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			state := initial

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return outbox.Send(ctx, state)
					}
					state = fn(state, item)
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
// Distinct / DistinctBy
// ---------------------------------------------------------------------------

// Distinct emits only items that have not been seen before, using == equality.
func Distinct[T comparable](p *Pipeline[T], opts ...StageOption) *Pipeline[T] {
	return DistinctBy(p, func(v T) T { return v }, opts...)
}

// DistinctBy emits only items whose key (returned by keyFn) has not been seen
// before. Items with duplicate keys are silently dropped. An in-memory map
// is used as the dedup backend; external backends (WithDedupSet) are not
// supported here: use Dedupe or DedupeBy if you need a custom backend.
func DistinctBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "distinct_by",
		name:   orDefault(cfg.name, "distinct_by"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
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
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()
			outbox := internal.NewBlockingOutbox(ch)
			seen := make(map[K]struct{})
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					k := keyFn(item)
					if _, dup := seen[k]; dup {
						continue
					}
					seen[k] = struct{}{}
					if err := outbox.Send(ctx, item); err != nil {
						return err
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
// Dedupe
// ---------------------------------------------------------------------------

// Dedupe drops duplicate items using == equality. By default, duplicates
// are suppressed globally for the lifetime of the pipeline. Use
// [DedupeWindow] to restrict suppression to a sliding window, or
// [WithDedupSet] to plug in an external backend with expiry or
// probabilistic semantics.
func Dedupe[T comparable](p *Pipeline[T], opts ...StageOption) *Pipeline[T] {
	return DedupeBy(p, func(v T) T { return v }, opts...)
}

// DedupeBy drops items whose key (returned by keyFn) duplicates a
// previously seen key. See [Dedupe] for the default and option behaviour.
func DedupeBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "dedupe_by",
		name:   orDefault(cfg.name, "dedupe_by"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
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
		var stage stageFunc
		switch {
		case cfg.dedupSet != nil:
			// External backend: global semantics with a custom set (e.g. TTL, Bloom, Redis).
			set := cfg.dedupSet
			stage = func(ctx context.Context) error {
				defer close(ch)
				defer func() { go internal.DrainChan(inCh) }()
				outbox := internal.NewBlockingOutbox(ch)
				for {
					select {
					case item, ok := <-inCh:
						if !ok {
							return nil
						}
						k := fmt.Sprintf("%v", keyFn(item))
						dup, err := set.Contains(ctx, k)
						if err != nil {
							return err
						}
						if dup {
							continue
						}
						if err := set.Add(ctx, k); err != nil {
							return err
						}
						if err := outbox.Send(ctx, item); err != nil {
							return err
						}
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}

		case cfg.dedupeWindow == 0:
			// Global in-memory: never re-emit a seen key (new default).
			stage = func(ctx context.Context) error {
				defer close(ch)
				defer func() { go internal.DrainChan(inCh) }()
				outbox := internal.NewBlockingOutbox(ch)
				seen := make(map[K]struct{})
				for {
					select {
					case item, ok := <-inCh:
						if !ok {
							return nil
						}
						k := keyFn(item)
						if _, dup := seen[k]; dup {
							continue
						}
						seen[k] = struct{}{}
						if err := outbox.Send(ctx, item); err != nil {
							return err
						}
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}

		case cfg.dedupeWindow == 1:
			// Consecutive: suppress only adjacent duplicates.
			stage = func(ctx context.Context) error {
				defer close(ch)
				defer func() { go internal.DrainChan(inCh) }()
				outbox := internal.NewBlockingOutbox(ch)
				var lastKey K
				first := true
				for {
					select {
					case item, ok := <-inCh:
						if !ok {
							return nil
						}
						k := keyFn(item)
						if !first && k == lastKey {
							continue
						}
						first = false
						lastKey = k
						if err := outbox.Send(ctx, item); err != nil {
							return err
						}
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}

		default:
			// Sliding window of size cfg.dedupeWindow (n > 1).
			n := cfg.dedupeWindow
			stage = func(ctx context.Context) error {
				defer close(ch)
				defer func() { go internal.DrainChan(inCh) }()
				outbox := internal.NewBlockingOutbox(ch)
				window := make([]K, 0, n)
				inWindow := func(k K) bool {
					for _, w := range window {
						if w == k {
							return true
						}
					}
					return false
				}
				for {
					select {
					case item, ok := <-inCh:
						if !ok {
							return nil
						}
						k := keyFn(item)
						if inWindow(k) {
							continue
						}
						if len(window) >= n {
							window = window[1:]
						}
						window = append(window, k)
						if err := outbox.Send(ctx, item); err != nil {
							return err
						}
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}

// ---------------------------------------------------------------------------
// GroupBy
// ---------------------------------------------------------------------------

// GroupBy buffers all items from p, groups them by the key returned by keyFn,
// and emits a single map[K][]T when the source closes. Use [Single] (once
// available) to collect the result, or pipe the map into further stages.
//
// Items within each group preserve arrival order. Empty input produces a
// single emission of an empty map.
func GroupBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[map[K][]T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "group_by",
		name:   orDefault(cfg.name, "group_by"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan map[K][]T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan map[K][]T)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan map[K][]T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()
			outbox := internal.NewBlockingOutbox(ch)
			groups := make(map[K][]T)
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return outbox.Send(ctx, groups)
					}
					k := keyFn(item)
					groups[k] = append(groups[k], item)
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
// Frequencies / FrequenciesBy
// ---------------------------------------------------------------------------

// Frequencies runs the pipeline and returns a count of how many times each
// item appeared.
func Frequencies[T comparable](ctx context.Context, p *Pipeline[T], opts ...RunOption) (map[T]int, error) {
	return FrequenciesBy(ctx, p, func(v T) T { return v }, opts...)
}

// FrequenciesBy runs the pipeline and returns a count of how many times each
// key (returned by keyFn) appeared.
func FrequenciesBy[T any, K comparable](ctx context.Context, p *Pipeline[T], keyFn func(T) K, opts ...RunOption) (map[K]int, error) {
	counts := make(map[K]int)
	err := p.ForEach(func(_ context.Context, v T) error {
		counts[keyFn(v)]++
		return nil
	}).Run(ctx, opts...)
	return counts, err
}

// RunningFrequencies emits a running count-per-item snapshot after each item.
// The emitted map is a new copy on each item: safe to retain across iterations.
// For a single terminal result use [Frequencies].
func RunningFrequencies[T comparable](p *Pipeline[T], opts ...StageOption) *Pipeline[map[T]int64] {
	return RunningFrequenciesBy(p, func(v T) T { return v }, opts...)
}

// RunningFrequenciesBy emits a running count-per-key snapshot after each item.
// The map key type K is determined by keyFn. The emitted map is a new copy
// on each item: safe to retain across iterations.
func RunningFrequenciesBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[map[K]int64] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "running_frequencies_by",
		name:   orDefault(cfg.name, "running_frequencies_by"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan map[K]int64 {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan map[K]int64)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan map[K]int64, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			counts := make(map[K]int64)

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					counts[keyFn(item)]++
					snapshot := make(map[K]int64, len(counts))
					for k, v := range counts {
						snapshot[k] = v
					}
					if err := outbox.Send(ctx, snapshot); err != nil {
						return err
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
// RunningCountBy / RunningSumBy
// ---------------------------------------------------------------------------

// RunningCountBy emits a running count-per-key snapshot after each item.
// The map key type K is determined by keyFn. The emitted map is a new copy
// on each item: safe to retain across iterations.
func RunningCountBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[map[K]int64] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "running_count_by",
		name:   orDefault(cfg.name, "running_count_by"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan map[K]int64 {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan map[K]int64)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan map[K]int64, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()
			outbox := internal.NewBlockingOutbox(ch)
			counts := make(map[K]int64)
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					counts[keyFn(item)]++
					snapshot := make(map[K]int64, len(counts))
					for k, v := range counts {
						snapshot[k] = v
					}
					if err := outbox.Send(ctx, snapshot); err != nil {
						return err
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

// RunningSumBy emits a running sum-per-key snapshot after each item.
// The map key type K is determined by keyFn; values are summed using valueFn.
// The emitted map is a new copy on each item: safe to retain across iterations.
func RunningSumBy[T any, K comparable, V Numeric](p *Pipeline[T], keyFn func(T) K, valueFn func(T) V, opts ...StageOption) *Pipeline[map[K]V] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "running_sum_by",
		name:   orDefault(cfg.name, "running_sum_by"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan map[K]V {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan map[K]V)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan map[K]V, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()
			outbox := internal.NewBlockingOutbox(ch)
			sums := make(map[K]V)
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					sums[keyFn(item)] += valueFn(item)
					snapshot := make(map[K]V, len(sums))
					for k, v := range sums {
						snapshot[k] = v
					}
					if err := outbox.Send(ctx, snapshot); err != nil {
						return err
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
