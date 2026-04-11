package kitsune

import (
	"context"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// Filter
// ---------------------------------------------------------------------------

// Filter emits only items for which pred returns true.
func Filter[T any](p *Pipeline[T], pred func(context.Context, T) (bool, error), opts ...StageOption) *Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:       id,
		kind:     "filter",
		name:     orDefault(cfg.name, "filter"),
		buffer:   cfg.buffer,
		overflow: cfg.overflow,
		inputs:   []int{p.id},
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
		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		var stage stageFunc
		if isFastPathEligible(cfg, hook) {
			stage = filterFastPath(inCh, ch, pred)
		} else {
			stage = func(ctx context.Context) error {
				defer close(ch)
				defer func() { go internal.DrainChan(inCh) }()

				hook.OnStageStart(ctx, cfg.name)
				var processed, errs int64
				defer func() { hook.OnStageDone(ctx, cfg.name, processed, errs) }()

				outbox := internal.NewOutbox(ch, cfg.overflow, hook, cfg.name)

				inner := func() error {
					for {
						select {
						case item, ok := <-inCh:
							if !ok {
								return nil
							}
							keep, err := pred(ctx, item)
							if err != nil {
								errs++
								return internal.WrapStageErr(cfg.name, err, 0)
							}
							if !keep {
								continue
							}
							processed++
							if err := outbox.Send(ctx, item); err != nil {
								return err
							}
						case <-ctx.Done():
							return ctx.Err()
						}
					}
				}
				return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
			}
		}
		rc.add(stage, m)
		return ch
	}
	result := newPipeline(id, meta, build)
	// Set fusionEntry when cfg conditions hold (hook check deferred to run time).
	if isFastPathEligibleCfg(cfg) {
		pred0 := pred
		p0 := p
		result.fusionEntry = func(rc *runCtx, sink func(context.Context, T) error) stageFunc {
			if p0.fusionEntry != nil && p0.consumerCount.Load() == 1 {
				return p0.fusionEntry(rc, func(ctx context.Context, item T) error {
					keep, err := pred0(ctx, item)
					if err != nil {
						return err
					}
					if !keep {
						return nil
					}
					return sink(ctx, item)
				})
			}
			inCh := p0.build(rc)
			return func(ctx context.Context) error {
				defer func() { go internal.DrainChan(inCh) }()
				var buf [internal.ReceiveBatchSize]T
				for {
					item, ok := <-inCh
					if !ok {
						return nil
					}
					buf[0] = item
					n := 1
					closed := false
				fillFusedFilter:
					for n < internal.ReceiveBatchSize {
						select {
						case v, ok2 := <-inCh:
							if !ok2 {
								closed = true
								break fillFusedFilter
							}
							buf[n] = v
							n++
						default:
							break fillFusedFilter
						}
					}
					for i := range n {
						it := buf[i]
						var zero T
						buf[i] = zero
						keep, err := pred0(ctx, it)
						if err != nil {
							return err
						}
						if keep {
							if err := sink(ctx, it); err != nil {
								return err
							}
						}
					}
					if closed {
						return nil
					}
					if ctx.Err() != nil {
						return ctx.Err()
					}
				}
			}
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// Tap
// ---------------------------------------------------------------------------

// filterFastPath is the drain-protocol + micro-batching fast path for Filter.
// Conditions: DefaultHandler, OverflowBlock, no supervision, no timeout, NoopHook.
func filterFastPath[T any](inCh <-chan T, outCh chan T, pred func(context.Context, T) (bool, error)) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		var buf [internal.ReceiveBatchSize]T
		for {
			item, ok := <-inCh
			if !ok {
				return nil
			}
			buf[0] = item
			n := 1
			closed := false
		fillFilter:
			for n < internal.ReceiveBatchSize {
				select {
				case v, ok2 := <-inCh:
					if !ok2 {
						closed = true
						break fillFilter
					}
					buf[n] = v
					n++
				default:
					break fillFilter
				}
			}
			for i := range n {
				it := buf[i]
				var zero T
				buf[i] = zero
				keep, err := pred(ctx, it)
				if err != nil {
					return err
				}
				if keep {
					outCh <- it
				}
			}
			if closed {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
}

// Tap calls fn for each item as a side effect, then passes the item downstream
// unchanged. Errors from fn halt the pipeline (use OnError to change this).
func Tap[T any](p *Pipeline[T], fn func(context.Context, T) error, opts ...StageOption) *Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "tap",
		name:   orDefault(cfg.name, "tap"),
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
		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			hook.OnStageStart(ctx, cfg.name)
			var processed, errs int64
			defer func() { hook.OnStageDone(ctx, cfg.name, processed, errs) }()

			outbox := internal.NewOutbox(ch, cfg.overflow, hook, cfg.name)

			inner := func() error {
				for {
					select {
					case item, ok := <-inCh:
						if !ok {
							return nil
						}
						start := time.Now()
						err := fn(ctx, item)
						dur := time.Since(start)
						if err != nil {
							errs++
							hook.OnItem(ctx, cfg.name, dur, err)
							return internal.WrapStageErr(cfg.name, err, 0)
						}
						processed++
						hook.OnItem(ctx, cfg.name, dur, nil)
						if err := outbox.Send(ctx, item); err != nil {
							return err
						}
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
			return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}

// TapError calls fn as a side-effect when the pipeline terminates with a
// non-context error, then re-propagates that error unchanged. It is the
// complement to [Tap]: Tap observes items; TapError observes terminal errors.
// Useful for logging, metrics, or alerting on error paths without altering
// pipeline flow.
//
// fn receives the terminal error and the context that was active when the
// pipeline exited. Context cancellation (ctx.Err() != nil) does not trigger
// fn — only pipeline-level errors do.
func TapError[T any](p *Pipeline[T], fn func(context.Context, error)) *Pipeline[T] {
	return Generate(func(ctx context.Context, yield func(T) bool) error {
		innerCtx, cancel := context.WithCancel(ctx)
		stopped := false
		err := p.ForEach(func(_ context.Context, item T) error {
			if !yield(item) {
				stopped = true
				cancel()
			}
			return nil
		}).Run(innerCtx)
		cancel()
		if stopped {
			return nil
		}
		if err != nil && ctx.Err() == nil {
			fn(ctx, err)
		}
		return err
	})
}

// Finally calls fn as a side-effect when the pipeline exits for any reason —
// successful completion, context cancellation, or error — then re-propagates
// the outcome unchanged. Use it for guaranteed cleanup, resource tracking, or
// test assertions that must run regardless of how the pipeline terminates.
//
// fn receives the terminal error (nil on success or early consumer stop) and
// the context that was active at exit time. Unlike [TapError], fn fires even
// on context cancellation.
func Finally[T any](p *Pipeline[T], fn func(context.Context, error)) *Pipeline[T] {
	return Generate(func(ctx context.Context, yield func(T) bool) error {
		innerCtx, cancel := context.WithCancel(ctx)
		stopped := false
		err := p.ForEach(func(_ context.Context, item T) error {
			if !yield(item) {
				stopped = true
				cancel()
			}
			return nil
		}).Run(innerCtx)
		cancel()
		if stopped {
			fn(ctx, nil)
			return nil
		}
		fn(ctx, err)
		return err
	})
}

// ExpandMap performs a breadth-first expansion of a pipeline. For each item
// emitted by p, fn is called to produce a child [*Pipeline[T]]; the children
// are emitted and then themselves expanded, level by level, until no more
// children are produced.
//
// Emission order is BFS: all items at depth N are emitted before any item at
// depth N+1. fn may return nil to signal that an item has no children.
//
// Options:
//   - [WithName] labels the stage for metrics and traces.
//   - [VisitedBy] prevents re-visiting items whose key was already seen,
//     breaking infinite loops in cyclic graphs. Defaults to [MemoryDedupSet];
//     override the backend with [WithDedupSet].
//
// Typical uses: tree traversal, recursive API pagination, graph walks where
// each node expands into its neighbours.
//
//	// Walk a directory tree
//	kitsune.ExpandMap(kitsune.FromSlice(roots), func(ctx context.Context, dir Dir) *kitsune.Pipeline[Dir] {
//	    children, _ := dir.ReadChildren(ctx)
//	    return kitsune.FromSlice(children)
//	})
func ExpandMap[T any](p *Pipeline[T], fn func(context.Context, T) *Pipeline[T], opts ...StageOption) *Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "expand_map",
		name:   orDefault(cfg.name, "expand_map"),
		buffer: cfg.buffer,
	}
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		ch := make(chan T, cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)

		// Resolve dedup: VisitedBy sets visitedKeyFn; WithDedupSet overrides the backend.
		var keyFn func(T) string
		if raw := cfg.visitedKeyFn; raw != nil {
			keyFn = raw.(func(T) string)
		}
		var set DedupSet
		if keyFn != nil {
			if cfg.dedupSet != nil {
				set = cfg.dedupSet
			} else {
				set = MemoryDedupSet()
			}
		}

		stage := func(ctx context.Context) error {
			defer close(ch)
			outbox := internal.NewBlockingOutbox(ch)

			queue := []*Pipeline[T]{p}
			for len(queue) > 0 {
				current := queue[0]
				queue = queue[1:]

				innerCtx, cancel := context.WithCancel(ctx)
				var sendErr error
				err := current.ForEach(func(_ context.Context, item T) error {
					// Dedup check — skip item and its subtree if already visited.
					if set != nil {
						key := keyFn(item)
						dup, err := set.Contains(innerCtx, key)
						if err != nil {
							return err
						}
						if dup {
							return nil
						}
						if err := set.Add(innerCtx, key); err != nil {
							return err
						}
					}
					// Emit item to downstream.
					if err := outbox.Send(ctx, item); err != nil {
						sendErr = err
						cancel()
						return nil
					}
					// Enqueue children for BFS expansion.
					if child := fn(ctx, item); child != nil {
						queue = append(queue, child)
					}
					return nil
				}).Run(innerCtx)
				cancel()

				if sendErr != nil {
					return sendErr
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err != nil {
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
