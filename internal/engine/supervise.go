package engine

import (
	"context"
	"fmt"
	"time"
)

// supervise wraps stageFunc with restart-on-failure and panic-recovery logic.
// When policy.HasSupervision() is false it is a zero-overhead pass-through.
func supervise(ctx context.Context, policy SupervisionPolicy, hook Hook, name string, stageFunc func() error) error {
	if !policy.HasSupervision() {
		return stageFunc()
	}

	restarts := 0
	var windowStart time.Time

	for {
		err, panicked, panicVal := runProtected(stageFunc, policy.OnPanic)

		// Normal completion.
		if err == nil && !panicked {
			return nil
		}

		// Handle panic according to policy.
		if panicked {
			switch policy.OnPanic {
			case PanicPropagate:
				panic(panicVal) // re-panic; caller's defer will propagate
			case PanicSkip:
				// Recover silently; the item that caused the panic is already lost.
				return nil
			case PanicRestart:
				err = fmt.Errorf("kitsune: stage %q panicked: %v", name, panicVal)
			}
		}

		// Respect context cancellation.
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Reset counter if outside the window.
		if policy.Window > 0 && !windowStart.IsZero() && time.Since(windowStart) > policy.Window {
			restarts = 0
			windowStart = time.Time{}
		}

		// Budget exhausted — propagate the error.
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
// When onPanic is PanicPropagate the defer is skipped entirely (no overhead).
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
