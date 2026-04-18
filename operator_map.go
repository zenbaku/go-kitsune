package kitsune

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// Map
// ---------------------------------------------------------------------------

// Map transforms each item from p using fn. The resulting pipeline emits one
// output item per input item.
//
// With Concurrency(n) > 1, items are processed in parallel. Add Ordered() to
// emit results in the original input order. Without Ordered(), results are
// emitted as they complete (faster but unordered).
//
// Use Timeout(d) to impose a per-item deadline. Use OnError to control what
// happens when fn returns an error (default: Halt).
func Map[I, O any](p *Pipeline[I], fn func(context.Context, I) (O, error), opts ...StageOption) *Pipeline[O] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	fastPathCfg := isFastPathEligibleCfg(cfg) && cfg.cacheConfig == nil
	meta := stageMeta{
		id:               id,
		kind:             "map",
		name:             orDefault(cfg.name, "map"),
		concurrency:      cfg.concurrency,
		buffer:           cfg.buffer,
		overflow:         cfg.overflow,
		timeout:          cfg.timeout,
		hasRetry:         !internal.IsDefaultHandler(cfg.errorHandler),
		inputs:           []int64{p.id},
		hasSuperv:        cfg.supervision.HasSupervision(),
		supportsFastPath: true,
		isFastPathCfg:    fastPathCfg,
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

		// Wrap fn with cache lookup/write when CacheBy is set.
		actualFn := fn
		if cc := cfg.cacheConfig; cc != nil {
			keyFn := cc.keyFn.(func(I) string)
			cache := cc.cache
			if cache == nil {
				cache = rc.cache
			}
			ttl := cc.ttl
			if ttl == 0 {
				ttl = rc.cacheTTL
			}
			codec := rc.codec
			if codec == nil {
				codec = internal.JSONCodec{}
			}
			if cache != nil {
				origFn := fn
				actualFn = func(ctx context.Context, item I) (O, error) {
					key := keyFn(item)
					if data, ok, err := cache.Get(ctx, key); err == nil && ok {
						var out O
						if uErr := codec.Unmarshal(data, &out); uErr == nil {
							return out, nil
						}
					}
					out, err := origFn(ctx, item)
					if err != nil {
						return out, err
					}
					if data, mErr := codec.Marshal(out); mErr == nil {
						_ = cache.Set(ctx, key, data, ttl)
					}
					return out, nil
				}
			}
		}

		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		cfg := cfg // local copy; resolve pipeline-level default handler
		cfg.errorHandler = resolveHandler(cfg, rc)

		var stage stageFunc
		switch {
		case cfg.concurrency > 1 && cfg.ordered:
			rc.initDrainNotify(id, out.consumerCount.Load())
			drainFn := func() { rc.signalDrain(p.id) }
			drainCh := rc.drainCh(id)
			stage = mapOrdered(inCh, ch, actualFn, cfg, hook, drainFn, drainCh)
		case cfg.concurrency > 1:
			rc.initDrainNotify(id, out.consumerCount.Load())
			drainFn := func() { rc.signalDrain(p.id) }
			drainCh := rc.drainCh(id)
			stage = mapConcurrent(inCh, ch, actualFn, cfg, hook, drainFn, drainCh)
		case isFastPathEligible(cfg, hook) && cfg.cacheConfig == nil:
			rc.initDrainNotify(id, out.consumerCount.Load())
			drainFn := func() { rc.signalDrain(p.id) }
			drainCh := rc.drainCh(id)
			stage = mapSerialFastPath(inCh, ch, actualFn, cfg.name, drainFn, drainCh)
		default:
			rc.initDrainNotify(id, out.consumerCount.Load())
			drainFn := func() { rc.signalDrain(p.id) }
			drainCh := rc.drainCh(id)
			stage = mapSerial(inCh, ch, actualFn, cfg, hook, drainFn, drainCh)
		}
		rc.add(stage, m)
		return ch
	}
	out = newPipeline(id, meta, build)
	// Set fusionEntry when cfg conditions hold (hook check deferred to run time).
	if fastPathCfg {
		// Update optimization metadata captured by the build closure.
		// The build closure captures meta by reference (Go variable capture),
		// so these updates are visible when build runs during IsOptimized or Run.
		meta.hasFusionEntry = true
		meta.getConsumerCount = func() int32 { return out.consumerCount.Load() }

		name0 := orDefault(cfg.name, "map")
		fn0 := fn
		p0 := p
		out.fusionEntry = func(rc *runCtx, sink func(context.Context, O) error) stageFunc {
			// Compose with upstream if it is also fused and has a single consumer.
			if p0.fusionEntry != nil && p0.consumerCount.Load() == 1 {
				return p0.fusionEntry(rc, func(ctx context.Context, item I) error {
					out, err := fn0(ctx, item)
					if err != nil {
						return internal.WrapStageErr(name0, err, 0)
					}
					return sink(ctx, out)
				})
			}
			// Base: use upstream channel with micro-batching.
			inCh := p0.build(rc)
			rc.initDrainNotify(p0.id, 1)
			drainCh := rc.drainCh(p0.id)
			return func(ctx context.Context) error {
				cooperativeDrain := false
				defer func() {
					if !cooperativeDrain {
						go internal.DrainChan(inCh)
					}
				}()
				defer func() { rc.signalDrain(p0.id) }()
				var buf [internal.ReceiveBatchSize]I
				for {
					var item I
					var ok bool
					select {
					case item, ok = <-inCh:
						if !ok {
							return nil
						}
					case <-drainCh:
						cooperativeDrain = true
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
					buf[0] = item
					n := 1
					closed := false
				fillFusedMap:
					for n < internal.ReceiveBatchSize {
						select {
						case v, ok2 := <-inCh:
							if !ok2 {
								closed = true
								break fillFusedMap
							}
							buf[n] = v
							n++
						default:
							break fillFusedMap
						}
					}
					for i := range n {
						it := buf[i]
						var zero I
						buf[i] = zero
						out, err := fn0(ctx, it)
						if err != nil {
							return internal.WrapStageErr(name0, err, 0)
						}
						if err := sink(ctx, out); err != nil {
							return err
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
	}
	return out
}

func mapSerial[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), cfg stageConfig, hook internal.Hook, drainFn func(), drainCh <-chan struct{}) stageFunc {
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

		inner := func() error {
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					itemCtx, cancelItem := itemContext(ctx, cfg)
					start := time.Now()
					val, err, attempt := internal.ProcessItem(itemCtx, fn, item, cfg.errorHandler, ctxMapper)
					dur := time.Since(start)
					cancelItem()
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
					if err := ctx.Err(); err != nil {
						return err
					}
					if err := outbox.Send(ctx, val); err != nil {
						return err
					}
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

// mapSerialFastPath is the drain-protocol + micro-batching fast path for serial Map.
// Conditions: Concurrency(1), DefaultHandler, OverflowBlock, no timeout, no cache, NoopHook.
// Skips outbox, per-item select, time.Now, ProcessItem, and all hook calls.
func mapSerialFastPath[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), name string, drainFn func(), drainCh <-chan struct{}) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		cooperativeDrain := false
		defer func() {
			if !cooperativeDrain {
				go internal.DrainChan(inCh)
			}
		}()
		defer drainFn()

		var buf [internal.ReceiveBatchSize]I
		for {
			// One blocking receive to avoid spinning.
			var item I
			var ok bool
			select {
			case item, ok = <-inCh:
				if !ok {
					return nil
				}
			case <-drainCh:
				cooperativeDrain = true
				return nil
			}
			buf[0] = item
			n := 1
			closed := false
		fillMap:
			for n < internal.ReceiveBatchSize {
				select {
				case v, ok2 := <-inCh:
					if !ok2 {
						closed = true
						break fillMap
					}
					buf[n] = v
					n++
				default:
					break fillMap
				}
			}
			for i := range n {
				it := buf[i]
				var zero I
				buf[i] = zero // release reference for GC
				result, err := fn(internal.ItemCtx(ctx, it), it)
				if err != nil {
					return internal.WrapStageErr(name, err, 0)
				}
				select {
				case outCh <- result:
				case <-drainCh:
					cooperativeDrain = true
					return nil
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

func mapConcurrent[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), cfg stageConfig, hook internal.Hook, drainFn func(), drainCh <-chan struct{}) stageFunc {
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
			// sustained backpressure. Each spawned goroutine captures its own
			// shard at spawn time; the dispatcher is the sole writer of
			// workerIdx so no locking is required here.
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
						// Use innerCtx.Done() only — reading errCh here would drain it (BUG: removed for test)
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
							start := time.Now()
							val, err, attempt := internal.ProcessItem(itemCtx, fn, it, cfg.errorHandler, ctxMapper)
							dur := time.Since(start)
							cancelItem()
							if err == internal.ErrSkipped {
								errCount.Add(1)
								hook.OnItem(ctx, cfg.name, dur, err)
								return
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

func mapOrdered[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), cfg stageConfig, hook internal.Hook, drainFn func(), drainCh <-chan struct{}) stageFunc {
	var ctxMapper func(I) context.Context
	if raw := cfg.contextMapperFn; raw != nil {
		ctxMapper = raw.(func(I) context.Context)
	}
	type result struct {
		val O
		dur time.Duration
		err error
		att int
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
			// slots channels depth = concurrency*2 to let the dispatcher stay ahead
			slots := make(chan chan result, cfg.concurrency*2)
			drainErrs := make(chan error, 1)

			// Drainer: reads slots in order, emits results in sequence.
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
						// Drain remaining slots so workers aren't stuck.
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

			// Dispatcher: reads input, launches per-item goroutines in order.
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
						start := time.Now()
						val, err, att := internal.ProcessItem(itemCtx, fn, it, cfg.errorHandler, ctxMapper)
						dur := time.Since(start)
						cancelItem()
						sc <- result{val: val, dur: dur, err: err, att: att}
					}(item, slotCh)
				}
			}()

			// Wait for all in-flight workers.
			for i := 0; i < cfg.concurrency; i++ {
				sem <- struct{}{}
			}
			close(slots)
			return <-drainErrs
		}

		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}
