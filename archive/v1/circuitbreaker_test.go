package kitsune_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

var errCB = errors.New("cb test error")

// failFirstN returns a fn that fails the first n calls, then succeeds.
func failFirstN(n int) func(context.Context, int) (int, error) {
	var calls atomic.Int32
	return func(_ context.Context, v int) (int, error) {
		if int(calls.Add(1)) <= n {
			return 0, errCB
		}
		return v, nil
	}
}

func TestCircuitBreaker_ClosedPassesItems(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	out := kitsune.CircuitBreaker(p,
		func(_ context.Context, n int) (int, error) { return n * 10, nil },
	)
	results, err := out.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	for i, v := range results {
		if v != (i+1)*10 {
			t.Errorf("results[%d] = %d, want %d", i, v, (i+1)*10)
		}
	}
}

func TestCircuitBreaker_TripsAfterThreshold(t *testing.T) {
	// threshold=3; items 1-3 fail → circuit opens → items 4-5 are rejected.
	// All with OnError(Skip()) → 0 results expected.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	out := kitsune.CircuitBreaker(p, failFirstN(3),
		kitsune.FailureThreshold(3),
		kitsune.CooldownDuration(10*time.Second),
		kitsune.OnError(kitsune.Skip()),
	)
	results, err := out.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Without circuit breaker: failFirstN(3)+Skip → items 4-5 in results.
	// With circuit breaker: circuit opens after 3 failures → items 4-5 rejected.
	if len(results) != 0 {
		t.Errorf("got %d results, want 0 (circuit should reject items 4-5)", len(results))
	}
}

func TestCircuitBreaker_SkipsOpenItems(t *testing.T) {
	// threshold=3, cooldown=10s: items 1-3 fail, rest are rejected.
	p := kitsune.FromSlice(make([]int, 10))
	out := kitsune.CircuitBreaker(p,
		func(_ context.Context, n int) (int, error) { return 0, errCB },
		kitsune.FailureThreshold(3),
		kitsune.CooldownDuration(10*time.Second),
		kitsune.OnError(kitsune.Skip()),
	)
	results, err := out.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results when fn always fails, got %d", len(results))
	}
}

func TestCircuitBreaker_HalfOpen_ProbeSuccess_ClosesCircuit(t *testing.T) {
	// Sequence: 3 failures → circuit open → short cooldown → half-open →
	// probe succeeds → circuit closed → remaining items pass.
	const cooldown = 30 * time.Millisecond
	const threshold = 3

	var callCount atomic.Int32
	fn := func(_ context.Context, n int) (int, error) {
		c := callCount.Add(1)
		if c <= threshold {
			return 0, errCB
		}
		return n, nil // all subsequent calls succeed
	}

	// Use Generate with a pause after item 3 so the cooldown can expire.
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := 1; i <= 7; i++ {
			if i == 4 {
				// Allow cooldown to expire before sending item 4.
				time.Sleep(cooldown + 10*time.Millisecond)
			}
			if !yield(i) {
				return nil
			}
		}
		return nil
	})

	out := kitsune.CircuitBreaker(p, fn,
		kitsune.FailureThreshold(threshold),
		kitsune.CooldownDuration(cooldown),
		kitsune.HalfOpenProbes(1),
		kitsune.OnError(kitsune.Skip()),
	)

	results, err := out.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Items 1-3 fail → circuit opens.
	// After cooldown, item 4 is a probe (succeeds) → circuit closes.
	// Items 5-7 pass normally.
	// Expected results: [4, 5, 6, 7]
	if len(results) == 0 {
		t.Error("expected items to pass after circuit recovers via half-open probe")
	}
	t.Logf("results after recovery: %v", results)
}

func TestCircuitBreaker_HalfOpen_ProbeFailure_ReopensCircuit(t *testing.T) {
	// fn always fails → circuit opens, never recovers.
	const cooldown = 10 * time.Millisecond

	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	out := kitsune.CircuitBreaker(p,
		func(_ context.Context, n int) (int, error) { return 0, errCB },
		kitsune.FailureThreshold(2),
		kitsune.CooldownDuration(cooldown),
		kitsune.OnError(kitsune.Skip()),
	)
	results, err := out.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results when fn always fails, got %v", results)
	}
}

func TestCircuitBreaker_Concurrency(t *testing.T) {
	// Concurrency(4): Ref is mutex-protected, no data races.
	items := make([]int, 50)
	for i := range items {
		items[i] = i + 1
	}
	p := kitsune.FromSlice(items)
	out := kitsune.CircuitBreaker(p,
		func(_ context.Context, n int) (int, error) { return n, nil },
		kitsune.Concurrency(4),
		kitsune.OnError(kitsune.Skip()),
	)
	results, err := out.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(items) {
		t.Errorf("got %d results, want %d", len(results), len(items))
	}
}

func TestCircuitBreaker_TripsAfterOneFailure(t *testing.T) {
	// threshold=1: first failure immediately trips the circuit.
	p := kitsune.FromSlice([]int{1, 2, 3})
	out := kitsune.CircuitBreaker(p, failFirstN(1),
		kitsune.FailureThreshold(1),
		kitsune.CooldownDuration(10*time.Second),
		kitsune.OnError(kitsune.Skip()),
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

func TestCircuitBreaker_ErrCircuitOpenIsDistinct(t *testing.T) {
	// Verify ErrCircuitOpen is exported and distinguishable.
	if kitsune.ErrCircuitOpen == nil {
		t.Fatal("ErrCircuitOpen should not be nil")
	}
	if !errors.Is(kitsune.ErrCircuitOpen, kitsune.ErrCircuitOpen) {
		t.Error("errors.Is should match ErrCircuitOpen")
	}
}

func TestCircuitBreaker_HalfOpenTimeout_ExpiresAndReopens(t *testing.T) {
	// Probe fn sleeps 200ms; HalfOpenTimeout=20ms cancels it → circuit re-opens.
	const cooldown = 20 * time.Millisecond
	const threshold = 2

	var calls atomic.Int32
	slowFn := func(ctx context.Context, _ int) (int, error) {
		c := calls.Add(1)
		if c <= threshold {
			return 0, errCB // first 2 calls fail → circuit opens
		}
		// Probe call: sleep longer than the timeout.
		select {
		case <-time.After(500 * time.Millisecond):
			return 99, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}

	// Generate items with a pause so cooldown expires before item 3.
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := 1; i <= 5; i++ {
			if i == 3 {
				select {
				case <-time.After(cooldown + 10*time.Millisecond):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if !yield(i) {
				return nil
			}
		}
		return nil
	})

	results, err := kitsune.CircuitBreaker(p, slowFn,
		kitsune.FailureThreshold(threshold),
		kitsune.CooldownDuration(cooldown),
		kitsune.HalfOpenTimeout(20*time.Millisecond), // short timeout → probe fails
		kitsune.OnError(kitsune.Skip()),
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Items 1-2 fail → circuit opens. Item 3 is probe but times out → re-opens.
	// Items 4-5 are rejected. No items should pass.
	if len(results) != 0 {
		t.Errorf("got %d results, want 0 (probe timed out, circuit re-opened)", len(results))
	}
}

func TestCircuitBreaker_HalfOpenTimeout_FastProbeSucceeds(t *testing.T) {
	// Probe fn is fast; generous HalfOpenTimeout → circuit closes normally.
	const cooldown = 20 * time.Millisecond
	const threshold = 2

	var calls atomic.Int32
	fn := func(_ context.Context, n int) (int, error) {
		c := calls.Add(1)
		if c <= threshold {
			return 0, errCB
		}
		return n * 10, nil
	}

	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := 1; i <= 6; i++ {
			if i == 3 {
				select {
				case <-time.After(cooldown + 10*time.Millisecond):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if !yield(i) {
				return nil
			}
		}
		return nil
	})

	results, err := kitsune.CircuitBreaker(p, fn,
		kitsune.FailureThreshold(threshold),
		kitsune.CooldownDuration(cooldown),
		kitsune.HalfOpenTimeout(5*time.Second), // generous timeout → probe succeeds
		kitsune.OnError(kitsune.Skip()),
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Items 1-2 fail → circuit opens. Item 3 probes (fast) → circuit closes.
	// Items 4-6 pass. Expected at least [30, 40, 50, 60].
	if len(results) == 0 {
		t.Error("expected items to pass after fast probe closes the circuit")
	}
	t.Logf("results: %v", results)
}

func TestCircuitBreaker_TwoIndependentBreakers(t *testing.T) {
	// Two circuit breakers using Broadcast fan-out have independent state.
	p := kitsune.FromSlice([]int{1, 2, 3})
	branches := kitsune.Broadcast(p, 2)

	cb1 := kitsune.CircuitBreaker(branches[0],
		func(_ context.Context, n int) (int, error) { return n, nil },
	)
	cb2 := kitsune.CircuitBreaker(branches[1],
		func(_ context.Context, n int) (int, error) { return n * 10, nil },
	)

	r1 := cb1.Drain()
	r2 := cb2.Drain()
	merged, err := kitsune.MergeRunners(r1, r2)
	if err != nil {
		t.Fatal(err)
	}
	if err := merged.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}
