package kitsune

import (
	"context"
	"sync"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// Zip / ZipWith
// ---------------------------------------------------------------------------

// Pair holds one item from each of two pipelines.
type Pair[A, B any] struct {
	First  A
	Second B
}

// Zip pairs items from a and b positionally into [Pair] values.
// The pipeline completes when either input completes.
func Zip[A, B any](a *Pipeline[A], b *Pipeline[B]) *Pipeline[Pair[A, B]] {
	return ZipWith(a, b, func(_ context.Context, av A, bv B) (Pair[A, B], error) {
		return Pair[A, B]{First: av, Second: bv}, nil
	})
}

// ZipWith pairs items from a and b positionally and transforms them using fn.
// The pipeline completes when either input completes.
func ZipWith[A, B, O any](a *Pipeline[A], b *Pipeline[B], fn func(context.Context, A, B) (O, error), opts ...StageOption) *Pipeline[O] {
	track(a)
	track(b)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "zip",
		name:   orDefault(cfg.name, "zip"),
		buffer: cfg.buffer,
		inputs: []int{a.id, b.id},
	}
	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}
		aCh := a.build(rc)
		bCh := b.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan O, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() {
				go internal.DrainChan(aCh)
				go internal.DrainChan(bCh)
			}()

			outbox := internal.NewBlockingOutbox(ch)

			for {
				// Read from a first.
				var av A
				select {
				case v, ok := <-aCh:
					if !ok {
						return nil
					}
					av = v
				case <-ctx.Done():
					return ctx.Err()
				}

				// Then read from b.
				var bv B
				select {
				case v, ok := <-bCh:
					if !ok {
						return nil
					}
					bv = v
				case <-ctx.Done():
					return ctx.Err()
				}

				result, err := fn(ctx, av, bv)
				if err != nil {
					return internal.WrapStageErr(cfg.name, err, 0)
				}
				if err := outbox.Send(ctx, result); err != nil {
					return err
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}

// ---------------------------------------------------------------------------
// CombineLatest
// ---------------------------------------------------------------------------

// CombineLatest emits a new [Pair] whenever either a or b emits an item,
// combining the latest value from each. It starts emitting only after both
// pipelines have emitted at least one item.
func CombineLatest[A, B any](a *Pipeline[A], b *Pipeline[B]) *Pipeline[Pair[A, B]] {
	return CombineLatestWith(a, b, func(_ context.Context, av A, bv B) (Pair[A, B], error) {
		return Pair[A, B]{First: av, Second: bv}, nil
	})
}

// CombineLatestWith emits a new transformed value whenever either a or b emits,
// combining the latest values from each using fn. Emitting starts after both
// have emitted at least one item.
func CombineLatestWith[A, B, O any](a *Pipeline[A], b *Pipeline[B], fn func(context.Context, A, B) (O, error), opts ...StageOption) *Pipeline[O] {
	track(a)
	track(b)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "combine_latest",
		name:   orDefault(cfg.name, "combine_latest"),
		buffer: cfg.buffer,
		inputs: []int{a.id, b.id},
	}
	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}
		aCh := a.build(rc)
		bCh := b.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan O, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() {
				go internal.DrainChan(aCh)
				go internal.DrainChan(bCh)
			}()

			// outbox is called outside any mutex (see CombineLatest deadlock analysis).
			outbox := internal.NewBlockingOutbox(ch)

			var mu sync.Mutex
			var latA A
			var latB B
			var hasA, hasB bool

			errCh := make(chan error, 2)

			go func() {
				for {
					select {
					case v, ok := <-aCh:
						if !ok {
							errCh <- nil
							return
						}
						mu.Lock()
						latA = v
						hasA = true
						ready := hasA && hasB
						snapA, snapB := latA, latB
						mu.Unlock()
						// Send OUTSIDE the lock to prevent deadlock under backpressure.
						if ready {
							result, err := fn(ctx, snapA, snapB)
							if err != nil {
								errCh <- internal.WrapStageErr(cfg.name, err, 0)
								return
							}
							if err := outbox.Send(ctx, result); err != nil {
								errCh <- err
								return
							}
						}
					case <-ctx.Done():
						errCh <- ctx.Err()
						return
					}
				}
			}()

			go func() {
				for {
					select {
					case v, ok := <-bCh:
						if !ok {
							errCh <- nil
							return
						}
						mu.Lock()
						latB = v
						hasB = true
						ready := hasA && hasB
						snapA, snapB := latA, latB
						mu.Unlock()
						// Send OUTSIDE the lock to prevent deadlock under backpressure.
						if ready {
							result, err := fn(ctx, snapA, snapB)
							if err != nil {
								errCh <- internal.WrapStageErr(cfg.name, err, 0)
								return
							}
							if err := outbox.Send(ctx, result); err != nil {
								errCh <- err
								return
							}
						}
					case <-ctx.Done():
						errCh <- ctx.Err()
						return
					}
				}
			}()

			// Wait for both goroutines.
			for i := 0; i < 2; i++ {
				if err := <-errCh; err != nil {
					return err
				}
			}
			return nil
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}

// ---------------------------------------------------------------------------
// WithLatestFrom
// ---------------------------------------------------------------------------

// WithLatestFrom combines each item from main with the most recent item from
// other. Items from main are only emitted after other has emitted at least one
// item. Items from other that arrive between main items are not independently
// emitted.
func WithLatestFrom[A, B any](main *Pipeline[A], other *Pipeline[B]) *Pipeline[Pair[A, B]] {
	return WithLatestFromWith(main, other, func(_ context.Context, a A, b B) (Pair[A, B], error) {
		return Pair[A, B]{First: a, Second: b}, nil
	})
}

// WithLatestFromWith combines each item from main with the most recent item from
// other using fn. Emitting starts after other has provided at least one item.
func WithLatestFromWith[A, B, O any](main *Pipeline[A], other *Pipeline[B], fn func(context.Context, A, B) (O, error), opts ...StageOption) *Pipeline[O] {
	track(main)
	track(other)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "with_latest_from",
		name:   orDefault(cfg.name, "with_latest_from"),
		buffer: cfg.buffer,
		inputs: []int{main.id, other.id},
	}
	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}
		mainCh := main.build(rc)
		otherCh := other.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan O, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() {
				go internal.DrainChan(mainCh)
				go internal.DrainChan(otherCh)
			}()

			outbox := internal.NewBlockingOutbox(ch)

			var mu sync.Mutex
			var latOther B
			var hasOther bool

			// Background goroutine tracks the latest value from other.
			go func() {
				for {
					select {
					case v, ok := <-otherCh:
						if !ok {
							return
						}
						mu.Lock()
						latOther = v
						hasOther = true
						mu.Unlock()
					case <-ctx.Done():
						return
					}
				}
			}()

			for {
				select {
				case av, ok := <-mainCh:
					if !ok {
						return nil
					}
					mu.Lock()
					ready := hasOther
					bv := latOther
					mu.Unlock()
					if !ready {
						continue
					}
					result, err := fn(ctx, av, bv)
					if err != nil {
						return internal.WrapStageErr(cfg.name, err, 0)
					}
					if err := outbox.Send(ctx, result); err != nil {
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
// SampleWith
// ---------------------------------------------------------------------------

// SampleWith emits the most recent item from p whenever the sampler pipeline
// fires. If no item has arrived since the last sampler signal, that tick is
// skipped silently. The latest item is consumed on emit: if the sampler fires
// twice without a new source item in between, only the first fire emits.
//
// Unlike [Sample] (driven by a fixed wall-clock interval) and [Throttle]
// (rate-limits on item arrival), SampleWith is driven by an arbitrary pipeline:
// a [Ticker], a user-action stream, a heartbeat, or any other event source.
// The sampler's item values are discarded; only the occurrence of each item
// matters.
//
// The pipeline completes when the sampler closes. If the source closes and
// its last item has already been emitted, the pipeline also completes early
// to avoid a hung goroutine waiting for a sampler that may never fire again.
//
// Supports [Buffer], [WithName].
//
//	clock  := kitsune.Ticker(1 * time.Second)
//	polled := kitsune.SampleWith(liveQuotes, clock)
//	// emits the latest quote each second, driven by the clock signal
func SampleWith[T, S any](p *Pipeline[T], sampler *Pipeline[S], opts ...StageOption) *Pipeline[T] {
	track(p)
	track(sampler)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "sample_with",
		name:   orDefault(cfg.name, "sample_with"),
		buffer: cfg.buffer,
		inputs: []int{p.id, sampler.id},
	}
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		srcCh := p.build(rc)
		samCh := sampler.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() {
				go internal.DrainChan(srcCh)
				go internal.DrainChan(samCh)
			}()

			outbox := internal.NewBlockingOutbox(ch)

			var (
				mu        sync.Mutex
				latest    T
				hasLatest bool
				srcDone   bool
			)

			// Background goroutine tracks the latest value from the source.
			go func() {
				for {
					select {
					case v, ok := <-srcCh:
						if !ok {
							mu.Lock()
							srcDone = true
							mu.Unlock()
							return
						}
						mu.Lock()
						latest = v
						hasLatest = true
						mu.Unlock()
					case <-ctx.Done():
						return
					}
				}
			}()

			// Main loop: sampler signals drive emissions.
			for {
				select {
				case _, ok := <-samCh:
					if !ok {
						return nil
					}
					mu.Lock()
					ready := hasLatest
					v := latest
					if ready {
						hasLatest = false // consume-on-emit
					}
					// Early exit: source is done and no item is pending.
					earlyExit := srcDone && !hasLatest
					mu.Unlock()

					if !ready {
						if earlyExit {
							return nil
						}
						continue
					}
					if err := outbox.Send(ctx, v); err != nil {
						return err
					}
					if earlyExit {
						return nil
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
