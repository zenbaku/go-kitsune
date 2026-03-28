package kredis_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
	"github.com/jonathan/go-kitsune/tails/kredis"
	"github.com/redis/go-redis/v9"
)

func newClient(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// flushKeys removes test keys to ensure isolation.
func flushKeys(t *testing.T, client *redis.Client, keys ...string) {
	t.Helper()
	ctx := context.Background()
	for _, k := range keys {
		client.Del(ctx, k)
	}
	t.Cleanup(func() {
		for _, k := range keys {
			client.Del(ctx, k)
		}
	})
}

func TestRedisStore(t *testing.T) {
	client := newClient(t)
	flushKeys(t, client, "ktest:store:counter")

	counter := kitsune.NewKey[int]("counter", 0)
	input := kitsune.FromSlice([]string{"a", "b", "c"})

	numbered := kitsune.MapWith(input, counter,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (string, error) {
			if err := ref.Update(ctx, func(n int) (int, error) { return n + 1, nil }); err != nil {
				return "", err
			}
			val, err := ref.Get(ctx)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s-%d", s, val), nil
		},
	)

	store := kredis.NewStore(client, "ktest:store:")
	results, err := numbered.Collect(context.Background(), kitsune.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}

	expected := []string{"a-1", "b-2", "c-3"}
	if len(results) != len(expected) {
		t.Fatalf("expected %d results, got %d: %v", len(expected), len(results), results)
	}
	for i, v := range results {
		if v != expected[i] {
			t.Errorf("results[%d] = %q, want %q", i, v, expected[i])
		}
	}
}

func TestRedisCache(t *testing.T) {
	client := newClient(t)
	flushKeys(t, client, "kcache:a", "kcache:b", "kcache:c")

	callCount := 0
	input := kitsune.FromSlice([]string{"a", "b", "a", "c", "b", "a"})

	cache := kredis.NewCache(client, "kcache:")
	cached := kitsune.Map(input, func(_ context.Context, s string) (string, error) {
		callCount++
		return s + "!", nil
	}, kitsune.CacheBy(func(s string) string { return s }, kitsune.CacheBackend(cache), kitsune.CacheTTL(10*time.Second)))

	results, err := cached.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 6 {
		t.Fatalf("expected 6 results, got %d", len(results))
	}
	// a, b, c = 3 unique keys → 3 fn calls
	if callCount != 3 {
		t.Fatalf("expected 3 fn calls (cached), got %d", callCount)
	}
}

func TestRedisDedupSet(t *testing.T) {
	client := newClient(t)
	flushKeys(t, client, "kdedup:test")

	input := kitsune.FromSlice([]string{"x", "y", "x", "z", "y", "x"})
	deduped := input.Dedupe(func(s string) string { return s },
		kredis.NewDedupSet(client, "kdedup:test"))

	results, err := deduped.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	expected := []string{"x", "y", "z"}
	if len(results) != len(expected) {
		t.Fatalf("expected %d results, got %d: %v", len(expected), len(results), results)
	}
	for i, v := range results {
		if v != expected[i] {
			t.Errorf("results[%d] = %q, want %q", i, v, expected[i])
		}
	}
}

func TestRedisSourceSink(t *testing.T) {
	client := newClient(t)
	flushKeys(t, client, "ksrc:input", "ksrc:output")
	ctx := context.Background()

	// Seed the input list.
	for _, v := range []string{"alpha", "bravo", "charlie"} {
		client.RPush(ctx, "ksrc:input", v)
	}

	// Pipeline: read from one list → uppercase → write to another list.
	input := kredis.FromList(client, "ksrc:input")
	upper := kitsune.Map(input, func(_ context.Context, s string) (string, error) {
		return fmt.Sprintf("[%s]", s), nil
	})

	err := upper.ForEach(kredis.ListPush(client, "ksrc:output")).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Verify output list.
	vals, err := client.LRange(ctx, "ksrc:output", 0, -1).Result()
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"[alpha]", "[bravo]", "[charlie]"}
	if len(vals) != len(expected) {
		t.Fatalf("expected %d items in output list, got %d: %v", len(expected), len(vals), vals)
	}
	for i, v := range vals {
		if v != expected[i] {
			t.Errorf("output[%d] = %q, want %q", i, v, expected[i])
		}
	}

	// Input list should be empty (all items popped).
	remaining, _ := client.LLen(ctx, "ksrc:input").Result()
	if remaining != 0 {
		t.Errorf("expected input list empty, got %d items", remaining)
	}
}

func TestRedisStoreE2E(t *testing.T) {
	// Full pipeline: source → stateful map (build lookup) → flatmap (expand) →
	// stateful map (correlate) → sink. State lives in Redis.
	client := newClient(t)
	flushKeys(t, client, "ke2e:store:origins")

	type Item struct {
		ID   string
		Name string
	}

	origins := kitsune.NewKey[map[string]string]("origins", make(map[string]string))

	items := kitsune.FromSlice([]Item{
		{"i1", "Widget"},
		{"i2", "Gadget"},
	})

	// Build origin map and expand each item into two queries.
	queries := kitsune.FlatMapWith(items, origins,
		func(ctx context.Context, ref *kitsune.Ref[map[string]string], item Item) ([]string, error) {
			q1, q2 := item.ID+"-q1", item.ID+"-q2"
			err := ref.Update(ctx, func(m map[string]string) (map[string]string, error) {
				m[q1] = item.Name
				m[q2] = item.Name
				return m, nil
			})
			if err != nil {
				return nil, err
			}
			return []string{q1, q2}, nil
		},
	)

	// Correlate each query back to its origin.
	correlated := kitsune.MapWith(queries, origins,
		func(ctx context.Context, ref *kitsune.Ref[map[string]string], qid string) (string, error) {
			m, err := ref.Get(ctx)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s→%s", qid, m[qid]), nil
		},
	)

	store := kredis.NewStore(client, "ke2e:store:")
	results, err := correlated.Collect(context.Background(), kitsune.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}

	expected := []string{"i1-q1→Widget", "i1-q2→Widget", "i2-q1→Gadget", "i2-q2→Gadget"}
	if len(results) != len(expected) {
		t.Fatalf("expected %d results, got %d: %v", len(expected), len(results), results)
	}
	for i, v := range results {
		if v != expected[i] {
			t.Errorf("results[%d] = %q, want %q", i, v, expected[i])
		}
	}
}
