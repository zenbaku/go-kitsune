// Example: redis — Redis source, sink, store, cache, and dedup.
//
// Demonstrates: kredis.FromList, kredis.ListPush, kredis.NewStore (WithStore),
// kredis.NewCache (CachedMap), kredis.NewDedupSet (Dedupe).
//
// Requires Redis running on localhost:6379.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
	"github.com/jonathan/go-kitsune/tails/kredis"
	"github.com/redis/go-redis/v9"
)

func main() {
	ctx := context.Background()

	// User-managed connection.
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Redis not available: %v", err)
	}

	// Clean up test keys.
	for _, k := range []string{"kex:input", "kex:output", "kex:dedup", "kex:cache:a", "kex:cache:b", "kex:cache:c", "kex:store:counter"} {
		rdb.Del(ctx, k)
	}

	// --- Source → Transform → Sink (Redis lists) ---
	fmt.Println("=== Redis list source → sink ===")
	for _, v := range []string{"hello", "world", "kitsune"} {
		rdb.RPush(ctx, "kex:input", v)
	}

	input := kredis.FromList(rdb, "kex:input")
	upper := kitsune.Map(input, func(_ context.Context, s string) (string, error) {
		return fmt.Sprintf("[%s]", s), nil
	})
	err := upper.ForEach(kredis.ListPush(rdb, "kex:output")).Run(ctx)
	if err != nil {
		log.Fatal(err)
	}

	output, _ := rdb.LRange(ctx, "kex:output", 0, -1).Result()
	fmt.Println("Output list:", output)

	// --- Dedup via Redis set ---
	fmt.Println("\n=== Redis-backed dedup ===")
	items := kitsune.FromSlice([]string{"a", "b", "a", "c", "b", "a"})
	deduped := kitsune.Dedupe(items,
		func(s string) string { return s },
		kredis.NewDedupSet(rdb, "kex:dedup"),
	)
	unique, _ := deduped.Collect(ctx)
	fmt.Println("Unique:", unique)

	// --- CachedMap via Redis ---
	fmt.Println("\n=== Redis-backed cache ===")
	lookupCount := 0
	ids := kitsune.FromSlice([]string{"a", "b", "a", "c", "b", "a"})
	cached := kitsune.CachedMap(ids,
		func(_ context.Context, id string) (string, error) {
			lookupCount++
			return fmt.Sprintf("result(%s)", id), nil
		},
		func(id string) string { return id },
		kredis.NewCache(rdb, "kex:cache:"),
		10*time.Second,
	)
	results, _ := cached.Collect(ctx)
	fmt.Printf("Results: %v (%d lookups, rest cached in Redis)\n", results, lookupCount)

	// --- Store-backed state (Ref persisted to Redis) ---
	fmt.Println("\n=== Redis-backed state (WithStore) ===")
	counter := kitsune.NewKey[int]("counter", 0)
	words := kitsune.FromSlice([]string{"alpha", "bravo", "charlie"})

	numbered := kitsune.MapWith(words, counter,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (string, error) {
			if err := ref.Update(ctx, func(n int) (int, error) { return n + 1, nil }); err != nil {
				return "", err
			}
			val, _ := ref.Get(ctx)
			return fmt.Sprintf("%d:%s", val, s), nil
		},
	)

	store := kredis.NewStore(rdb, "kex:store:")
	tagged, _ := numbered.Collect(ctx, kitsune.WithStore(store))
	fmt.Println("Tagged:", tagged)

	// Clean up.
	for _, k := range []string{"kex:input", "kex:output", "kex:dedup", "kex:cache:a", "kex:cache:b", "kex:cache:c", "kex:store:counter"} {
		rdb.Del(ctx, k)
	}
}
