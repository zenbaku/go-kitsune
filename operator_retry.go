package kitsune

import (
	"context"
	"time"
)

// RetryStrategy controls how [Retry] re-subscribes to its upstream pipeline
// after a failure.
//
// The zero value performs a single attempt with no retry.
type RetryStrategy struct {
	// MaxAttempts is the total number of upstream runs allowed, including the
	// first. Values ≤ 0 mean retry indefinitely. 1 means no retry (a single
	// attempt only).
	MaxAttempts int

	// Backoff computes the wait duration before the Nth retry (0-indexed: the
	// delay before the first retry is Backoff(0)). If nil, retries happen
	// immediately.
	Backoff Backoff

	// Retryable decides whether a given upstream error warrants another
	// attempt. If nil, every non-context error is retried. Context errors from
	// the outer context are never retried regardless of this predicate.
	Retryable func(error) bool

	// OnRetry, if non-nil, is called just before sleeping between attempts.
	// attempt is 0-indexed (0 = just before the first retry). Useful for
	// logging or metrics.
	OnRetry func(attempt int, err error, wait time.Duration)
}

// WithRetryable returns a copy of pol with Retryable set to fn.
func (pol RetryStrategy) WithRetryable(fn func(error) bool) RetryStrategy {
	pol.Retryable = fn
	return pol
}

// WithOnRetry returns a copy of pol with OnRetry set to fn.
func (pol RetryStrategy) WithOnRetry(fn func(attempt int, err error, wait time.Duration)) RetryStrategy {
	pol.OnRetry = fn
	return pol
}

// RetryUpTo returns a [RetryStrategy] that allows at most n total attempts
// (including the first) with the given backoff between retries.
func RetryUpTo(n int, b Backoff) RetryStrategy {
	return RetryStrategy{MaxAttempts: n, Backoff: b}
}

// RetryForever returns a [RetryStrategy] that retries indefinitely with the
// given backoff. The pipeline only stops when the outer context is cancelled
// or when the Retryable predicate returns false.
func RetryForever(b Backoff) RetryStrategy {
	return RetryStrategy{MaxAttempts: -1, Backoff: b}
}

// Retry re-runs the entire pipeline p whenever it errors, according to pol.
// It is the right primitive for sources that must reconnect on failure —
// websocket tails, change-data-capture streams, long-poll HTTP: where the
// correct response to a disconnect is to re-establish the connection and
// resume.
//
// Items produced during any attempt (including partial output from a failed
// attempt) are forwarded downstream immediately. Retry does not buffer or
// replay: downstream observes the concatenation of each attempt's output.
//
// This is distinct from [OnError] / [RetryMax] which retry individual item
// transformations within a single run. Use Retry when the pipeline source
// itself fails and needs to be re-subscribed from scratch.
//
//	kitsune.Retry(
//	    kitsune.Generate(websocketTail),
//	    kitsune.RetryForever(kitsune.ExponentialBackoff(100*time.Millisecond, 30*time.Second)),
//	)
//
// Context cancellation from the outer context is never retried and terminates
// the pipeline immediately.
func Retry[T any](p *Pipeline[T], pol RetryStrategy) *Pipeline[T] {
	isRetryable := pol.Retryable
	if isRetryable == nil {
		isRetryable = func(err error) bool { return err != nil }
	}

	return Generate(func(ctx context.Context, yield func(T) bool) error {
		retries := 0 // number of retries performed so far (not including the initial run)

		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			// Run p as an isolated sub-pipeline so each attempt gets a fresh
			// runCtx and a clean source reconnection.
			innerCtx, cancel := context.WithCancel(ctx)
			stopped := false
			_, runErr := p.ForEach(func(_ context.Context, item T) error {
				if !yield(item) {
					stopped = true
					cancel() // unblock upstream stages in the sub-pipeline
				}
				return nil
			}).Run(innerCtx)
			cancel()

			// Downstream called Take / TakeWhile: stop cleanly.
			if stopped {
				return nil
			}

			// Outer context cancelled: propagate without retry.
			if ctx.Err() != nil {
				return ctx.Err()
			}

			// Successful run.
			if runErr == nil {
				return nil
			}

			// Non-retryable error.
			if !isRetryable(runErr) {
				return runErr
			}

			// Max attempts exhausted (MaxAttempts ≤ 0 means unlimited).
			if pol.MaxAttempts > 0 && retries+1 >= pol.MaxAttempts {
				return runErr
			}

			// Backoff before next attempt.
			var wait time.Duration
			if pol.Backoff != nil {
				wait = pol.Backoff(retries)
			}
			if pol.OnRetry != nil {
				pol.OnRetry(retries, runErr, wait)
			}
			if wait > 0 {
				t := time.NewTimer(wait)
				select {
				case <-t.C:
				case <-ctx.Done():
					t.Stop()
					return ctx.Err()
				}
			}

			retries++
		}
	})
}
