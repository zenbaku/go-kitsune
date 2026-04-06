package internal

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Supervise wraps fn with restart-on-failure and panic-recovery logic.
// When policy.HasSupervision() is false it is a zero-overhead pass-through.
func Supervise(ctx context.Context, policy SupervisionPolicy, hook Hook, name string, fn func() error) error {
	if !policy.HasSupervision() {
		return fn()
	}

	restarts := 0
	var windowStart time.Time

	for {
		err, panicked, panicVal := runProtected(fn, policy.OnPanic)

		if err == nil && !panicked {
			return nil
		}

		if panicked {
			switch policy.OnPanic {
			case PanicPropagate:
				panic(panicVal)
			case PanicSkip:
				return nil
			case PanicRestart:
				err = fmt.Errorf("kitsune: stage %q panicked: %v", name, panicVal)
			}
		} else if policy.PanicOnly {
			return err
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		if policy.Window > 0 && !windowStart.IsZero() && time.Since(windowStart) > policy.Window {
			restarts = 0
			windowStart = time.Time{}
		}

		if restarts >= policy.MaxRestarts {
			return err
		}

		restarts++
		if windowStart.IsZero() {
			windowStart = time.Now()
		}

		if sh, ok := hook.(SupervisionHook); ok {
			sh.OnStageRestart(ctx, name, restarts, err)
		}

		if policy.Backoff != nil {
			d := policy.Backoff(restarts - 1)
			if d > 0 {
				select {
				case <-time.After(d):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}

// runProtected calls fn and recovers any panic when onPanic != PanicPropagate.
func runProtected(fn func() error, onPanic PanicAction) (err error, panicked bool, panicVal any) {
	if onPanic == PanicPropagate {
		return fn(), false, nil
	}
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			panicVal = r
		}
	}()
	err = fn()
	return
}

// WrapStageErr wraps err in a [StageError] unless it is nil, [ErrSkipped],
// [context.Canceled], or [context.DeadlineExceeded] — errors that either carry
// no stage context or are infrastructure signals rather than user-function failures.
func WrapStageErr(name string, err error, attempt int) error {
	if err == nil || err == ErrSkipped || err == context.Canceled || err == context.DeadlineExceeded {
		return err
	}
	// Don't double-wrap.
	var se *StageError
	if errors.As(err, &se) {
		return err
	}
	return &StageError{Stage: name, Attempt: attempt, Cause: err}
}

// ProcessItem invokes fn with retry/skip/halt logic.
// The third return value is the final attempt index (0-based); callers use it
// to populate [StageError] via [WrapStageErr].
func ProcessItem[I, O any](
	ctx context.Context,
	fn func(context.Context, I) (O, error),
	item I,
	h ErrorHandler,
) (O, error, int) {
	for attempt := 0; ; attempt++ {
		result, err := fn(ctx, item)
		if err == nil {
			return result, nil, attempt
		}
		switch h.Handle(err, attempt) {
		case ActionSkip:
			var zero O
			return zero, ErrSkipped, attempt
		case ActionReturn:
			if r, ok := h.(Returner); ok {
				val := r.ReturnValue()
				if typed, ok := val.(O); ok {
					return typed, nil, attempt
				}
			}
			var zero O
			return zero, err, attempt
		case ActionRetry:
			bo := h.Backoff()
			if bo == nil {
				var zero O
				return zero, err, attempt
			}
			select {
			case <-time.After(bo(attempt)):
				continue
			case <-ctx.Done():
				var zero O
				return zero, ctx.Err(), attempt
			}
		default:
			var zero O
			return zero, err, attempt
		}
	}
}

// ProcessFlatMapItem invokes fn with a yield callback, handling retry/skip/halt logic.
// Returns (err, attempt). On ActionReturn, behaves like ActionSkip (no single replacement).
func ProcessFlatMapItem[I, O any](
	ctx context.Context,
	fn func(context.Context, I, func(O) error) error,
	item I,
	h ErrorHandler,
	send func(O) error,
) (error, int) {
	for attempt := 0; ; attempt++ {
		// Collect results into a buffer; only flush on success.
		var buf []O
		bufYield := func(v O) error {
			buf = append(buf, v)
			return nil
		}

		err := fn(ctx, item, bufYield)
		if err == nil {
			// Flush buffered results downstream.
			for _, v := range buf {
				if sendErr := send(v); sendErr != nil {
					return sendErr, attempt
				}
			}
			return nil, attempt
		}
		buf = buf[:0]

		switch h.Handle(err, attempt) {
		case ActionSkip, ActionReturn:
			return ErrSkipped, attempt
		case ActionRetry:
			bo := h.Backoff()
			if bo == nil {
				return err, attempt
			}
			select {
			case <-time.After(bo(attempt)):
				continue
			case <-ctx.Done():
				return ctx.Err(), attempt
			}
		default:
			return err, attempt
		}
	}
}
