package kitsune

import (
	"context"
	"sync"

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
	cfg := buildStageConfig(opts)
	ch := make(chan O, cfg.buffer)
	meta := stageMeta{
		kind:        "map",
		name:        orDefault(cfg.name, "map"),
		concurrency: cfg.concurrency,
		buffer:      cfg.buffer,
		overflow:    cfg.overflow,
		inputs:      []int{p.id},
		getChanLen:  func() int { return len(ch) },
		getChanCap:  func() int { return cap(ch) },
	}

	var stage stageFunc
	switch {
	case cfg.concurrency > 1 && cfg.ordered:
		stage = mapOrdered(p.ch, ch, fn, cfg)
	case cfg.concurrency > 1:
		stage = mapConcurrent(p.ch, ch, fn, cfg)
	default:
		stage = mapSerial(p.ch, ch, fn, cfg)
	}

	id := p.sl.add(stage, meta)
	return newPipeline(ch, p.sl, id)
}

func mapSerial[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), cfg stageConfig) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		outbox := internal.NewOutbox(outCh, cfg.overflow, nil, cfg.name)

		for {
			select {
			case item, ok := <-inCh:
				if !ok {
					return nil
				}
				itemCtx, cancelItem := itemContext(ctx, cfg)
				val, err, attempt := internal.ProcessItem(itemCtx, fn, item, cfg.errorHandler)
				cancelItem()
				if err == internal.ErrSkipped {
					continue
				}
				if err != nil {
					return internal.WrapStageErr(cfg.name, err, attempt)
				}
				if err := outbox.Send(ctx, val); err != nil {
					return err
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func mapConcurrent[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), cfg stageConfig) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

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
						val, err, attempt := internal.ProcessItem(itemCtx, fn, it, cfg.errorHandler)
						cancelItem()
						if err == internal.ErrSkipped {
							return
						}
						if err != nil {
							reportErr(errCh, internal.WrapStageErr(cfg.name, err, attempt))
							cancel()
							return
						}
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

func mapOrdered[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), cfg stageConfig) stageFunc {
	type result struct {
		val O
		err error
		att int
	}
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

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
					cancel()
					// Drain remaining slots so workers aren't stuck.
					go func() { for s := range slots { <-s } }()
					drainErrs <- internal.WrapStageErr(cfg.name, r.err, r.att)
					return
				}
				if err := outbox.Send(ctx, r.val); err != nil {
					cancel()
					go func() { for s := range slots { <-s } }()
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
					val, err, att := internal.ProcessItem(itemCtx, fn, it, cfg.errorHandler)
					cancelItem()
					sc <- result{val: val, err: err, att: att}
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
	cfg := buildStageConfig(opts)
	ch := make(chan O, cfg.buffer)
	meta := stageMeta{
		kind:        "flat_map",
		name:        orDefault(cfg.name, "flat_map"),
		concurrency: cfg.concurrency,
		buffer:      cfg.buffer,
		overflow:    cfg.overflow,
		inputs:      []int{p.id},
		getChanLen:  func() int { return len(ch) },
		getChanCap:  func() int { return cap(ch) },
	}

	var stage stageFunc
	switch {
	case cfg.concurrency > 1 && cfg.ordered:
		stage = flatMapOrdered(p.ch, ch, fn, cfg)
	case cfg.concurrency > 1:
		stage = flatMapConcurrent(p.ch, ch, fn, cfg)
	default:
		stage = flatMapSerial(p.ch, ch, fn, cfg)
	}

	id := p.sl.add(stage, meta)
	return newPipeline(ch, p.sl, id)
}

func flatMapSerial[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I, func(O) error) error, cfg stageConfig) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		outbox := internal.NewOutbox(outCh, cfg.overflow, nil, cfg.name)
		send := func(v O) error { return outbox.Send(ctx, v) }

		for {
			select {
			case item, ok := <-inCh:
				if !ok {
					return nil
				}
				itemCtx, cancelItem := itemContext(ctx, cfg)
				err, attempt := internal.ProcessFlatMapItem(itemCtx, fn, item, cfg.errorHandler, send)
				cancelItem()
				if err == internal.ErrSkipped {
					continue
				}
				if err != nil {
					return internal.WrapStageErr(cfg.name, err, attempt)
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func flatMapConcurrent[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I, func(O) error) error, cfg stageConfig) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

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
						err, attempt := internal.ProcessFlatMapItem(itemCtx, fn, it, cfg.errorHandler, send)
						cancelItem()
						if err != nil && err != internal.ErrSkipped {
							reportErr(errCh, internal.WrapStageErr(cfg.name, err, attempt))
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

func flatMapOrdered[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I, func(O) error) error, cfg stageConfig) stageFunc {
	type result struct {
		items []O
		err   error
		att   int
	}
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

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
					cancel()
					go func() { for s := range slots { <-s } }()
					drainErrs <- internal.WrapStageErr(cfg.name, r.err, r.att)
					return
				}
				for _, v := range r.items {
					if err := outbox.Send(ctx, v); err != nil {
						cancel()
						go func() { for s := range slots { <-s } }()
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
					err, att := internal.ProcessFlatMapItem(itemCtx, fn, it, cfg.errorHandler, collect)
					cancelItem()
					sc <- result{items: buf, err: err, att: att}
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
	cfg := buildStageConfig(opts)
	ch := make(chan T, cfg.buffer)
	meta := stageMeta{
		kind:       "filter",
		name:       orDefault(cfg.name, "filter"),
		buffer:     cfg.buffer,
		overflow:   cfg.overflow,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	stage := func(ctx context.Context) error {
		defer close(ch)
		defer func() { go internal.DrainChan(p.ch) }()

		outbox := internal.NewOutbox(ch, cfg.overflow, nil, cfg.name)

		for {
			select {
			case item, ok := <-p.ch:
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

	id := p.sl.add(stage, meta)
	return newPipeline(ch, p.sl, id)
}

// ---------------------------------------------------------------------------
// Tap
// ---------------------------------------------------------------------------

// Tap calls fn for each item as a side effect, then passes the item downstream
// unchanged. Errors from fn halt the pipeline (use OnError to change this).
func Tap[T any](p *Pipeline[T], fn func(context.Context, T) error, opts ...StageOption) *Pipeline[T] {
	cfg := buildStageConfig(opts)
	ch := make(chan T, cfg.buffer)
	meta := stageMeta{
		kind:       "tap",
		name:       orDefault(cfg.name, "tap"),
		buffer:     cfg.buffer,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	stage := func(ctx context.Context) error {
		defer close(ch)
		defer func() { go internal.DrainChan(p.ch) }()

		outbox := internal.NewOutbox(ch, cfg.overflow, nil, cfg.name)

		for {
			select {
			case item, ok := <-p.ch:
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

	id := p.sl.add(stage, meta)
	return newPipeline(ch, p.sl, id)
}

// ---------------------------------------------------------------------------
// Take / Skip
// ---------------------------------------------------------------------------

// Take emits the first n items and then closes the pipeline.
func Take[T any](p *Pipeline[T], n int) *Pipeline[T] {
	ch := make(chan T, internal.DefaultBuffer)
	meta := stageMeta{
		kind:       "take",
		name:       "take",
		buffer:     internal.DefaultBuffer,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	stage := func(ctx context.Context) error {
		defer close(ch)
		defer func() { go internal.DrainChan(p.ch) }()

		count := 0
		for {
			if count >= n {
				return nil
			}
			select {
			case item, ok := <-p.ch:
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

	id := p.sl.add(stage, meta)
	return newPipeline(ch, p.sl, id)
}

// Drop discards the first n items and emits the rest.
// This is the count-based complement of [Take].
// Not to be confused with [Skip] (the [ErrorHandler] for skipping failed items).
func Drop[T any](p *Pipeline[T], n int) *Pipeline[T] {
	ch := make(chan T, internal.DefaultBuffer)
	meta := stageMeta{
		kind:       "drop",
		name:       "drop",
		buffer:     internal.DefaultBuffer,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	stage := func(ctx context.Context) error {
		defer close(ch)
		defer func() { go internal.DrainChan(p.ch) }()

		count := 0
		for {
			select {
			case item, ok := <-p.ch:
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

	id := p.sl.add(stage, meta)
	return newPipeline(ch, p.sl, id)
}

// ---------------------------------------------------------------------------
// TakeWhile / DropWhile
// ---------------------------------------------------------------------------

// TakeWhile emits items as long as pred returns true, then stops.
func TakeWhile[T any](p *Pipeline[T], pred func(T) bool) *Pipeline[T] {
	ch := make(chan T, internal.DefaultBuffer)
	meta := stageMeta{
		kind:       "take_while",
		name:       "take_while",
		buffer:     internal.DefaultBuffer,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	stage := func(ctx context.Context) error {
		defer close(ch)
		defer func() { go internal.DrainChan(p.ch) }()

		for {
			select {
			case item, ok := <-p.ch:
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

	id := p.sl.add(stage, meta)
	return newPipeline(ch, p.sl, id)
}

// DropWhile discards items as long as pred returns true, then emits the rest.
func DropWhile[T any](p *Pipeline[T], pred func(T) bool) *Pipeline[T] {
	ch := make(chan T, internal.DefaultBuffer)
	meta := stageMeta{
		kind:       "drop_while",
		name:       "drop_while",
		buffer:     internal.DefaultBuffer,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	stage := func(ctx context.Context) error {
		defer close(ch)
		defer func() { go internal.DrainChan(p.ch) }()

		dropping := true
		for {
			select {
			case item, ok := <-p.ch:
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

	id := p.sl.add(stage, meta)
	return newPipeline(ch, p.sl, id)
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
