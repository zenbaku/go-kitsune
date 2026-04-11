package kitsune

import (
	"context"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// Reject
// ---------------------------------------------------------------------------

// Reject is the inverse of [Filter]: it emits only items for which pred returns false.
func Reject[T any](p *Pipeline[T], pred func(context.Context, T) (bool, error), opts ...StageOption) *Pipeline[T] {
	return Filter(p, func(ctx context.Context, v T) (bool, error) {
		keep, err := pred(ctx, v)
		return !keep, err
	}, opts...)
}

// ---------------------------------------------------------------------------
// WithIndex
// ---------------------------------------------------------------------------

// Indexed pairs a value with its 0-based position in the stream.
type Indexed[T any] struct {
	Index int
	Value T
}

// WithIndex tags each item with its 0-based index in the stream.
func WithIndex[T any](p *Pipeline[T], opts ...StageOption) *Pipeline[Indexed[T]] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "with_index",
		name:   orDefault(cfg.name, "with_index"),
		buffer: cfg.buffer,
		inputs: []int{p.id},
	}
	build := func(rc *runCtx) chan Indexed[T] {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan Indexed[T])
		}
		inCh := p.build(rc)
		ch := make(chan Indexed[T], cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			idx := 0
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if err := outbox.Send(ctx, Indexed[T]{Index: idx, Value: item}); err != nil {
						return err
					}
					idx++
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
// Pairwise
// ---------------------------------------------------------------------------

// Pairwise emits overlapping consecutive pairs: (item[0], item[1]), (item[1], item[2]), …
// The first item is buffered silently; no pair is emitted until the second item arrives.
func Pairwise[T any](p *Pipeline[T], opts ...StageOption) *Pipeline[Pair[T, T]] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "pairwise",
		name:   orDefault(cfg.name, "pairwise"),
		buffer: cfg.buffer,
		inputs: []int{p.id},
	}
	build := func(rc *runCtx) chan Pair[T, T] {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan Pair[T, T])
		}
		inCh := p.build(rc)
		ch := make(chan Pair[T, T], cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			var prev T
			first := true
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if !first {
						if err := outbox.Send(ctx, Pair[T, T]{First: prev, Second: item}); err != nil {
							return err
						}
					}
					first = false
					prev = item
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
// TakeEvery / DropEvery / MapEvery
// ---------------------------------------------------------------------------

// TakeEvery emits every nth item starting with the first (index 0).
// n must be > 0; panics otherwise.
func TakeEvery[T any](p *Pipeline[T], n int) *Pipeline[T] {
	track(p)
	if n <= 0 {
		panic("kitsune: TakeEvery n must be > 0")
	}
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "take_every",
		name:   "take_every",
		buffer: internal.DefaultBuffer,
		inputs: []int{p.id},
	}
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inCh := p.build(rc)
		ch := make(chan T, internal.DefaultBuffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			i := 0
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if i%n == 0 {
						if err := outbox.Send(ctx, item); err != nil {
							return err
						}
					}
					i++
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

// DropEvery drops every nth item starting with the first (index 0) and emits the rest.
// n must be > 0; panics otherwise.
func DropEvery[T any](p *Pipeline[T], n int) *Pipeline[T] {
	track(p)
	if n <= 0 {
		panic("kitsune: DropEvery n must be > 0")
	}
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "drop_every",
		name:   "drop_every",
		buffer: internal.DefaultBuffer,
		inputs: []int{p.id},
	}
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inCh := p.build(rc)
		ch := make(chan T, internal.DefaultBuffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			i := 0
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if i%n != 0 {
						if err := outbox.Send(ctx, item); err != nil {
							return err
						}
					}
					i++
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

// MapEvery applies fn to every nth item (index 0, n, 2n, …) and passes other items unchanged.
// n must be > 0; panics otherwise.
func MapEvery[T any](p *Pipeline[T], n int, fn func(context.Context, T) (T, error), opts ...StageOption) *Pipeline[T] {
	track(p)
	if n <= 0 {
		panic("kitsune: MapEvery n must be > 0")
	}
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "map_every",
		name:   orDefault(cfg.name, "map_every"),
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
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			i := 0
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					out := item
					if i%n == 0 {
						mapped, err := fn(ctx, item)
						if err != nil {
							return internal.WrapStageErr(cfg.name, err, 0)
						}
						out = mapped
					}
					if err := outbox.Send(ctx, out); err != nil {
						return err
					}
					i++
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
// Intersperse
// ---------------------------------------------------------------------------

// Intersperse inserts sep between every two consecutive items.
//
//	kitsune.FromSlice([]int{1, 2, 3}).Intersperse(0) // → 1, 0, 2, 0, 3
func Intersperse[T any](p *Pipeline[T], sep T, opts ...StageOption) *Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "intersperse",
		name:   orDefault(cfg.name, "intersperse"),
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
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			first := true
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if !first {
						if err := outbox.Send(ctx, sep); err != nil {
							return err
						}
					}
					first = false
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
