// Example: ttldedup — time-bounded deduplication with TTLDedupSet.
//
// Demonstrates: TTLDedupSet, WithDedupSet, Dedupe.
//
// TTLDedupSet forgets keys after a configured TTL. Unlike MemoryDedupSet
// (unbounded memory) and BloomDedupSet (bounded but lossy), it bounds memory
// by the size of the active time window while guaranteeing zero false positives.
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()

	// --- Webhook idempotency window ---
	//
	// Suppress duplicate webhook deliveries within a 5-second window.
	// Keys that were never re-seen within the window would be forgotten,
	// allowing re-delivery after the TTL.

	set := kitsune.TTLDedupSet(5 * time.Second)

	deliveries := kitsune.FromSlice([]string{
		"wh-1", "wh-2", "wh-1", // immediate duplicate: suppressed
		"wh-3", "wh-2", // duplicate within window: suppressed
	})

	unique := kitsune.DedupeBy(deliveries, func(id string) string { return id },
		kitsune.WithDedupSet(set),
	)

	out, err := kitsune.Collect(ctx, unique)
	if err != nil {
		panic(err)
	}
	fmt.Println("delivered:", out)

	// --- Expiry after TTL elapses ---
	//
	// With a short TTL, the same key is allowed through once the window expires.

	shortSet := kitsune.TTLDedupSet(50 * time.Millisecond)
	_ = shortSet.Add(ctx, "job-42")

	if ok, _ := shortSet.Contains(ctx, "job-42"); ok {
		fmt.Println("job-42 present immediately after Add")
	}

	time.Sleep(75 * time.Millisecond)

	if ok, _ := shortSet.Contains(ctx, "job-42"); !ok {
		fmt.Println("job-42 forgotten after TTL elapsed")
	}
}
