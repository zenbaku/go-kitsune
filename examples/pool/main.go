// Example: pool — memory pooling to reduce allocations in high-throughput pipelines.
//
// Demonstrates: Pool, NewPool, Warmup, MapPooled, Pooled, ReleaseAll.
//
// MapPooled acquires a pre-allocated object from a Pool before calling the
// transform function, then wraps the result in Pooled[O] so the caller can
// return it to the pool after use. This avoids a heap allocation per item for
// the output object, which matters in tight loops processing millions of items.
package main

import (
	"context"
	"fmt"
	"strings"

	kitsune "github.com/jonathan/go-kitsune"
)

// record is a reusable output object. In a real system this might be a large
// struct or a byte buffer whose allocation we want to amortize.
type record struct {
	Key   string
	Value string
	Tags  []string
}

func (r *record) reset() {
	r.Key = ""
	r.Value = ""
	r.Tags = r.Tags[:0]
}

func main() {
	// --- Basic MapPooled usage ---
	//
	// Each input string is transformed into a *record. Instead of allocating
	// a new *record per item, MapPooled reuses objects from the pool. After
	// processing, ReleaseAll returns every object to the pool.
	fmt.Println("=== MapPooled: transform strings into pooled records ===")

	pool := kitsune.NewPool(func() *record {
		return &record{Tags: make([]string, 0, 4)}
	})
	// Warmup pre-allocates objects before the first pipeline run to avoid
	// cold-start allocation bursts under high concurrency.
	pool.Warmup(8)

	input := kitsune.FromSlice([]string{
		"user:alice:admin,reader",
		"user:bob:reader",
		"service:payments:writer,reader",
	})

	out := kitsune.MapPooled(input, pool,
		func(_ context.Context, line string, rec *record) (*record, error) {
			rec.reset()
			parts := strings.SplitN(line, ":", 3)
			if len(parts) == 3 {
				rec.Key = parts[0] + ":" + parts[1]
				rec.Value = parts[1]
				rec.Tags = append(rec.Tags, strings.Split(parts[2], ",")...)
			}
			return rec, nil
		},
		kitsune.WithName("parse"),
	)

	results, err := out.Collect(context.Background())
	if err != nil {
		panic(err)
	}

	for _, r := range results {
		fmt.Printf("  key=%-20s value=%-10s tags=%v\n",
			r.Value.Key, r.Value.Value, r.Value.Tags)
	}

	// Release all records back to the pool — call this after you are done
	// reading the results. Any subsequent Get() from the same pool will reuse
	// one of these returned objects.
	kitsune.ReleaseAll(results)
	fmt.Printf("Released %d records back to pool\n", len(results))

	// --- Concurrent MapPooled ---
	//
	// Pool.Get and Pool.Put are safe for concurrent use. MapPooled with
	// Concurrency(4) shows multiple workers sharing the same pool without
	// data races.
	fmt.Println("\n=== Concurrent MapPooled: 4 workers sharing one pool ===")

	items := make([]int, 20)
	for i := range items {
		items[i] = (i + 1) * 7
	}

	type summary struct{ N, Square int }
	summaryPool := kitsune.NewPool(func() *summary { return new(summary) })

	concResults, err := kitsune.MapPooled(
		kitsune.FromSlice(items),
		summaryPool,
		func(_ context.Context, n int, s *summary) (*summary, error) {
			s.N = n
			s.Square = n * n
			return s, nil
		},
		kitsune.Concurrency(4),
		kitsune.Ordered(),
		kitsune.WithName("square"),
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}

	fmt.Printf("Processed %d items concurrently\n", len(concResults))
	if len(concResults) >= 3 {
		for _, r := range concResults[:3] {
			fmt.Printf("  %d² = %d\n", r.Value.N, r.Value.Square)
		}
		fmt.Println("  ...")
	}
	kitsune.ReleaseAll(concResults)

	// --- Explicit per-item Release ---
	//
	// Instead of ReleaseAll, you can Release each item individually —
	// useful when processing items one at a time in a ForEach terminal.
	fmt.Println("\n=== ForEach with per-item Release ===")

	processed := 0
	err = kitsune.MapPooled(
		kitsune.FromSlice([]int{10, 20, 30}),
		summaryPool,
		func(_ context.Context, n int, s *summary) (*summary, error) {
			s.N = n
			s.Square = n * n
			return s, nil
		},
	).ForEach(func(_ context.Context, r kitsune.Pooled[*summary]) error {
		fmt.Printf("  %d² = %d\n", r.Value.N, r.Value.Square)
		r.Release() // return to pool immediately after use
		processed++
		return nil
	}).Run(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("Processed %d items with per-item release\n", processed)
}
