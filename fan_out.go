package kitsune

import (
	"context"
	"strconv"
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
	inputs := make([]int64, len(pipelines))
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
	var out *Pipeline[T]
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inChans := make([]chan T, len(pipelines))
		for i, p := range pipelines {
			inChans[i] = p.build(rc)
		}
		buf := rc.defaultBufSize()
		ch := make(chan T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		rc.initDrainNotify(id, out.consumerCount.Load())
		drainCh := rc.drainCh(id)
		stage := func(ctx context.Context) error {
			defer close(ch)
			cooperativeDrain := false
			defer func() {
				if !cooperativeDrain {
					for _, ic := range inChans {
						go internal.DrainChan(ic)
					}
				}
			}()
			for _, p := range pipelines {
				p := p
				defer func() { rc.signalDrain(p.id) }()
			}

			outbox := internal.NewBlockingOutbox(ch)
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()
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
							if err := outbox.Send(innerCtx, item); err != nil {
								return
							}
						case <-innerCtx.Done():
							return
						}
					}
				}()
			}

			// Wait for drainCh or all inputs to close.
			doneCh := make(chan struct{})
			go func() {
				wg.Wait()
				close(doneCh)
			}()
			select {
			case <-doneCh:
				// If the context was cancelled externally (e.g. user called cancel()),
				// return the context error so callers see a non-nil result. When
				// upstream sources finished naturally or rc.done was closed by a
				// downstream Take, ctx.Err() is nil and we return nil as before.
				return ctx.Err()
			case <-drainCh:
				cooperativeDrain = true
				cancel() // stop worker goroutines
				wg.Wait()
				return nil
			case <-ctx.Done():
				cancel()
				wg.Wait()
				return ctx.Err()
			}
		}
		rc.add(stage, m)
		return ch
	}
	out = newPipeline(id, meta, build)
	return out
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
		inputs: []int64{p.id},
	}
	falseMeta := stageMeta{
		id:     falseID,
		kind:   "partition",
		name:   orDefault(cfg.name, "partition") + "_false",
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}

	// sharedBuild creates both channels and the stage on the first call.
	// Subsequent calls return the already-built channels.
	sharedBuild := func(rc *runCtx) (chan T, chan T) {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T), rc.getChan(falseID).(chan T)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		trueC := make(chan T, buf)
		falseC := make(chan T, buf)
		m := trueMeta
		m.buffer = buf
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
// Broadcast
// ---------------------------------------------------------------------------

// Broadcast fans out each item to n identical output pipelines.
// All n pipelines must be consumed. The stage blocks until all consumers
// have accepted each item (synchronised fan-out).
func Broadcast[T any](p *Pipeline[T], n int, opts ...StageOption) []*Pipeline[T] {
	if n < 2 {
		panic("kitsune: Broadcast requires n >= 2")
	}
	track(p)
	cfg := buildStageConfig(opts)

	// Allocate IDs for all outputs at construction time.
	ids := make([]int64, n)
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
			inputs: []int64{p.id},
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
		buf := rc.effectiveBufSize(cfg)
		chans := make([]chan T, n)
		for i := range chans {
			chans[i] = make(chan T, buf)
		}
		m := metas[0]
		m.buffer = buf
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
	ids := make([]int64, n)
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
			inputs: []int64{p.id},
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
		buf := rc.effectiveBufSize(cfg)
		chans := make([]chan T, n)
		for i := range chans {
			chans[i] = make(chan T, buf)
		}
		m := metas[0]
		m.buffer = buf
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
// KeyedBalance
// ---------------------------------------------------------------------------

// KeyedBalance distributes items across n output pipelines by consistent hash
// of a key derived from each item. All items with the same key are routed to
// the same output branch, enabling per-entity parallelism without cross-branch
// coordination. Each item goes to exactly one output. All n pipelines must be
// consumed. Complements MapWithKey for stateful, shard-parallel workloads.
//
// Note: if keyFn produces a low-cardinality key set, some branches may receive
// more items than others. A slow consumer on any branch will block the
// dispatcher for that shard; increase buffer via Buffer to absorb skew.
func KeyedBalance[T any](p *Pipeline[T], n int, keyFn func(T) string, opts ...StageOption) []*Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)

	// Allocate IDs for all outputs at construction time.
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = nextPipelineID()
	}

	metas := make([]stageMeta, n)
	for i := range metas {
		name := orDefault(cfg.name, "keyed_balance")
		if i > 0 {
			name = name + "_" + string(rune('0'+i))
		}
		metas[i] = stageMeta{
			id:     ids[i],
			kind:   "keyed_balance",
			name:   name,
			buffer: cfg.buffer,
			inputs: []int64{p.id},
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
		buf := rc.effectiveBufSize(cfg)
		chans := make([]chan T, n)
		for i := range chans {
			chans[i] = make(chan T, buf)
		}
		m := metas[0]
		m.buffer = buf
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
					shard := int(fnv32str(keyFn(item)) % uint32(len(outboxes)))
					if err := outboxes[shard].Send(ctx, item); err != nil {
						return err
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
// Share
// ---------------------------------------------------------------------------

// Share returns a subscription factory for hot multicast without a fixed
// subscriber count. Call the returned function once per desired output branch;
// each branch receives every item from p.
//
// # Share vs Broadcast
//
// [Broadcast] requires the exact number of consumers to be known at
// construction time — all branches are created in one call and share the same
// options. Share is the right choice when:
//
//   - The consumer list is built dynamically (e.g. in a loop driven by config,
//     feature flags, or a plugin registry).
//   - Different consumers need different buffer sizes or stage names.
//   - You want to name each branch at the call site for readability.
//
// Example — building subscribers from a config-driven list:
//
//	subscribe := kitsune.Share(events)
//
//	// Each consumer opts in independently with its own settings.
//	audit   := subscribe(kitsune.WithName("audit"),   kitsune.Buffer(1000))
//	metrics := subscribe(kitsune.WithName("metrics"),  kitsune.Buffer(16))
//	if cfg.FraudDetectionEnabled {
//	    fraud = subscribe(kitsune.WithName("fraud"))
//	}
//
// The equivalent with Broadcast would require knowing N upfront and indexing
// into an array, which obscures which branch is which.
//
// # Delivery semantics
//
// All branches are wired into a single fan-out stage. Every item is delivered
// to every branch in order (synchronised fan-out, same blocking semantics as
// Broadcast). A slow branch backpressures the upstream and all other branches.
//
// # Late-subscriber semantics
//
// "Subscribing late" means calling the factory after earlier subscribe calls
// but before [Runner.Run]. Since the pipeline graph is fully static at Run
// time, every registered branch receives every item from the start — there is
// no replay and no runtime subscription. Calling the factory after Run has
// started panics.
//
// At least one subscribe call is required before building the runner.
// All returned pipelines must be consumed (directly or via [MergeRunners]).
//
// StageOption values passed to Share act as defaults for all branches.
// Options passed to individual subscribe calls override the defaults for
// that branch (later options win, so per-branch opts shadow factory opts).
// In particular, [Buffer] and [WithName] can be set per-branch.
func Share[T any](p *Pipeline[T], opts ...StageOption) func(...StageOption) *Pipeline[T] {
	track(p)

	type branchInfo struct {
		id             int64
		meta           stageMeta
		buffer         int
		bufferExplicit bool
	}

	var (
		mu       sync.Mutex
		branches []branchInfo
		frozen   bool
	)

	var once sync.Once

	sharedBuild := func(rc *runCtx) {
		once.Do(func() {
			mu.Lock()
			frozen = true
			bs := make([]branchInfo, len(branches))
			copy(bs, branches)
			mu.Unlock()

			if len(bs) == 0 {
				panic("kitsune: Share requires at least one subscribe() call before Run()")
			}

			inCh := p.build(rc)
			chans := make([]chan T, len(bs))
			for i, b := range bs {
				bufSize := b.buffer
				if !b.bufferExplicit {
					bufSize = rc.defaultBufSize()
				}
				ch := make(chan T, bufSize)
				chans[i] = ch
				rc.setChan(b.id, ch)
			}

			firstCh := chans[0]
			m := bs[0].meta
			if !bs[0].bufferExplicit {
				m.buffer = rc.defaultBufSize()
			}
			m.getChanLen = func() int { return len(firstCh) }
			m.getChanCap = func() int { return cap(firstCh) }

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
		})
	}

	return func(subOpts ...StageOption) *Pipeline[T] {
		cfg := buildStageConfig(append(append([]StageOption{}, opts...), subOpts...))

		mu.Lock()
		if frozen {
			mu.Unlock()
			panic("kitsune: Share subscribe() called after Run() started")
		}
		idx := len(branches)
		name := cfg.name
		if name == "" {
			if idx == 0 {
				name = "share"
			} else {
				name = "share_" + strconv.Itoa(idx)
			}
		}
		id := nextPipelineID()
		b := branchInfo{
			id:             id,
			buffer:         cfg.buffer,
			bufferExplicit: cfg.bufferExplicit,
			meta: stageMeta{
				id:     id,
				kind:   "share",
				name:   name,
				buffer: cfg.buffer,
				inputs: []int64{p.id},
			},
		}
		branches = append(branches, b)
		mu.Unlock()

		return newPipeline(id, b.meta, func(rc *runCtx) chan T {
			if existing := rc.getChan(id); existing != nil {
				return existing.(chan T)
			}
			sharedBuild(rc)
			return rc.getChan(id).(chan T)
		})
	}
}
