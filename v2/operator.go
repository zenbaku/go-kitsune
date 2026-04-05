package kitsune

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zenbaku/go-kitsune/v2/internal"
)

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// orDefault returns s if non-empty, otherwise def.
func orDefault(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

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
	meta := stageMeta{
		id:          id,
		kind:        "map",
		name:        orDefault(cfg.name, "map"),
		concurrency: cfg.concurrency,
		buffer:      cfg.buffer,
		overflow:    cfg.overflow,
		inputs:      []int{p.id},
	}
	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}
		inCh := p.build(rc)
		ch := make(chan O, cfg.buffer)
		m := meta
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

		var stage stageFunc
		switch {
		case cfg.concurrency > 1 && cfg.ordered:
			stage = mapOrdered(inCh, ch, actualFn, cfg, hook)
		case cfg.concurrency > 1:
			stage = mapConcurrent(inCh, ch, actualFn, cfg, hook)
		case isFastPathEligible(cfg, hook) && cfg.cacheConfig == nil:
			stage = mapSerialFastPath(inCh, ch, actualFn, cfg.name)
		default:
			stage = mapSerial(inCh, ch, actualFn, cfg, hook)
		}
		rc.add(stage, m)
		return ch
	}
	result := newPipeline(id, meta, build)
	// Set fusionEntry when cfg conditions hold (hook check deferred to run time).
	if isFastPathEligibleCfg(cfg) && cfg.cacheConfig == nil {
		name0 := orDefault(cfg.name, "map")
		fn0 := fn
		p0 := p
		result.fusionEntry = func(rc *runCtx, sink func(context.Context, O) error) stageFunc {
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
			return func(ctx context.Context) error {
				defer func() { go internal.DrainChan(inCh) }()
				var buf [internal.ReceiveBatchSize]I
				for {
					item, ok := <-inCh
					if !ok {
						return nil
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
				}
			}
		}
	}
	return result
}

func mapSerial[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), cfg stageConfig, hook internal.Hook) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var processed, errs int64
		defer func() { hook.OnStageDone(ctx, cfg.name, processed, errs) }()

		outbox := internal.NewOutbox(outCh, cfg.overflow, nil, cfg.name)

		for {
			select {
			case item, ok := <-inCh:
				if !ok {
					return nil
				}
				itemCtx, cancelItem := itemContext(ctx, cfg)
				start := time.Now()
				val, err, attempt := internal.ProcessItem(itemCtx, fn, item, cfg.errorHandler)
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
				if err := outbox.Send(ctx, val); err != nil {
					return err
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// mapSerialFastPath is the drain-protocol + micro-batching fast path for serial Map.
// Conditions: Concurrency(1), DefaultHandler, OverflowBlock, no timeout, no cache, NoopHook.
// Skips outbox, per-item select, time.Now, ProcessItem, and all hook calls.
func mapSerialFastPath[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), name string) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		var buf [internal.ReceiveBatchSize]I
		for {
			// One blocking receive to avoid spinning.
			item, ok := <-inCh
			if !ok {
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
				result, err := fn(ctx, it)
				if err != nil {
					return internal.WrapStageErr(name, err, 0)
				}
				outCh <- result // plain send — drain goroutine unblocks on exit
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

func mapConcurrent[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), cfg stageConfig, hook internal.Hook) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		outbox := internal.NewOutbox(outCh, cfg.overflow, nil, cfg.name)
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
					select {
					case sem <- struct{}{}:
					case <-ctx.Done():
						return
					case err := <-errCh:
						_ = err
						return
					}
					wg.Add(1)
					go func(it I) {
						defer wg.Done()
						defer func() { <-sem }()

						itemCtx, cancelItem := itemContext(ctx, cfg)
						start := time.Now()
						val, err, attempt := internal.ProcessItem(itemCtx, fn, it, cfg.errorHandler)
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
						if sendErr := outbox.Send(ctx, val); sendErr != nil {
							reportErr(errCh, sendErr)
							cancel()
						}
					}(item)
				case <-ctx.Done():
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
}

func mapOrdered[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), cfg stageConfig, hook internal.Hook) stageFunc {
	type result struct {
		val O
		dur time.Duration
		err error
		att int
	}
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		outbox := internal.NewOutbox(outCh, cfg.overflow, nil, cfg.name)
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
				if err := outbox.Send(ctx, r.val); err != nil {
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
				case <-ctx.Done():
					return
				}

				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}

				slotCh := make(chan result, 1)
				select {
				case slots <- slotCh:
				case <-ctx.Done():
					<-sem
					return
				}

				go func(it I, sc chan result) {
					defer func() { <-sem }()
					itemCtx, cancelItem := itemContext(ctx, cfg)
					start := time.Now()
					val, err, att := internal.ProcessItem(itemCtx, fn, it, cfg.errorHandler)
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
}

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
		inputs:      []int{p.id},
	}
	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}
		inCh := p.build(rc)
		ch := make(chan O, cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		fmHook := rc.hook
		if fmHook == nil {
			fmHook = internal.NoopHook{}
		}

		var stage stageFunc
		switch {
		case cfg.concurrency > 1 && cfg.ordered:
			stage = flatMapOrdered(inCh, ch, fn, cfg, fmHook)
		case cfg.concurrency > 1:
			stage = flatMapConcurrent(inCh, ch, fn, cfg, fmHook)
		case isFastPathEligible(cfg, fmHook):
			stage = flatMapSerialFastPath(inCh, ch, fn, cfg.name)
		default:
			stage = flatMapSerial(inCh, ch, fn, cfg, fmHook)
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}

// flatMapSerialFastPath is the drain-protocol + micro-batching fast path for serial FlatMap.
// The yield closure is allocated once outside the loop — zero allocs per item.
func flatMapSerialFastPath[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I, func(O) error) error, name string) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		// yield is allocated once; outCh <- v is a plain send (drain protocol).
		yield := func(v O) error {
			outCh <- v
			return nil
		}

		var buf [internal.ReceiveBatchSize]I
		for {
			item, ok := <-inCh
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
				if err := fn(ctx, it, yield); err != nil {
					return internal.WrapStageErr(name, err, 0)
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

func flatMapSerial[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I, func(O) error) error, cfg stageConfig, hook internal.Hook) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var processed, errs int64
		defer func() { hook.OnStageDone(ctx, cfg.name, processed, errs) }()

		outbox := internal.NewOutbox(outCh, cfg.overflow, nil, cfg.name)
		send := func(v O) error { return outbox.Send(ctx, v) }

		for {
			select {
			case item, ok := <-inCh:
				if !ok {
					return nil
				}
				itemCtx, cancelItem := itemContext(ctx, cfg)
				start := time.Now()
				err, attempt := internal.ProcessFlatMapItem(itemCtx, fn, item, cfg.errorHandler, send)
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
			}
		}
	}
}

func flatMapConcurrent[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I, func(O) error) error, cfg stageConfig, hook internal.Hook) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		outbox := internal.NewOutbox(outCh, cfg.overflow, nil, cfg.name)
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
					select {
					case sem <- struct{}{}:
					case <-ctx.Done():
						return
					case err := <-errCh:
						_ = err
						return
					}
					wg.Add(1)
					go func(it I) {
						defer wg.Done()
						defer func() { <-sem }()

						itemCtx, cancelItem := itemContext(ctx, cfg)
						send := func(v O) error { return outbox.Send(ctx, v) }
						start := time.Now()
						err, attempt := internal.ProcessFlatMapItem(itemCtx, fn, it, cfg.errorHandler, send)
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
					}(item)
				case <-ctx.Done():
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
}

func flatMapOrdered[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I, func(O) error) error, cfg stageConfig, hook internal.Hook) stageFunc {
	type result struct {
		items []O
		dur   time.Duration
		err   error
		att   int
	}
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		outbox := internal.NewOutbox(outCh, cfg.overflow, nil, cfg.name)
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
					if err := outbox.Send(ctx, v); err != nil {
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
				case <-ctx.Done():
					return
				}

				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}

				slotCh := make(chan result, 1)
				select {
				case slots <- slotCh:
				case <-ctx.Done():
					<-sem
					return
				}

				go func(it I, sc chan result) {
					defer func() { <-sem }()
					itemCtx, cancelItem := itemContext(ctx, cfg)
					var buf []O
					collect := func(v O) error {
						buf = append(buf, v)
						return nil
					}
					start := time.Now()
					err, att := internal.ProcessFlatMapItem(itemCtx, fn, it, cfg.errorHandler, collect)
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
}

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

				outbox := internal.NewOutbox(ch, cfg.overflow, nil, cfg.name)

				for {
					select {
					case item, ok := <-inCh:
						if !ok {
							return nil
						}
						keep, err := pred(ctx, item)
						if err != nil {
							return internal.WrapStageErr(cfg.name, err, 0)
						}
						if !keep {
							continue
						}
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
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewOutbox(ch, cfg.overflow, nil, cfg.name)

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if err := fn(ctx, item); err != nil {
						return internal.WrapStageErr(cfg.name, err, 0)
					}
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

// ---------------------------------------------------------------------------
// Take / Skip
// ---------------------------------------------------------------------------

// Take emits the first n items and then closes the pipeline.
func Take[T any](p *Pipeline[T], n int) *Pipeline[T] {
	track(p)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "take",
		name:   "take",
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

			count := 0
			for {
				if count >= n {
					return nil
				}
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					select {
					case ch <- item:
						count++
					case <-ctx.Done():
						return ctx.Err()
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

// Drop discards the first n items and emits the rest.
// This is the count-based complement of [Take].
// Not to be confused with [Skip] (the [ErrorHandler] for skipping failed items).
func Drop[T any](p *Pipeline[T], n int) *Pipeline[T] {
	track(p)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "drop",
		name:   "drop",
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

			count := 0
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if count < n {
						count++
						continue
					}
					select {
					case ch <- item:
					case <-ctx.Done():
						return ctx.Err()
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
// TakeWhile / DropWhile
// ---------------------------------------------------------------------------

// TakeWhile emits items as long as pred returns true, then stops.
func TakeWhile[T any](p *Pipeline[T], pred func(T) bool) *Pipeline[T] {
	track(p)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "take_while",
		name:   "take_while",
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

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if !pred(item) {
						return nil
					}
					select {
					case ch <- item:
					case <-ctx.Done():
						return ctx.Err()
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

// DropWhile discards items as long as pred returns true, then emits the rest.
func DropWhile[T any](p *Pipeline[T], pred func(T) bool) *Pipeline[T] {
	track(p)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "drop_while",
		name:   "drop_while",
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

			dropping := true
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if dropping && pred(item) {
						continue
					}
					dropping = false
					select {
					case ch <- item:
					case <-ctx.Done():
						return ctx.Err()
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
						if err := outbox.Send(ctx, Pair[T, T]{Left: prev, Right: item}); err != nil {
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

// ---------------------------------------------------------------------------
// Private helpers
// ---------------------------------------------------------------------------

// itemContext returns a context for a single item, applying per-item timeout
// if configured. The returned cancel must always be called.
func itemContext(ctx context.Context, cfg stageConfig) (context.Context, context.CancelFunc) {
	if cfg.timeout > 0 {
		return context.WithTimeout(ctx, cfg.timeout)
	}
	return ctx, func() {}
}

// reportErr delivers err to errCh without blocking (first error wins).
func reportErr(errCh chan error, err error) {
	select {
	case errCh <- err:
	default:
	}
}
