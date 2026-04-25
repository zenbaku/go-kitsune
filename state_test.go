package kitsune_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// MapWith
// ---------------------------------------------------------------------------

func TestMapWith(t *testing.T) {
	ctx := context.Background()
	counterKey := kitsune.NewKey[int]("counter", 0)

	p := kitsune.FromSlice([]string{"a", "b", "c"})
	numbered := kitsune.MapWith(p, counterKey, func(ctx context.Context, ref *kitsune.Ref[int], s string) (string, error) {
		n, err := ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d:%s", n, s), nil
	})

	got, err := kitsune.Collect(ctx, numbered)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"1:a", "2:b", "3:c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("item %d: got %q, want %q", i, got[i], w)
		}
	}
}

func TestMapWithStateIsolatedPerRun(t *testing.T) {
	// Each Run() should get a fresh Ref with the initial value.
	ctx := context.Background()
	counterKey := kitsune.NewKey[int]("counter", 0)

	p := kitsune.FromSlice([]int{1, 2, 3})
	sumPipeline := kitsune.MapWith(p, counterKey, func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
		return ref.UpdateAndGet(ctx, func(acc int) (int, error) { return acc + v, nil })
	})

	// First run: 0+1=1, 1+2=3, 3+3=6
	got1, err := kitsune.Collect(ctx, sumPipeline)
	if err != nil {
		t.Fatal(err)
	}
	// Second run: should start from 0 again
	got2, err := kitsune.Collect(ctx, sumPipeline)
	if err != nil {
		t.Fatal(err)
	}
	if len(got1) != 3 || len(got2) != 3 {
		t.Fatalf("unexpected lengths: %v, %v", got1, got2)
	}
	if got1[2] != 6 {
		t.Errorf("run1 final: got %d, want 6", got1[2])
	}
	if got2[2] != 6 {
		t.Errorf("run2 final: got %d, want 6 (state not reset)", got2[2])
	}
}

// ---------------------------------------------------------------------------
// FlatMapWith
// ---------------------------------------------------------------------------

func TestFlatMapWith(t *testing.T) {
	ctx := context.Background()
	seenKey := kitsune.NewKey[[]string]("seen", nil)

	_ = seenKey // declared to verify Key type is usable

	yieldKey := kitsune.NewKey[int]("count2", 0)
	p2 := kitsune.FromSlice([]int{1, 2, 3})
	doubled := kitsune.FlatMapWith(p2, yieldKey, func(ctx context.Context, ref *kitsune.Ref[int], v int, yield func(int) error) error {
		n, _ := ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + v, nil })
		if err := yield(v); err != nil {
			return err
		}
		return yield(n)
	})

	got, err := kitsune.Collect(ctx, doubled)
	if err != nil {
		t.Fatal(err)
	}
	// item=1: yield(1), yield(0+1=1) → [1,1]
	// item=2: yield(2), yield(1+2=3) → [2,3]
	// item=3: yield(3), yield(3+3=6) → [3,6]
	want := []int{1, 1, 2, 3, 3, 6}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("item %d: got %d, want %d", i, got[i], w)
		}
	}
}

// ---------------------------------------------------------------------------
// MapWithKey
// ---------------------------------------------------------------------------

func TestMapWithKey(t *testing.T) {
	ctx := context.Background()
	countKey := kitsune.NewKey[int]("peruser", 0)

	type event struct{ user, action string }
	events := []event{
		{"alice", "login"},
		{"bob", "login"},
		{"alice", "click"},
		{"bob", "click"},
		{"alice", "logout"},
	}

	p := kitsune.FromSlice(events)
	counted := kitsune.MapWithKey(p,
		func(e event) string { return e.user },
		countKey,
		func(ctx context.Context, ref *kitsune.Ref[int], e event) (string, error) {
			n, err := ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s#%d", e.user, n), nil
		},
	)

	got, err := kitsune.Collect(ctx, counted)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alice#1", "bob#1", "alice#2", "bob#2", "alice#3"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("item %d: got %q, want %q", i, got[i], w)
		}
	}
}

// ---------------------------------------------------------------------------
// LookupBy / Enrich
// ---------------------------------------------------------------------------

func TestLookupBy(t *testing.T) {
	ctx := context.Background()
	db := map[int]string{1: "alice", 2: "bob", 3: "carol"}

	p := kitsune.FromSlice([]int{1, 2, 3, 1}) // key 1 repeated
	cfg := kitsune.NewLookupConfig(
		func(id int) int { return id },
		func(_ context.Context, ids []int) (map[int]string, error) {
			result := make(map[int]string, len(ids))
			for _, id := range ids {
				result[id] = db[id]
			}
			return result, nil
		},
	)

	pairs, err := kitsune.Collect(ctx, kitsune.LookupBy(p, cfg))
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 4 {
		t.Fatalf("want 4 pairs, got %d", len(pairs))
	}
	for _, pair := range pairs {
		if pair.Value != db[pair.Item] {
			t.Errorf("id %d: got %q, want %q", pair.Item, pair.Value, db[pair.Item])
		}
	}
}

func TestEnrich(t *testing.T) {
	ctx := context.Background()
	db := map[string]int{"a": 1, "b": 2, "c": 3}

	p := kitsune.FromSlice([]string{"a", "b", "c", "a"})
	cfg := kitsune.NewEnrichConfig(
		func(s string) string { return s },
		func(_ context.Context, keys []string) (map[string]int, error) {
			result := make(map[string]int, len(keys))
			for _, k := range keys {
				result[k] = db[k]
			}
			return result, nil
		},
		func(s string, n int) string { return fmt.Sprintf("%s=%d", s, n) },
	)

	got, err := kitsune.Collect(ctx, kitsune.Enrich(p, cfg))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{"a=1", "a=1", "b=2", "c=3"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("item %d: got %q, want %q", i, got[i], w)
		}
	}
}

func TestLookupByDeduplicatesKeys(t *testing.T) {
	ctx := context.Background()
	fetchCalls := 0

	p := kitsune.FromSlice([]int{1, 1, 1, 1, 1}) // all same key, batch=10
	cfg := kitsune.LookupConfig[int, int, string]{
		Key: func(id int) int { return id },
		Fetch: func(_ context.Context, ids []int) (map[int]string, error) {
			fetchCalls++
			if len(ids) != 1 {
				return nil, fmt.Errorf("expected 1 deduplicated key, got %d", len(ids))
			}
			return map[int]string{1: "one"}, nil
		},
		BatchSize: 10,
	}

	_, err := kitsune.Collect(ctx, kitsune.LookupBy(p, cfg))
	if err != nil {
		t.Fatal(err)
	}
	if fetchCalls != 1 {
		t.Errorf("expected 1 fetch call, got %d", fetchCalls)
	}
}

func TestLookupByBatchTimeoutFlushesPartialBatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var fetchMu sync.Mutex
	var fetchCalls [][]int

	ch := kitsune.NewChannel[int](10)
	cfg := kitsune.LookupConfig[int, int, string]{
		Key: func(id int) int { return id },
		Fetch: func(_ context.Context, ids []int) (map[int]string, error) {
			fetchMu.Lock()
			cp := make([]int, len(ids))
			copy(cp, ids)
			fetchCalls = append(fetchCalls, cp)
			fetchMu.Unlock()
			result := make(map[int]string, len(ids))
			for _, id := range ids {
				result[id] = fmt.Sprintf("v%d", id)
			}
			return result, nil
		},
		BatchSize:    100,
		BatchTimeout: 20 * time.Millisecond,
	}

	pairs := make(chan kitsune.Enriched[int, string], 16)
	done := make(chan error, 1)
	go func() {
		_, err := kitsune.LookupBy(ch.Source(), cfg).
			ForEach(func(_ context.Context, p kitsune.Enriched[int, string]) error {
				pairs <- p
				return nil
			}).Run(ctx)
		done <- err
	}()

	// Send two items; far below BatchSize. Without BatchTimeout these would
	// sit in the Batch buffer indefinitely until the source closes.
	if err := ch.Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(ctx, 2); err != nil {
		t.Fatal(err)
	}

	// Wait past the timeout so the ticker fires and flushes the partial batch.
	time.Sleep(50 * time.Millisecond)

	// We must see the two pairs emitted *before* we close the channel, proving
	// the flush came from the timeout rather than the source-closed path.
	seen := make(map[int]string)
	for i := 0; i < 2; i++ {
		select {
		case p := <-pairs:
			seen[p.Item] = p.Value
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("timeout waiting for pair %d (seen so far: %v)", i, seen)
		}
	}
	if seen[1] != "v1" || seen[2] != "v2" {
		t.Fatalf("got %v, want map[1:v1 2:v2]", seen)
	}

	fetchMu.Lock()
	if len(fetchCalls) != 1 {
		fetchMu.Unlock()
		t.Fatalf("expected exactly 1 fetch call before close, got %d: %v", len(fetchCalls), fetchCalls)
	}
	if len(fetchCalls[0]) != 2 {
		fetchMu.Unlock()
		t.Fatalf("expected fetch batch of size 2, got %d: %v", len(fetchCalls[0]), fetchCalls[0])
	}
	fetchMu.Unlock()

	ch.Close()
	if err := <-done; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
}

func TestEnrichBatchTimeoutFlushesPartialBatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var fetchMu sync.Mutex
	fetchCalls := 0

	ch := kitsune.NewChannel[string](10)
	cfg := kitsune.EnrichConfig[string, string, int, string]{
		Key: func(s string) string { return s },
		Fetch: func(_ context.Context, keys []string) (map[string]int, error) {
			fetchMu.Lock()
			fetchCalls++
			fetchMu.Unlock()
			result := make(map[string]int, len(keys))
			for i, k := range keys {
				result[k] = i + 1
			}
			return result, nil
		},
		Join:         func(s string, n int) string { return fmt.Sprintf("%s=%d", s, n) },
		BatchSize:    100,
		BatchTimeout: 20 * time.Millisecond,
	}

	out := make(chan string, 16)
	done := make(chan error, 1)
	go func() {
		_, err := kitsune.Enrich(ch.Source(), cfg).
			ForEach(func(_ context.Context, s string) error {
				out <- s
				return nil
			}).Run(ctx)
		done <- err
	}()

	if err := ch.Send(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(ctx, "b"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	got := make(map[string]bool)
	for i := 0; i < 2; i++ {
		select {
		case s := <-out:
			got[s] = true
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("timeout waiting for enrich output %d (got %v)", i, got)
		}
	}
	if !got["a=1"] || !got["b=2"] {
		t.Fatalf("got %v, want a=1 and b=2", got)
	}

	fetchMu.Lock()
	if fetchCalls != 1 {
		fetchMu.Unlock()
		t.Fatalf("expected 1 fetch call before close, got %d", fetchCalls)
	}
	fetchMu.Unlock()

	ch.Close()
	if err := <-done; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Lift alias
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// StateTTL tests
// ---------------------------------------------------------------------------

func TestStateTTL_ExpiryOnGet(t *testing.T) {
	ttl := 20 * time.Millisecond
	key := kitsune.NewKey("ttl-expire", 0, kitsune.StateTTL(ttl))
	input := kitsune.FromSlice([]int{1})

	var gotValue int
	p := kitsune.MapWith(input, key, func(ctx context.Context, ref *kitsune.Ref[int], _ int) (int, error) {
		if err := ref.Set(ctx, 42); err != nil {
			return 0, err
		}
		time.Sleep(ttl + 5*time.Millisecond) // sleep past TTL
		v, err := ref.Get(ctx)
		if err != nil {
			return 0, err
		}
		gotValue = v
		return v, nil
	})
	if _, err := p.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotValue != 0 {
		t.Errorf("expected initial value 0 after TTL expiry, got %d", gotValue)
	}
}

func TestStateTTL_ResetOnWrite(t *testing.T) {
	ttl := 30 * time.Millisecond
	key := kitsune.NewKey("ttl-reset", 0, kitsune.StateTTL(ttl))
	input := kitsune.FromSlice([]int{1})

	var gotValue int
	p := kitsune.MapWith(input, key, func(ctx context.Context, ref *kitsune.Ref[int], _ int) (int, error) {
		if err := ref.Set(ctx, 99); err != nil {
			return 0, err
		}
		time.Sleep(ttl / 2)
		// Write again; resets the TTL clock.
		if err := ref.Set(ctx, 99); err != nil {
			return 0, err
		}
		time.Sleep(ttl / 2)
		// Should NOT be expired yet (TTL clock was reset by second write).
		v, err := ref.Get(ctx)
		if err != nil {
			return 0, err
		}
		gotValue = v
		return v, nil
	})
	if _, err := p.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotValue != 99 {
		t.Errorf("expected 99 (not expired), got %d", gotValue)
	}
}

func TestStateTTL_ZeroTTL_NoExpiry(t *testing.T) {
	// Default key (no TTL); value must never expire.
	key := kitsune.NewKey("ttl-zero", 0)
	input := kitsune.FromSlice([]int{1})

	var gotValue int
	p := kitsune.MapWith(input, key, func(ctx context.Context, ref *kitsune.Ref[int], _ int) (int, error) {
		if err := ref.Set(ctx, 7); err != nil {
			return 0, err
		}
		v, err := ref.Get(ctx)
		if err != nil {
			return 0, err
		}
		gotValue = v
		return v, nil
	})
	if _, err := p.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotValue != 7 {
		t.Errorf("expected 7 (no TTL), got %d", gotValue)
	}
}

func TestStateTTL_UpdateResetsTimer(t *testing.T) {
	ttl := 30 * time.Millisecond
	key := kitsune.NewKey("ttl-update", 0, kitsune.StateTTL(ttl))
	input := kitsune.FromSlice([]int{1})

	var gotValue int
	p := kitsune.MapWith(input, key, func(ctx context.Context, ref *kitsune.Ref[int], _ int) (int, error) {
		if err := ref.Set(ctx, 5); err != nil {
			return 0, err
		}
		time.Sleep(ttl / 2)
		// Update; resets the TTL clock.
		if err := ref.Update(ctx, func(v int) (int, error) { return v + 1, nil }); err != nil {
			return 0, err
		}
		time.Sleep(ttl / 2)
		// Should NOT be expired.
		v, err := ref.Get(ctx)
		if err != nil {
			return 0, err
		}
		gotValue = v
		return v, nil
	})
	if _, err := p.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotValue != 6 {
		t.Errorf("expected 6 (after Update, not expired), got %d", gotValue)
	}
}

// ---------------------------------------------------------------------------
// 3l. Ref[T] API: item 3l from parity-gaps.md
// ---------------------------------------------------------------------------

func TestRefWithMemoryStore(t *testing.T) {
	// Get/Set/Update round-trip via MapWith (in-memory ref).
	ctx := context.Background()
	key := kitsune.NewKey[int]("mem", 0)
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.MapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
		if err := ref.Set(ctx, v*10); err != nil {
			return 0, err
		}
		return ref.Get(ctx)
	}).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Each item overwrites the ref, so we get 10, 20, 30.
	want := []int{10, 20, 30}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("item %d: got %d, want %d", i, got[i], w)
		}
	}
}

func TestRef_GetOrSet(t *testing.T) {
	// For store-backed refs, GetOrSet calls fn on the first access and returns
	// the cached value on subsequent calls.
	ctx := context.Background()
	key := kitsune.NewKey[string]("gos", "")
	p := kitsune.FromSlice([]int{1, 2, 3})
	calls := 0
	got, err := kitsune.MapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[string], v int) (string, error) {
		return ref.GetOrSet(ctx, func() (string, error) {
			calls++
			return "first", nil
		})
	}).Collect(ctx, kitsune.WithStore(kitsune.MemoryStore()))
	if err != nil {
		t.Fatal(err)
	}
	// fn should be called exactly once; all items return "first".
	if calls != 1 {
		t.Errorf("fn called %d times, want 1", calls)
	}
	for i, s := range got {
		if s != "first" {
			t.Errorf("item %d: got %q, want %q", i, s, "first")
		}
	}
}

func TestRef_UpdateAndGet(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("uag", 0)
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.MapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
		return ref.UpdateAndGet(ctx, func(acc int) (int, error) { return acc + v, nil })
	}).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Running sum: 1, 3, 6.
	want := []int{1, 3, 6}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("item %d: got %d, want %d", i, got[i], w)
		}
	}
}

func TestRef_StoreBacked(t *testing.T) {
	// Same Get/Set/Update semantics when backed by an explicit MemoryStore.
	ctx := context.Background()
	key := kitsune.NewKey[int]("sbref", 0)
	p := kitsune.FromSlice([]int{5, 10, 15})
	got, err := kitsune.MapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
		return ref.UpdateAndGet(ctx, func(acc int) (int, error) { return acc + v, nil })
	}).Collect(ctx, kitsune.WithStore(kitsune.MemoryStore()))
	if err != nil {
		t.Fatal(err)
	}
	// Running sum: 5, 15, 30.
	want := []int{5, 15, 30}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("item %d: got %d, want %d", i, got[i], w)
		}
	}
}

func TestMapWithKey_SameKeySharesState(t *testing.T) {
	// Two items with the same key increment the same counter.
	ctx := context.Background()
	countKey := kitsune.NewKey[int]("shared", 0)
	p := kitsune.FromSlice([]string{"x", "x", "y", "x"})
	got, err := kitsune.MapWithKey(p,
		func(s string) string { return s },
		countKey,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (int, error) {
			return ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
		},
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// "x" seen 3 times → counts 1, 2, 3; "y" seen once → 1.
	want := []int{1, 2, 1, 3}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("item %d: got %d, want %d", i, got[i], w)
		}
	}
}

func TestMapWithKey_StoreBacked(t *testing.T) {
	// MapWithKey with an explicit MemoryStore.
	ctx := context.Background()
	countKey := kitsune.NewKey[int]("sbkey", 0)
	p := kitsune.FromSlice([]string{"a", "b", "a"})
	got, err := kitsune.MapWithKey(p,
		func(s string) string { return s },
		countKey,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (int, error) {
			return ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
		},
	).Collect(ctx, kitsune.WithStore(kitsune.MemoryStore()))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 1, 2}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("item %d: got %d, want %d", i, got[i], w)
		}
	}
}

func TestMapWithKey_WithTTL(t *testing.T) {
	// A key with StateTTL expires between items. TTL is checked on Get, so we
	// Set the value on the first item, sleep past TTL, then Get on the second.
	ctx := context.Background()
	ttl := 20 * time.Millisecond
	key := kitsune.NewKey("ttlkey", 0, kitsune.StateTTL(ttl))
	p := kitsune.MapWithKey(
		kitsune.FromSlice([]int{1, 2}),
		func(_ int) string { return "k" },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
			if v == 1 {
				// Set to 99 for first item.
				if err := ref.Set(ctx, 99); err != nil {
					return 0, err
				}
				return ref.Get(ctx)
			}
			// Second item: sleep past TTL, then Get; should return initial value (0).
			time.Sleep(ttl * 2)
			return ref.Get(ctx)
		},
	)
	got, err := p.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	if got[0] != 99 {
		t.Errorf("first item: got %d, want 99", got[0])
	}
	// After TTL expiry, Get returns the initial value (0).
	if got[1] != 0 {
		t.Errorf("second item (after TTL expiry): got %d, want 0", got[1])
	}
}

// ---------------------------------------------------------------------------
// WithKeyTTL tests
// ---------------------------------------------------------------------------

func TestMapWithKey_KeyTTL_EvictsInactiveKey(t *testing.T) {
	// After the TTL elapses between items, the Ref for that key is evicted.
	// The next item for the same key starts from the initial value again.
	//
	// The sleep is placed inside the processing function so it executes while
	// the pipeline is running, ensuring the TTL genuinely expires between the
	// second and third calls to getRef.
	ctx := context.Background()
	ttl := 20 * time.Millisecond
	key := kitsune.NewKey[int]("keyttl-evict", 0)

	p := kitsune.MapWithKey(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(v int) string { return "k" },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
			if v == 2 {
				// Sleep past the TTL after the second item so that when item 3
				// calls getRef, the inactivity window has elapsed.
				time.Sleep(ttl * 3)
			}
			return ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
		},
		kitsune.WithKeyTTL(ttl),
	)

	got, err := p.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d: %v", len(got), got)
	}
	if got[0] != 1 {
		t.Errorf("item 0: got %d, want 1", got[0])
	}
	if got[1] != 2 {
		t.Errorf("item 1: got %d, want 2", got[1])
	}
	// After TTL eviction the counter resets to 0 and increments to 1.
	if got[2] != 1 {
		t.Errorf("item 2 (after key eviction): got %d, want 1", got[2])
	}
}

func TestMapWithKey_KeyTTL_KeepsActiveKey(t *testing.T) {
	// When items arrive more frequently than the TTL, the key is never evicted
	// and the counter keeps incrementing.
	ctx := context.Background()
	ttl := 100 * time.Millisecond
	key := kitsune.NewKey[int]("keyttl-keep", 0)

	p := kitsune.MapWithKey(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(v int) string { return "k" },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
			return ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
		},
		kitsune.WithKeyTTL(ttl),
	)

	got, err := p.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("item %d: got %d, want %d", i, got[i], w)
		}
	}
}

func TestMapWithKey_KeyTTL_ZeroMeansDisabled(t *testing.T) {
	// WithKeyTTL(0) disables eviction: the key persists across items regardless
	// of inactivity. The counter must keep accumulating even when time passes.
	ctx := context.Background()
	ttl := 20 * time.Millisecond
	key := kitsune.NewKey[int]("keyttl-zero", 0)

	p := kitsune.MapWithKey(
		kitsune.FromSlice([]int{1, 2}),
		func(v int) string { return "k" },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
			if v == 1 {
				// Simulate TTL*3 of inactivity between items.
				time.Sleep(ttl * 3)
			}
			return ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
		},
		kitsune.WithKeyTTL(0), // TTL explicitly disabled
	)

	got, err := p.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Counter must keep accumulating; no eviction despite the sleep.
	if got[0] != 1 || got[1] != 2 {
		t.Errorf("expected [1 2], got %v", got)
	}
}

func TestMapWithKey_DefaultKeyTTL_EvictsInactiveKey(t *testing.T) {
	// WithDefaultKeyTTL at the run level evicts keys that are inactive for
	// longer than the TTL, just like per-stage WithKeyTTL.
	ctx := context.Background()
	ttl := 20 * time.Millisecond
	key := kitsune.NewKey[int]("defkeyttl", 0)

	p := kitsune.MapWithKey(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(v int) string { return "k" },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
			if v == 2 {
				time.Sleep(ttl * 3)
			}
			return ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
		},
		// No WithKeyTTL here; rely on the run-level default below.
	)

	got, err := kitsune.Collect(ctx, p, kitsune.WithDefaultKeyTTL(ttl))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d: %v", len(got), got)
	}
	if got[0] != 1 || got[1] != 2 {
		t.Errorf("first two items: got %v, want [1 2]", got[:2])
	}
	if got[2] != 1 {
		t.Errorf("item 2 (after key eviction via default TTL): got %d, want 1", got[2])
	}
}

func TestMapWithKey_StageKeyTTL_OverridesDefault(t *testing.T) {
	// Per-stage WithKeyTTL(0) disables eviction even when WithDefaultKeyTTL
	// is set on the runner.
	ctx := context.Background()
	defaultTTL := 20 * time.Millisecond
	key := kitsune.NewKey[int]("keyttl-override", 0)

	p := kitsune.MapWithKey(
		kitsune.FromSlice([]int{1, 2}),
		func(v int) string { return "k" },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
			if v == 1 {
				// Simulate defaultTTL*3 of inactivity.
				time.Sleep(defaultTTL * 3)
			}
			return ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
		},
		kitsune.WithKeyTTL(0), // explicitly disable TTL for this stage
	)

	got, err := kitsune.Collect(ctx, p, kitsune.WithDefaultKeyTTL(defaultTTL))
	if err != nil {
		t.Fatal(err)
	}
	// Key must NOT be evicted because the per-stage WithKeyTTL(0) overrides the default.
	if got[0] != 1 || got[1] != 2 {
		t.Errorf("expected [1 2] (no eviction), got %v", got)
	}
}

func TestFlatMapWithKey_KeyTTL_EvictsInactiveKey(t *testing.T) {
	// WithKeyTTL also works on FlatMapWithKey: keys inactive for longer than
	// the TTL are evicted and the next access starts from the initial value.
	ctx := context.Background()
	ttl := 20 * time.Millisecond
	key := kitsune.NewKey[int]("fmwk-keyttl", 0)

	p := kitsune.FlatMapWithKey(
		kitsune.FromSlice([]string{"a", "a", "a"}),
		func(s string) string { return s },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], s string, yield func(int) error) error {
			n, err := ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
			if err != nil {
				return err
			}
			if n == 2 {
				// Sleep past the TTL after the second item.
				time.Sleep(ttl * 3)
			}
			return yield(n)
		},
		kitsune.WithKeyTTL(ttl),
	)

	got, err := p.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d: %v", len(got), got)
	}
	if got[0] != 1 || got[1] != 2 {
		t.Errorf("first two items: got %v, want [1 2]", got[:2])
	}
	if got[2] != 1 {
		t.Errorf("item 2 (after FlatMapWithKey key eviction): got %d, want 1", got[2])
	}
}

func TestFlatMapWithKey_MultipleOutputs(t *testing.T) {
	// FlatMapWithKey emitting multiple items per input item.
	ctx := context.Background()
	cntKey := kitsune.NewKey[int]("fmwk", 0)
	p := kitsune.FromSlice([]string{"a", "b", "a"})
	got, err := kitsune.FlatMapWithKey(p,
		func(s string) string { return s },
		cntKey,
		func(ctx context.Context, ref *kitsune.Ref[int], s string, yield func(string) error) error {
			n, err := ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
			if err != nil {
				return err
			}
			// Emit n copies of s.
			for i := 0; i < n; i++ {
				if err := yield(s); err != nil {
					return err
				}
			}
			return nil
		},
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// "a" first time → 1 copy; "b" first time → 1 copy; "a" second time → 2 copies.
	want := []string{"a", "b", "a", "a"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("item %d: got %q, want %q", i, got[i], w)
		}
	}
}

// ---------------------------------------------------------------------------
// OnError: all four operators
// ---------------------------------------------------------------------------

func TestMapWith_OnError_Skip(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("skip_test", 0)
	boom := fmt.Errorf("boom")
	calls := 0
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.MapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
		calls++
		if v == 2 {
			return 0, boom
		}
		return v * 10, nil
	}, kitsune.OnError(kitsune.ActionDrop())).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// item 2 skipped; 1 and 3 emitted.
	if len(got) != 2 || got[0] != 10 || got[1] != 30 {
		t.Errorf("got %v, want [10 30]", got)
	}
	if calls != 3 {
		t.Errorf("fn called %d times, want 3", calls)
	}
}

func TestMapWith_OnError_Halt(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("halt_test", 0)
	boom := fmt.Errorf("boom")
	p := kitsune.FromSlice([]int{1, 2, 3})
	_, err := kitsune.MapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
		if v == 2 {
			return 0, boom
		}
		return v, nil
	}).Collect(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFlatMapWith_OnError_Skip(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("fmw_skip", 0)
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.FlatMapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], v int, yield func(int) error) error {
		if v == 2 {
			return fmt.Errorf("skip me")
		}
		return yield(v * 10)
	}, kitsune.OnError(kitsune.ActionDrop())).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 10 || got[1] != 30 {
		t.Errorf("got %v, want [10 30]", got)
	}
}

func TestMapWithKey_OnError_Skip(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("mwk_skip", 0)
	p := kitsune.FromSlice([]string{"a", "b", "a"})
	got, err := kitsune.MapWithKey(p,
		func(s string) string { return s },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (string, error) {
			if s == "b" {
				return "", fmt.Errorf("skip b")
			}
			n, _ := ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
			return fmt.Sprintf("%s#%d", s, n), nil
		},
		kitsune.OnError(kitsune.ActionDrop()),
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// "b" skipped; "a" appears twice.
	if len(got) != 2 || got[0] != "a#1" || got[1] != "a#2" {
		t.Errorf("got %v, want [a#1 a#2]", got)
	}
}

func TestFlatMapWithKey_OnError_Skip(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("fmwk_skip", 0)
	p := kitsune.FromSlice([]string{"a", "b", "a"})
	got, err := kitsune.FlatMapWithKey(p,
		func(s string) string { return s },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], s string, yield func(string) error) error {
			if s == "b" {
				return fmt.Errorf("skip b")
			}
			return yield(s + "!")
		},
		kitsune.OnError(kitsune.ActionDrop()),
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a!" || got[1] != "a!" {
		t.Errorf("got %v, want [a! a!]", got)
	}
}

// ---------------------------------------------------------------------------
// Supervise: ref preserved across restart
// ---------------------------------------------------------------------------

func TestMapWith_Supervise_RestartOnError(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("sup_restart", 0)
	attempt := 0
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.MapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
		attempt++
		// Fail on the very first call; succeed on restart.
		if attempt == 1 {
			return 0, fmt.Errorf("first attempt fail")
		}
		n, _ := ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + v, nil })
		return n, nil
	}, kitsune.Supervise(kitsune.RestartOnError(3, nil))).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Item 1 was consumed from the channel before the error and is not re-delivered
	// after restart (Supervise restarts the stage loop, not individual items).
	// After restart: items 2,3 → running sum 2, 5.
	if len(got) != 2 {
		t.Fatalf("got %v (len %d), want 2 items", got, len(got))
	}
	if got[0] != 2 || got[1] != 5 {
		t.Errorf("got %v, want [2 5]", got)
	}
}

func TestMapWith_Supervise_RefPreservedAcrossRestart(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("sup_ref_preserved", 0)

	type state struct {
		setOnFirst bool
		refVal     int
	}
	var s state

	p := kitsune.FromSlice([]int{1, 2})
	_, err := kitsune.MapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
		if !s.setOnFirst {
			// First item: set ref to 42 then fail, triggering a restart.
			s.setOnFirst = true
			if setErr := ref.Set(ctx, 42); setErr != nil {
				return 0, setErr
			}
			return 0, fmt.Errorf("trigger restart")
		}
		// After restart: check ref still holds 42.
		val, _ := ref.Get(ctx)
		s.refVal = val
		return val, nil
	}, kitsune.Supervise(kitsune.RestartOnError(1, nil))).Collect(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.refVal != 42 {
		t.Errorf("ref after restart: got %d, want 42", s.refVal)
	}
}

func TestMapWithKey_Supervise_RestartOnError(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("mwk_sup", 0)
	attempt := 0
	p := kitsune.FromSlice([]string{"x", "y", "x"})
	got, err := kitsune.MapWithKey(p,
		func(s string) string { return s },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (string, error) {
			attempt++
			if attempt == 1 {
				return "", fmt.Errorf("trigger restart")
			}
			n, _ := ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
			return fmt.Sprintf("%s#%d", s, n), nil
		},
		kitsune.Supervise(kitsune.RestartOnError(3, nil)),
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The first item ("x") was consumed before the error. After restart, "y" and
	// the second "x" are processed. Supervise restarts the loop, not the item.
	if len(got) != 2 {
		t.Fatalf("got %v (len %d), want 2 items after restart", got, len(got))
	}
}

func TestMapWithKey_Supervise_KeyedRefPreserved(t *testing.T) {
	// Use two identical keys so the second occurrence is processed after restart,
	// allowing us to verify the Ref value survived.
	ctx := context.Background()
	key := kitsune.NewKey[int]("mwk_ref_preserved", 0)
	calls := 0
	p := kitsune.FromSlice([]string{"a", "a"}) // same key twice
	got, err := kitsune.MapWithKey(p,
		func(s string) string { return s },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (int, error) {
			calls++
			if calls == 1 {
				// Set "a"'s ref to 99, then fail, triggering restart.
				_ = ref.Set(ctx, 99)
				return 0, fmt.Errorf("trigger restart")
			}
			// After restart: the second "a" reads back the preserved ref.
			return ref.Get(ctx)
		},
		kitsune.Supervise(kitsune.RestartOnError(1, nil)),
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// First "a" was consumed before the error; after restart the second "a" is
	// processed and reads ref["a"] = 99 (preserved across the restart).
	if len(got) != 1 {
		t.Fatalf("got %v (len %d), want 1 item", got, len(got))
	}
	if got[0] != 99 {
		t.Errorf("ref value after restart: got %d, want 99", got[0])
	}
}

func TestMapWithKey_Supervise_KeyedRefPreservedOnPanic(t *testing.T) {
	// Contract: per-key Ref state is preserved across supervised restarts
	// within a single Run() call, regardless of whether the restart is
	// triggered by an error or a panic. The keyedRefMap is constructed once
	// in refRegistry.init() before any stage starts and is captured by the
	// stage's inner loop closure; internal.Supervise re-invokes that closure
	// on restart, so the map (and every Ref it holds) survives.
	//
	// State is NOT preserved across separate Run() calls with the default
	// in-memory MemoryStore: a new Run() builds a fresh refRegistry. For
	// cross-Run durability the caller must configure an external Store.
	ctx := context.Background()
	key := kitsune.NewKey[int]("mwk_ref_panic_preserved", 0)
	calls := 0
	p := kitsune.FromSlice([]string{"a", "a"}) // same key twice
	got, err := kitsune.MapWithKey(p,
		func(s string) string { return s },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (int, error) {
			calls++
			if calls == 1 {
				// Set "a"'s ref to 77, then panic: triggers supervised restart.
				_ = ref.Set(ctx, 77)
				panic("trigger restart via panic")
			}
			// After restart: the second "a" reads back the preserved ref.
			return ref.Get(ctx)
		},
		kitsune.Supervise(kitsune.RestartAlways(1, nil)),
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %v (len %d), want 1 item", got, len(got))
	}
	if got[0] != 77 {
		t.Errorf("ref value after panic restart: got %d, want 77", got[0])
	}
}

// ---------------------------------------------------------------------------
// MapWithKey: concurrency (key-sharded workers)
// ---------------------------------------------------------------------------

func TestMapWithKey_Concurrency_AllProcessed(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("mwk_conc", 0)
	n := 100
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	got, err := kitsune.MapWithKey(p,
		func(v int) string { return fmt.Sprintf("key%d", v%10) },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
			return v * 2, nil
		},
		kitsune.Concurrency(4),
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("got %d items, want %d", len(got), n)
	}
	sum := 0
	for _, v := range got {
		sum += v
	}
	wantSum := 0
	for i := range n {
		wantSum += i * 2
	}
	if sum != wantSum {
		t.Errorf("sum of results: got %d, want %d", sum, wantSum)
	}
}

func TestMapWithKey_Concurrency_KeyAffinity(t *testing.T) {
	// Items with the same key must accumulate into the same counter.
	// With 4 workers and key-sharding, "alice" and "bob" each map to one worker.
	ctx := context.Background()
	key := kitsune.NewKey[int]("mwk_affinity", 0)
	items := []string{"alice", "bob", "alice", "bob", "alice"}
	p := kitsune.FromSlice(items)
	got, err := kitsune.MapWithKey(p,
		func(s string) string { return s },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (int, error) {
			return ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
		},
		kitsune.Concurrency(4),
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Sort got to get final counts; alice appears 3 times, bob 2.
	sort.Ints(got)
	// Counts: alice[1,2,3], bob[1,2] → sorted: [1,1,2,2,3]
	want := []int{1, 1, 2, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("index %d: got %d, want %d", i, got[i], w)
		}
	}
}

func TestMapWithKey_Concurrency_Ordered(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("mwk_ordered", 0)
	n := 50
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	got, err := kitsune.MapWithKey(p,
		func(v int) string { return fmt.Sprintf("k%d", v%5) },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
			return v, nil
		},
		kitsune.Concurrency(3),
		kitsune.Ordered(),
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("got %d items, want %d", len(got), n)
	}
	// Ordered() must preserve input order.
	for i, v := range got {
		if v != items[i] {
			t.Errorf("position %d: got %d, want %d", i, v, items[i])
		}
	}
}

func TestMapWithKey_Concurrency_OnError_Skip(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("mwk_conc_skip", 0)
	items := []string{"a", "b", "a", "b", "a"}
	p := kitsune.FromSlice(items)
	got, err := kitsune.MapWithKey(p,
		func(s string) string { return s },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (string, error) {
			if s == "b" {
				return "", fmt.Errorf("skip b")
			}
			n, _ := ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
			return fmt.Sprintf("a#%d", n), nil
		},
		kitsune.Concurrency(2),
		kitsune.OnError(kitsune.ActionDrop()),
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Only "a" items survive: 3 of them.
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3: %v", len(got), got)
	}
}

// ---------------------------------------------------------------------------
// MapWith: concurrency (worker-local state)
// ---------------------------------------------------------------------------

func TestMapWith_Concurrency_AllProcessed(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("mw_conc", 0)
	n := 100
	items := make([]int, n)
	for i := range items {
		items[i] = i + 1
	}
	p := kitsune.FromSlice(items)
	got, err := kitsune.MapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
		return v * 2, nil
	}, kitsune.Concurrency(4)).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("got %d items, want %d", len(got), n)
	}
	sum := 0
	for _, v := range got {
		sum += v
	}
	wantSum := 0
	for i := range n {
		wantSum += (i + 1) * 2
	}
	if sum != wantSum {
		t.Errorf("sum: got %d, want %d", sum, wantSum)
	}
}

func TestMapWith_Concurrency_WorkerLocalState(t *testing.T) {
	// Each worker accumulates its own counter independently.
	// With n=4 workers, the sum of all workers' final counts equals len(items).
	ctx := context.Background()
	key := kitsune.NewKey[int]("mw_worker_local", 0)
	n := 40
	items := make([]int, n)
	for i := range items {
		items[i] = 1
	}
	p := kitsune.FromSlice(items)
	got, err := kitsune.MapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
		return ref.UpdateAndGet(ctx, func(acc int) (int, error) { return acc + v, nil })
	}, kitsune.Concurrency(4)).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("got %d items, want %d", len(got), n)
	}
	// All items were processed; individual counts are worker-local (not a global sum).
	// Just verify total items processed and no panics.
}

func TestMapWith_Concurrency_Ordered(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("mw_ordered", 0)
	n := 50
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	got, err := kitsune.MapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
		return v, nil
	}, kitsune.Concurrency(3), kitsune.Ordered()).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("got %d items, want %d", len(got), n)
	}
	for i, v := range got {
		if v != items[i] {
			t.Errorf("position %d: got %d, want %d", i, v, items[i])
		}
	}
}

// ---------------------------------------------------------------------------
// FlatMapWithKey: concurrency (key-sharded workers)
// ---------------------------------------------------------------------------

func TestFlatMapWithKey_Concurrency_AllProcessed(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("fmwk_conc", 0)
	items := []string{"a", "b", "a", "c", "b"}
	p := kitsune.FromSlice(items)
	got, err := kitsune.FlatMapWithKey(p,
		func(s string) string { return s },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], s string, yield func(string) error) error {
			n, _ := ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
			// Emit n copies.
			for i := 0; i < n; i++ {
				if err := yield(s); err != nil {
					return err
				}
			}
			return nil
		},
		kitsune.Concurrency(3),
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// a:1 b:1 a:2 c:1 b:2 → 1+1+2+1+2 = 7 items
	if len(got) != 7 {
		t.Fatalf("got %d items, want 7: %v", len(got), got)
	}
}

func TestFlatMapWithKey_Concurrency_Ordered(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("fmwk_ord", 0)
	items := []string{"a", "b", "c"}
	p := kitsune.FromSlice(items)
	got, err := kitsune.FlatMapWithKey(p,
		func(s string) string { return s },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], s string, yield func(string) error) error {
			if err := yield(s + "1"); err != nil {
				return err
			}
			return yield(s + "2")
		},
		kitsune.Concurrency(3),
		kitsune.Ordered(),
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a1", "a2", "b1", "b2", "c1", "c2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("position %d: got %q, want %q", i, got[i], w)
		}
	}
}

// ---------------------------------------------------------------------------
// FlatMapWith: concurrency (worker-local state)
// ---------------------------------------------------------------------------

func TestFlatMapWith_Concurrency_AllProcessed(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("fmw_conc", 0)
	n := 30
	items := make([]int, n)
	for i := range items {
		items[i] = i + 1
	}
	p := kitsune.FromSlice(items)
	got, err := kitsune.FlatMapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], v int, yield func(int) error) error {
		if err := yield(v); err != nil {
			return err
		}
		return yield(v * -1)
	}, kitsune.Concurrency(3)).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n*2 {
		t.Fatalf("got %d items, want %d", len(got), n*2)
	}
}

func TestFlatMapWith_Concurrency_Ordered(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("fmw_ord", 0)
	items := []int{1, 2, 3}
	p := kitsune.FromSlice(items)
	got, err := kitsune.FlatMapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], v int, yield func(int) error) error {
		if err := yield(v * 10); err != nil {
			return err
		}
		return yield(v * 100)
	}, kitsune.Concurrency(3), kitsune.Ordered()).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{10, 100, 20, 200, 30, 300}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("position %d: got %d, want %d", i, got[i], w)
		}
	}
}

// ---------------------------------------------------------------------------
// Race detector: run with -race to validate lock-free key-sharding
// ---------------------------------------------------------------------------

func TestMapWithKey_Concurrency_Race(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("race_key", 0)
	n := 500
	items := make([]string, n)
	keys := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	for i := range items {
		items[i] = keys[i%len(keys)]
	}
	p := kitsune.FromSlice(items)
	_, err := kitsune.MapWithKey(p,
		func(s string) string { return s },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (int, error) {
			return ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
		},
		kitsune.Concurrency(4),
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
}

func TestMapWith_Concurrency_Race(t *testing.T) {
	ctx := context.Background()
	key := kitsune.NewKey[int]("mw_race", 0)
	n := 500
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	_, err := kitsune.MapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
		return ref.UpdateAndGet(ctx, func(acc int) (int, error) { return acc + 1, nil })
	}, kitsune.Concurrency(4)).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
}
