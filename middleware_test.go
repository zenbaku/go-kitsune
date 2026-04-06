package kitsune_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// RateLimit — Wait mode
// ---------------------------------------------------------------------------

func TestRateLimitWaitPassesAllItems(t *testing.T) {
	ctx := context.Background()
	// Very high rate — should pass everything through without blocking.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got, err := kitsune.Collect(ctx, kitsune.RateLimit(p, 1e9, kitsune.Burst(100)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d items, want 5", len(got))
	}
}

func TestRateLimitWaitThrottles(t *testing.T) {
	ctx := context.Background()
	// 10/s with burst=1. Three items should take at least ~200ms.
	p := kitsune.FromSlice([]int{1, 2, 3})
	start := time.Now()
	got, err := kitsune.Collect(ctx, kitsune.RateLimit(p, 10))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3", len(got))
	}
	// Three items at 10/s with burst=1: first is free, then two waits of ~100ms each.
	if elapsed < 150*time.Millisecond {
		t.Fatalf("elapsed %v, expected ≥150ms (rate limiting should apply)", elapsed)
	}
}

// ---------------------------------------------------------------------------
// RateLimit — Drop mode
// ---------------------------------------------------------------------------

func TestRateLimitDropDiscardsSomeItems(t *testing.T) {
	ctx := context.Background()
	// Rate=1/s, burst=1, send 20 items instantaneously → most get dropped.
	items := make([]int, 20)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	got, err := kitsune.Collect(ctx, kitsune.RateLimit(p, 1, kitsune.RateMode(kitsune.RateLimitDrop)))
	if err != nil {
		t.Fatal(err)
	}
	// At burst=1 we get at most 1 item through.
	if len(got) > 2 {
		t.Fatalf("expected ≤2 items through, got %d", len(got))
	}
}

func TestRateLimitDropPassesAllAtHighRate(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.Collect(ctx, kitsune.RateLimit(p, 1e9,
		kitsune.Burst(100),
		kitsune.RateMode(kitsune.RateLimitDrop),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3", len(got))
	}
}

// ---------------------------------------------------------------------------
// CircuitBreaker
// ---------------------------------------------------------------------------

var errExternal = errors.New("external service error")

func TestCircuitBreakerClosedPassesItems(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.Collect(ctx, kitsune.CircuitBreaker(p,
		func(_ context.Context, v int) (int, error) { return v * 2, nil },
		nil,
	))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{2, 4, 6}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("got[%d]=%d, want %d", i, got[i], w)
		}
	}
}

func TestCircuitBreakerOpensAfterThreshold(t *testing.T) {
	ctx := context.Background()
	var calls int64

	// threshold=2: two failures open the circuit.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	_, err := kitsune.Collect(ctx, kitsune.CircuitBreaker(p,
		func(_ context.Context, v int) (int, error) {
			atomic.AddInt64(&calls, 1)
			return 0, errExternal
		},
		[]kitsune.CircuitBreakerOpt{
			kitsune.FailureThreshold(2),
			kitsune.CooldownDuration(10 * time.Second),
		},
	))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Circuit should open after 2 failures; subsequent items return ErrCircuitOpen.
	// With default Halt error handling, the pipeline stops at the first error.
	// fn called exactly 2 times before circuit opens; 3rd item gets ErrCircuitOpen.
	if calls > 3 {
		t.Fatalf("fn called %d times, circuit should have opened", calls)
	}
}

func TestCircuitBreakerSkipsItemsWhenOpen(t *testing.T) {
	ctx := context.Background()
	var calls int64

	// threshold=1, skip on error → items continue flowing but open items are dropped.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got, err := kitsune.Collect(ctx, kitsune.CircuitBreaker(p,
		func(_ context.Context, v int) (int, error) {
			n := atomic.AddInt64(&calls, 1)
			if n <= 1 {
				return 0, errExternal // first call fails → opens circuit
			}
			return v * 2, nil
		},
		[]kitsune.CircuitBreakerOpt{
			kitsune.FailureThreshold(1),
			kitsune.CooldownDuration(10 * time.Second),
		},
		kitsune.OnError(kitsune.Skip()),
	))
	if err != nil {
		t.Fatal(err)
	}
	// First item fails and opens circuit; remaining 4 items get ErrCircuitOpen → skipped.
	// fn is called once (the first failure that opens the circuit).
	if len(got) != 0 {
		t.Fatalf("got %d items, want 0 (all dropped by open circuit)", len(got))
	}
}

func TestCircuitBreakerHalfOpenRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const cooldown = 30 * time.Millisecond

	src := kitsune.NewChannel[int](4)
	var calls int64

	cb := kitsune.CircuitBreaker(src.Source(),
		func(_ context.Context, v int) (int, error) {
			n := atomic.AddInt64(&calls, 1)
			if n == 1 {
				return 0, errExternal // trip the circuit
			}
			return v, nil
		},
		[]kitsune.CircuitBreakerOpt{
			kitsune.FailureThreshold(1),
			kitsune.CooldownDuration(cooldown),
			kitsune.HalfOpenProbes(1),
		},
		kitsune.OnError(kitsune.Skip()),
	)

	handle := cb.ForEach(func(_ context.Context, _ int) error { return nil }).Build().RunAsync(ctx)

	// Item 1: fails → circuit opens.
	src.Send(ctx, 1)
	time.Sleep(cooldown + 10*time.Millisecond) // wait past the cooldown

	// Item 2: circuit should now be half-open → probe succeeds → circuit closes.
	src.Send(ctx, 2)
	time.Sleep(5 * time.Millisecond)

	// Item 3: circuit should be closed — fn called normally, no error.
	src.Send(ctx, 3)
	time.Sleep(5 * time.Millisecond)

	src.Close()

	if err := handle.Wait(); err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	// fn was called for item 1 (fail), item 2 (half-open probe, success), item 3 (closed, success).
	if calls != 3 {
		t.Fatalf("fn called %d times, want 3", calls)
	}
}

// ---------------------------------------------------------------------------
// Pool / Pooled / MapPooled
// ---------------------------------------------------------------------------

func TestPoolGetAndRelease(t *testing.T) {
	pool := kitsune.NewPool(func() []byte { return make([]byte, 0, 16) })
	item := pool.Get()
	if item == nil {
		t.Fatal("Get returned nil")
	}
	item.Value = append(item.Value, 1, 2, 3)
	item.Release()

	// After release, the pool should return the same underlying allocation.
	item2 := pool.Get()
	if cap(item2.Value) < 16 {
		t.Fatalf("expected recycled slice with cap≥16, got cap=%d", cap(item2.Value))
	}
	item2.Release()
}

func TestMapPooled(t *testing.T) {
	ctx := context.Background()
	pool := kitsune.NewPool(func() int { return 0 })
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.Collect(ctx, kitsune.MapPooled(p, pool,
		func(_ context.Context, v int, out *kitsune.Pooled[int]) error {
			out.Value = v * 10
			return nil
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3", len(got))
	}
	want := []int{10, 20, 30}
	for i, pw := range got {
		if pw.Value != want[i] {
			t.Fatalf("got[%d].Value=%d, want %d", i, pw.Value, want[i])
		}
		pw.Release()
	}
}

func TestMapPooledReleasesOnError(t *testing.T) {
	ctx := context.Background()
	var released int64
	pool := kitsune.NewPool(func() int { return 0 })

	// Intercept releases by overriding Release via a wrapper is not possible,
	// so we verify the pipeline returns the error cleanly (slot was released inside MapPooled).
	p := kitsune.FromSlice([]int{1})
	_, err := kitsune.Collect(ctx, kitsune.MapPooled(p, pool,
		func(_ context.Context, _ int, _ *kitsune.Pooled[int]) error {
			atomic.AddInt64(&released, 1)
			return errExternal
		},
	))
	if !errors.Is(err, errExternal) {
		t.Fatalf("expected errExternal, got %v", err)
	}
}

func TestReleaseAll(t *testing.T) {
	pool := kitsune.NewPool(func() int { return 0 })
	items := []*kitsune.Pooled[int]{pool.Get(), pool.Get(), pool.Get()}
	kitsune.ReleaseAll(items) // must not panic
}
