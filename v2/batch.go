package kitsune

import (
	"context"
	"time"

	"github.com/zenbaku/go-kitsune/v2/internal"
)

// ---------------------------------------------------------------------------
// Batch
// ---------------------------------------------------------------------------

// Batch collects items into slices of up to size items. If BatchTimeout is set,
// a partial batch is flushed when the timeout elapses even if size is not reached.
// An empty batch is never emitted.
func Batch[T any](p *Pipeline[T], size int, opts ...StageOption) *Pipeline[[]T] {
	cfg := buildStageConfig(opts)
	ch := make(chan []T, cfg.buffer)
	meta := stageMeta{
		kind:       "batch",
		name:       orDefault(cfg.name, "batch"),
		buffer:     cfg.buffer,
		batchSize:  size,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	stage := func(ctx context.Context) error {
		defer close(ch)
		defer func() { go internal.DrainChan(p.ch) }()

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

		if cfg.batchTimeout == 0 {
			// No timeout: collect exactly size items per batch.
			for {
				select {
				case item, ok := <-p.ch:
					if !ok {
						return flush()
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
			case item, ok := <-p.ch:
				if !ok {
					return flush()
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

	id := p.sl.add(stage, meta)
	return newPipeline(ch, p.sl, id)
}

// ---------------------------------------------------------------------------
// Unbatch
// ---------------------------------------------------------------------------

// Unbatch flattens a pipeline of slices into a pipeline of individual items.
func Unbatch[T any](p *Pipeline[[]T], opts ...StageOption) *Pipeline[T] {
	cfg := buildStageConfig(opts)
	ch := make(chan T, cfg.buffer)
	meta := stageMeta{
		kind:       "unbatch",
		name:       orDefault(cfg.name, "unbatch"),
		buffer:     cfg.buffer,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	stage := func(ctx context.Context) error {
		defer close(ch)
		defer func() { go internal.DrainChan(p.ch) }()

		outbox := internal.NewBlockingOutbox(ch)

		for {
			select {
			case batch, ok := <-p.ch:
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

	id := p.sl.add(stage, meta)
	return newPipeline(ch, p.sl, id)
}

// ---------------------------------------------------------------------------
// Window (count-based)
// ---------------------------------------------------------------------------

// Window emits non-overlapping slices of exactly size items. The last window
// may be smaller if the source completes before size items are available.
// An empty window is never emitted.
//
// For time-based windows use SessionWindow or SlidingWindow.
func Window[T any](p *Pipeline[T], size int, opts ...StageOption) *Pipeline[[]T] {
	cfg := buildStageConfig(opts)
	ch := make(chan []T, cfg.buffer)
	meta := stageMeta{
		kind:       "window",
		name:       orDefault(cfg.name, "window"),
		buffer:     cfg.buffer,
		batchSize:  size,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	stage := func(ctx context.Context) error {
		defer close(ch)
		defer func() { go internal.DrainChan(p.ch) }()

		outbox := internal.NewBlockingOutbox(ch)
		buf := make([]T, 0, size)

		for {
			select {
			case item, ok := <-p.ch:
				if !ok {
					if len(buf) > 0 {
						return outbox.Send(ctx, buf)
					}
					return nil
				}
				buf = append(buf, item)
				if len(buf) == size {
					win := make([]T, size)
					copy(win, buf)
					buf = buf[:0]
					if err := outbox.Send(ctx, win); err != nil {
						return err
					}
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
// SlidingWindow (count-based)
// ---------------------------------------------------------------------------

// SlidingWindow emits overlapping slices of exactly size items, advancing by
// step items each time. step must be > 0 and ≤ size. Items are only emitted
// once a full window of size items is available; partial windows at the end
// of the stream are dropped.
func SlidingWindow[T any](p *Pipeline[T], size, step int, opts ...StageOption) *Pipeline[[]T] {
	if step <= 0 || step > size {
		panic("kitsune: SlidingWindow step must be > 0 and <= size")
	}
	cfg := buildStageConfig(opts)
	ch := make(chan []T, cfg.buffer)
	meta := stageMeta{
		kind:       "sliding_window",
		name:       orDefault(cfg.name, "sliding_window"),
		buffer:     cfg.buffer,
		batchSize:  size,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	stage := func(ctx context.Context) error {
		defer close(ch)
		defer func() { go internal.DrainChan(p.ch) }()

		outbox := internal.NewBlockingOutbox(ch)
		buf := make([]T, 0, size)

		for {
			select {
			case item, ok := <-p.ch:
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

	id := p.sl.add(stage, meta)
	return newPipeline(ch, p.sl, id)
}

// ---------------------------------------------------------------------------
// SessionWindow (gap-based)
// ---------------------------------------------------------------------------

// SessionWindow groups items into sessions separated by periods of inactivity.
// A new session starts whenever no item arrives within gap. The accumulated
// session buffer is emitted when the gap timer fires.
// An empty session is never emitted.
func SessionWindow[T any](p *Pipeline[T], gap time.Duration, opts ...StageOption) *Pipeline[[]T] {
	cfg := buildStageConfig(opts)
	ch := make(chan []T, cfg.buffer)
	meta := stageMeta{
		kind:       "session_window",
		name:       orDefault(cfg.name, "session_window"),
		buffer:     cfg.buffer,
		inputs:     []int{p.id},
		getChanLen: func() int { return len(ch) },
		getChanCap: func() int { return cap(ch) },
	}

	stage := func(ctx context.Context) error {
		defer close(ch)
		defer func() { go internal.DrainChan(p.ch) }()

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
			case item, ok := <-p.ch:
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

	id := p.sl.add(stage, meta)
	return newPipeline(ch, p.sl, id)
}
