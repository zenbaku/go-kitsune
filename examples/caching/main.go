// Example: caching: skip redundant work with CacheBy + MemoryCache.
//
// Demonstrates: CacheBy, CacheTTL, MemoryCache, WithCache (runner-level default)
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()

	calls := 0
	expensive := func(_ context.Context, id string) (string, error) {
		calls++
		time.Sleep(5 * time.Millisecond) // simulate I/O
		return "data-for-" + id, nil
	}

	// CacheBy[I](keyFn) tells Map to cache results keyed by keyFn(item).
	// Cache misses call fn normally; hits skip fn entirely.
	items := kitsune.FromSlice([]string{"a", "b", "a", "c", "b", "a"})
	results := kitsune.Map(items, expensive,
		kitsune.CacheBy(func(s string) string { return s },
			kitsune.CacheTTL(5*time.Minute),
			kitsune.CacheBackend(kitsune.MemoryCache(128)),
		),
		kitsune.WithName("fetch"),
	)

	out, err := kitsune.Collect(ctx, results)
	if err != nil {
		panic(err)
	}

	fmt.Println("results:", out)
	fmt.Printf("fn called %d times for %d items (%d cache hits)\n",
		calls, len(out), len(out)-calls)

	// --- Runner-level cache default ---
	//
	// WithCache sets a default cache for all CacheBy stages that don't specify
	// their own CacheBackend. Useful when many stages share the same cache.

	calls2 := 0
	transform := func(_ context.Context, n int) (int, error) {
		calls2++
		return n * n, nil
	}

	nums := kitsune.FromSlice([]int{1, 2, 1, 3, 2, 1})
	squared := kitsune.Map(nums, transform,
		kitsune.CacheBy(func(n int) string { return fmt.Sprint(n) }),
	)

	out2, err := kitsune.Collect(ctx, squared,
		kitsune.WithCache(kitsune.MemoryCache(64), time.Hour),
	)
	if err != nil {
		panic(err)
	}

	fmt.Println("squares:", out2)
	fmt.Printf("fn called %d times for %d items (%d cache hits)\n",
		calls2, len(out2), len(out2)-calls2)
}
