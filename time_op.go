package kitsune

import (
	"context"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
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
		inputs: []int64{p.id},
	}
	var throttleOut *Pipeline[T]
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
		rc.initDrainNotify(id, throttleOut.consumerCount.Load())
		drainCh := rc.drainCh(id)
		stage := func(ctx context.Context) error {
			defer close(ch)
			cooperativeDrain := false
			defer func() {
				if !cooperativeDrain {
					go internal.DrainChan(inCh)
				}
			}()
			defer func() { rc.signalDrain(p.id) }()

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
				case <-drainCh:
					cooperativeDrain = true
					return nil
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	throttleOut = newPipeline(id, meta, build)
	return throttleOut
}

// ---------------------------------------------------------------------------
// Debounce
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Sample
// ---------------------------------------------------------------------------

// Sample emits the most-recently-seen item from p at each tick of a
// d-duration interval. If no item has arrived since the last tick, that tick
// is skipped silently. Unlike [Throttle] (which rate-limits on item arrival)
// and [Debounce] (which waits for a quiet period), Sample samples at a fixed
// wall-clock rate regardless of upstream speed.
//
// The latest item is NOT flushed when upstream closes between ticks; use
// [Debounce] if you need a final flush on source close.
//
// Supports [Buffer], [WithName], and [WithClock].
//
//	kitsune.Sample(liveQuotes, 100*time.Millisecond)
//	// emits at most one quote every 100 ms, always the latest one
func Sample[T any](p *Pipeline[T], d time.Duration, opts ...StageOption) *Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "sample",
		name:   orDefault(cfg.name, "sample"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	var sampleOut *Pipeline[T]
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
		rc.initDrainNotify(id, sampleOut.consumerCount.Load())
		drainCh := rc.drainCh(id)
		stage := func(ctx context.Context) error {
			defer close(ch)
			cooperativeDrain := false
			defer func() {
				if !cooperativeDrain {
					go internal.DrainChan(inCh)
				}
			}()
			defer func() { rc.signalDrain(p.id) }()

			clk := cfg.clock
			if clk == nil {
				clk = internal.RealClock{}
			}

			outbox := internal.NewBlockingOutbox(ch)
			ticker := clk.NewTicker(d)
			defer ticker.Stop()

			var (
				latest    T
				hasLatest bool
			)

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					latest = item
					hasLatest = true
				case <-ticker.C():
					if hasLatest {
						if err := outbox.Send(ctx, latest); err != nil {
							return err
						}
						hasLatest = false
					}
				case <-ctx.Done():
					return ctx.Err()
				case <-drainCh:
					cooperativeDrain = true
					return nil
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	sampleOut = newPipeline(id, meta, build)
	return sampleOut
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
		inputs: []int64{p.id},
	}
	var debounceOut *Pipeline[T]
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
		rc.initDrainNotify(id, debounceOut.consumerCount.Load())
		drainCh := rc.drainCh(id)
		stage := func(ctx context.Context) error {
			defer close(ch)
			cooperativeDrain := false
			defer func() {
				if !cooperativeDrain {
					go internal.DrainChan(inCh)
				}
			}()
			defer func() { rc.signalDrain(p.id) }()

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
				case <-drainCh:
					cooperativeDrain = true
					return nil
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	debounceOut = newPipeline(id, meta, build)
	return debounceOut
}
