package kitsune

import (
	"context"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// Notification[T]
// ---------------------------------------------------------------------------

// Notification[T] is a sum type that wraps a single event from a pipeline:
// a value, a terminal error, or normal completion.
//
//   - Value notification: Done=false, Err=nil — carries a live item.
//   - Error notification: Done=true, Err!=nil — carries the terminal error.
//   - Complete notification: Done=true, Err=nil — signals normal completion.
//
// Produced by [Materialize] and consumed by [Dematerialize].
//
// The zero value of Notification[T] is a valid value notification (IsValue
// returns true); it is not a sentinel for "no event".
type Notification[T any] struct {
	Value T
	Err   error
	Done  bool
}

// IsValue reports whether n carries a live value (not an error, not a
// completion signal).
func (n Notification[T]) IsValue() bool { return !n.Done && n.Err == nil }

// IsError reports whether n carries a terminal error.
func (n Notification[T]) IsError() bool { return n.Done && n.Err != nil }

// IsComplete reports whether n signals normal completion.
func (n Notification[T]) IsComplete() bool { return n.Done && n.Err == nil }

// NextNotification returns a value notification for v.
func NextNotification[T any](v T) Notification[T] { return Notification[T]{Value: v} }

// ErrorNotification returns a terminal error notification for err.
func ErrorNotification[T any](err error) Notification[T] {
	return Notification[T]{Err: err, Done: true}
}

// CompleteNotification returns a normal-completion notification.
func CompleteNotification[T any]() Notification[T] { return Notification[T]{Done: true} }

// ---------------------------------------------------------------------------
// Materialize
// ---------------------------------------------------------------------------

// Materialize converts each item and the terminal outcome of p into a
// [Notification[T]] value, emitting all of them on a single output pipeline.
//
// Emission rules:
//   - For each item T: emit NextNotification(item).
//   - On normal completion: emit CompleteNotification[T](), then complete normally.
//   - On pipeline error: emit ErrorNotification[T](err), then complete normally.
//     The error is not propagated; it is encoded into the notification stream.
//
// Context cancellation is not materialized: if the run context is cancelled,
// Materialize exits with the context error rather than emitting an
// ErrorNotification. Cancellation is a runner-level concern, not a pipeline
// error.
//
// See also: [Dematerialize], [MapResult], [TapError].
func Materialize[T any](p *Pipeline[T]) *Pipeline[Notification[T]] {
	return Generate(func(ctx context.Context, yield func(Notification[T]) bool) error {
		innerCtx, cancel := context.WithCancel(ctx)
		stopped := false

		err := p.ForEach(func(_ context.Context, item T) error {
			if !yield(NextNotification(item)) {
				stopped = true
				cancel()
			}
			return nil
		}).Run(innerCtx)
		cancel()

		if stopped {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err != nil {
			yield(ErrorNotification[T](err))
			return nil
		}
		yield(CompleteNotification[T]())
		return nil
	})
}

// ---------------------------------------------------------------------------
// Dematerialize
// ---------------------------------------------------------------------------

// Dematerialize is the inverse of [Materialize]: it unwraps a
// [Notification[T]] stream back into a plain T stream.
//
// Processing rules per notification:
//   - Value notification (IsValue): emit n.Value downstream.
//   - Complete notification (IsComplete): stop reading and complete normally.
//   - Error notification (IsError): re-inject n.Err as a pipeline error.
//   - Malformed notification (Done=false, Err!=nil): treated as an error
//     notification; n.Err is propagated.
//
// If the upstream channel closes without emitting a terminal notification,
// Dematerialize completes normally (defensive handling of unconventional
// producers).
//
// See also: [Materialize], [MapResult], [Catch].
func Dematerialize[T any](p *Pipeline[Notification[T]], opts ...StageOption) *Pipeline[T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:       id,
		kind:     "dematerialize",
		name:     orDefault(cfg.name, "dematerialize"),
		buffer:   cfg.buffer,
		overflow: cfg.overflow,
		inputs:   []int{p.id},
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
		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			hook.OnStageStart(ctx, cfg.name)
			var processed, errs int64
			defer func() { hook.OnStageDone(ctx, cfg.name, processed, errs) }()

			outbox := internal.NewOutbox(ch, cfg.overflow, hook, cfg.name)

			inner := func() error {
				for {
					select {
					case n, ok := <-inCh:
						if !ok {
							// Upstream closed without a terminal notification;
							// treat as clean completion.
							return nil
						}
						if n.Err != nil {
							// Error notification (or malformed mid-stream error).
							errs++
							return internal.WrapStageErr(cfg.name, n.Err, 0)
						}
						if n.Done {
							// Complete notification: stop normally.
							return nil
						}
						// Value notification.
						processed++
						if err := outbox.Send(ctx, n.Value); err != nil {
							return err
						}
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
			return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}
