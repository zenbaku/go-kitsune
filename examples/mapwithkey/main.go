// Example: mapwithkey — per-entity keyed state in a pipeline.
//
// Demonstrates:
//   - MapWithKey: 1:1 transform with per-user session state
//     (counting how many events each user has produced)
//   - FlatMapWithKey: 1:N expansion with per-user state
//     (each event emits one tagged output per "credit", labelled with the
//     user's running total)
//   - StateTTL: how to give a key a time-to-live so idle state self-resets
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// --- shared types -----------------------------------------------------------

// UserEvent is a simple event carrying a user ID and an action name.
type UserEvent struct {
	UserID string
	Action string
}

// CreditEvent is a user event that also carries a number of "credits" —
// used to show 1:N expansion in FlatMapWithKey.
type CreditEvent struct {
	UserID  string
	Credits int
}

// ---------------------------------------------------------------------------
// Part 1: MapWithKey — per-user event counter (1:1)
// ---------------------------------------------------------------------------
//
// eventCountKey holds an int per user. The key name is scoped to this
// pipeline; each distinct UserID gets its own independent Ref[int].
var eventCountKey = kitsune.NewKey("event-count", 0)

func demoMapWithKey() {
	fmt.Println("=== MapWithKey: per-user event counter ===")
	fmt.Println("  Each event increments a counter that is private to its user.")
	fmt.Println()

	events := kitsune.FromSlice([]UserEvent{
		{"alice", "login"},
		{"bob", "login"},
		{"alice", "click"},
		{"alice", "purchase"},
		{"bob", "click"},
		{"carol", "login"},
		{"bob", "logout"},
	})

	// MapWithKey gives every (userID → Ref) pair its own state.
	// keyFn extracts the partition key; the Ref for that key is passed
	// into the transform function alongside the current item.
	results, err := kitsune.MapWithKey(events,
		func(e UserEvent) string { return e.UserID }, // partition by user
		eventCountKey,
		func(ctx context.Context, ref *kitsune.Ref[int], e UserEvent) (string, error) {
			// Atomically increment and read back the new count.
			count, err := ref.UpdateAndGet(ctx, func(v int) (int, error) {
				return v + 1, nil
			})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("  %-6s %-10s  →  event #%d for this user", e.UserID, e.Action, count), nil
		},
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}

	for _, line := range results {
		fmt.Println(line)
	}
	fmt.Println()
}

// ---------------------------------------------------------------------------
// Part 2: FlatMapWithKey — per-user running total with 1:N expansion
// ---------------------------------------------------------------------------
//
// creditTotalKey tracks how many outputs have been emitted for each user.
// Each CreditEvent expands into e.Credits output lines, each labelled with
// the user's global output counter at that moment.
var creditTotalKey = kitsune.NewKey("credit-total", 0)

func demoFlatMapWithKey() {
	fmt.Println("=== FlatMapWithKey: per-user 1:N expansion with running total ===")
	fmt.Println("  Each event yields one output per credit, tagged with the user's")
	fmt.Println("  cumulative output number so far.")
	fmt.Println()

	events := kitsune.FromSlice([]CreditEvent{
		{"alice", 3}, // alice emits 3 outputs
		{"bob", 1},   // bob emits 1 output
		{"alice", 2}, // alice emits 2 more — her counter continues from 3
		{"bob", 2},   // bob emits 2 more  — his counter continues from 1
	})

	results, err := kitsune.FlatMapWithKey(events,
		func(e CreditEvent) string { return e.UserID }, // partition by user
		creditTotalKey,
		func(ctx context.Context, ref *kitsune.Ref[int], e CreditEvent, yield func(string) error) error {
			for i := 0; i < e.Credits; i++ {
				// Bump the per-user output counter and get the new value.
				total, err := ref.UpdateAndGet(ctx, func(v int) (int, error) {
					return v + 1, nil
				})
				if err != nil {
					return err
				}
				// Yield one output line for each credit.
				if err := yield(fmt.Sprintf("  %-6s output #%d", e.UserID, total)); err != nil {
					return err
				}
			}
			return nil
		},
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}

	for _, line := range results {
		fmt.Println(line)
	}
	fmt.Println()
}

// ---------------------------------------------------------------------------
// Part 3: StateTTL — keyed state that expires after a quiet period
// ---------------------------------------------------------------------------
//
// StateTTL wraps NewKey to add a time-to-live.  When a Ref has not been
// written for longer than the TTL, the next read returns the initial value
// and the slot resets — useful for session-like patterns where idle users
// should be treated as if they just arrived.
func demoStateTTL() {
	fmt.Println("=== StateTTL: session state that resets after inactivity ===")
	fmt.Println("  A short TTL simulates an idle-timeout for user sessions.")
	fmt.Println()

	// 30 ms is enough to demonstrate expiry in an example without slow tests.
	const sessionTTL = 30 * time.Millisecond

	// The key now carries a TTL.  Pass kitsune.StateTTL as an option to NewKey.
	sessionKey := kitsune.NewKey("session-state", "new-session", kitsune.StateTTL(sessionTTL))

	type taggedRequest struct {
		UserID  string
		Payload string
		// sleepBefore simulates a gap between requests.
		sleepBefore time.Duration
	}

	requests := []taggedRequest{
		{"alice", "req-1", 0},                    // cold start — session is "new-session"
		{"alice", "req-2", 0},                    // within TTL  — session still set from req-1
		{"alice", "req-3", sessionTTL * 2},       // after TTL   — session has expired, resets
		{"bob", "req-A", 0},                      // bob is independent of alice
		{"bob", "req-B", sessionTTL * 2},         // bob's session also expires
	}

	for _, r := range requests {
		if r.sleepBefore > 0 {
			time.Sleep(r.sleepBefore)
		}

		// Run each request as its own single-item pipeline so we can sleep
		// between items — pipelines are synchronous and process items
		// sequentially, which is all we need for this demo.
		result, err := kitsune.MapWithKey(
			kitsune.FromSlice([]taggedRequest{r}),
			func(req taggedRequest) string { return req.UserID },
			sessionKey,
			func(ctx context.Context, ref *kitsune.Ref[string], req taggedRequest) (string, error) {
				// Read the current session state (returns initial if expired or never set).
				prev, err := ref.Get(ctx)
				if err != nil {
					return "", err
				}
				// Write the current payload as the new session state.
				if err := ref.Set(ctx, req.Payload); err != nil {
					return "", err
				}
				return fmt.Sprintf("  %-6s %-7s  prev-session=%q", req.UserID, req.Payload, prev), nil
			},
		).Collect(context.Background())
		if err != nil {
			panic(err)
		}
		for _, line := range result {
			fmt.Println(line)
		}
	}
	fmt.Println()
	fmt.Println("  Note: after sleeping past the TTL, prev-session resets to \"new-session\".")
}

// ---------------------------------------------------------------------------

func main() {
	demoMapWithKey()
	demoFlatMapWithKey()
	demoStateTTL()
}
