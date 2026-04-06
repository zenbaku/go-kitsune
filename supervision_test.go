package kitsune_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

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
	err := kitsune.FromSlice([]int{1, 2, 3, 4, 5}).ForEach(
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

	err := kitsune.FromSlice([]int{1, 2, 3}).ForEach(
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
	err := kitsune.FromSlice([]int{1, 2, 3, 4, 5}).ForEach(
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
// Map serial supervision (baseline — already wired, verifying test pattern)
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
