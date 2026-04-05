package kitsune

import (
	"context"
	"fmt"

	"github.com/zenbaku/go-kitsune/v2/internal"
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
		inputs: []int{p.id},
	}
	build := func(rc *runCtx) chan S {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan S)
		}
		inCh := p.build(rc)
		ch := make(chan S, cfg.buffer)
		m := meta
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
		inputs: []int{p.id},
	}
	build := func(rc *runCtx) chan S {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan S)
		}
		inCh := p.build(rc)
		ch := make(chan S, cfg.buffer)
		m := meta
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
// before. Items with duplicate keys are silently dropped.
func DistinctBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "distinct_by",
		name:   orDefault(cfg.name, "distinct_by"),
		buffer: cfg.buffer,
		inputs: []int{p.id},
	}
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inCh := p.build(rc)
		ch := make(chan T, cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		var stage stageFunc
		if cfg.dedupSet != nil {
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
		} else {
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
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}

// ---------------------------------------------------------------------------
// Dedupe
// ---------------------------------------------------------------------------

// Dedupe drops consecutive duplicate items using == equality.
// Unlike [Distinct], it only suppresses adjacent duplicates.
func Dedupe[T comparable](p *Pipeline[T], opts ...StageOption) *Pipeline[T] {
	return DedupeBy(p, func(v T) T { return v }, opts...)
}

// DedupeBy drops consecutive items whose key (returned by keyFn) equals the
// previous item's key. Non-consecutive duplicates are NOT suppressed.
func DedupeBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "dedupe_by",
		name:   orDefault(cfg.name, "dedupe_by"),
		buffer: cfg.buffer,
		inputs: []int{p.id},
	}
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inCh := p.build(rc)
		ch := make(chan T, cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		var stage stageFunc
		if cfg.dedupSet != nil {
			// When an external DedupSet is provided, switch to global dedup:
			// drop any item whose key was seen at any point, not just the last.
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
		} else {
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
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}

// ---------------------------------------------------------------------------
// GroupBy
// ---------------------------------------------------------------------------

// Group holds all items sharing a common key.
type Group[K comparable, V any] struct {
	Key   K
	Items []V
}

// GroupBy partitions items by key and emits one [Group] per distinct key when
// the source completes. Order of groups matches first-seen key order.
func GroupBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[Group[K, T]] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "group_by",
		name:   orDefault(cfg.name, "group_by"),
		buffer: cfg.buffer,
		inputs: []int{p.id},
	}
	build := func(rc *runCtx) chan Group[K, T] {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan Group[K, T])
		}
		inCh := p.build(rc)
		ch := make(chan Group[K, T], cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			groups := make(map[K]*Group[K, T])
			var order []K // preserve insertion order

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						// Emit all groups in first-seen order.
						for _, k := range order {
							if err := outbox.Send(ctx, *groups[k]); err != nil {
								return err
							}
						}
						return nil
					}
					k := keyFn(item)
					if g, exists := groups[k]; exists {
						g.Items = append(g.Items, item)
					} else {
						groups[k] = &Group[K, T]{Key: k, Items: []T{item}}
						order = append(order, k)
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
// Frequencies / FrequenciesBy
// ---------------------------------------------------------------------------

// Frequencies counts how many times each item appears and emits a single
// map[T]int64 when the source completes.
func Frequencies[T comparable](p *Pipeline[T], opts ...StageOption) *Pipeline[map[T]int64] {
	return FrequenciesBy(p, func(v T) T { return v }, opts...)
}

// FrequenciesBy counts how many times each key (returned by keyFn) appears and
// emits a single map[K]int64 when the source completes.
func FrequenciesBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[map[K]int64] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "frequencies_by",
		name:   orDefault(cfg.name, "frequencies_by"),
		buffer: cfg.buffer,
		inputs: []int{p.id},
	}
	build := func(rc *runCtx) chan map[K]int64 {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan map[K]int64)
		}
		inCh := p.build(rc)
		ch := make(chan map[K]int64, cfg.buffer)
		m := meta
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
						return outbox.Send(ctx, counts)
					}
					counts[keyFn(item)]++
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
