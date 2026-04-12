package kitsune

import (
	"context"

	"github.com/zenbaku/go-kitsune/internal"
)

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
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inCh := p.build(rc)
		buf := rc.defaultBufSize()
		ch := make(chan T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		signalDone := rc.signalDone
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()
			defer signalDone() // stop infinite sources when we exit early

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
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inCh := p.build(rc)
		buf := rc.defaultBufSize()
		ch := make(chan T, buf)
		m := meta
		m.buffer = buf
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
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inCh := p.build(rc)
		buf := rc.defaultBufSize()
		ch := make(chan T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		signalDone := rc.signalDone
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()
			defer signalDone() // stop infinite sources when predicate stops

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
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inCh := p.build(rc)
		buf := rc.defaultBufSize()
		ch := make(chan T, buf)
		m := meta
		m.buffer = buf
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
// SkipLast
// ---------------------------------------------------------------------------

// SkipLast omits the last n items of p, emitting all earlier items. If n ≤ 0
// all items are forwarded unchanged. If n ≥ the total number of items the
// output is empty.
//
// It maintains an internal ring buffer of size n: each arriving item displaces
// the oldest buffered item, which is then emitted. The n items still in the
// buffer when the source closes are discarded.
//
//	kitsune.SkipLast(kitsune.FromSlice([]int{1,2,3,4,5}), 2)
//	// emits 1, 2, 3
func SkipLast[T any](p *Pipeline[T], n int) *Pipeline[T] {
	if n <= 0 {
		return p
	}
	track(p)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "skip_last",
		name:   "skip_last",
		buffer: internal.DefaultBuffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inCh := p.build(rc)
		bufSize := rc.defaultBufSize()
		ch := make(chan T, bufSize)
		m := meta
		m.buffer = bufSize
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			buf := make([]T, n)
			head := 0 // index of oldest item
			count := 0
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if count < n {
						buf[count] = item
						count++
					} else {
						// Emit oldest, insert new item at its slot.
						oldest := buf[head]
						buf[head] = item
						head = (head + 1) % n
						select {
						case ch <- oldest:
						case <-ctx.Done():
							return ctx.Err()
						}
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
// TakeUntil / SkipUntil
// ---------------------------------------------------------------------------

// TakeUntil passes items from p through until the boundary pipeline emits its
// first item (or closes), then stops. The boundary item type U is ignored;
// only the signal matters. This is distinct from [TakeWhile], which evaluates
// a predicate on each item from p itself.
//
// Calls [signalDone] on exit so that infinite sources upstream of p are
// cancelled cleanly.
//
//	stop := kitsune.Timer(5*time.Second, func(_ context.Context) (struct{}, error) { return struct{}{}, nil })
//	kitsune.TakeUntil(stream, stop)
//	// forwards items from stream for 5 seconds then stops
func TakeUntil[T, U any](p *Pipeline[T], boundary *Pipeline[U], opts ...StageOption) *Pipeline[T] {
	track(p)
	track(boundary)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "take_until",
		name:   orDefault(cfg.name, "take_until"),
		buffer: cfg.buffer,
		inputs: []int64{p.id, boundary.id},
	}
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inCh := p.build(rc)
		boundaryCh := boundary.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		signalDone := rc.signalDone
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()
			defer func() { go internal.DrainChan(boundaryCh) }()
			defer signalDone()

			outbox := internal.NewBlockingOutbox(ch)

			// Close stopCh when the boundary emits its first item or closes.
			stopCh := make(chan struct{})
			go func() {
				select {
				case <-boundaryCh:
					close(stopCh)
				case <-ctx.Done():
				}
			}()

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if err := outbox.Send(ctx, item); err != nil {
						return err
					}
				case <-stopCh:
					return nil
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

// SkipUntil suppresses all items from p until the boundary pipeline emits its
// first item (or closes), then passes everything through. The boundary item
// type U is ignored; only the signal matters.
//
//	kitsune.SkipUntil(stream, readySignal)
//	// discards items until readySignal fires, then forwards all subsequent items
func SkipUntil[T, U any](p *Pipeline[T], boundary *Pipeline[U], opts ...StageOption) *Pipeline[T] {
	track(p)
	track(boundary)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "skip_until",
		name:   orDefault(cfg.name, "skip_until"),
		buffer: cfg.buffer,
		inputs: []int64{p.id, boundary.id},
	}
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inCh := p.build(rc)
		boundaryCh := boundary.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		signalDone := rc.signalDone
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()
			defer func() { go internal.DrainChan(boundaryCh) }()
			defer signalDone() // stop boundary and any infinite upstream sources when p closes

			outbox := internal.NewBlockingOutbox(ch)

			// Close gateCh when the boundary emits its first item or closes.
			gateCh := make(chan struct{})
			go func() {
				select {
				case <-boundaryCh:
					close(gateCh)
				case <-ctx.Done():
				}
			}()

			// gateChRef becomes nil once the gate is open to prevent a
			// perpetually-closed channel from dominating the select.
			var gateChRef <-chan struct{} = gateCh
			gateOpen := false
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if !gateOpen {
						// Non-blocking check: did the gate open while we were
						// waiting for this item?
						select {
						case <-gateCh:
							gateOpen = true
							gateChRef = nil
						default:
							continue // gate still closed — skip item
						}
					}
					if err := outbox.Send(ctx, item); err != nil {
						return err
					}
				case <-gateChRef:
					gateOpen = true
					gateChRef = nil
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
