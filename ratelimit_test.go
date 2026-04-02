package kitsune_test

import (
	"context"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func TestRateLimit_WaitMode_PassesAllItems(t *testing.T) {
	// High rate → all items pass through without dropping.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	limited := kitsune.RateLimit(p, 10_000)

	results, err := limited.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Errorf("got %d items, want 5", len(results))
	}
}

func TestRateLimit_WaitMode_PreservesOrder(t *testing.T) {
	p := kitsune.FromSlice([]int{10, 20, 30})
	limited := kitsune.RateLimit(p, 10_000)

	results, err := limited.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []int{10, 20, 30}
	for i, v := range results {
		if v != expected[i] {
			t.Errorf("results[%d] = %d, want %d", i, v, expected[i])
		}
	}
}

func TestRateLimit_WaitMode_EnforcesRate(t *testing.T) {
	// 5 items at 10 items/sec with burst=1 should take ~400ms.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	limited := kitsune.RateLimit(p, 10, kitsune.Burst(1))

	start := time.Now()
	results, err := limited.Collect(context.Background())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Errorf("got %d items, want 5", len(results))
	}
	// 5 items at 10/s = ~400ms minimum (4 waits of 100ms each).
	// Allow generous margin for CI.
	if elapsed < 300*time.Millisecond {
		t.Errorf("elapsed %v, expected >= 300ms for rate limiting to take effect", elapsed)
	}
}

func TestRateLimit_Burst(t *testing.T) {
	// burst=5 means the first 5 items pass immediately.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	limited := kitsune.RateLimit(p, 1, kitsune.Burst(5))

	start := time.Now()
	results, err := limited.Collect(context.Background())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Errorf("got %d items, want 5", len(results))
	}
	// With burst=5 the whole batch should pass immediately (no wait needed).
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed %v, expected burst of 5 to pass quickly", elapsed)
	}
}

func TestRateLimit_DropMode_DropsExcess(t *testing.T) {
	// Very slow rate, no burst — most items should be dropped.
	p := kitsune.FromSlice(make([]int, 100))
	limited := kitsune.RateLimit(p, 1,
		kitsune.Burst(1),
		kitsune.RateMode(kitsune.RateLimitDrop),
	)

	results, err := limited.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// At most 1 item should pass (burst=1, rate=1/sec, pipeline runs instantly).
	if len(results) > 5 {
		t.Errorf("got %d items, expected most to be dropped (<=5)", len(results))
	}
}

func TestRateLimit_DropMode_PassesAll_HighRate(t *testing.T) {
	// High rate+burst → nothing should be dropped.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	limited := kitsune.RateLimit(p, 100_000,
		kitsune.Burst(100),
		kitsune.RateMode(kitsune.RateLimitDrop),
	)

	results, err := limited.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Errorf("got %d items, want 5", len(results))
	}
}

func TestRateLimit_WaitMode_ContextCancellation(t *testing.T) {
	// Very slow rate; cancel context before all items pass.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	limited := kitsune.RateLimit(p, 1, kitsune.Burst(1))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := limited.Collect(ctx)
	if err == nil {
		t.Error("expected context cancellation error")
	}
}

func TestRateLimit_WithName(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	limited := kitsune.RateLimit(p, 10_000, kitsune.WithName("throttle"))

	m := kitsune.NewMetricsHook()
	results, err := limited.Collect(context.Background(), kitsune.WithHook(m))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Errorf("got %d items, want 3", len(results))
	}
	s := m.Stage("throttle")
	if s.Processed != 3 {
		t.Errorf("stage 'throttle' Processed = %d, want 3", s.Processed)
	}
}
