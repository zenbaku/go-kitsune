package kitsune_test

import (
	"context"
	"sync/atomic"
	"testing"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// CacheBy wired into Map
// ---------------------------------------------------------------------------

func TestCacheBySkipsCallOnHit(t *testing.T) {
	ctx := context.Background()
	cache := kitsune.MemoryCache(64)

	var calls int64
	double := func(_ context.Context, v int) (int, error) {
		atomic.AddInt64(&calls, 1)
		return v * 2, nil
	}

	p := kitsune.FromSlice([]int{1, 2, 1, 3, 2}) // 1 and 2 appear twice
	got, err := kitsune.Collect(ctx,
		kitsune.Map(p, double, kitsune.CacheBy(func(v int) string {
			return string(rune('0' + v))
		})),
		kitsune.WithCache(cache, 0),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("got %v, want 5 items", got)
	}
	// 1→2, 2→4, 1→2 (cached), 3→6, 2→4 (cached) → [2,4,2,6,4]
	want := []int{2, 4, 2, 6, 4}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("got[%d]=%d, want %d", i, got[i], w)
		}
	}
	// Only 3 distinct keys: fn called 3 times not 5
	if calls != 3 {
		t.Fatalf("fn called %d times, want 3", calls)
	}
}

func TestCacheByPerStageCacheOverridesDefault(t *testing.T) {
	ctx := context.Background()

	// Stage-level cache (not the runner default).
	stageCache := kitsune.MemoryCache(64)
	var calls int64
	fn := func(_ context.Context, v int) (int, error) {
		atomic.AddInt64(&calls, 1)
		return v * 10, nil
	}

	p := kitsune.FromSlice([]int{5, 5, 5})
	got, err := kitsune.Collect(ctx,
		kitsune.Map(p, fn, kitsune.CacheBy(
			func(v int) string { return "k" },
			kitsune.CacheBackend(stageCache),
		)),
		// No WithCache run option; stage supplies its own backend.
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range got {
		if v != 50 {
			t.Fatalf("got %v, want 50", v)
		}
	}
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1", calls)
	}
}

func TestCacheByNoopWhenNoCacheBackend(t *testing.T) {
	ctx := context.Background()
	// CacheBy with no stage-level cache and no WithCache run option → fn always called.
	var calls int64
	fn := func(_ context.Context, v int) (int, error) {
		atomic.AddInt64(&calls, 1)
		return v, nil
	}
	p := kitsune.FromSlice([]int{1, 1, 1})
	_, err := kitsune.Collect(ctx,
		kitsune.Map(p, fn, kitsune.CacheBy(func(v int) string { return "k" })),
		// No WithCache; cache is nil, so fn is always called.
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("fn called %d times, want 3 (no cache backend → no skipping)", calls)
	}
}

// ---------------------------------------------------------------------------
// WithDedupSet wired into DedupeBy (upgrades to global dedup)
// ---------------------------------------------------------------------------

func TestDedupeByWithExternalDedupSetGlobalDedup(t *testing.T) {
	ctx := context.Background()
	set := kitsune.MemoryDedupSet()
	// 1 appears non-consecutively; without a set it would pass through; with a set it's suppressed.
	p := kitsune.FromSlice([]int{1, 2, 1, 3, 2})
	got, err := kitsune.Collect(ctx, kitsune.DedupeBy(p, func(v int) int { return v },
		kitsune.WithDedupSet(set),
	))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("got[%d]=%d, want %d", i, got[i], w)
		}
	}
}

// ---------------------------------------------------------------------------
// MemoryCache TTL / eviction
// ---------------------------------------------------------------------------

func TestMemoryCacheTTL(t *testing.T) {
	cache := kitsune.MemoryCache(10)
	ctx := context.Background()

	// 1 nanosecond TTL; effectively expired immediately.
	_ = cache.Set(ctx, "k", []byte("v"), 1)
	_, ok, _ := cache.Get(ctx, "k")
	if ok {
		t.Fatal("expected cache miss for expired entry")
	}
}

func TestMemoryCacheEviction(t *testing.T) {
	cache := kitsune.MemoryCache(2)
	ctx := context.Background()

	_ = cache.Set(ctx, "a", []byte("1"), 0)
	_ = cache.Set(ctx, "b", []byte("2"), 0)
	_ = cache.Set(ctx, "c", []byte("3"), 0) // evicts "a"

	_, ok, _ := cache.Get(ctx, "a")
	if ok {
		t.Fatal("expected 'a' to be evicted")
	}
	data, ok, _ := cache.Get(ctx, "b")
	if !ok || string(data) != "2" {
		t.Fatalf("expected 'b'='2', got ok=%v data=%q", ok, data)
	}
}

func TestMapWithCache(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int64
	input := kitsune.FromSlice([]string{"a", "b", "a", "c", "a"})

	cached := kitsune.Map(input, func(_ context.Context, s string) (string, error) {
		calls.Add(1)
		return s + "!", nil
	}, kitsune.CacheBy(func(s string) string { return s }, kitsune.CacheBackend(kitsune.MemoryCache(100))))

	results, err := cached.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
	// "a" appears 3 times but fn should only be called once for it.
	if calls.Load() != 3 { // a, b, c
		t.Fatalf("expected 3 fn calls (cached), got %d", calls.Load())
	}
	for _, r := range results {
		if r != "a!" && r != "b!" && r != "c!" {
			t.Errorf("unexpected result: %q", r)
		}
	}
}
