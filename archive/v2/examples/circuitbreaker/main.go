// Example: circuitbreaker — protect a flaky dependency with a circuit breaker.
//
// Demonstrates: CircuitBreaker, FailureThreshold, CooldownDuration, HalfOpenProbes,
// ErrCircuitOpen, OnError(Skip())
package main

import (
	"context"
	"errors"
	"fmt"

	kitsune "github.com/zenbaku/go-kitsune/v2"
)

func main() {
	ctx := context.Background()

	// A simulated backend that fails on items 3-7.
	callCount := 0
	backend := func(_ context.Context, n int) (string, error) {
		callCount++
		if n >= 3 && n <= 7 {
			return "", fmt.Errorf("backend error on item %d", n)
		}
		return fmt.Sprintf("ok-%d", n), nil
	}

	// After 3 consecutive failures the circuit opens and subsequent items
	// receive ErrCircuitOpen immediately (backend is never called).
	// OnError(Skip()) silently drops failed items rather than halting the pipeline.
	items := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	out := kitsune.CircuitBreaker(items, backend,
		[]kitsune.CircuitBreakerOpt{
			kitsune.FailureThreshold(3),
			kitsune.CooldownDuration(0), // instant cooldown for the demo
			kitsune.HalfOpenProbes(1),
		},
		kitsune.OnError(kitsune.Skip()),
		kitsune.WithName("backend"),
	)

	results, err := kitsune.Collect(ctx, out)
	if err != nil {
		panic(err)
	}

	fmt.Println("results:", results)
	fmt.Printf("backend called %d times (circuit blocked some calls)\n", callCount)

	// --- Verify ErrCircuitOpen is detectable via the returned error ---

	errBackend := func(_ context.Context, n int) (string, error) {
		return "", errors.New("always fails")
	}
	err = kitsune.CircuitBreaker(kitsune.FromSlice([]int{1, 2, 3}), errBackend,
		[]kitsune.CircuitBreakerOpt{kitsune.FailureThreshold(2)},
	).Drain().Run(ctx)

	if errors.Is(err, kitsune.ErrCircuitOpen) {
		fmt.Println("pipeline stopped with ErrCircuitOpen")
	} else if err != nil {
		fmt.Println("pipeline stopped with:", err)
	}
}
