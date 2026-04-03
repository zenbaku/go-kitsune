package kitsune

import (
	"context"
	"sync"

	"github.com/zenbaku/go-kitsune/v2/internal"
)

// ---------------------------------------------------------------------------
// Merge
// ---------------------------------------------------------------------------

// Merge combines multiple pipelines of the same type into a single pipeline.
// Items are emitted as they arrive from any source (race order). The merged
// pipeline completes when all inputs have completed.
//
// Pipelines from independent sources (different FromSlice/From/etc. calls) are
// automatically combined into a single execution graph.
func Merge[T any](pipelines ...*Pipeline[T]) *Pipeline[T] {
	if len(pipelines) == 0 {
		return FromSlice[T](nil)
	}
	stageLists := make([]*stageList, len(pipelines))
	for i, p := range pipelines {
		stageLists[i] = p.sl
	}
	sl := combineStageLists(stageLists...)
	ch := make(chan T, internal.DefaultBuffer)
	inputs := make([]int, len(pipelines))
	inChans := make([]<-chan T, len(pipelines))
	for i, p := range pipelines {
		inputs[i] = p.id
		inChans[i] = p.ch
	}

	meta := stageMeta{
		kind:       "merge",
		name:       "merge",
		buffer:     internal.DefaultBuffer,
		inputs:     inputs,
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	stage := func(ctx context.Context) error {
		defer close(ch)
		defer func() {
			for _, ic := range inChans {
				go internal.DrainChan(ic)
			}
		}()

		outbox := internal.NewBlockingOutbox(ch)
		var wg sync.WaitGroup

		for _, ic := range inChans {
			ic := ic
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case item, ok := <-ic:
						if !ok {
							return
						}
						if err := outbox.Send(ctx, item); err != nil {
							return
						}
					case <-ctx.Done():
						return
					}
				}
			}()
		}

		wg.Wait()
		return nil
	}

	id := sl.add(stage, meta)
	return newPipeline(ch, sl, id)
}

// ---------------------------------------------------------------------------
// Partition
// ---------------------------------------------------------------------------

// Partition splits a pipeline into two: items for which pred returns true go
// to the first pipeline; items for which it returns false go to the second.
// Both pipelines must be consumed (directly or via [MergeRunners]).
func Partition[T any](p *Pipeline[T], pred func(T) bool, opts ...StageOption) (*Pipeline[T], *Pipeline[T]) {
	cfg := buildStageConfig(opts)
	trueC := make(chan T, cfg.buffer)
	falseC := make(chan T, cfg.buffer)

	meta := stageMeta{
		kind:       "partition",
		name:       orDefault(cfg.name, "partition"),
		buffer:     cfg.buffer,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(trueC) },
		getChanCap: func() int { return cap(trueC) },
	}

	stage := func(ctx context.Context) error {
		defer close(trueC)
		defer close(falseC)
		defer func() { go internal.DrainChan(p.ch) }()

		trueBox := internal.NewBlockingOutbox(trueC)
		falseBox := internal.NewBlockingOutbox(falseC)

		for {
			select {
			case item, ok := <-p.ch:
				if !ok {
					return nil
				}
				var err error
				if pred(item) {
					err = trueBox.Send(ctx, item)
				} else {
					err = falseBox.Send(ctx, item)
				}
				if err != nil {
					return err
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	// One stage drives both output channels. Both Pipelines share the same ID.
	id := p.sl.add(stage, meta)
	return newPipeline(trueC, p.sl, id), newPipeline(falseC, p.sl, id)
}

// ---------------------------------------------------------------------------
// Broadcast / BroadcastN
// ---------------------------------------------------------------------------

// Broadcast fans out each item to n identical output pipelines.
// All n pipelines must be consumed. The stage blocks until all consumers
// have accepted each item (synchronised fan-out).
func Broadcast[T any](p *Pipeline[T], n int, opts ...StageOption) []*Pipeline[T] {
	return BroadcastN(p, n, opts...)
}

// BroadcastN fans out each item to n identical output pipelines.
func BroadcastN[T any](p *Pipeline[T], n int, opts ...StageOption) []*Pipeline[T] {
	cfg := buildStageConfig(opts)

	chans := make([]chan T, n)
	for i := range chans {
		chans[i] = make(chan T, cfg.buffer)
	}

	meta := stageMeta{
		kind:       "broadcast",
		name:       orDefault(cfg.name, "broadcast"),
		buffer:     cfg.buffer,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(chans[0]) },
		getChanCap: func() int { return cap(chans[0]) },
	}

	stage := func(ctx context.Context) error {
		defer func() {
			for _, c := range chans {
				close(c)
			}
		}()
		defer func() { go internal.DrainChan(p.ch) }()

		outboxes := make([]internal.Outbox[T], len(chans))
		for i, c := range chans {
			outboxes[i] = internal.NewBlockingOutbox(c)
		}

		for {
			select {
			case item, ok := <-p.ch:
				if !ok {
					return nil
				}
				for _, ob := range outboxes {
					if err := ob.Send(ctx, item); err != nil {
						return err
					}
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	// One stage drives all output channels; all Pipelines share the same ID.
	id := p.sl.add(stage, meta)

	out := make([]*Pipeline[T], n)
	for i := range out {
		out[i] = newPipeline(chans[i], p.sl, id)
	}
	return out
}

// ---------------------------------------------------------------------------
// Balance
// ---------------------------------------------------------------------------

// Balance distributes items across n output pipelines in round-robin order.
// Each item goes to exactly one output. All n pipelines must be consumed.
func Balance[T any](p *Pipeline[T], n int, opts ...StageOption) []*Pipeline[T] {
	cfg := buildStageConfig(opts)

	chans := make([]chan T, n)
	for i := range chans {
		chans[i] = make(chan T, cfg.buffer)
	}

	meta := stageMeta{
		kind:       "balance",
		name:       orDefault(cfg.name, "balance"),
		buffer:     cfg.buffer,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(chans[0]) },
		getChanCap: func() int { return cap(chans[0]) },
	}

	stage := func(ctx context.Context) error {
		defer func() {
			for _, c := range chans {
				close(c)
			}
		}()
		defer func() { go internal.DrainChan(p.ch) }()

		outboxes := make([]internal.Outbox[T], len(chans))
		for i, c := range chans {
			outboxes[i] = internal.NewBlockingOutbox(c)
		}

		i := 0
		for {
			select {
			case item, ok := <-p.ch:
				if !ok {
					return nil
				}
				if err := outboxes[i%len(outboxes)].Send(ctx, item); err != nil {
					return err
				}
				i++
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	id := p.sl.add(stage, meta)

	out := make([]*Pipeline[T], n)
	for i := range out {
		out[i] = newPipeline(chans[i], p.sl, id)
	}
	return out
}

// ---------------------------------------------------------------------------
// Zip / ZipWith
// ---------------------------------------------------------------------------

// Pair holds one item from each of two pipelines.
type Pair[A, B any] struct {
	Left  A
	Right B
}

// Zip pairs items from a and b positionally into [Pair] values.
// The pipeline completes when either input completes.
func Zip[A, B any](a *Pipeline[A], b *Pipeline[B]) *Pipeline[Pair[A, B]] {
	return ZipWith(a, b, func(_ context.Context, av A, bv B) (Pair[A, B], error) {
		return Pair[A, B]{Left: av, Right: bv}, nil
	})
}

// ZipWith pairs items from a and b positionally and transforms them using fn.
// The pipeline completes when either input completes.
func ZipWith[A, B, O any](a *Pipeline[A], b *Pipeline[B], fn func(context.Context, A, B) (O, error), opts ...StageOption) *Pipeline[O] {
	cfg := buildStageConfig(opts)
	ch := make(chan O, cfg.buffer)
	meta := stageMeta{
		kind:       "zip",
		name:       orDefault(cfg.name, "zip"),
		buffer:     cfg.buffer,
		inputs:     []int{a.id, b.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	aCh := a.ch
	bCh := b.ch

	// Combine stageLists so pipelines from independent sources run together.
	sl := combineStageLists(a.sl, b.sl)

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

	id := sl.add(stage, meta)
	return newPipeline(ch, sl, id)
}

// ---------------------------------------------------------------------------
// CombineLatest
// ---------------------------------------------------------------------------

// CombineLatest emits a new [Pair] whenever either a or b emits an item,
// combining the latest value from each. It starts emitting only after both
// pipelines have emitted at least one item.
func CombineLatest[A, B any](a *Pipeline[A], b *Pipeline[B]) *Pipeline[Pair[A, B]] {
	return CombineLatestWith(a, b, func(_ context.Context, av A, bv B) (Pair[A, B], error) {
		return Pair[A, B]{Left: av, Right: bv}, nil
	})
}

// CombineLatestWith emits a new transformed value whenever either a or b emits,
// combining the latest values from each using fn. Emitting starts after both
// have emitted at least one item.
func CombineLatestWith[A, B, O any](a *Pipeline[A], b *Pipeline[B], fn func(context.Context, A, B) (O, error), opts ...StageOption) *Pipeline[O] {
	cfg := buildStageConfig(opts)
	ch := make(chan O, cfg.buffer)
	meta := stageMeta{
		kind:       "combine_latest",
		name:       orDefault(cfg.name, "combine_latest"),
		buffer:     cfg.buffer,
		inputs:     []int{a.id, b.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	aCh := a.ch
	bCh := b.ch

	sl := combineStageLists(a.sl, b.sl)

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

	id := sl.add(stage, meta)
	return newPipeline(ch, sl, id)
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
		return Pair[A, B]{Left: a, Right: b}, nil
	})
}

// WithLatestFromWith combines each item from main with the most recent item from
// other using fn. Emitting starts after other has provided at least one item.
func WithLatestFromWith[A, B, O any](main *Pipeline[A], other *Pipeline[B], fn func(context.Context, A, B) (O, error), opts ...StageOption) *Pipeline[O] {
	cfg := buildStageConfig(opts)
	ch := make(chan O, cfg.buffer)
	meta := stageMeta{
		kind:       "with_latest_from",
		name:       orDefault(cfg.name, "with_latest_from"),
		buffer:     cfg.buffer,
		inputs:     []int{main.id, other.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	mainCh := main.ch
	otherCh := other.ch

	sl := combineStageLists(main.sl, other.sl)

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

	id := sl.add(stage, meta)
	return newPipeline(ch, sl, id)
}
