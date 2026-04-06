package kitsune

import (
	"context"
	"sync"

	"github.com/zenbaku/go-kitsune/internal"
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
	for _, p := range pipelines {
		track(p)
	}
	inputs := make([]int, len(pipelines))
	for i, p := range pipelines {
		inputs[i] = p.id
	}
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "merge",
		name:   "merge",
		buffer: internal.DefaultBuffer,
		inputs: inputs,
	}
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inChans := make([]chan T, len(pipelines))
		for i, p := range pipelines {
			inChans[i] = p.build(rc)
		}
		ch := make(chan T, internal.DefaultBuffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
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
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}

// ---------------------------------------------------------------------------
// Partition
// ---------------------------------------------------------------------------

// Partition splits a pipeline into two: items for which pred returns true go
// to the first pipeline; items for which it returns false go to the second.
// Both pipelines must be consumed (directly or via [MergeRunners]).
func Partition[T any](p *Pipeline[T], pred func(T) bool, opts ...StageOption) (*Pipeline[T], *Pipeline[T]) {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	falseID := nextPipelineID()

	trueMeta := stageMeta{
		id:     id,
		kind:   "partition",
		name:   orDefault(cfg.name, "partition"),
		buffer: cfg.buffer,
		inputs: []int{p.id},
	}
	falseMeta := stageMeta{
		id:     falseID,
		kind:   "partition",
		name:   orDefault(cfg.name, "partition") + "_false",
		buffer: cfg.buffer,
		inputs: []int{p.id},
	}

	// sharedBuild creates both channels and the stage on the first call.
	// Subsequent calls return the already-built channels.
	sharedBuild := func(rc *runCtx) (chan T, chan T) {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T), rc.getChan(falseID).(chan T)
		}
		inCh := p.build(rc)
		trueC := make(chan T, cfg.buffer)
		falseC := make(chan T, cfg.buffer)
		m := trueMeta
		m.getChanLen = func() int { return len(trueC) }
		m.getChanCap = func() int { return cap(trueC) }
		rc.setChan(id, trueC)
		rc.setChan(falseID, falseC)
		stage := func(ctx context.Context) error {
			defer close(trueC)
			defer close(falseC)
			defer func() { go internal.DrainChan(inCh) }()

			trueBox := internal.NewBlockingOutbox(trueC)
			falseBox := internal.NewBlockingOutbox(falseC)

			for {
				select {
				case item, ok := <-inCh:
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
		rc.add(stage, m)
		return trueC, falseC
	}

	trueP := newPipeline(id, trueMeta, func(rc *runCtx) chan T {
		t, _ := sharedBuild(rc)
		return t
	})
	falseP := newPipeline(falseID, falseMeta, func(rc *runCtx) chan T {
		_, f := sharedBuild(rc)
		return f
	})
	return trueP, falseP
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
	track(p)
	cfg := buildStageConfig(opts)

	// Allocate IDs for all outputs at construction time.
	ids := make([]int, n)
	for i := range ids {
		ids[i] = nextPipelineID()
	}

	metas := make([]stageMeta, n)
	for i := range metas {
		name := orDefault(cfg.name, "broadcast")
		if i > 0 {
			name = name + "_" + string(rune('0'+i))
		}
		metas[i] = stageMeta{
			id:     ids[i],
			kind:   "broadcast",
			name:   name,
			buffer: cfg.buffer,
			inputs: []int{p.id},
		}
	}

	// sharedBuild creates all channels and the stage on the first call.
	sharedBuild := func(rc *runCtx) []chan T {
		if existing := rc.getChan(ids[0]); existing != nil {
			chans := make([]chan T, n)
			for i, id := range ids {
				chans[i] = rc.getChan(id).(chan T)
			}
			return chans
		}
		inCh := p.build(rc)
		chans := make([]chan T, n)
		for i := range chans {
			chans[i] = make(chan T, cfg.buffer)
		}
		m := metas[0]
		m.getChanLen = func() int { return len(chans[0]) }
		m.getChanCap = func() int { return cap(chans[0]) }
		for i, id := range ids {
			rc.setChan(id, chans[i])
		}
		stage := func(ctx context.Context) error {
			defer func() {
				for _, c := range chans {
					close(c)
				}
			}()
			defer func() { go internal.DrainChan(inCh) }()

			outboxes := make([]internal.Outbox[T], len(chans))
			for i, c := range chans {
				outboxes[i] = internal.NewBlockingOutbox(c)
			}

			for {
				select {
				case item, ok := <-inCh:
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
		rc.add(stage, m)
		return chans
	}

	out := make([]*Pipeline[T], n)
	for i := range out {
		i := i
		out[i] = newPipeline(ids[i], metas[i], func(rc *runCtx) chan T {
			return sharedBuild(rc)[i]
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Balance
// ---------------------------------------------------------------------------

// Balance distributes items across n output pipelines in round-robin order.
// Each item goes to exactly one output. All n pipelines must be consumed.
func Balance[T any](p *Pipeline[T], n int, opts ...StageOption) []*Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)

	// Allocate IDs for all outputs at construction time.
	ids := make([]int, n)
	for i := range ids {
		ids[i] = nextPipelineID()
	}

	metas := make([]stageMeta, n)
	for i := range metas {
		name := orDefault(cfg.name, "balance")
		if i > 0 {
			name = name + "_" + string(rune('0'+i))
		}
		metas[i] = stageMeta{
			id:     ids[i],
			kind:   "balance",
			name:   name,
			buffer: cfg.buffer,
			inputs: []int{p.id},
		}
	}

	// sharedBuild creates all channels and the stage on the first call.
	sharedBuild := func(rc *runCtx) []chan T {
		if existing := rc.getChan(ids[0]); existing != nil {
			chans := make([]chan T, n)
			for i, id := range ids {
				chans[i] = rc.getChan(id).(chan T)
			}
			return chans
		}
		inCh := p.build(rc)
		chans := make([]chan T, n)
		for i := range chans {
			chans[i] = make(chan T, cfg.buffer)
		}
		m := metas[0]
		m.getChanLen = func() int { return len(chans[0]) }
		m.getChanCap = func() int { return cap(chans[0]) }
		for i, id := range ids {
			rc.setChan(id, chans[i])
		}
		stage := func(ctx context.Context) error {
			defer func() {
				for _, c := range chans {
					close(c)
				}
			}()
			defer func() { go internal.DrainChan(inCh) }()

			outboxes := make([]internal.Outbox[T], len(chans))
			for i, c := range chans {
				outboxes[i] = internal.NewBlockingOutbox(c)
			}

			idx := 0
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if err := outboxes[idx%len(outboxes)].Send(ctx, item); err != nil {
						return err
					}
					idx++
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return chans
	}

	out := make([]*Pipeline[T], n)
	for i := range out {
		i := i
		out[i] = newPipeline(ids[i], metas[i], func(rc *runCtx) chan T {
			return sharedBuild(rc)[i]
		})
	}
	return out
}

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
		ch := make(chan O, cfg.buffer)
		m := meta
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
		ch := make(chan O, cfg.buffer)
		m := meta
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
		ch := make(chan O, cfg.buffer)
		m := meta
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
