// Example: supervise — per-stage restart and panic recovery.
//
// Demonstrates: Supervise, RestartOnError, RestartOnPanic, RestartAlways,
// PanicSkip, SupervisionPolicy with Window, OnStageRestart hook.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
)

// restartHook logs supervision events in addition to the standard lifecycle events.
type restartHook struct {
	kitsune.Hook
}

func (h *restartHook) OnStageRestart(ctx context.Context, stage string, attempt int, cause error) {
	fmt.Printf("[restart] stage=%q attempt=%d cause=%v\n", stage, attempt, cause)
}

func main() {
	// --- Restart on transient error ---
	fmt.Println("=== RestartOnError ===")
	var errorCalls atomic.Int32
	p1 := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	results, err := kitsune.Map(p1, func(_ context.Context, v int) (int, error) {
		n := errorCalls.Add(1)
		if n <= 2 {
			return 0, fmt.Errorf("transient error on call %d", n)
		}
		return v * 10, nil
	},
		kitsune.WithName("error-stage"),
		kitsune.Supervise(kitsune.RestartOnError(5, kitsune.FixedBackoff(0))),
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("results after restart: %v\n\n", results)

	// --- Restart on panic ---
	fmt.Println("=== RestartOnPanic ===")
	var panicCalls atomic.Int32
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	hook := &restartHook{Hook: kitsune.LogHook(logger)}
	p2 := kitsune.FromSlice([]int{10, 20, 30})
	results2, err := kitsune.Map(p2, func(_ context.Context, v int) (int, error) {
		n := panicCalls.Add(1)
		if n == 1 {
			panic(fmt.Sprintf("boom on item %d", v))
		}
		return v * 2, nil
	},
		kitsune.WithName("panic-stage"),
		kitsune.Supervise(kitsune.RestartOnPanic(3, kitsune.FixedBackoff(0))),
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		panic(err)
	}
	fmt.Printf("results after panic restart: %v\n\n", results2)

	// --- PanicSkip: recover silently, no restart ---
	fmt.Println("=== PanicSkip ===")
	p3 := kitsune.FromSlice([]int{1})
	_, err = kitsune.Map(p3, func(_ context.Context, v int) (int, error) {
		panic("this panic is silently recovered")
	},
		kitsune.WithName("skip-panic-stage"),
		kitsune.Supervise(kitsune.SupervisionPolicy{OnPanic: kitsune.PanicSkip}),
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println("completed without error (panic was skipped)")
	fmt.Println()

	// --- RestartAlways with window-based counter reset ---
	fmt.Println("=== RestartAlways with Window ===")
	var windowCalls atomic.Int32
	p4 := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	results4, err := kitsune.Map(p4, func(_ context.Context, v int) (int, error) {
		n := windowCalls.Add(1)
		if n%3 == 1 && n > 1 {
			return 0, fmt.Errorf("periodic failure at call %d", n)
		}
		return v, nil
	},
		kitsune.WithName("window-stage"),
		kitsune.Supervise(kitsune.SupervisionPolicy{
			MaxRestarts: 2,
			Window:      100 * time.Millisecond, // counter resets after quiet period
			Backoff:     kitsune.FixedBackoff(0),
		}),
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("results with windowed restart budget: %v\n", results4)
}
