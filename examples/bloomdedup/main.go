// Example: bloomdedup — probabilistic deduplication with a Bloom filter.
//
// Demonstrates: BloomDedupSet, WithDedupSet, Dedupe, DedupeBy
//
// BloomDedupSet provides bounded memory regardless of key-space size.
// Unlike MemoryDedupSet, it trades a configurable false-positive rate for
// O(n·log(1/p)) memory instead of O(n). Inserted keys are never missed
// (zero false-negative guarantee).
package main

import (
	"context"
	"fmt"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()

	// --- DedupeBy with a Bloom filter ---
	//
	// BloomDedupSet(expectedItems, falsePositiveRate) creates a filter sized
	// for expectedItems unique keys at the given FP probability.
	// Here: 1 000 expected keys at 1% false-positive rate (~1.17 KB of memory).

	events := kitsune.FromSlice([]string{
		"login:alice", "purchase:bob", "login:alice", // duplicate
		"logout:carol", "purchase:bob", // duplicate
		"login:dave",
	})

	set := kitsune.BloomDedupSet(1_000, 0.01)
	unique := kitsune.DedupeBy(events, func(e string) string { return e },
		kitsune.WithDedupSet(set),
	)

	results, err := kitsune.Collect(ctx, unique)
	if err != nil {
		panic(err)
	}
	fmt.Println("deduped events:", results)
	// Output: deduped events: [login:alice purchase:bob logout:carol login:dave]

	// --- Shared filter across pipeline runs ---
	//
	// The same BloomDedupSet can be reused across multiple Run calls.
	// Keys seen in earlier runs are remembered (the filter is not reset).

	sharedSet := kitsune.BloomDedupSet(10_000, 0.01)

	run1 := kitsune.FromSlice([]int{1, 2, 3})
	_, err = kitsune.Collect(ctx, kitsune.DedupeBy(run1, func(n int) int { return n },
		kitsune.WithDedupSet(sharedSet),
	))
	if err != nil {
		panic(err)
	}

	run2 := kitsune.FromSlice([]int{2, 3, 4, 5}) // 2 and 3 already seen
	newOnly, err := kitsune.Collect(ctx, kitsune.DedupeBy(run2, func(n int) int { return n },
		kitsune.WithDedupSet(sharedSet),
	))
	if err != nil {
		panic(err)
	}
	fmt.Println("new items in run 2:", newOnly)
	// Output: new items in run 2: [4 5]

	// --- Dedupe (consecutive → global) ---
	//
	// Pipeline.Dedupe defaults to consecutive deduplication (only adjacent
	// duplicates are suppressed). Passing WithDedupSet upgrades it to global
	// deduplication: any previously seen key is suppressed regardless of
	// position.

	nums := kitsune.FromSlice([]int{1, 2, 1, 3, 2})
	globalDeduped, err := kitsune.Collect(ctx,
		kitsune.DedupeBy(nums, func(n int) int { return n },
			kitsune.WithDedupSet(kitsune.BloomDedupSet(100, 0.01)),
		),
	)
	if err != nil {
		panic(err)
	}
	fmt.Println("global dedup:", globalDeduped)
	// Output: global dedup: [1 2 3]
}
