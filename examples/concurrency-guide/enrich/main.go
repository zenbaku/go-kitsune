// Example: concurrency-guide/enrich: parallel enrichment, with and without ordering.
//
// A stream of events is enriched via a simulated slow API call. The example runs
// the same pipeline twice: once unordered (fastest, output in completion order)
// and once with Ordered() (output in input order, ~10-15% lower throughput).
//
// Use this as a reference for the first decision in the flowchart:
// "Does the downstream need input order?" If no, drop Ordered() and gain
// free throughput. If yes, add Ordered() and accept the head-of-line risk.
//
// Demonstrates:
//   - Concurrency(n) for parallel I/O-bound stages
//   - Ordered() to restore input order at a resequencer cost
//   - Buffer(n) alongside Concurrency to absorb bursts
//
// See: doc/concurrency-guide.md#concurrencyn-unordered-parallel-workers
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// Event represents a record that needs external enrichment.
type Event struct {
	ID    int
	Value string
}

// Enriched holds the original event plus data from the "API".
type Enriched struct {
	ID     int
	Value  string
	Detail string
}

// enrichFn simulates a slow API call. Fixed sleep so timings are reproducible.
func enrichFn(_ context.Context, e Event) (Enriched, error) {
	time.Sleep(20 * time.Millisecond)
	return Enriched{ID: e.ID, Value: e.Value, Detail: fmt.Sprintf("detail-%02d", e.ID)}, nil
}

func main() {
	ctx := context.Background()

	events := make([]Event, 20)
	for i := range events {
		events[i] = Event{ID: i, Value: fmt.Sprintf("event-%02d", i)}
	}

	// -------------------------------------------------------------------------
	// Unordered: n goroutines, output arrives in whichever order they finish.
	// 20 items × 20 ms each, with 10 workers = ~40 ms wall time.
	// -------------------------------------------------------------------------

	fmt.Println("=== Unordered (completion order) ===")
	start := time.Now()
	unordered, err := kitsune.Collect(ctx,
		kitsune.Map(kitsune.FromSlice(events), enrichFn,
			kitsune.Concurrency(10),
			kitsune.Buffer(32),
			kitsune.WithName("enrich-unordered"),
		))
	if err != nil {
		panic(err)
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	fmt.Printf("  %d items in %v\n", len(unordered), elapsed)
	fmt.Printf("  first ID=%d  last ID=%d  (arbitrary completion order)\n\n",
		unordered[0].ID, unordered[len(unordered)-1].ID)

	// -------------------------------------------------------------------------
	// Ordered: same concurrency, but a slot-based resequencer restores input
	// order before items reach the downstream stage. Throughput is bounded by
	// the slowest in-flight item (head-of-line blocking).
	// -------------------------------------------------------------------------

	fmt.Println("=== Ordered (input order restored) ===")
	start = time.Now()
	ordered, err := kitsune.Collect(ctx,
		kitsune.Map(kitsune.FromSlice(events), enrichFn,
			kitsune.Concurrency(10),
			kitsune.Ordered(),
			kitsune.Buffer(32),
			kitsune.WithName("enrich-ordered"),
		))
	if err != nil {
		panic(err)
	}
	elapsed = time.Since(start).Round(time.Millisecond)
	fmt.Printf("  %d items in %v\n", len(ordered), elapsed)
	fmt.Printf("  first ID=%d  last ID=%d  (preserved input order)\n", ordered[0].ID, ordered[len(ordered)-1].ID)

	// Verify input order is actually preserved.
	for i, e := range ordered {
		if e.ID != i {
			panic(fmt.Sprintf("order violated: position %d has ID %d", i, e.ID))
		}
	}
	fmt.Println("  order verified: all IDs in sequence")
}
