package kitsune

import (
	"context"
	"time"

	"github.com/zenbaku/go-kitsune/v2/internal"
)

// ---------------------------------------------------------------------------
// Throttle (sample / rate-limit)
// ---------------------------------------------------------------------------

// Throttle emits at most one item per window duration. The first item in each
// window is emitted; subsequent items within the same window are dropped.
// This is also known as "throttle-leading" or "rate-limit".
func Throttle[T any](p *Pipeline[T], window time.Duration, opts ...StageOption) *Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "throttle",
		name:   orDefault(cfg.name, "throttle"),
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

			clk := cfg.clock
			if clk == nil {
				clk = internal.RealClock{}
			}

			outbox := internal.NewBlockingOutbox(ch)
			var lastEmit time.Time

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					now := clk.Now()
					if lastEmit.IsZero() || now.Sub(lastEmit) >= window {
						if err := outbox.Send(ctx, item); err != nil {
							return err
						}
						lastEmit = now
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
// Debounce
// ---------------------------------------------------------------------------

// Debounce suppresses rapid bursts: an item is only emitted after no new items
// have arrived for the silence duration. If items arrive faster than silence,
// only the last item in each burst is forwarded.
func Debounce[T any](p *Pipeline[T], silence time.Duration, opts ...StageOption) *Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "debounce",
		name:   orDefault(cfg.name, "debounce"),
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

			clk := cfg.clock
			if clk == nil {
				clk = internal.RealClock{}
			}

			outbox := internal.NewBlockingOutbox(ch)

			var (
				pending    T
				hasPending bool
			)

			// Start timer in stopped state.
			timer := clk.NewTimer(silence)
			timer.Stop()
			// Drain any initial tick.
			select {
			case <-timer.C():
			default:
			}
			timerActive := false

			for {
				var timerCh <-chan time.Time
				if timerActive {
					timerCh = timer.C()
				}

				select {
				case item, ok := <-inCh:
					if !ok {
						// Flush any pending item.
						if hasPending {
							return outbox.Send(ctx, pending)
						}
						return nil
					}
					pending = item
					hasPending = true
					// Reset the silence timer.
					if timerActive {
						timer.Stop()
						select {
						case <-timer.C():
						default:
						}
					}
					timer.Reset(silence)
					timerActive = true

				case <-timerCh:
					timerActive = false
					if hasPending {
						hasPending = false
						if err := outbox.Send(ctx, pending); err != nil {
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
