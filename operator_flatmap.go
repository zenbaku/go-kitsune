package kitsune

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// FlatMap
// ---------------------------------------------------------------------------

// FlatMap transforms each item from p into zero or more output items using fn.
// fn calls yield for each output item it wants to emit.
//
// With Concurrency(n) > 1, items are processed in parallel. Add Ordered() to
// emit results in the original input order (all items from input i are emitted
// before any from input i+1).
func FlatMap[I, O any](p *Pipeline[I], fn func(context.Context, I, func(O) error) error, opts ...StageOption) *Pipeline[O] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:          id,
		kind:        "flat_map",
		name:        orDefault(cfg.name, "flat_map"),
		concurrency: cfg.concurrency,
		buffer:      cfg.buffer,
		overflow:    cfg.overflow,
		inputs:      []int64{p.id},
	}
	var out *Pipeline[O]
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
		rc.initDrainNotify(id, out.consumerCount.Load())
		drainCh := rc.drainCh(id)
		drainFn := func() { rc.signalDrain(p.id) }
		fmHook := rc.hook
		if fmHook == nil {
			fmHook = internal.NoopHook{}
		}
		cfg := cfg // local copy; resolve pipeline-level default handler
		cfg.errorHandler = resolveHandler(cfg, rc)

		var stage stageFunc
		switch {
		case cfg.concurrency > 1 && cfg.ordered:
			stage = flatMapOrdered(inCh, ch, fn, cfg, fmHook, drainFn, drainCh)
		case cfg.concurrency > 1:
			stage = flatMapConcurrent(inCh, ch, fn, cfg, fmHook, drainFn, drainCh)
		case isFastPathEligible(cfg, fmHook):
			stage = flatMapSerialFastPath(inCh, ch, fn, cfg.name, drainFn, drainCh)
		default:
			stage = flatMapSerial(inCh, ch, fn, cfg, fmHook, drainFn, drainCh)
		}
		rc.add(stage, m)
		return ch
	}
	out = newPipeline(id, meta, build)
	return out
}

// flatMapSerialFastPath is the drain-protocol + micro-batching fast path for serial FlatMap.
// The yield closure is allocated once outside the loop; zero allocs per item.
func flatMapSerialFastPath[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I, func(O) error) error, name string, drainFn func(), drainCh <-chan struct{}) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		cooperativeDrain := false
		defer func() {
			if !cooperativeDrain {
				go internal.DrainChan(inCh)
			}
		}()
		defer drainFn()

		// yield is allocated once; outCh <- v is a plain send (drain protocol).
		yield := func(v O) error {
			outCh <- v
			return nil
		}

		var buf [internal.ReceiveBatchSize]I
		for {
			var item I
			var ok bool
			select {
			case item, ok = <-inCh:
			case <-drainCh:
				cooperativeDrain = true
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
			if !ok {
				return nil
			}
			buf[0] = item
			n := 1
			closed := false
		fillFlatMap:
			for n < internal.ReceiveBatchSize {
				select {
				case v, ok2 := <-inCh:
					if !ok2 {
						closed = true
						break fillFlatMap
					}
					buf[n] = v
					n++
				default:
					break fillFlatMap
				}
			}
			for i := range n {
				it := buf[i]
				var zero I
				buf[i] = zero
				if err := fn(internal.ItemCtx(ctx, it), it, yield); err != nil {
					return internal.WrapStageErr(name, err, 0)
				}
			}
			if closed {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			select {
			case <-drainCh:
				cooperativeDrain = true
				return nil
			default:
			}
		}
	}
}

func flatMapSerial[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I, func(O) error) error, cfg stageConfig, hook internal.Hook, drainFn func(), drainCh <-chan struct{}) stageFunc {
	var ctxMapper func(I) context.Context
	if raw := cfg.contextMapperFn; raw != nil {
		ctxMapper = raw.(func(I) context.Context)
	}
	return func(ctx context.Context) error {
		defer close(outCh)
		cooperativeDrain := false
		defer func() {
			if !cooperativeDrain {
				go internal.DrainChan(inCh)
			}
		}()
		defer drainFn()

		hook.OnStageStart(ctx, cfg.name)
		var processed, errs int64
		defer func() { hook.OnStageDone(ctx, cfg.name, processed, errs) }()

		outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
		send := func(v O) error { return outbox.Send(ctx, v) }

		inner := func() error {
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					itemCtx, cancelItem := itemContext(ctx, cfg)
					start := time.Now()
					err, attempt := internal.ProcessFlatMapItem(itemCtx, fn, item, cfg.errorHandler, send, ctxMapper)
					dur := time.Since(start)
					cancelItem()
					if err == internal.ErrSkipped {
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
				case <-drainCh:
					cooperativeDrain = true
					return nil
				}
			}
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

// flatMapConcurrent does not support supervision (Supervise stage option is silently
// ignored when Concurrency > 1). Use Concurrency(1) to enable supervision on FlatMap.
func flatMapConcurrent[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I, func(O) error) error, cfg stageConfig, hook internal.Hook, drainFn func(), drainCh <-chan struct{}) stageFunc {
	var ctxMapper func(I) context.Context
	if raw := cfg.contextMapperFn; raw != nil {
		ctxMapper = raw.(func(I) context.Context)
	}
	return func(ctx context.Context) error {
		defer close(outCh)
		cooperativeDrain := false
		defer func() {
			if !cooperativeDrain {
				go internal.DrainChan(inCh)
			}
		}()
		defer drainFn()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			// When DropOldest and Concurrency > 1 are both active, shard the
			// outbox so workers do not contend on a single mutex under
			// sustained backpressure. See mapConcurrent for the same pattern.
			var boxes []internal.Outbox[O]
			if cfg.overflow == internal.OverflowDropOldest && cfg.concurrency > 1 {
				boxes = internal.NewShardedDropOldestOutbox(outCh, cfg.concurrency, hook, cfg.name)
			} else {
				boxes = []internal.Outbox[O]{internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)}
			}
			workerIdx := 0

			sem := make(chan struct{}, cfg.concurrency)
			errCh := make(chan error, 1)
			var wg sync.WaitGroup

			func() {
				for {
					select {
					case item, ok := <-inCh:
						if !ok {
							return
						}
						// Use innerCtx.Done() only; reading errCh here would drain it (BUG: removed for test)
						// before the final select below can return it to Supervise.
						select {
						case sem <- struct{}{}:
						case <-innerCtx.Done():
							return
						}
						outbox := boxes[workerIdx%len(boxes)]
						workerIdx++
						wg.Add(1)
						go func(it I, outbox internal.Outbox[O]) {
							defer wg.Done()
							defer func() { <-sem }()

							itemCtx, cancelItem := itemContext(innerCtx, cfg)
							send := func(v O) error { return outbox.Send(innerCtx, v) }
							start := time.Now()
							err, attempt := internal.ProcessFlatMapItem(itemCtx, fn, it, cfg.errorHandler, send, ctxMapper)
							dur := time.Since(start)
							cancelItem()
							if err != nil && err != internal.ErrSkipped {
								errCount.Add(1)
								hook.OnItem(ctx, cfg.name, dur, err)
								reportErr(errCh, internal.WrapStageErr(cfg.name, err, attempt))
								cancel()
								return
							}
							if err == nil {
								procCount.Add(1)
								hook.OnItem(ctx, cfg.name, dur, nil)
							}
						}(item, outbox)
					case <-innerCtx.Done():
						return
					case <-drainCh:
						cooperativeDrain = true
						cancel()
						return
					}
				}
			}()

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

func flatMapOrdered[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I, func(O) error) error, cfg stageConfig, hook internal.Hook, drainFn func(), drainCh <-chan struct{}) stageFunc {
	var ctxMapper func(I) context.Context
	if raw := cfg.contextMapperFn; raw != nil {
		ctxMapper = raw.(func(I) context.Context)
	}
	type result struct {
		items []O
		dur   time.Duration
		err   error
		att   int
	}
	return func(ctx context.Context) error {
		defer close(outCh)
		cooperativeDrain := false
		defer func() {
			if !cooperativeDrain {
				go internal.DrainChan(inCh)
			}
		}()
		defer drainFn()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
			sem := make(chan struct{}, cfg.concurrency)
			slots := make(chan chan result, cfg.concurrency*2)
			drainErrs := make(chan error, 1)

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

			func() {
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
					case <-drainCh:
						cooperativeDrain = true
						cancel()
						return
					}

					select {
					case sem <- struct{}{}:
					case <-innerCtx.Done():
						return
					}

					slotCh := make(chan result, 1)
					select {
					case slots <- slotCh:
					case <-innerCtx.Done():
						<-sem
						return
					}

					go func(it I, sc chan result) {
						defer func() { <-sem }()
						itemCtx, cancelItem := itemContext(innerCtx, cfg)
						var buf []O
						collect := func(v O) error {
							buf = append(buf, v)
							return nil
						}
						start := time.Now()
						err, att := internal.ProcessFlatMapItem(itemCtx, fn, it, cfg.errorHandler, collect, ctxMapper)
						dur := time.Since(start)
						cancelItem()
						sc <- result{items: buf, dur: dur, err: err, att: att}
					}(item, slotCh)
				}
			}()

			for i := 0; i < cfg.concurrency; i++ {
				sem <- struct{}{}
			}
			close(slots)
			return <-drainErrs
		}

		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}
