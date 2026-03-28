// Example: timebased — rate-control a high-frequency event stream.
//
// Demonstrates: Throttle, Debounce, Generate, Map, Filter, Tap.
package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
)

func main() {
	// --- Throttle: cap a sensor stream to one reading per 100ms ---
	//
	// A temperature sensor publishes 10 readings in quick succession.
	// Throttle keeps the first reading per 100ms window; the rest are dropped.
	// Useful for rate-limiting API calls, coalescing UI refresh events, etc.
	fmt.Println("=== Throttle: sensor stream capped to 1 per 100ms ===")
	var sentCount, receivedCount atomic.Int64

	sensor := kitsune.Generate(func(ctx context.Context, yield func(float64) bool) error {
		readings := []float64{98.6, 98.7, 98.8, 98.6, 98.5, 99.0, 98.9, 98.7, 98.6, 98.5}
		for _, r := range readings {
			sentCount.Add(1)
			if !yield(r) {
				return nil
			}
		}
		return nil
	})

	throttled := kitsune.Throttle(sensor, 100*time.Millisecond)
	results, err := kitsune.Map(
		throttled.Tap(func(_ float64) { receivedCount.Add(1) }),
		func(_ context.Context, r float64) (string, error) {
			return fmt.Sprintf("%.1f°F", r), nil
		},
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("Sent %d readings, received %d after throttle\n",
		sentCount.Load(), receivedCount.Load())
	fmt.Println("Readings forwarded:", results)

	// --- Debounce: wait for search input to settle before querying ---
	//
	// A user types "g", "go", "gol", "gola", "golang" in quick succession.
	// Debounce waits 60ms of silence before forwarding — only the final
	// "golang" query reaches the search function.
	fmt.Println("\n=== Debounce: coalesce rapid keystrokes into one query ===")
	keystrokes := []string{"g", "go", "gol", "gola", "golang"}
	// Deliver keystrokes immediately (burst), then the debounce window fires.
	queries, err := kitsune.Map(
		kitsune.Debounce(kitsune.FromSlice(keystrokes), 40*time.Millisecond),
		func(_ context.Context, q string) (string, error) {
			// Simulate a search — only called for the debounced item.
			return fmt.Sprintf("search(%q) → results", q), nil
		},
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("Keystrokes: %v\n", keystrokes)
	fmt.Printf("Queries executed: %d\n", len(queries)) // 1
	for _, q := range queries {
		fmt.Println(" ", q)
	}

	// --- Both together: Debounce then Throttle ---
	//
	// Use Debounce to coalesce config-change events, then Throttle to ensure
	// the downstream reload handler is called at most once every 200ms.
	fmt.Println("\n=== Debounce → Throttle: config reload pipeline ===")
	configEvents := kitsune.Generate(func(ctx context.Context, yield func(string) bool) error {
		// Burst 1: three rapid changes → debounce to one
		for _, e := range []string{"change-1", "change-2", "change-3"} {
			if !yield(e) {
				return nil
			}
		}
		// Wait long enough for debounce to fire (50ms quiet period).
		select {
		case <-time.After(150 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		// Burst 2: two rapid changes → debounce to one
		for _, e := range []string{"change-4", "change-5"} {
			if !yield(e) {
				return nil
			}
		}
		return nil
	})

	debounced := kitsune.Debounce(configEvents, 50*time.Millisecond)
	throttled2 := kitsune.Throttle(debounced, 200*time.Millisecond)
	reloads, err := kitsune.Map(throttled2, func(_ context.Context, event string) (string, error) {
		return fmt.Sprintf("reload triggered by %q", event), nil
	}).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("Config events: 5 total, reloads triggered: %d\n", len(reloads))
	for _, r := range reloads {
		fmt.Println(" ", r)
	}
}
