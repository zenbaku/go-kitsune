package kitsune_test

import (
	"context"
	"sync/atomic"
	"testing"

	kitsune "github.com/zenbaku/go-kitsune/v2"
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
			return string(rune('0'+v))
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
		// No WithCache run option — stage supplies its own backend.
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
		// No WithCache — cache is nil, so fn is always called.
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("fn called %d times, want 3 (no cache backend → no skipping)", calls)
	}
}

// ---------------------------------------------------------------------------
// WithDedupSet wired into DistinctBy
// ---------------------------------------------------------------------------

func TestDistinctByWithExternalDedupSet(t *testing.T) {
	ctx := context.Background()
	set := kitsune.MemoryDedupSet()
	p := kitsune.FromSlice([]int{1, 2, 1, 3, 2, 4})
	got, err := kitsune.Collect(ctx, kitsune.DistinctBy(p, func(v int) int { return v },
		kitsune.WithDedupSet(set),
	))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("got[%d]=%d, want %d", i, got[i], w)
		}
	}
}

func TestDistinctByExternalSetPersistsAcrossRuns(t *testing.T) {
	ctx := context.Background()
	// The same set is shared across two runs — items seen in run 1 are filtered in run 2.
	set := kitsune.MemoryDedupSet()
	keyFn := func(v int) int { return v }

	p1 := kitsune.FromSlice([]int{1, 2, 3})
	_, err := kitsune.Collect(ctx, kitsune.DistinctBy(p1, keyFn, kitsune.WithDedupSet(set)))
	if err != nil {
		t.Fatal(err)
	}

	p2 := kitsune.FromSlice([]int{2, 3, 4, 5})
	got, err := kitsune.Collect(ctx, kitsune.DistinctBy(p2, keyFn, kitsune.WithDedupSet(set)))
	if err != nil {
		t.Fatal(err)
	}
	// 2 and 3 were seen in run 1 → only 4 and 5 pass through.
	want := []int{4, 5}
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
// WithDedupSet wired into DedupeBy (upgrades to global dedup)
// ---------------------------------------------------------------------------

func TestDedupeByWithExternalDedupSetGlobalDedup(t *testing.T) {
	ctx := context.Background()
	set := kitsune.MemoryDedupSet()
	// 1 appears non-consecutively — without a set it would pass through; with a set it's suppressed.
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
