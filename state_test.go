package kitsune_test

import (
	"context"
	"fmt"
	"sort"
	"testing"

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
		if pair.Second != db[pair.First] {
			t.Errorf("id %d: got %q, want %q", pair.First, pair.Second, db[pair.First])
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

// ---------------------------------------------------------------------------
// Lift alias
// ---------------------------------------------------------------------------

func TestLiftAlias(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.Collect(ctx, kitsune.Map(p, kitsune.Lift(func(n int) (int, error) {
		return n * 2, nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != 2 || got[1] != 4 || got[2] != 6 {
		t.Fatalf("got %v", got)
	}
}
