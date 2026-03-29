// Example: circuitbreaker — fault tolerance via the circuit breaker pattern.
//
// Demonstrates: CircuitBreaker, FailureThreshold, CooldownDuration,
// HalfOpenProbes, ErrCircuitOpen, OnError(Skip()).
//
// The circuit breaker protects a downstream service (or slow operation) from
// being overwhelmed when it is already failing. When too many consecutive
// errors occur the circuit "opens" and all items are rejected immediately
// (fast-fail) until a cooldown period expires. After cooldown a single probe
// item tests recovery; success closes the circuit, failure re-opens it.
package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
)

func main() {
	// --- Closed circuit: normal operation ---
	//
	// When the downstream function succeeds the circuit stays closed and all
	// items flow through unchanged.
	fmt.Println("=== Circuit closed: all items pass ===")

	results, err := kitsune.CircuitBreaker(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		func(_ context.Context, n int) (int, error) { return n * 10, nil },
		nil, // defaults: threshold=5, cooldown=30s, probes=1
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println("Results:", results)

	// --- Circuit trips after repeated failures ---
	//
	// With threshold=3: the first 3 failures open the circuit. Items 4-10
	// arrive while the circuit is open and are rejected with ErrCircuitOpen.
	// Using OnError(Skip()) turns both failures and rejections into silent
	// drops, so the pipeline completes without error.
	fmt.Println("\n=== Circuit opens after 3 failures (threshold=3) ===")

	var calls atomic.Int32
	failFirst3 := func(_ context.Context, n int) (int, error) {
		if int(calls.Add(1)) <= 3 {
			return 0, errors.New("service unavailable")
		}
		return n, nil
	}

	m := kitsune.NewMetricsHook()
	tripped, err := kitsune.CircuitBreaker(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}),
		failFirst3,
		[]kitsune.CircuitBreakerOption{
			kitsune.FailureThreshold(3),
			kitsune.CooldownDuration(10 * time.Second), // long cooldown so circuit stays open
		},
		kitsune.OnError(kitsune.Skip()),
		kitsune.WithName("cb-trips"),
	).Collect(context.Background(), kitsune.WithHook(m))
	if err != nil {
		panic(err)
	}
	s := m.Stage("cb-trips")
	fmt.Printf("Items attempted: 10, passed: %d, skipped: %d (3 failures + 7 rejected)\n",
		len(tripped), s.Skipped)

	// --- Half-open recovery ---
	//
	// After the cooldown the circuit enters half-open state and allows one
	// probe item. If the probe succeeds the circuit closes and normal
	// processing resumes. Use Generate with a deliberate pause so the
	// cooldown expires before items 4+ arrive.
	fmt.Println("\n=== Half-open recovery: circuit closes after successful probe ===")

	const cooldown = 60 * time.Millisecond
	var callIdx atomic.Int32
	recoverFn := func(_ context.Context, n int) (int, error) {
		i := int(callIdx.Add(1))
		if i <= 3 {
			return 0, errors.New("service unavailable") // first 3 calls fail
		}
		return n, nil // subsequent calls succeed
	}

	recovering := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := 1; i <= 7; i++ {
			if i == 4 {
				// Pause so the cooldown expires; circuit transitions to half-open.
				select {
				case <-time.After(cooldown + 20*time.Millisecond):
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

	recovered, err := kitsune.CircuitBreaker(recovering, recoverFn,
		[]kitsune.CircuitBreakerOption{
			kitsune.FailureThreshold(3),
			kitsune.CooldownDuration(cooldown),
			kitsune.HalfOpenProbes(1),
		},
		kitsune.OnError(kitsune.Skip()),
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}

	// Items 1-3 fail (circuit opens). Item 4 is a probe after cooldown —
	// it succeeds, closing the circuit. Items 5-7 pass normally.
	fmt.Printf("After recovery, received %d items: %v\n", len(recovered), recovered)
	if len(recovered) > 0 {
		fmt.Println("Circuit closed and processing resumed ✓")
	}
}
