// Example: perkeyratelimit — per-entity rate limiting with MapWithKey.
//
// Each user is allowed at most 3 requests per window (every 5 ticks). Requests
// within budget are "accepted"; excess requests are "rejected". Because
// MapWithKey routes all items for the same userID to the same worker, per-user
// state never crosses goroutine boundaries — lock-free by design.
//
// Ticks are logical (an integer field on the request), not wall-clock time,
// so the example is deterministic and runs instantly.
//
// Demonstrates:
//   - MapWithKey for sharded per-key state
//   - Ref.UpdateAndGet for atomic read-modify-write decisions
//   - Concurrency(4): 4 workers, items routed by hash(userID) % 4
package main

import (
	"context"
	"fmt"
	"sort"

	kitsune "github.com/zenbaku/go-kitsune"
)

// Request simulates an API call with a logical tick timestamp.
type Request struct {
	UserID string
	Tick   int
}

// Result carries the verdict for one request.
type Result struct {
	UserID string
	Tick   int
	Window int
	Status string // "accepted" or "rejected"
}

// bucket tracks the current window and requests accepted within it.
// lastAccepted records the verdict for the most recent UpdateAndGet call so
// the caller can read the outcome from the returned snapshot.
type bucket struct {
	windowStart  int
	count        int
	lastAccepted bool
}

const (
	windowSize = 5 // window spans this many ticks
	rateLimit  = 3 // max requests accepted per window per user
)

var rateLimitKey = kitsune.NewKey[bucket]("rate_limit", bucket{})

func main() {
	ctx := context.Background()

	// Simulate requests across three users spread over several windows.
	requests := []Request{
		// Window 0 (ticks 0–4): alice sends 4 — 3 accepted, 1 rejected
		{"alice", 0}, {"alice", 1}, {"alice", 2}, {"alice", 3},
		// Window 0: bob sends 2 — 2 accepted
		{"bob", 1}, {"bob", 2},
		// Window 0: carol sends 1 — 1 accepted
		{"carol", 0},
		// Window 1 (ticks 5–9): alice sends 2 — both accepted (fresh window)
		{"alice", 5}, {"alice", 6},
		// Window 1: bob sends 5 — 3 accepted, 2 rejected
		{"bob", 5}, {"bob", 6}, {"bob", 7}, {"bob", 8}, {"bob", 9},
		// Window 1: carol sends 3 — all accepted
		{"carol", 5}, {"carol", 6}, {"carol", 7},
		// Window 2 (ticks 10–14): alice sends 4 — 3 accepted, 1 rejected
		{"alice", 10}, {"alice", 11}, {"alice", 12}, {"alice", 13},
		// Window 2: carol sends 1 — accepted
		{"carol", 10},
	}

	results, err := kitsune.Collect(ctx, kitsune.MapWithKey(
		kitsune.FromSlice(requests),
		func(r Request) string { return r.UserID },
		rateLimitKey,
		func(ctx context.Context, ref *kitsune.Ref[bucket], r Request) (Result, error) {
			window := r.Tick / windowSize
			b, _ := ref.UpdateAndGet(ctx, func(b bucket) (bucket, error) {
				if window != b.windowStart {
					// New window — reset counter.
					b = bucket{windowStart: window}
				}
				if b.count < rateLimit {
					b.count++
					b.lastAccepted = true
				} else {
					b.lastAccepted = false
				}
				return b, nil
			})
			st := "rejected"
			if b.lastAccepted {
				st = "accepted"
			}
			return Result{UserID: r.UserID, Tick: r.Tick, Window: window, Status: st}, nil
		},
		kitsune.Concurrency(4),
	))
	if err != nil {
		panic(err)
	}

	// Sort for stable output: by user then tick.
	sort.Slice(results, func(i, j int) bool {
		if results[i].UserID != results[j].UserID {
			return results[i].UserID < results[j].UserID
		}
		return results[i].Tick < results[j].Tick
	})

	fmt.Println("=== Per-user rate limit (3 req / 5-tick window) ===")
	tally := map[string][2]int{} // [accepted, rejected]
	for _, r := range results {
		t := tally[r.UserID]
		if r.Status == "accepted" {
			t[0]++
		} else {
			t[1]++
		}
		tally[r.UserID] = t
		fmt.Printf("  %-6s tick=%-2d window=%d  %s\n", r.UserID, r.Tick, r.Window, r.Status)
	}

	fmt.Println()
	for _, u := range []string{"alice", "bob", "carol"} {
		t := tally[u]
		fmt.Printf("  %-6s accepted=%d  rejected=%d\n", u, t[0], t[1])
	}
}
