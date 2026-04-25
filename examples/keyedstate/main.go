// Example: keyedstate: per-entity stateful processing with key-sharded concurrency.
//
// Demonstrates MapWithKey with and without concurrency. In serial mode, a single
// goroutine handles all entity keys in one map. In concurrent mode, the key space
// is sharded across n workers using a stable hash: hash(userID) % n. Items for
// the same user always land on the same worker, so per-user state is never
// accessed by more than one goroutine; lock-free by design.
//
// This is the in-process actor model: each worker acts as a lightweight actor
// that owns a disjoint partition of the state space.
package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// Event represents a user payment event.
type Event struct {
	UserID string
	Amount int
}

func main() {
	ctx := context.Background()

	// Simulate a stream of payment events across four users.
	events := []Event{
		{"alice", 100}, {"bob", 200}, {"carol", 50},
		{"dave", 300}, {"alice", 150}, {"bob", 100},
		{"carol", 200}, {"alice", 75}, {"dave", 50},
		{"bob", 300}, {"carol", 125}, {"dave", 200},
		{"alice", 50}, {"bob", 75}, {"carol", 300},
		{"dave", 100}, {"alice", 200}, {"bob", 50},
	}

	// State key: running total per user.
	totalKey := kitsune.NewKey[int]("user_total", 0)
	keyFn := func(e Event) string { return e.UserID }

	// -------------------------------------------------------------------------
	// Serial: one goroutine, all keys in a shared map.
	// -------------------------------------------------------------------------

	fmt.Println("=== Serial MapWithKey ===")
	start := time.Now()

	serialResults, err := kitsune.Collect(ctx, kitsune.MapWithKey(
		kitsune.FromSlice(events),
		keyFn,
		totalKey,
		func(ctx context.Context, ref *kitsune.Ref[int], e Event) (string, error) {
			total, _ := ref.UpdateAndGet(ctx, func(t int) (int, error) {
				return t + e.Amount, nil
			})
			return fmt.Sprintf("%-8s total=%d", e.UserID, total), nil
		},
	))
	if err != nil {
		panic(err)
	}
	fmt.Printf("  %d events in %v\n", len(serialResults), time.Since(start).Round(time.Microsecond))
	for _, r := range serialResults {
		fmt.Println(" ", r)
	}

	// -------------------------------------------------------------------------
	// Concurrent(4): 4 workers, items routed by hash(userID) % 4.
	//
	// Each worker owns a partition of the key space. With 4 users and 4 workers,
	// each user typically maps to a dedicated worker; zero contention.
	//
	// Key-sharding guarantee: fnv32(key) % n is stable across the run, so a given
	// key always reaches the same worker. Per-user state never crosses goroutine
	// boundaries; no mutex is needed on the per-user Ref in the hot path.
	// -------------------------------------------------------------------------

	fmt.Println("\n=== Concurrent(4) MapWithKey ===")
	start = time.Now()

	concurrentResults, err := kitsune.Collect(ctx, kitsune.MapWithKey(
		kitsune.FromSlice(events),
		keyFn,
		totalKey,
		func(ctx context.Context, ref *kitsune.Ref[int], e Event) (string, error) {
			total, _ := ref.UpdateAndGet(ctx, func(t int) (int, error) {
				return t + e.Amount, nil
			})
			return fmt.Sprintf("%-8s total=%d", e.UserID, total), nil
		},
		kitsune.Concurrency(4),
	))
	if err != nil {
		panic(err)
	}
	fmt.Printf("  %d events in %v\n", len(concurrentResults), time.Since(start).Round(time.Microsecond))
	for _, r := range concurrentResults {
		fmt.Println(" ", r)
	}

	// -------------------------------------------------------------------------
	// Verify: final totals must be identical in both modes.
	// Key-sharding routes same-key items to the same worker, so per-user
	// accumulation is correct even under concurrency.
	// -------------------------------------------------------------------------

	finalSerial := extractFinals(serialResults)
	finalConcurrent := extractFinals(concurrentResults)

	fmt.Println("\n=== Final totals comparison ===")
	allMatch := true
	for user, total := range finalSerial {
		concTotal, ok := finalConcurrent[user]
		match := ok && concTotal == total
		if !match {
			allMatch = false
		}
		status := "✓"
		if !match {
			status = "✗"
		}
		fmt.Printf("  %s %-8s serial=%d concurrent=%d\n", status, user, total, concTotal)
	}
	if allMatch {
		fmt.Println("\n  All totals match; key-sharding preserves per-entity correctness.")
	}
}

// extractFinals returns the last (highest) total seen for each user.
func extractFinals(results []string) map[string]int {
	finals := make(map[string]int)
	for _, r := range results {
		var user string
		var total int
		fmt.Sscanf(r, "%s total=%d", &user, &total)
		if cur, ok := finals[user]; !ok || total > cur {
			finals[user] = total
		}
	}
	// Sort is just for stable output in the example; not needed in production.
	_ = sort.Search // import used elsewhere
	return finals
}
