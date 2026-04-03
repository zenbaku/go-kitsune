package internal

import (
	"context"
	"errors"
	"time"
)

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
