package kitsune

import (
	"context"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// Batch
// ---------------------------------------------------------------------------

// Batch collects items into slices of up to size items. If BatchTimeout is set,
// a partial batch is flushed when the timeout elapses even if size is not reached.
// An empty batch is never emitted.
func Batch[T any](p *Pipeline[T], size int, opts ...StageOption) *Pipeline[[]T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:        id,
		kind:      "batch",
		name:      orDefault(cfg.name, "batch"),
		buffer:    cfg.buffer,
		batchSize: size,
		inputs:    []int64{p.id},
	}
	build := func(rc *runCtx) chan []T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan []T)
		}
		inCh := p.build(rc)
		bufSize := rc.effectiveBufSize(cfg)
		ch := make(chan []T, bufSize)
		m := meta
		m.buffer = bufSize
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			var buf []T

			flush := func() error {
				if len(buf) == 0 {
					return nil
				}
				batch := make([]T, len(buf))
				copy(batch, buf)
				buf = buf[:0]
				return outbox.Send(ctx, batch)
			}

			flushOnClose := func() error {
				if cfg.dropPartial {
					return nil
				}
				return flush()
			}

			if cfg.batchTimeout == 0 {
				// No timeout: collect exactly size items per batch.
				for {
					select {
					case item, ok := <-inCh:
						if !ok {
							return flushOnClose()
						}
						buf = append(buf, item)
						if len(buf) >= size {
							if err := flush(); err != nil {
								return err
							}
						}
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}

			// With timeout: flush when size reached OR timer fires.
			clk := cfg.clock
			if clk == nil {
				clk = internal.RealClock{}
			}

			ticker := clk.NewTicker(cfg.batchTimeout)
			defer ticker.Stop()

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return flushOnClose()
					}
					buf = append(buf, item)
					if len(buf) >= size {
						if err := flush(); err != nil {
							return err
						}
					}
				case <-ticker.C():
					if err := flush(); err != nil {
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
// BufferWith
// ---------------------------------------------------------------------------

// BufferWith collects items from p into a slice, emitting the accumulated
// buffer each time closingSelector fires. An empty buffer is never emitted.
// When the source closes, any remaining buffered items are flushed before the
// output closes. When closingSelector closes, any remaining buffered items are
// flushed and the output closes.
//
// BufferWith generalizes Batch (fixed-size boundary) and BatchTimeout
// (periodic boundary) to arbitrary external boundary signals:
//
//	// Flush whenever a heartbeat pipeline fires.
//	heartbeat := kitsune.Ticker(5 * time.Second)
//	batches := kitsune.BufferWith(events, heartbeat)
func BufferWith[T, S any](p *Pipeline[T], closingSelector *Pipeline[S], opts ...StageOption) *Pipeline[[]T] {
	if closingSelector == nil {
		panic("kitsune: BufferWith closingSelector must not be nil")
	}
	track(p)
	track(closingSelector)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "buffer_with",
		name:   orDefault(cfg.name, "buffer_with"),
		buffer: cfg.buffer,
		inputs: []int64{p.id, closingSelector.id},
	}
	build := func(rc *runCtx) chan []T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan []T)
		}
		srcCh := p.build(rc)
		selCh := closingSelector.build(rc)
		bufSize := rc.effectiveBufSize(cfg)
		ch := make(chan []T, bufSize)
		m := meta
		m.buffer = bufSize
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() {
				go internal.DrainChan(srcCh)
				go internal.DrainChan(selCh)
			}()

			outbox := internal.NewBlockingOutbox(ch)
			var buf []T

			flush := func() error {
				if len(buf) == 0 {
					return nil
				}
				batch := make([]T, len(buf))
				copy(batch, buf)
				buf = buf[:0]
				return outbox.Send(ctx, batch)
			}

			for {
				select {
				case item, ok := <-srcCh:
					if !ok {
						return flush()
					}
					buf = append(buf, item)
				case _, ok := <-selCh:
					if !ok {
						return flush()
					}
					if err := flush(); err != nil {
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
// Unbatch
// ---------------------------------------------------------------------------

// Unbatch flattens a pipeline of slices into a pipeline of individual items.
func Unbatch[T any](p *Pipeline[[]T], opts ...StageOption) *Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "unbatch",
		name:   orDefault(cfg.name, "unbatch"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)

			for {
				select {
				case batch, ok := <-inCh:
					if !ok {
						return nil
					}
					for _, item := range batch {
						if err := outbox.Send(ctx, item); err != nil {
							return err
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
// SlidingWindow (count-based)
// ---------------------------------------------------------------------------

// SlidingWindow emits overlapping slices of exactly size items, advancing by
// step items each time. step must be > 0 and ≤ size. Items are only emitted
// once a full window of size items is available; partial windows at the end
// of the stream are dropped.
func SlidingWindow[T any](p *Pipeline[T], size, step int, opts ...StageOption) *Pipeline[[]T] {
	track(p)
	if step <= 0 || step > size {
		panic("kitsune: SlidingWindow step must be > 0 and <= size")
	}
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:        id,
		kind:      "sliding_window",
		name:      orDefault(cfg.name, "sliding_window"),
		buffer:    cfg.buffer,
		batchSize: size,
		inputs:    []int64{p.id},
	}
	build := func(rc *runCtx) chan []T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan []T)
		}
		inCh := p.build(rc)
		bufSize := rc.effectiveBufSize(cfg)
		ch := make(chan []T, bufSize)
		m := meta
		m.buffer = bufSize
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			buf := make([]T, 0, size)

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					buf = append(buf, item)
					if len(buf) == size {
						win := make([]T, size)
						copy(win, buf)
						if err := outbox.Send(ctx, win); err != nil {
							return err
						}
						// Advance by step: drop the first step elements.
						buf = append(buf[:0], buf[step:]...)
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
// SessionWindow (gap-based)
// ---------------------------------------------------------------------------

// SessionWindow groups items into sessions separated by periods of inactivity.
// A new session starts whenever no item arrives within gap. The accumulated
// session buffer is emitted when the gap timer fires.
// An empty session is never emitted.
func SessionWindow[T any](p *Pipeline[T], gap time.Duration, opts ...StageOption) *Pipeline[[]T] {
	if gap <= 0 {
		panic("kitsune: SessionWindow gap must be > 0")
	}
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "session_window",
		name:   orDefault(cfg.name, "session_window"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan []T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan []T)
		}
		inCh := p.build(rc)
		bufSize := rc.effectiveBufSize(cfg)
		ch := make(chan []T, bufSize)
		m := meta
		m.buffer = bufSize
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			clk := cfg.clock
			if clk == nil {
				clk = internal.RealClock{}
			}

			outbox := internal.NewBlockingOutbox(ch)
			var buf []T

			// Start with a stopped timer; reset it on each item.
			timer := clk.NewTimer(gap)
			// Immediately drain the initial tick since we haven't seen any items yet.
			select {
			case <-timer.C():
			default:
			}
			timer.Stop()

			timerActive := false

			flush := func() error {
				if len(buf) == 0 {
					return nil
				}
				session := make([]T, len(buf))
				copy(session, buf)
				buf = buf[:0]
				return outbox.Send(ctx, session)
			}

			for {
				var timerCh <-chan time.Time
				if timerActive {
					timerCh = timer.C()
				}

				select {
				case item, ok := <-inCh:
					if !ok {
						return flush()
					}
					buf = append(buf, item)
					// Reset the gap timer on each item.
					if timerActive {
						timer.Stop()
						// Drain any pending tick from the old timer.
						select {
						case <-timer.C():
						default:
						}
					}
					timer.Reset(gap)
					timerActive = true

				case <-timerCh:
					timerActive = false
					if err := flush(); err != nil {
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
// ChunkBy
// ---------------------------------------------------------------------------

// ChunkBy groups consecutive items that share the same key into slices.
// A new chunk begins whenever keyFn returns a different key than the previous item.
// The last chunk is emitted when the source completes.
//
//	// [1,1,2,2,1] → [[1,1],[2,2],[1]]
//	kitsune.ChunkBy(p, func(v int) int { return v })
func ChunkBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[[]T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "chunk_by",
		name:   orDefault(cfg.name, "chunk_by"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan []T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan []T)
		}
		inCh := p.build(rc)
		bufSize := rc.effectiveBufSize(cfg)
		ch := make(chan []T, bufSize)
		m := meta
		m.buffer = bufSize
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			var buf []T
			var curKey K
			first := true

			flush := func() error {
				if len(buf) == 0 {
					return nil
				}
				chunk := make([]T, len(buf))
				copy(chunk, buf)
				buf = buf[:0]
				return outbox.Send(ctx, chunk)
			}

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return flush()
					}
					k := keyFn(item)
					if !first && k != curKey {
						if err := flush(); err != nil {
							return err
						}
					}
					first = false
					curKey = k
					buf = append(buf, item)
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
// ChunkWhile
// ---------------------------------------------------------------------------

// ChunkWhile groups consecutive items into chunks while pred(prev, current) returns true.
// A new chunk begins when pred returns false. The last chunk is emitted when the source completes.
//
//	// group ascending runs: [1,2,3,1,2] → [[1,2,3],[1,2]]
//	kitsune.ChunkWhile(p, func(prev, curr int) bool { return curr > prev })
func ChunkWhile[T any](p *Pipeline[T], pred func(prev, curr T) bool, opts ...StageOption) *Pipeline[[]T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "chunk_while",
		name:   orDefault(cfg.name, "chunk_while"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan []T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan []T)
		}
		inCh := p.build(rc)
		bufSize := rc.effectiveBufSize(cfg)
		ch := make(chan []T, bufSize)
		m := meta
		m.buffer = bufSize
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			var buf []T

			flush := func() error {
				if len(buf) == 0 {
					return nil
				}
				chunk := make([]T, len(buf))
				copy(chunk, buf)
				buf = buf[:0]
				return outbox.Send(ctx, chunk)
			}

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return flush()
					}
					if len(buf) > 0 && !pred(buf[len(buf)-1], item) {
						if err := flush(); err != nil {
							return err
						}
					}
					buf = append(buf, item)
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
// MapBatch
// ---------------------------------------------------------------------------

// MapBatch collects up to size items, passes the slice to fn, and flattens
// the returned slice back into individual items. Useful for bulk DB or API
// calls where batching reduces round-trips.
//
// Use [BatchTimeout] in opts to flush partial batches after a duration.
// Use [Concurrency] to process multiple batches in parallel.
//
//	kitsune.MapBatch(terms, 200, func(ctx context.Context, batch []Term) ([]Enriched, error) {
//	    return db.BulkLookup(ctx, batch)
//	})
func MapBatch[I, O any](p *Pipeline[I], size int, fn func(context.Context, []I) ([]O, error), opts ...StageOption) *Pipeline[O] {
	batched := Batch(p, size, batchCollectOpts(opts)...)
	return FlatMap(batched, func(ctx context.Context, batch []I, yield func(O) error) error {
		results, err := fn(ctx, batch)
		if err != nil {
			return err
		}
		for _, r := range results {
			if err := yield(r); err != nil {
				return err
			}
		}
		return nil
	}, opts...)
}

// batchCollectOpts extracts only the BatchTimeout option for the Batch stage;
// all other options (Concurrency, OnError, etc.) apply to the FlatMap stage.
func batchCollectOpts(opts []StageOption) []StageOption {
	cfg := buildStageConfig(opts)
	if cfg.batchTimeout == 0 {
		return nil
	}
	return []StageOption{BatchTimeout(cfg.batchTimeout)}
}
