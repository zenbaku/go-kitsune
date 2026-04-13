// Example: lookupby — bulk-fetch user records with BatchTimeout for low
// throughput.
//
// LookupBy batches items internally and issues one Fetch call per batch with
// deduplicated keys. Under low throughput a pure size-based batch can stall
// waiting for items that never arrive. Setting BatchTimeout on LookupConfig
// flushes the partial batch after the configured duration, bounding latency
// at the cost of smaller batches.
//
// Demonstrates:
//   - LookupConfig with BatchSize and BatchTimeout
//   - A simulated user database fetched in bulk
//   - Pair[T,V] output attaching the fetched value to the original item
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// userDB stands in for a remote user store. A real example would call out to
// a database or HTTP service; here we just sleep briefly to simulate latency.
var userDB = map[int]string{
	1: "alice",
	2: "bob",
	3: "carol",
	4: "dave",
	5: "eve",
}

func fetchUsers(_ context.Context, ids []int) (map[int]string, error) {
	// Simulate a bulk lookup round-trip.
	time.Sleep(2 * time.Millisecond)
	out := make(map[int]string, len(ids))
	for _, id := range ids {
		if name, ok := userDB[id]; ok {
			out[id] = name
		}
	}
	return out, nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Low-throughput producer: a handful of ids trickle in with gaps between
	// them. BatchSize is large (50) so without BatchTimeout the pipeline would
	// wait for the source to close before ever calling fetchUsers.
	ch := kitsune.NewChannel[int](8)

	cfg := kitsune.LookupConfig[int, int, string]{
		Key:          func(id int) int { return id },
		Fetch:        fetchUsers,
		BatchSize:    50,
		BatchTimeout: 25 * time.Millisecond,
	}

	done := make(chan error, 1)
	go func() {
		done <- kitsune.LookupBy(ch.Source(), cfg).
			ForEach(func(_ context.Context, p kitsune.Pair[int, string]) error {
				fmt.Printf("  id=%d  name=%q\n", p.First, p.Second)
				return nil
			}).Run(ctx)
	}()

	fmt.Println("=== lookupby with BatchTimeout ===")
	ids := []int{1, 2, 3, 4, 5}
	for _, id := range ids {
		if err := ch.Send(ctx, id); err != nil {
			panic(err)
		}
		// Trickle: each gap is longer than BatchTimeout, so each item flushes
		// as its own partial batch rather than waiting to fill up.
		time.Sleep(40 * time.Millisecond)
	}

	ch.Close()
	if err := <-done; err != nil {
		panic(err)
	}
}
