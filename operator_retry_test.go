package kitsune_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// buildCountedSource returns a Generate pipeline and an atomic counter.
// On each run (subscription), the factory function is called once.
// If failFor > 0, the first failFor runs return errAttempt after emitting
// the items slice; subsequent runs succeed.
func buildRetrySource(items []int, failFor int) (*kitsune.Pipeline[int], *atomic.Int32) {
	var runs atomic.Int32
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		n := int(runs.Add(1))
		for _, v := range items {
			if !yield(v) {
				return nil
			}
		}
		if failFor > 0 && n <= failFor {
			return errAttempt
		}
		return nil
	})
	return p, &runs
}

var errAttempt = errors.New("attempt error")

// ---------------------------------------------------------------------------
// Basic success
// ---------------------------------------------------------------------------

func TestRetry_NoErrorSingleAttempt(t *testing.T) {
	src, runs := buildRetrySource([]int{1, 2, 3}, 0)
	got, err := runWith(t, kitsune.Retry(src, kitsune.RetryUpTo(3, kitsune.FixedBackoff(0))))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("unexpected output: %v", got)
	}
	if runs.Load() != 1 {
		t.Fatalf("expected 1 run, got %d", runs.Load())
	}
}

// ---------------------------------------------------------------------------
// Transient failure then success
// ---------------------------------------------------------------------------

func TestRetry_TransientThenSuccess(t *testing.T) {
	// Source fails on the first 2 runs, succeeds on the 3rd.
	src, runs := buildRetrySource([]int{1, 2, 3}, 2)
	got, err := runWith(t, kitsune.Retry(src, kitsune.RetryUpTo(5, kitsune.FixedBackoff(0))))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Each failed attempt still emits items; successful run also emits.
	// Total: [1,2,3, 1,2,3, 1,2,3]
	if len(got) != 9 {
		t.Fatalf("expected 9 items (3 per run × 3 runs), got %v", got)
	}
	if runs.Load() != 3 {
		t.Fatalf("expected 3 runs, got %d", runs.Load())
	}
}

// ---------------------------------------------------------------------------
// Exhaust max attempts
// ---------------------------------------------------------------------------

func TestRetry_ExhaustAttempts(t *testing.T) {
	src, runs := buildRetrySource([]int{10}, 100) // always fails
	var onRetryCalls int
	pol := kitsune.RetryUpTo(3, kitsune.FixedBackoff(0)).
		WithOnRetry(func(_ int, _ error, _ time.Duration) { onRetryCalls++ })

	_, err := runWith(t, kitsune.Retry(src, pol))
	if !errors.Is(err, errAttempt) {
		t.Fatalf("expected errAttempt, got %v", err)
	}
	if runs.Load() != 3 {
		t.Fatalf("expected 3 runs (MaxAttempts=3), got %d", runs.Load())
	}
	// OnRetry is called before each retry: 2 retries → 2 calls.
	if onRetryCalls != 2 {
		t.Fatalf("expected 2 OnRetry calls, got %d", onRetryCalls)
	}
}

// ---------------------------------------------------------------------------
// Non-retryable error
// ---------------------------------------------------------------------------

func TestRetry_NonRetryableError(t *testing.T) {
	permanent := errors.New("permanent")
	runs := 0
	src := kitsune.Generate(func(_ context.Context, yield func(int) bool) error {
		runs++
		return permanent
	})
	pol := kitsune.RetryForever(kitsune.FixedBackoff(0)).
		WithRetryable(func(err error) bool { return !errors.Is(err, permanent) })

	_, err := runWith(t, kitsune.Retry(src, pol))
	if !errors.Is(err, permanent) {
		t.Fatalf("expected permanent error, got %v", err)
	}
	if runs != 1 {
		t.Fatalf("expected 1 run (non-retryable stops immediately), got %d", runs)
	}
}

// ---------------------------------------------------------------------------
// RetryForever stops on context cancellation
// ---------------------------------------------------------------------------

func TestRetry_Forever_CancelStops(t *testing.T) {
	src := kitsune.Generate(func(_ context.Context, yield func(int) bool) error {
		return errAttempt // always fails
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := kitsune.Retry(src, kitsune.RetryForever(kitsune.FixedBackoff(0))).
		ForEach(func(_ context.Context, _ int) error { return nil }).
		Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Backoff timing
// ---------------------------------------------------------------------------

func TestRetry_Backoff(t *testing.T) {
	src, _ := buildRetrySource(nil, 2) // fail 2 times, then succeed on 3rd
	start := time.Now()
	_, err := runWith(t, kitsune.Retry(src, kitsune.RetryUpTo(3, kitsune.FixedBackoff(30*time.Millisecond))))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 2 retries × 30 ms each = 60 ms minimum.
	if elapsed < 60*time.Millisecond {
		t.Fatalf("expected at least 60ms elapsed (got %v)", elapsed)
	}
	if elapsed > 400*time.Millisecond {
		t.Fatalf("elapsed %v is suspiciously long (expected < 400ms)", elapsed)
	}
}

// ---------------------------------------------------------------------------
// Partial emissions from failed attempts are forwarded
// ---------------------------------------------------------------------------

func TestRetry_PartialEmissions(t *testing.T) {
	// Attempt 1 emits [1,2] then errors.
	// Attempt 2 emits [1,2,3] and succeeds.
	var run atomic.Int32
	src := kitsune.Generate(func(_ context.Context, yield func(int) bool) error {
		n := run.Add(1)
		yield(1)
		yield(2)
		if n == 1 {
			return errAttempt
		}
		yield(3)
		return nil
	})
	got, err := runWith(t, kitsune.Retry(src, kitsune.RetryUpTo(2, kitsune.FixedBackoff(0))))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []int{1, 2, 1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Downstream Take terminates retry loop cleanly
// ---------------------------------------------------------------------------

func TestRetry_DownstreamTakeStops(t *testing.T) {
	// Infinite source that would retry forever.
	src := kitsune.Generate(func(_ context.Context, yield func(int) bool) error {
		for i := 0; ; i++ {
			if !yield(i) {
				return nil
			}
		}
	})
	p := kitsune.Retry(src, kitsune.RetryForever(kitsune.FixedBackoff(0)))
	got, err := runWith(t, p.Take(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 items, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Context cancellation during backoff
// ---------------------------------------------------------------------------

func TestRetry_ContextCanceledDuringBackoff(t *testing.T) {
	src := kitsune.Generate(func(_ context.Context, yield func(int) bool) error {
		return errAttempt
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := kitsune.Retry(src, kitsune.RetryForever(kitsune.FixedBackoff(10*time.Second))).
		ForEach(func(_ context.Context, _ int) error { return nil }).
		Run(ctx)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	// Should return well before the 10-second backoff expires.
	if elapsed > 2*time.Second {
		t.Fatalf("took too long to cancel: %v", elapsed)
	}
}

// ---------------------------------------------------------------------------
// OnRetry callback arguments
// ---------------------------------------------------------------------------

func TestRetry_OnRetryCallback(t *testing.T) {
	src, _ := buildRetrySource(nil, 2) // fail twice, succeed on third
	type call struct {
		attempt int
		err     error
		wait    time.Duration
	}
	var calls []call
	pol := kitsune.RetryUpTo(3, kitsune.FixedBackoff(5*time.Millisecond)).
		WithOnRetry(func(attempt int, err error, wait time.Duration) {
			calls = append(calls, call{attempt, err, wait})
		})

	_, runErr := runWith(t, kitsune.Retry(src, pol))
	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 OnRetry calls, got %d", len(calls))
	}
	// attempt is 0-indexed: first retry = 0, second retry = 1.
	if calls[0].attempt != 0 || calls[1].attempt != 1 {
		t.Fatalf("unexpected attempt indices: %v", calls)
	}
	for _, c := range calls {
		if !errors.Is(c.err, errAttempt) {
			t.Fatalf("expected errAttempt in callback, got %v", c.err)
		}
		if c.wait != 5*time.Millisecond {
			t.Fatalf("expected 5ms wait, got %v", c.wait)
		}
	}
}
