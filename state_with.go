package kitsune

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// fnv32str returns a stable FNV-1a hash of s. Used to shard items by key.
func fnv32str(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// ---------------------------------------------------------------------------
// MapWith helpers (serial / concurrent / ordered)
// ---------------------------------------------------------------------------

func mapWithSerial[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	fn func(context.Context, *Ref[S], I) (O, error),
	ref *Ref[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	adaptedFn := func(ctx context.Context, it I) (O, error) { return fn(ctx, ref, it) }
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var processed, errs int64
		defer func() { hook.OnStageDone(ctx, cfg.name, processed, errs) }()

		outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)

		inner := func() error {
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					start := time.Now()
					val, err, attempt := internal.ProcessItem(ctx, adaptedFn, item, cfg.errorHandler)
					dur := time.Since(start)
					if err == internal.ErrSkipped {
						errs++
						hook.OnItem(ctx, cfg.name, dur, err)
						continue
					}
					if err != nil {
						errs++
						hook.OnItem(ctx, cfg.name, dur, err)
						return internal.WrapStageErr(cfg.name, err, attempt)
					}
					processed++
					hook.OnItem(ctx, cfg.name, dur, nil)
					if err := outbox.Send(ctx, val); err != nil {
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

// mapWithConcurrent launches n persistent worker goroutines that all read from
// the shared inCh. Each worker owns an independent Ref[S] initialised from
// key.initial, so no cross-worker locking is needed.
func mapWithConcurrent[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	fn func(context.Context, *Ref[S], I) (O, error),
	workerRefs []*Ref[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
			errCh := make(chan error, 1)
			var wg sync.WaitGroup

			for _, ref := range workerRefs {
				ref := ref
				wg.Add(1)
				go func() {
					defer wg.Done()
					adaptedFn := func(ctx context.Context, it I) (O, error) { return fn(ctx, ref, it) }
					for {
						var item I
						var ok bool
						select {
						case item, ok = <-inCh:
							if !ok {
								return
							}
						case <-innerCtx.Done():
							return
						}
						start := time.Now()
						val, err, attempt := internal.ProcessItem(innerCtx, adaptedFn, item, cfg.errorHandler)
						dur := time.Since(start)
						if err == internal.ErrSkipped {
							errCount.Add(1)
							hook.OnItem(ctx, cfg.name, dur, err)
							continue
						}
						if err != nil {
							errCount.Add(1)
							hook.OnItem(ctx, cfg.name, dur, err)
							reportErr(errCh, internal.WrapStageErr(cfg.name, err, attempt))
							cancel()
							return
						}
						procCount.Add(1)
						hook.OnItem(ctx, cfg.name, dur, nil)
						if sendErr := outbox.Send(innerCtx, val); sendErr != nil {
							reportErr(errCh, sendErr)
							cancel()
							return
						}
					}
				}()
			}

			wg.Wait()
			select {
			case err := <-errCh:
				return err
			default:
				return nil
			}
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

// mapWithOrdered distributes items round-robin to n workers (each with its own
// Ref[S]) and re-assembles output in input order via per-item slot channels.
func mapWithOrdered[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	fn func(context.Context, *Ref[S], I) (O, error),
	workerRefs []*Ref[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	type result struct {
		val O
		dur time.Duration
		err error
		att int
	}
	type workItem struct {
		item I
		slot chan result
	}
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			n := len(workerRefs)
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
			workerChs := make([]chan workItem, n)
			for i := range workerChs {
				workerChs[i] = make(chan workItem, cfg.buffer+1)
			}
			slots := make(chan chan result, n*2)
			drainErrs := make(chan error, 1)

			// Drainer: reads slots in insertion order, emits results in sequence.
			go func() {
				for slotCh := range slots {
					r := <-slotCh
					if r.err == internal.ErrSkipped {
						continue
					}
					if r.err != nil {
						errCount.Add(1)
						hook.OnItem(ctx, cfg.name, r.dur, r.err)
						cancel()
						go func() {
							for s := range slots {
								<-s
							}
						}()
						drainErrs <- internal.WrapStageErr(cfg.name, r.err, r.att)
						return
					}
					procCount.Add(1)
					hook.OnItem(ctx, cfg.name, r.dur, nil)
					if err := outbox.Send(innerCtx, r.val); err != nil {
						cancel()
						go func() {
							for s := range slots {
								<-s
							}
						}()
						drainErrs <- err
						return
					}
				}
				drainErrs <- nil
			}()

			// Workers: each reads from its dedicated channel.
			var workerWg sync.WaitGroup
			for i, ref := range workerRefs {
				ref := ref
				workerWg.Add(1)
				go func() {
					defer workerWg.Done()
					adaptedFn := func(ctx context.Context, it I) (O, error) { return fn(ctx, ref, it) }
					for wi := range workerChs[i] {
						start := time.Now()
						val, err, att := internal.ProcessItem(innerCtx, adaptedFn, wi.item, cfg.errorHandler)
						dur := time.Since(start)
						wi.slot <- result{val: val, dur: dur, err: err, att: att}
					}
				}()
			}

			// Dispatcher: round-robin items to workers with ordered slots.
			nextWorker := 0
			func() {
				defer func() {
					for _, wCh := range workerChs {
						close(wCh)
					}
				}()
				for {
					var item I
					var ok bool
					select {
					case item, ok = <-inCh:
						if !ok {
							return
						}
					case <-innerCtx.Done():
						return
					}
					slotCh := make(chan result, 1)
					select {
					case slots <- slotCh:
					case <-innerCtx.Done():
						return
					}
					select {
					case workerChs[nextWorker] <- workItem{item: item, slot: slotCh}:
					case <-innerCtx.Done():
						return
					}
					nextWorker = (nextWorker + 1) % n
				}
			}()

			workerWg.Wait()
			close(slots)
			return <-drainErrs
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

// ---------------------------------------------------------------------------
// MapWith — stateful 1:1 transform
// ---------------------------------------------------------------------------

// MapWith applies a 1:1 transform with access to typed pipeline state.
// The Ref is scoped to the run; its value persists for the lifetime of [Runner.Run].
// Each call to Run starts with a fresh Ref initialized to key.initial.
//
// With Concurrency(n) > 1, n worker goroutines are launched. Each worker owns
// an independent Ref[S] initialized from key.initial — state is worker-local,
// not shared across workers. Add Ordered() to preserve input order.
//
// Use OnError to control what happens when fn returns an error (default: Halt).
// Use Supervise to restart the stage on error or panic; the Ref value is
// preserved across restarts.
//
//	var counterKey = kitsune.NewKey[int]("counter", 0)
//	numbered := kitsune.MapWith(p, counterKey,
//	    func(ctx context.Context, ref *kitsune.Ref[int], s string) (string, error) {
//	        n, _ := ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
//	        return fmt.Sprintf("%d: %s", n, s), nil
//	    },
//	)
func MapWith[I, O, S any](p *Pipeline[I], key Key[S], fn func(context.Context, *Ref[S], I) (O, error), opts ...StageOption) *Pipeline[O] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	n := max(1, cfg.concurrency)
	meta := stageMeta{
		id:          id,
		kind:        "map_with",
		name:        orDefault(cfg.name, "map_with"),
		concurrency: n,
		buffer:      cfg.buffer,
		overflow:    cfg.overflow,
		inputs:      []int{p.id},
		hasSuperv:   cfg.supervision.HasSupervision(),
	}

	keyName := key.name
	initial := key.initial
	ttl := key.ttl

	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}

		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan O, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)

		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		cfg := cfg // local copy; resolve pipeline-level default handler
		cfg.buffer = buf
		cfg.errorHandler = resolveHandler(cfg, rc)

		if n == 1 {
			// Serial path: single shared Ref registered through the refRegistry.
			rc.refs.register(keyName, func(store internal.Store, codec internal.Codec) any {
				return newRef[S](initial, keyName, store, codec, ttl)
			})
			stage := func(ctx context.Context) error {
				ref := rc.refs.get(keyName).(*Ref[S])
				return mapWithSerial(inCh, ch, fn, ref, cfg, hook)(ctx)
			}
			rc.add(stage, m)
		} else {
			// Concurrent path: N worker-local Refs, one per worker.
			workersKey := keyName + ":__mw_workers__"
			rc.refs.register(workersKey, func(store internal.Store, codec internal.Codec) any {
				refs := make([]*Ref[S], n)
				for i := range refs {
					refs[i] = newRef[S](initial, fmt.Sprintf("%s:worker:%d", keyName, i), store, codec, ttl)
				}
				return refs
			})
			var stage stageFunc
			if cfg.ordered {
				stage = func(ctx context.Context) error {
					workerRefs := rc.refs.get(workersKey).([]*Ref[S])
					return mapWithOrdered(inCh, ch, fn, workerRefs, cfg, hook)(ctx)
				}
			} else {
				stage = func(ctx context.Context) error {
					workerRefs := rc.refs.get(workersKey).([]*Ref[S])
					return mapWithConcurrent(inCh, ch, fn, workerRefs, cfg, hook)(ctx)
				}
			}
			rc.add(stage, m)
		}
		return ch
	}
	return newPipeline(id, meta, build)
}

// ---------------------------------------------------------------------------
// FlatMapWith helpers (serial / concurrent / ordered)
// ---------------------------------------------------------------------------

func flatMapWithSerial[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	fn func(context.Context, *Ref[S], I, func(O) error) error,
	ref *Ref[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var processed, errs int64
		defer func() { hook.OnStageDone(ctx, cfg.name, processed, errs) }()

		outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
		send := func(v O) error { return outbox.Send(ctx, v) }
		adaptedFn := func(ctx context.Context, it I, yield func(O) error) error {
			return fn(ctx, ref, it, yield)
		}

		inner := func() error {
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					start := time.Now()
					err, attempt := internal.ProcessFlatMapItem(ctx, adaptedFn, item, cfg.errorHandler, send)
					dur := time.Since(start)
					if err == internal.ErrSkipped {
						errs++
						hook.OnItem(ctx, cfg.name, dur, err)
						continue
					}
					if err != nil {
						errs++
						hook.OnItem(ctx, cfg.name, dur, err)
						return internal.WrapStageErr(cfg.name, err, attempt)
					}
					processed++
					hook.OnItem(ctx, cfg.name, dur, nil)
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

func flatMapWithConcurrent[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	fn func(context.Context, *Ref[S], I, func(O) error) error,
	workerRefs []*Ref[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
			errCh := make(chan error, 1)
			var wg sync.WaitGroup

			for _, ref := range workerRefs {
				ref := ref
				wg.Add(1)
				go func() {
					defer wg.Done()
					adaptedFn := func(ctx context.Context, it I, yield func(O) error) error {
						return fn(ctx, ref, it, yield)
					}
					send := func(v O) error { return outbox.Send(innerCtx, v) }
					for {
						var item I
						var ok bool
						select {
						case item, ok = <-inCh:
							if !ok {
								return
							}
						case <-innerCtx.Done():
							return
						}
						start := time.Now()
						err, attempt := internal.ProcessFlatMapItem(innerCtx, adaptedFn, item, cfg.errorHandler, send)
						dur := time.Since(start)
						if err == internal.ErrSkipped {
							errCount.Add(1)
							hook.OnItem(ctx, cfg.name, dur, err)
							continue
						}
						if err != nil {
							errCount.Add(1)
							hook.OnItem(ctx, cfg.name, dur, err)
							reportErr(errCh, internal.WrapStageErr(cfg.name, err, attempt))
							cancel()
							return
						}
						procCount.Add(1)
						hook.OnItem(ctx, cfg.name, dur, nil)
					}
				}()
			}

			wg.Wait()
			select {
			case err := <-errCh:
				return err
			default:
				return nil
			}
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

func flatMapWithOrdered[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	fn func(context.Context, *Ref[S], I, func(O) error) error,
	workerRefs []*Ref[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	type result struct {
		items []O
		dur   time.Duration
		err   error
		att   int
	}
	type workItem struct {
		item I
		slot chan result
	}
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			n := len(workerRefs)
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
			workerChs := make([]chan workItem, n)
			for i := range workerChs {
				workerChs[i] = make(chan workItem, cfg.buffer+1)
			}
			slots := make(chan chan result, n*2)
			drainErrs := make(chan error, 1)

			// Drainer: emit each item group in order.
			go func() {
				for slotCh := range slots {
					r := <-slotCh
					if r.err == internal.ErrSkipped {
						continue
					}
					if r.err != nil {
						errCount.Add(1)
						hook.OnItem(ctx, cfg.name, r.dur, r.err)
						cancel()
						go func() {
							for s := range slots {
								<-s
							}
						}()
						drainErrs <- internal.WrapStageErr(cfg.name, r.err, r.att)
						return
					}
					procCount.Add(1)
					hook.OnItem(ctx, cfg.name, r.dur, nil)
					for _, v := range r.items {
						if err := outbox.Send(innerCtx, v); err != nil {
							cancel()
							go func() {
								for s := range slots {
									<-s
								}
							}()
							drainErrs <- err
							return
						}
					}
				}
				drainErrs <- nil
			}()

			// Workers.
			var workerWg sync.WaitGroup
			for i, ref := range workerRefs {
				ref := ref
				workerWg.Add(1)
				go func() {
					defer workerWg.Done()
					for wi := range workerChs[i] {
						var items []O
						collectYield := func(v O) error {
							items = append(items, v)
							return nil
						}
						adaptedFn := func(ctx context.Context, it I, yield func(O) error) error {
							return fn(ctx, ref, it, yield)
						}
						start := time.Now()
						err, att := internal.ProcessFlatMapItem(innerCtx, adaptedFn, wi.item, cfg.errorHandler, collectYield)
						dur := time.Since(start)
						wi.slot <- result{items: items, dur: dur, err: err, att: att}
					}
				}()
			}

			// Dispatcher: round-robin with ordered slots.
			nextWorker := 0
			func() {
				defer func() {
					for _, wCh := range workerChs {
						close(wCh)
					}
				}()
				for {
					var item I
					var ok bool
					select {
					case item, ok = <-inCh:
						if !ok {
							return
						}
					case <-innerCtx.Done():
						return
					}
					slotCh := make(chan result, 1)
					select {
					case slots <- slotCh:
					case <-innerCtx.Done():
						return
					}
					select {
					case workerChs[nextWorker] <- workItem{item: item, slot: slotCh}:
					case <-innerCtx.Done():
						return
					}
					nextWorker = (nextWorker + 1) % n
				}
			}()

			workerWg.Wait()
			close(slots)
			return <-drainErrs
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

// ---------------------------------------------------------------------------
// FlatMapWith — stateful 1:N transform
// ---------------------------------------------------------------------------

// FlatMapWith applies a 1:N transform with access to typed pipeline state.
// The Ref is scoped to the run; its value persists for the lifetime of [Runner.Run].
//
// With Concurrency(n) > 1, n worker goroutines are launched. Each worker owns
// an independent Ref[S] initialized from key.initial. Add Ordered() to preserve
// input order (all outputs for item i are emitted before any outputs for item i+1).
//
// Use OnError to control what happens when fn returns an error (default: Halt).
// Use Supervise to restart the stage on error or panic; the Ref value is
// preserved across restarts.
func FlatMapWith[I, O, S any](p *Pipeline[I], key Key[S], fn func(context.Context, *Ref[S], I, func(O) error) error, opts ...StageOption) *Pipeline[O] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	n := max(1, cfg.concurrency)
	meta := stageMeta{
		id:          id,
		kind:        "flat_map_with",
		name:        orDefault(cfg.name, "flat_map_with"),
		concurrency: n,
		buffer:      cfg.buffer,
		overflow:    cfg.overflow,
		inputs:      []int{p.id},
		hasSuperv:   cfg.supervision.HasSupervision(),
	}

	keyName := key.name
	initial := key.initial
	ttl := key.ttl

	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}

		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan O, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)

		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		cfg := cfg // local copy; resolve pipeline-level default handler
		cfg.buffer = buf
		cfg.errorHandler = resolveHandler(cfg, rc)

		if n == 1 {
			rc.refs.register(keyName, func(store internal.Store, codec internal.Codec) any {
				return newRef[S](initial, keyName, store, codec, ttl)
			})
			stage := func(ctx context.Context) error {
				ref := rc.refs.get(keyName).(*Ref[S])
				return flatMapWithSerial(inCh, ch, fn, ref, cfg, hook)(ctx)
			}
			rc.add(stage, m)
		} else {
			workersKey := keyName + ":__fmw_workers__"
			rc.refs.register(workersKey, func(store internal.Store, codec internal.Codec) any {
				refs := make([]*Ref[S], n)
				for i := range refs {
					refs[i] = newRef[S](initial, fmt.Sprintf("%s:fmw:worker:%d", keyName, i), store, codec, ttl)
				}
				return refs
			})
			var stage stageFunc
			if cfg.ordered {
				stage = func(ctx context.Context) error {
					workerRefs := rc.refs.get(workersKey).([]*Ref[S])
					return flatMapWithOrdered(inCh, ch, fn, workerRefs, cfg, hook)(ctx)
				}
			} else {
				stage = func(ctx context.Context) error {
					workerRefs := rc.refs.get(workersKey).([]*Ref[S])
					return flatMapWithConcurrent(inCh, ch, fn, workerRefs, cfg, hook)(ctx)
				}
			}
			rc.add(stage, m)
		}
		return ch
	}
	return newPipeline(id, meta, build)
}
