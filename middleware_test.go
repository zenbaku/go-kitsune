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
// RateLimit: Wait mode
// ---------------------------------------------------------------------------

func TestRateLimitWaitPassesAllItems(t *testing.T) {
	ctx := context.Background()
	// Very high rate; should pass everything through without blocking.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got, err := kitsune.Collect(ctx, kitsune.RateLimit(p, 1e9, []kitsune.RateLimitOpt{kitsune.Burst(100)}))
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
	got, err := kitsune.Collect(ctx, kitsune.RateLimit(p, 10, nil))
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

func TestRateLimitWaitPreservesOrder(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{10, 20, 30})
	got, err := kitsune.Collect(ctx, kitsune.RateLimit(p, 1e9, nil))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{10, 20, 30}
	for i, v := range got {
		if v != want[i] {
			t.Errorf("got[%d] = %d, want %d", i, v, want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// RateLimit: Drop mode
// ---------------------------------------------------------------------------

func TestRateLimitDropDiscardsSomeItems(t *testing.T) {
	ctx := context.Background()
	// Rate=1/s, burst=1, send 20 items instantaneously → most get dropped.
	items := make([]int, 20)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	got, err := kitsune.Collect(ctx, kitsune.RateLimit(p, 1, []kitsune.RateLimitOpt{kitsune.RateMode(kitsune.RateLimitDrop)}))
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
		[]kitsune.RateLimitOpt{kitsune.Burst(100), kitsune.RateMode(kitsune.RateLimitDrop)},
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
		kitsune.OnError(kitsune.ActionDrop()),
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
		kitsune.OnError(kitsune.ActionDrop()),
	)

	handle := cb.ForEach(func(_ context.Context, _ int) error { return nil }).Build().RunAsync(ctx)

	// Item 1: fails → circuit opens.
	src.Send(ctx, 1)
	time.Sleep(cooldown + 10*time.Millisecond) // wait past the cooldown

	// Item 2: circuit should now be half-open → probe succeeds → circuit closes.
	src.Send(ctx, 2)
	time.Sleep(5 * time.Millisecond)

	// Item 3: circuit should be closed; fn called normally, no error.
	src.Send(ctx, 3)
	time.Sleep(5 * time.Millisecond)

	src.Close()

	if _, err := handle.Wait(); err != nil {
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

// ---------------------------------------------------------------------------
// CircuitBreaker: additional coverage
// ---------------------------------------------------------------------------

func TestCircuitBreakerProbeFailureReopens(t *testing.T) {
	// fn always fails → circuit opens, never recovers.
	const cooldown = 10 * time.Millisecond

	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	out := kitsune.CircuitBreaker(p,
		func(_ context.Context, _ int) (int, error) { return 0, errExternal },
		[]kitsune.CircuitBreakerOpt{
			kitsune.FailureThreshold(2),
			kitsune.CooldownDuration(cooldown),
		},
		kitsune.OnError(kitsune.ActionDrop()),
	)
	results, err := out.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results when fn always fails, got %v", results)
	}
}

func TestCircuitBreakerConcurrency(t *testing.T) {
	// Concurrency(4): state ref is mutex-protected, no data races.
	items := make([]int, 50)
	for i := range items {
		items[i] = i + 1
	}
	out := kitsune.CircuitBreaker(kitsune.FromSlice(items),
		func(_ context.Context, n int) (int, error) { return n, nil },
		[]kitsune.CircuitBreakerOpt{kitsune.FailureThreshold(100)},
		kitsune.Concurrency(4),
	)
	results, err := out.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(items) {
		t.Errorf("got %d results, want %d", len(results), len(items))
	}
}

func TestCircuitBreakerTripsAfterOne(t *testing.T) {
	// threshold=1: first failure immediately trips the circuit.
	p := kitsune.FromSlice([]int{1, 2, 3})
	out := kitsune.CircuitBreaker(p, func() func(context.Context, int) (int, error) {
		var calls int32
		return func(_ context.Context, v int) (int, error) {
			calls++
			if calls <= 1 {
				return 0, errExternal
			}
			return v, nil
		}
	}(),
		[]kitsune.CircuitBreakerOpt{
			kitsune.FailureThreshold(1),
			kitsune.CooldownDuration(10 * time.Second),
		},
		kitsune.OnError(kitsune.ActionDrop()),
	)
	results, err := out.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Item 1 fails → circuit opens → items 2-3 rejected → 0 results.
	if len(results) != 0 {
		t.Errorf("got %d results, want 0 (threshold=1 should open circuit immediately)", len(results))
	}
}

func TestCircuitBreakerErrIsDistinct(t *testing.T) {
	// Verify ErrCircuitOpen is exported and distinguishable.
	if kitsune.ErrCircuitOpen == nil {
		t.Fatal("ErrCircuitOpen should not be nil")
	}
	if !errors.Is(kitsune.ErrCircuitOpen, kitsune.ErrCircuitOpen) {
		t.Error("errors.Is should match ErrCircuitOpen")
	}
}

func TestCircuitBreakerHalfOpenTimeoutExpires(t *testing.T) {
	// CB trips after 1 failure, enters half-open after 20ms cooldown.
	// HalfOpenTimeout is 30ms but HalfOpenProbes is 10, so a single probe
	// cannot close the circuit before the timeout expires. The next item
	// after the timeout should be rejected (ErrCircuitOpen, skipped).
	ctx := context.Background()
	const cooldown = 20 * time.Millisecond
	const hoTimeout = 30 * time.Millisecond

	src := kitsune.NewChannel[int](4)
	var calls atomic.Int32
	fn := func(_ context.Context, n int) (int, error) {
		if calls.Add(1) == 1 {
			return 0, errors.New("boom") // trip
		}
		return n, nil
	}
	out := kitsune.CircuitBreaker(src.Source(), fn,
		[]kitsune.CircuitBreakerOpt{
			kitsune.FailureThreshold(1),
			kitsune.CooldownDuration(cooldown),
			kitsune.HalfOpenProbes(10), // need 10 to close; won't happen
			kitsune.HalfOpenTimeout(hoTimeout),
		},
		kitsune.OnError(kitsune.ActionDrop()),
	)

	var got atomic.Int32
	handle := out.ForEach(func(_ context.Context, _ int) error {
		got.Add(1)
		return nil
	}).Build().RunAsync(ctx)

	// Item 1: trips the breaker.
	src.Send(ctx, 1)
	time.Sleep(cooldown + 5*time.Millisecond) // cooldown expires → half-open

	// Item 2: enters half-open but does not close the circuit (need 10).
	src.Send(ctx, 2)
	time.Sleep(5 * time.Millisecond)
	snapshot := got.Load()

	time.Sleep(hoTimeout + 5*time.Millisecond) // half-open timeout expires → reopens

	// Item 3: circuit should be open again; skipped.
	src.Send(ctx, 3)
	time.Sleep(5 * time.Millisecond)

	src.Close()
	if _, err := handle.Wait(); err != nil {
		t.Fatalf("pipeline error: %v", err)
	}

	// Item 2 produced a result; item 3 should have been skipped.
	if snapshot == 0 {
		t.Fatal("expected item 2 to pass through the half-open probe")
	}
	if got.Load() != snapshot {
		t.Fatalf("expected item 3 to be skipped after timeout; total results=%d", got.Load())
	}
}

func TestCircuitBreakerHalfOpenTimeoutFast(t *testing.T) {
	// CB trips after 1 failure, enters half-open after 10ms cooldown.
	// HalfOpenTimeout is generous (5s), and only 1 probe is needed, so
	// the circuit closes well before the timeout.
	ctx := context.Background()
	const cooldown = 10 * time.Millisecond

	src := kitsune.NewChannel[int](4)
	var calls atomic.Int32
	fn := func(_ context.Context, n int) (int, error) {
		if calls.Add(1) == 1 {
			return 0, errors.New("boom") // trip once
		}
		return n, nil
	}
	out := kitsune.CircuitBreaker(src.Source(), fn,
		[]kitsune.CircuitBreakerOpt{
			kitsune.FailureThreshold(1),
			kitsune.CooldownDuration(cooldown),
			kitsune.HalfOpenProbes(1),
			kitsune.HalfOpenTimeout(5 * time.Second),
		},
		kitsune.OnError(kitsune.ActionDrop()),
	)

	var got atomic.Int32
	handle := out.ForEach(func(_ context.Context, _ int) error {
		got.Add(1)
		return nil
	}).Build().RunAsync(ctx)

	src.Send(ctx, 1)                          // trips breaker → skipped
	time.Sleep(cooldown + 5*time.Millisecond) // cooldown → half-open
	src.Send(ctx, 2)                          // probe succeeds → circuit closes
	time.Sleep(5 * time.Millisecond)
	src.Send(ctx, 3) // normal operation
	src.Send(ctx, 4)
	src.Close()

	if _, err := handle.Wait(); err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	// Items 2, 3, 4 should have passed through (item 1 skipped).
	if got.Load() != 3 {
		t.Fatalf("expected 3 results (items 2-4); got %d", got.Load())
	}
}

func TestCircuitBreakerTwoIndependentBreakers(t *testing.T) {
	// Two circuit breakers on separate Broadcast branches have independent state.
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3})
	branches := kitsune.Broadcast(p, 2)

	cb1 := kitsune.CircuitBreaker(branches[0],
		func(_ context.Context, n int) (int, error) { return n, nil },
		nil,
	)
	cb2 := kitsune.CircuitBreaker(branches[1],
		func(_ context.Context, n int) (int, error) { return n * 10, nil },
		nil,
	)

	r1 := cb1.Drain().Build()
	r2 := cb2.Drain().Build()
	merged, err := kitsune.MergeRunners(r1, r2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := merged.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// RateLimit: additional coverage
// ---------------------------------------------------------------------------

func TestRateLimitBurst(t *testing.T) {
	// burst=5 means the first 5 items pass immediately.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	limited := kitsune.RateLimit(p, 1, []kitsune.RateLimitOpt{kitsune.Burst(5)})

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

func TestRateLimitContextCancellation(t *testing.T) {
	// Very slow rate; cancel context before all items pass.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	limited := kitsune.RateLimit(p, 1, []kitsune.RateLimitOpt{kitsune.Burst(1)})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := limited.Collect(ctx)
	if err == nil {
		t.Error("expected context cancellation error")
	}
}

func TestRateLimitWithName(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3})
	hook := &testkit.RecordingHook{}
	limited := kitsune.RateLimit(p, 1e9, nil, kitsune.WithName("my_limiter"))
	_, err := kitsune.Collect(ctx, limited, kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool)
	for _, n := range hook.Graph() {
		names[n.Name] = true
	}
	if !names["my_limiter"] {
		t.Fatalf("expected stage named 'my_limiter' in graph; got %v", hook.Graph())
	}
}

// ---------------------------------------------------------------------------
// Pool: additional coverage
// ---------------------------------------------------------------------------

func TestPooledDoubleReleasePanics(t *testing.T) {
	// Calling Release() twice on a Pooled item must panic (use-after-release guard).
	pool := kitsune.NewPool(func() int { return 0 })
	input := kitsune.FromSlice([]int{42})
	out := kitsune.MapPooled(input, pool, func(_ context.Context, n int, w *kitsune.Pooled[int]) error {
		w.Value = n
		return nil
	})
	results, err := out.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	results[0].Release()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on double Release, got none")
		}
	}()
	results[0].Release() // second release: must panic
}

func TestMapPooledConcurrency(t *testing.T) {
	// Pool is safe for concurrent use.
	type obj struct{ Value int }
	pool := kitsune.NewPool(func() obj { return obj{} })

	items := make([]int, 100)
	for i := range items {
		items[i] = i + 1
	}
	out := kitsune.MapPooled(kitsune.FromSlice(items), pool,
		func(_ context.Context, n int, w *kitsune.Pooled[obj]) error {
			w.Value.Value = n
			return nil
		},
		kitsune.Concurrency(8),
	)
	results, err := out.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(items) {
		t.Fatalf("got %d results, want %d", len(results), len(items))
	}
	kitsune.ReleaseAll(results)
}

func TestPoolPutAllowsReuse(t *testing.T) {
	pool := kitsune.NewPool(func() []byte { return make([]byte, 0, 64) })
	first := pool.Get()
	ptr := first
	pool.Put(first)
	second := pool.Get()
	if second != ptr {
		t.Fatal("expected same *Pooled pointer after Put; pool did not reuse the object")
	}
}

func TestPoolWarmupPreAllocates(t *testing.T) {
	var count atomic.Int32
	pool := kitsune.NewPool(func() int {
		count.Add(1)
		return 0
	})

	pool.Warmup(5)
	if got := count.Load(); got != 5 {
		t.Fatalf("Warmup(5) called factory %d times, want 5", got)
	}
	// Getting 5 items should not call the factory again.
	items := make([]*kitsune.Pooled[int], 5)
	for i := range items {
		items[i] = pool.Get()
	}
	if got := count.Load(); got != 5 {
		t.Fatalf("Get after Warmup called factory; total=%d, want 5", got)
	}
	kitsune.ReleaseAll(items)
}

func TestPoolWarmupZeroNoop(t *testing.T) {
	pool := kitsune.NewPool(func() int { return 0 })
	pool.Warmup(0)
	pool.Warmup(-3)
	// No panic is the assertion.
}

func TestPooledMustValuePanicsAfterRelease(t *testing.T) {
	pool := kitsune.NewPool(func() int { return 42 })
	item := pool.Get()
	if got := item.MustValue(); got != 42 {
		t.Fatalf("MustValue before release = %d, want 42", got)
	}
	item.Release()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic from MustValue after Release, got none")
		}
	}()
	_ = item.MustValue()
}

func TestPooledReuseClearsReleasedFlag(t *testing.T) {
	// After Release + Get, the same object must be usable again without panic.
	pool := kitsune.NewPool(func() int { return 7 })
	first := pool.Get()
	ptr := first
	first.Release()
	second := pool.Get()
	if second != ptr {
		t.Skip("pool returned a different object; LIFO reuse assumption not met")
	}
	// MustValue must not panic on the reused item.
	_ = second.MustValue()
	second.Release()
}
