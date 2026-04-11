// Example: concurrency-guide/useragg — per-user stateful aggregation with MapWithKey.
//
// A stream of payment events from multiple users is processed to maintain a
// running total per user. MapWithKey routes all events for the same user to
// the same worker via hash(userID) % n, so per-user state in Ref[int] is
// single-owner — no mutex required in the hot path.
//
// Use this when:
//   - State is per-entity (user, session, device), not global.
//   - Ordering only needs to be preserved within a key, not across keys.
//   - The key space is large enough that per-key goroutines would be wasteful
//     (MapWithKey uses n shared workers, not one goroutine per key).
//
// Demonstrates:
//   - MapWithKey for lock-free per-entity state
//   - Ref.UpdateAndGet for atomic read-modify-write
//   - Concurrency(4): 4 workers, items routed by hash(userID) % 4
//
// See: doc/concurrency-guide.md#mapwith--mapwithkey-key-sharded-per-entity-workers
package main

import (
	"context"
	"fmt"
	"sort"

	kitsune "github.com/zenbaku/go-kitsune"
)

// Payment is an incoming event on the stream.
type Payment struct {
	UserID string
	Amount int
}

// TotalUpdate is emitted downstream for each payment, carrying the running total.
type TotalUpdate struct {
	UserID       string
	Amount       int
	RunningTotal int
}

var totalKey = kitsune.NewKey[int]("running_total", 0)

func main() {
	ctx := context.Background()

	payments := []Payment{
		{"alice", 100}, {"bob", 200}, {"carol", 50},
		{"dave", 300}, {"alice", 150}, {"bob", 100},
		{"carol", 200}, {"alice", 75}, {"dave", 50},
		{"bob", 300}, {"carol", 125}, {"dave", 200},
		{"alice", 50}, {"bob", 75}, {"carol", 300},
	}

	results, err := kitsune.Collect(ctx, kitsune.MapWithKey(
		kitsune.FromSlice(payments),
		func(p Payment) string { return p.UserID },
		totalKey,
		func(ctx context.Context, ref *kitsune.Ref[int], p Payment) (TotalUpdate, error) {
			// UpdateAndGet is the atomic read-modify-write primitive on Ref[S].
			// Because all items for a given userID land on the same worker,
			// this call is never concurrent — no mutex needed.
			total, _ := ref.UpdateAndGet(ctx, func(t int) (int, error) {
				return t + p.Amount, nil
			})
			return TotalUpdate{UserID: p.UserID, Amount: p.Amount, RunningTotal: total}, nil
		},
		kitsune.Concurrency(4),
		kitsune.WithName("user-total"),
	))
	if err != nil {
		panic(err)
	}

	// Sort by user then by running total (which reflects event order per user)
	// so the output is deterministic across runs.
	sort.Slice(results, func(i, j int) bool {
		if results[i].UserID != results[j].UserID {
			return results[i].UserID < results[j].UserID
		}
		return results[i].RunningTotal < results[j].RunningTotal
	})

	fmt.Println("=== Per-user running totals (MapWithKey, Concurrency(4)) ===")
	for _, r := range results {
		fmt.Printf("  %-6s  +%3d  =>  total=%d\n", r.UserID, r.Amount, r.RunningTotal)
	}

	// Extract the final total per user and print a summary.
	finals := map[string]int{}
	for _, r := range results {
		finals[r.UserID] = r.RunningTotal
	}
	fmt.Println()
	fmt.Println("=== Final totals ===")
	for _, user := range []string{"alice", "bob", "carol", "dave"} {
		fmt.Printf("  %-6s  %d\n", user, finals[user])
	}
}
