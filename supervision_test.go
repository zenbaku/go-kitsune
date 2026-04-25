package kitsune_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/testkit"
)

// ---------------------------------------------------------------------------
// ForEach supervision
// ---------------------------------------------------------------------------

func TestForEachSupervision(t *testing.T) {
	ctx := context.Background()
	hook := &testkit.RecordingHook{}

	var calls atomic.Int64
	_, err := kitsune.FromSlice([]int{1, 2, 3, 4, 5}).ForEach(
		func(_ context.Context, _ int) error {
			c := calls.Add(1)
			if c == 2 {
				return errors.New("transient")
			}
			return nil
		},
		kitsune.Supervise(kitsune.RestartOnError(3, nil)),
		kitsune.WithName("test-foreach"),
	).Run(ctx, kitsune.WithHook(hook))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hook.Restarts()) != 1 {
		t.Errorf("want 1 restart, got %d", len(hook.Restarts()))
	}
}

func TestForEachSupervisionExhausted(t *testing.T) {
	ctx := context.Background()

	_, err := kitsune.FromSlice([]int{1, 2, 3}).ForEach(
		func(_ context.Context, _ int) error {
			return errors.New("always fails")
		},
		kitsune.Supervise(kitsune.RestartOnError(2, nil)),
		kitsune.WithName("test-foreach-exhausted"),
	).Run(ctx)

	if err == nil {
		t.Fatal("expected error after restarts exhausted, got nil")
	}
}

// ---------------------------------------------------------------------------
// FlatMap (serial via Concurrency(1)) supervision
// ---------------------------------------------------------------------------

func TestFlatMapSerialSupervision(t *testing.T) {
	ctx := context.Background()
	hook := &testkit.RecordingHook{}

	var calls atomic.Int64
	p := kitsune.FlatMap(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		func(_ context.Context, n int, yield func(int) error) error {
			c := calls.Add(1)
			if c == 2 {
				return errors.New("transient flatmap")
			}
			return yield(n * 10)
		},
		kitsune.Concurrency(1),
		kitsune.Supervise(kitsune.RestartOnError(3, nil)),
		kitsune.WithName("test-flatmap-serial"),
	)
	_, err := kitsune.Collect(ctx, p, kitsune.WithHook(hook))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hook.Restarts()) != 1 {
		t.Errorf("want 1 restart, got %d", len(hook.Restarts()))
	}
}

func TestFlatMapSerialSupervisionExhausted(t *testing.T) {
	ctx := context.Background()

	p := kitsune.FlatMap(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, _ int, _ func(int) error) error {
			return errors.New("always fails")
		},
		kitsune.Concurrency(1),
		kitsune.Supervise(kitsune.RestartOnError(1, nil)),
		kitsune.WithName("test-flatmap-exhausted"),
	)
	_, err := kitsune.Collect(ctx, p)

	if err == nil {
		t.Fatal("expected error after restarts exhausted, got nil")
	}
}

// ---------------------------------------------------------------------------
// Filter supervision
// ---------------------------------------------------------------------------

func TestFilterSupervision(t *testing.T) {
	ctx := context.Background()
	hook := &testkit.RecordingHook{}

	var calls atomic.Int64
	p := kitsune.Filter(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		func(_ context.Context, n int) (bool, error) {
			c := calls.Add(1)
			if c == 2 {
				return false, errors.New("transient filter")
			}
			return n%2 == 1, nil
		},
		kitsune.Supervise(kitsune.RestartOnError(3, nil)),
		kitsune.WithName("test-filter"),
	)
	_, err := kitsune.Collect(ctx, p, kitsune.WithHook(hook))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hook.Restarts()) != 1 {
		t.Errorf("want 1 restart, got %d", len(hook.Restarts()))
	}
}

func TestFilterSupervisionExhausted(t *testing.T) {
	ctx := context.Background()

	p := kitsune.Filter(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, _ int) (bool, error) {
			return false, errors.New("always fails")
		},
		kitsune.Supervise(kitsune.RestartOnError(1, nil)),
		kitsune.WithName("test-filter-exhausted"),
	)
	_, err := kitsune.Collect(ctx, p)

	if err == nil {
		t.Fatal("expected error after restarts exhausted, got nil")
	}
}

// ---------------------------------------------------------------------------
// Tap supervision
// ---------------------------------------------------------------------------

func TestTapSupervision(t *testing.T) {
	ctx := context.Background()
	hook := &testkit.RecordingHook{}

	var calls atomic.Int64
	p := kitsune.Tap(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		func(_ context.Context, _ int) error {
			c := calls.Add(1)
			if c == 2 {
				return errors.New("transient tap")
			}
			return nil
		},
		kitsune.Supervise(kitsune.RestartOnError(3, nil)),
		kitsune.WithName("test-tap"),
	)
	_, err := kitsune.Collect(ctx, p, kitsune.WithHook(hook))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hook.Restarts()) != 1 {
		t.Errorf("want 1 restart, got %d", len(hook.Restarts()))
	}
}

func TestTapSupervisionExhausted(t *testing.T) {
	ctx := context.Background()

	p := kitsune.Tap(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, _ int) error {
			return errors.New("always fails")
		},
		kitsune.Supervise(kitsune.RestartOnError(1, nil)),
		kitsune.WithName("test-tap-exhausted"),
	)
	_, err := kitsune.Collect(ctx, p)

	if err == nil {
		t.Fatal("expected error after restarts exhausted, got nil")
	}
}

// ---------------------------------------------------------------------------
// Panic recovery
// ---------------------------------------------------------------------------

func TestForEachPanicRestart(t *testing.T) {
	ctx := context.Background()
	hook := &testkit.RecordingHook{}

	var calls atomic.Int64
	_, err := kitsune.FromSlice([]int{1, 2, 3, 4, 5}).ForEach(
		func(_ context.Context, _ int) error {
			c := calls.Add(1)
			if c == 1 {
				panic("boom")
			}
			return nil
		},
		kitsune.Supervise(kitsune.RestartAlways(3, nil)),
		kitsune.WithName("test-foreach-panic"),
	).Run(ctx, kitsune.WithHook(hook))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hook.Restarts()) != 1 {
		t.Errorf("want 1 restart, got %d", len(hook.Restarts()))
	}
}

// ---------------------------------------------------------------------------
// Map serial supervision (baseline; already wired, verifying test pattern)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Map concurrent supervision
// ---------------------------------------------------------------------------

func TestMapConcurrentSupervision(t *testing.T) {
	ctx := context.Background()
	hook := &testkit.RecordingHook{}

	var calls atomic.Int64
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		func(_ context.Context, n int) (int, error) {
			c := calls.Add(1)
			if c == 1 {
				return 0, errors.New("transient concurrent")
			}
			return n * 2, nil
		},
		kitsune.Concurrency(2),
		kitsune.Supervise(kitsune.RestartOnError(3, nil)),
		kitsune.WithName("test-map-concurrent"),
	)
	_, err := kitsune.Collect(ctx, p, kitsune.WithHook(hook))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hook.Restarts()) < 1 {
		t.Errorf("want at least 1 restart, got %d", len(hook.Restarts()))
	}
}

func TestMapConcurrentSupervisionExhausted(t *testing.T) {
	ctx := context.Background()

	// Use enough items so some remain in the channel across all restarts.
	items := make([]int, 50)
	p := kitsune.Map(
		kitsune.FromSlice(items),
		func(_ context.Context, _ int) (int, error) {
			return 0, errors.New("always fails")
		},
		kitsune.Concurrency(2),
		kitsune.Supervise(kitsune.RestartOnError(2, nil)),
		kitsune.WithName("test-map-concurrent-exhausted"),
	)
	_, err := kitsune.Collect(ctx, p)

	if err == nil {
		t.Fatal("expected error after restarts exhausted, got nil")
	}
}

func TestMapOrderedSupervision(t *testing.T) {
	ctx := context.Background()
	hook := &testkit.RecordingHook{}

	var calls atomic.Int64
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		func(_ context.Context, n int) (int, error) {
			c := calls.Add(1)
			if c == 1 {
				return 0, errors.New("transient ordered")
			}
			return n * 2, nil
		},
		kitsune.Concurrency(2),
		kitsune.Ordered(),
		kitsune.Supervise(kitsune.RestartOnError(3, nil)),
		kitsune.WithName("test-map-ordered"),
	)
	_, err := kitsune.Collect(ctx, p, kitsune.WithHook(hook))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hook.Restarts()) < 1 {
		t.Errorf("want at least 1 restart, got %d", len(hook.Restarts()))
	}
}

func TestMapOrderedSupervisionExhausted(t *testing.T) {
	ctx := context.Background()

	items := make([]int, 50)
	p := kitsune.Map(
		kitsune.FromSlice(items),
		func(_ context.Context, _ int) (int, error) {
			return 0, errors.New("always fails")
		},
		kitsune.Concurrency(2),
		kitsune.Ordered(),
		kitsune.Supervise(kitsune.RestartOnError(2, nil)),
		kitsune.WithName("test-map-ordered-exhausted"),
	)
	_, err := kitsune.Collect(ctx, p)

	if err == nil {
		t.Fatal("expected error after restarts exhausted, got nil")
	}
}

// ---------------------------------------------------------------------------
// FlatMap concurrent supervision
// ---------------------------------------------------------------------------

func TestFlatMapConcurrentSupervision(t *testing.T) {
	ctx := context.Background()
	hook := &testkit.RecordingHook{}

	var calls atomic.Int64
	p := kitsune.FlatMap(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		func(_ context.Context, n int, yield func(int) error) error {
			c := calls.Add(1)
			if c == 1 {
				return errors.New("transient flatmap concurrent")
			}
			return yield(n * 10)
		},
		kitsune.Concurrency(2),
		kitsune.Supervise(kitsune.RestartOnError(3, nil)),
		kitsune.WithName("test-flatmap-concurrent"),
	)
	_, err := kitsune.Collect(ctx, p, kitsune.WithHook(hook))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hook.Restarts()) < 1 {
		t.Errorf("want at least 1 restart, got %d", len(hook.Restarts()))
	}
}

func TestFlatMapConcurrentSupervisionExhausted(t *testing.T) {
	ctx := context.Background()

	// Use enough items so some remain in the channel across all restarts.
	items := make([]int, 50)
	p := kitsune.FlatMap(
		kitsune.FromSlice(items),
		func(_ context.Context, _ int, _ func(int) error) error {
			return errors.New("always fails")
		},
		kitsune.Concurrency(2),
		kitsune.Supervise(kitsune.RestartOnError(2, nil)),
		kitsune.WithName("test-flatmap-concurrent-exhausted"),
	)
	_, err := kitsune.Collect(ctx, p)

	if err == nil {
		t.Fatal("expected error after restarts exhausted, got nil")
	}
}

// ---------------------------------------------------------------------------
// FlatMap ordered supervision
// ---------------------------------------------------------------------------

func TestFlatMapOrderedSupervision(t *testing.T) {
	ctx := context.Background()
	hook := &testkit.RecordingHook{}

	var calls atomic.Int64
	p := kitsune.FlatMap(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		func(_ context.Context, n int, yield func(int) error) error {
			c := calls.Add(1)
			if c == 1 {
				return errors.New("transient flatmap ordered")
			}
			return yield(n * 10)
		},
		kitsune.Concurrency(2),
		kitsune.Ordered(),
		kitsune.Supervise(kitsune.RestartOnError(3, nil)),
		kitsune.WithName("test-flatmap-ordered"),
	)
	_, err := kitsune.Collect(ctx, p, kitsune.WithHook(hook))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hook.Restarts()) < 1 {
		t.Errorf("want at least 1 restart, got %d", len(hook.Restarts()))
	}
}

func TestFlatMapOrderedSupervisionExhausted(t *testing.T) {
	ctx := context.Background()

	items := make([]int, 50)
	p := kitsune.FlatMap(
		kitsune.FromSlice(items),
		func(_ context.Context, _ int, _ func(int) error) error {
			return errors.New("always fails")
		},
		kitsune.Concurrency(2),
		kitsune.Ordered(),
		kitsune.Supervise(kitsune.RestartOnError(2, nil)),
		kitsune.WithName("test-flatmap-ordered-exhausted"),
	)
	_, err := kitsune.Collect(ctx, p)

	if err == nil {
		t.Fatal("expected error after restarts exhausted, got nil")
	}
}

// ---------------------------------------------------------------------------
// Regression: concurrent error propagation (errCh drain race)
//
// Before the fix, the dispatcher's semaphore select included a
// `case err := <-errCh: _ = err` arm that could drain errCh before the
// final `select { case err := <-errCh: }` read it, silently losing errors.
// This test forces the race by using Concurrency(1) with 2 items: the
// dispatcher is guaranteed to block on the semaphore while the first worker
// is running, exercising the exact code path that had the bug.
// ---------------------------------------------------------------------------

func TestMapConcurrentErrorAlwaysPropagated(t *testing.T) {
	sentinel := errors.New("expected error")
	for i := range 100 {
		_, err := kitsune.Collect(context.Background(),
			kitsune.Map(
				kitsune.FromSlice([]int{1, 2}),
				func(_ context.Context, _ int) (int, error) { return 0, sentinel },
				kitsune.Concurrency(1),
			),
		)
		if err == nil {
			t.Fatalf("iteration %d: error was silently dropped", i)
		}
	}
}

func TestFlatMapConcurrentErrorAlwaysPropagated(t *testing.T) {
	sentinel := errors.New("expected error")
	for i := range 100 {
		_, err := kitsune.Collect(context.Background(),
			kitsune.FlatMap(
				kitsune.FromSlice([]int{1, 2}),
				func(_ context.Context, _ int, _ func(int) error) error { return sentinel },
				kitsune.Concurrency(1),
			),
		)
		if err == nil {
			t.Fatalf("iteration %d: error was silently dropped", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Map serial supervision (baseline; already wired, verifying test pattern)
// ---------------------------------------------------------------------------

func TestMapSerialSupervision(t *testing.T) {
	ctx := context.Background()
	hook := &testkit.RecordingHook{}

	var calls atomic.Int64
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		func(_ context.Context, n int) (int, error) {
			c := calls.Add(1)
			if c == 2 {
				return 0, errors.New("transient map")
			}
			return n * 2, nil
		},
		kitsune.Supervise(kitsune.RestartOnError(3, nil)),
		kitsune.WithName("test-map-serial"),
	)
	_, err := kitsune.Collect(ctx, p, kitsune.WithHook(hook))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hook.Restarts()) != 1 {
		t.Errorf("want 1 restart, got %d", len(hook.Restarts()))
	}
}

// ---------------------------------------------------------------------------
// ExponentialBackoff / RetryThen / RestartAlways
// ---------------------------------------------------------------------------

func TestExponentialBackoff(t *testing.T) {
	bo := kitsune.ExponentialBackoff(10*time.Millisecond, 100*time.Millisecond)

	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 10 * time.Millisecond},
		{1, 20 * time.Millisecond},
		{2, 40 * time.Millisecond},
		{3, 80 * time.Millisecond},
		{4, 100 * time.Millisecond}, // capped
		{10, 100 * time.Millisecond},
	}
	for _, c := range cases {
		got := bo(c.attempt)
		if got != c.want {
			t.Errorf("attempt %d: got %v, want %v", c.attempt, got, c.want)
		}
	}
}

func TestRetryThenFallback(t *testing.T) {
	// RetryThen(2, ..., Skip()) must skip items after retries are exhausted.
	persistent := errors.New("always fails")
	var calls atomic.Int64

	results, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, _ int) (int, error) {
			calls.Add(1)
			return 0, persistent
		},
		kitsune.OnError(kitsune.RetryThen(2, kitsune.FixedBackoff(0), kitsune.ActionDrop())),
	).Collect(context.Background())
	if err != nil {
		t.Fatalf("expected nil error after skip fallback, got: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty results (all skipped), got %d", len(results))
	}
	// Each of 3 items gets 3 attempts (initial + 2 retries).
	if calls.Load() != 9 {
		t.Fatalf("expected 9 calls (3 items × 3 attempts), got %d", calls.Load())
	}
}

// ---------------------------------------------------------------------------
// Supervision gaps (6g)
// ---------------------------------------------------------------------------

func TestSupervisionNoRestartDefault(t *testing.T) {
	// Without Supervise, errors propagate normally; no restart, no retry.
	boom := errors.New("stage error")
	_, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				return 0, boom
			}
			return v, nil
		},
	).Collect(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom to propagate, got: %v", err)
	}
}

func TestRestartOnPanicHaltsOnError(t *testing.T) {
	// RestartOnPanic must NOT restart on regular (non-panic) errors.
	boom := errors.New("regular error")
	var calls atomic.Int64
	_, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			calls.Add(1)
			if v == 2 {
				return 0, boom
			}
			return v, nil
		},
		kitsune.Supervise(kitsune.RestartOnPanic(3, nil)),
	).Collect(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom to propagate (not restart), got: %v", err)
	}
	// With no restart, only items up to and including the failing one are processed.
	if calls.Load() > 3 {
		t.Errorf("stage was called %d times; expected at most 3 (no restart)", calls.Load())
	}
}

func TestSupervisionContextCancelled(t *testing.T) {
	// Context cancel during a restart loop should cause the pipeline to exit.
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := kitsune.Map(
			kitsune.Generate(func(_ context.Context, yield func(int) bool) error {
				for i := 0; ; i++ {
					if !yield(i) {
						return nil
					}
				}
			}),
			func(_ context.Context, _ int) (int, error) {
				return 0, errors.New("always fails")
			},
			kitsune.Supervise(kitsune.RestartOnError(1000, kitsune.FixedBackoff(0))),
		).Collect(ctx)
		done <- err
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected non-nil error after context cancel, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not terminate after context cancel")
	}
}

func TestRestartAlways(t *testing.T) {
	// RestartAlways restarts on both errors and panics.
	t.Run("recovers from error", func(t *testing.T) {
		var calls atomic.Int64
		results, err := kitsune.Map(
			kitsune.FromSlice([]int{1, 2, 3}),
			func(_ context.Context, v int) (int, error) {
				if calls.Add(1) == 1 {
					return 0, errors.New("transient")
				}
				return v * 2, nil
			},
			kitsune.Supervise(kitsune.RestartAlways(1, kitsune.FixedBackoff(0))),
		).Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		// Item 1 consumed by the erroring attempt; items 2 and 3 processed after restart.
		if len(results) != 2 {
			t.Fatalf("expected 2 results (items 2 and 3), got %v", results)
		}
	})

	t.Run("recovers from panic", func(t *testing.T) {
		var calls atomic.Int64
		results, err := kitsune.Map(
			kitsune.FromSlice([]int{1, 2, 3}),
			func(_ context.Context, v int) (int, error) {
				if calls.Add(1) == 1 {
					panic("transient panic")
				}
				return v * 2, nil
			},
			kitsune.Supervise(kitsune.RestartAlways(1, kitsune.FixedBackoff(0))),
		).Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 2 {
			t.Fatalf("expected 2 results (items 2 and 3), got %v", results)
		}
	})
}
