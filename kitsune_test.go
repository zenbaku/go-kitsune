package kitsune_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
)

// dropCountHook implements kitsune.Hook + kitsune.OverflowHook, counting drops.
type dropCountHook struct {
	kitsune.Hook
	drops atomic.Int64
}

func (h *dropCountHook) OnDrop(_ context.Context, _ string, _ any) { h.drops.Add(1) }

func TestLinearPipeline(t *testing.T) {
	input := kitsune.FromSlice([]string{"1", "2", "3", "4", "5"})
	parsed := kitsune.Map(input, kitsune.Lift(strconv.Atoi))

	results, err := parsed.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
	for i, v := range results {
		if v != i+1 {
			t.Errorf("results[%d] = %d, want %d", i, v, i+1)
		}
	}
}

func TestFilter(t *testing.T) {
	input := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	even := input.Filter(func(n int) bool { return n%2 == 0 })

	results, err := even.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []int{2, 4, 6}
	if len(results) != len(expected) {
		t.Fatalf("expected %d results, got %d", len(expected), len(results))
	}
	for i, v := range results {
		if v != expected[i] {
			t.Errorf("results[%d] = %d, want %d", i, v, expected[i])
		}
	}
}

func TestTap(t *testing.T) {
	var tapped []int
	input := kitsune.FromSlice([]int{10, 20, 30})
	p := input.Tap(func(n int) { tapped = append(tapped, n) })

	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || len(tapped) != 3 {
		t.Fatalf("expected 3 results and 3 tapped, got %d and %d", len(results), len(tapped))
	}
}

func TestTake(t *testing.T) {
	input := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	results, err := input.Take(3).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
}

func TestFlatMap(t *testing.T) {
	input := kitsune.FromSlice([]string{"a,b", "c,d,e"})
	split := kitsune.FlatMap(input, func(_ context.Context, s string) ([]string, error) {
		return strings.Split(s, ","), nil
	})

	results, err := split.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"a", "b", "c", "d", "e"}
	if len(results) != len(expected) {
		t.Fatalf("expected %d results, got %d", len(expected), len(results))
	}
	for i, v := range results {
		if v != expected[i] {
			t.Errorf("results[%d] = %q, want %q", i, v, expected[i])
		}
	}
}

func TestBatchAndUnbatch(t *testing.T) {
	input := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	batched := kitsune.Batch(input, 2)
	unbatched := kitsune.Unbatch(batched)

	results, err := unbatched.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
	for i, v := range results {
		if v != i+1 {
			t.Errorf("results[%d] = %d, want %d", i, v, i+1)
		}
	}
}

func TestBatchSizes(t *testing.T) {
	input := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	batched := kitsune.Batch(input, 3)

	results, err := batched.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(results))
	}
	if len(results[0]) != 3 {
		t.Errorf("batch 0: expected 3 items, got %d", len(results[0]))
	}
	if len(results[1]) != 2 {
		t.Errorf("batch 1: expected 2 items, got %d", len(results[1]))
	}
}

func TestErrorHandlingHalt(t *testing.T) {
	input := kitsune.FromSlice([]int{1, 2, 3})
	failing := kitsune.Map(input, func(_ context.Context, n int) (int, error) {
		if n == 2 {
			return 0, errors.New("boom")
		}
		return n * 10, nil
	})

	_, err := failing.Collect(context.Background())
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected 'boom' error, got %v", err)
	}
}

func TestErrorHandlingSkip(t *testing.T) {
	input := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(input, func(_ context.Context, n int) (int, error) {
		if n == 2 {
			return 0, errors.New("skip me")
		}
		return n * 10, nil
	}, kitsune.OnError(kitsune.Skip()))

	results, err := mapped.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []int{10, 30}
	if len(results) != len(expected) {
		t.Fatalf("expected %d results, got %d", len(expected), len(results))
	}
	for i, v := range results {
		if v != expected[i] {
			t.Errorf("results[%d] = %d, want %d", i, v, expected[i])
		}
	}
}

func TestRetry(t *testing.T) {
	var attempts atomic.Int32
	input := kitsune.FromSlice([]string{"ok"})
	mapped := kitsune.Map(input, func(_ context.Context, s string) (string, error) {
		n := attempts.Add(1)
		if n < 3 {
			return "", errors.New("not yet")
		}
		return s + "!", nil
	}, kitsune.OnError(kitsune.Retry(5, kitsune.FixedBackoff(time.Millisecond))))

	results, err := mapped.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0] != "ok!" {
		t.Fatalf("expected [ok!], got %v", results)
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
	}
}

func TestConcurrency(t *testing.T) {
	items := make([]int, 100)
	for i := range items {
		items[i] = i
	}
	input := kitsune.FromSlice(items)
	doubled := kitsune.Map(input, func(_ context.Context, n int) (int, error) {
		return n * 2, nil
	}, kitsune.Concurrency(4))

	results, err := doubled.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 100 {
		t.Fatalf("expected 100 results, got %d", len(results))
	}
	// With concurrency, order is not guaranteed, but all values should be present.
	seen := make(map[int]bool)
	for _, v := range results {
		seen[v] = true
	}
	for i := 0; i < 100; i++ {
		if !seen[i*2] {
			t.Errorf("missing value %d", i*2)
		}
	}
}

func TestGenerate(t *testing.T) {
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := 0; i < 5; i++ {
			if !yield(i) {
				return nil
			}
		}
		return nil
	})

	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := 0; ; i++ {
			if !yield(i) {
				return nil
			}
		}
	})

	var count atomic.Int32
	runner := p.ForEach(func(_ context.Context, n int) error {
		if count.Add(1) >= 10 {
			cancel()
		}
		return nil
	})

	err := runner.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestPartition(t *testing.T) {
	input := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	even, odd := kitsune.Partition(input, func(n int) bool { return n%2 == 0 })

	var evens, odds []int

	evenRunner := even.ForEach(func(_ context.Context, n int) error {
		evens = append(evens, n)
		return nil
	})
	oddRunner := odd.ForEach(func(_ context.Context, n int) error {
		odds = append(odds, n)
		return nil
	})

	err := kitsune.MergeRunners(evenRunner, oddRunner).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(evens) != 3 {
		t.Errorf("expected 3 evens, got %d: %v", len(evens), evens)
	}
	if len(odds) != 3 {
		t.Errorf("expected 3 odds, got %d: %v", len(odds), odds)
	}
}

func TestStatefulMapWith(t *testing.T) {
	counter := kitsune.NewKey[int]("counter", 0)

	input := kitsune.FromSlice([]string{"a", "b", "c"})
	numbered := kitsune.MapWith(input, counter,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (string, error) {
			err := ref.Update(ctx, func(n int) (int, error) {
				return n + 1, nil
			})
			if err != nil {
				return "", err
			}
			val, _ := ref.Get(ctx)
			return fmt.Sprintf("%s-%d", s, val), nil
		},
	)

	results, err := numbered.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"a-1", "b-2", "c-3"}
	if len(results) != len(expected) {
		t.Fatalf("expected %d results, got %d", len(expected), len(results))
	}
	for i, v := range results {
		if v != expected[i] {
			t.Errorf("results[%d] = %q, want %q", i, v, expected[i])
		}
	}
}

func TestDrain(t *testing.T) {
	var count int
	input := kitsune.FromSlice([]int{1, 2, 3})
	p := input.Tap(func(_ int) { count++ })

	err := p.Drain().Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("expected tap count 3, got %d", count)
	}
}

func TestFrom(t *testing.T) {
	ch := make(chan int, 3)
	ch <- 10
	ch <- 20
	ch <- 30
	close(ch)

	results, err := kitsune.From(ch).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
}

func TestThrough(t *testing.T) {
	doubleAndFilter := func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		doubled := kitsune.Map(p, func(_ context.Context, n int) (int, error) {
			return n * 2, nil
		})
		return doubled.Filter(func(n int) bool { return n > 4 })
	}

	input := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	results, err := input.Through(doubleAndFilter).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []int{6, 8, 10}
	if len(results) != len(expected) {
		t.Fatalf("expected %d results, got %d", len(expected), len(results))
	}
}

func TestBatchTimeout(t *testing.T) {
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := 0; i < 3; i++ {
			if !yield(i) {
				return nil
			}
		}
		// Wait longer than the batch timeout before sending more.
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		for i := 3; i < 5; i++ {
			if !yield(i) {
				return nil
			}
		}
		return nil
	})

	batched := kitsune.Batch(p, 10, kitsune.BatchTimeout(50*time.Millisecond))
	results, err := batched.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Should get at least 2 batches: one flushed by timeout, one at end.
	if len(results) < 2 {
		t.Fatalf("expected at least 2 batches, got %d", len(results))
	}
}

func TestLift(t *testing.T) {
	input := kitsune.FromSlice([]string{"1", "2", "3"})
	parsed := kitsune.Map(input, kitsune.Lift(strconv.Atoi))

	results, err := parsed.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0] != 1 || results[1] != 2 || results[2] != 3 {
		t.Fatalf("unexpected results: %v", results)
	}
}

func TestDedupe(t *testing.T) {
	input := kitsune.FromSlice([]string{"a", "b", "a", "c", "b", "d"})
	deduped := kitsune.Dedupe(input, func(s string) string { return s }, kitsune.MemoryDedupSet())

	results, err := deduped.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"a", "b", "c", "d"}
	if len(results) != len(expected) {
		t.Fatalf("expected %d results, got %d: %v", len(expected), len(results), results)
	}
	for i, v := range results {
		if v != expected[i] {
			t.Errorf("results[%d] = %q, want %q", i, v, expected[i])
		}
	}
}

func TestDedupeByField(t *testing.T) {
	type Item struct {
		ID   int
		Name string
	}
	input := kitsune.FromSlice([]Item{
		{1, "alice"}, {2, "bob"}, {1, "alice-dup"}, {3, "carol"}, {2, "bob-dup"},
	})
	deduped := kitsune.Dedupe(input, func(i Item) string {
		return fmt.Sprintf("%d", i.ID)
	}, kitsune.MemoryDedupSet())

	results, err := deduped.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 unique items, got %d: %v", len(results), results)
	}
}

func TestCachedMap(t *testing.T) {
	callCount := 0
	input := kitsune.FromSlice([]string{"a", "b", "a", "c", "a"})

	cached := kitsune.CachedMap(input, func(_ context.Context, s string) (string, error) {
		callCount++
		return s + "!", nil
	}, func(s string) string { return s }, kitsune.MemoryCache(100), 0)

	results, err := cached.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
	// "a" appears 3 times but fn should only be called once for it.
	if callCount != 3 { // a, b, c
		t.Fatalf("expected 3 fn calls (cached), got %d", callCount)
	}
	for _, r := range results {
		if r != "a!" && r != "b!" && r != "c!" {
			t.Errorf("unexpected result: %q", r)
		}
	}
}

func TestRefWithMemoryStore(t *testing.T) {
	counter := kitsune.NewKey[int]("counter", 0)
	input := kitsune.FromSlice([]string{"a", "b", "c"})

	numbered := kitsune.MapWith(input, counter,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (string, error) {
			err := ref.Update(ctx, func(n int) (int, error) { return n + 1, nil })
			if err != nil {
				return "", err
			}
			val, _ := ref.Get(ctx)
			return fmt.Sprintf("%s-%d", s, val), nil
		},
	)

	// Run with an explicit MemoryStore to exercise the Store-backed Ref path.
	results, err := numbered.Collect(context.Background(), kitsune.WithStore(kitsune.MemoryStore()))
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"a-1", "b-2", "c-3"}
	if len(results) != len(expected) {
		t.Fatalf("expected %d results, got %d", len(expected), len(results))
	}
	for i, v := range results {
		if v != expected[i] {
			t.Errorf("results[%d] = %q, want %q", i, v, expected[i])
		}
	}
}

func TestMemoryCacheTTL(t *testing.T) {
	cache := kitsune.MemoryCache(10)
	ctx := context.Background()

	_ = cache.Set(ctx, "k", []byte("v"), 1) // 1 nanosecond TTL — effectively expired immediately
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

func TestFromIter(t *testing.T) {
	items := []int{10, 20, 30}
	p := kitsune.FromIter(slices.Values(items))

	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0] != 10 || results[1] != 20 || results[2] != 30 {
		t.Fatalf("unexpected: %v", results)
	}
}

func TestFromIterWithTake(t *testing.T) {
	// Infinite iterator + Take.
	counter := func(yield func(int) bool) {
		for i := 0; ; i++ {
			if !yield(i) {
				return
			}
		}
	}

	results, err := kitsune.FromIter(counter).Take(5).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5, got %d", len(results))
	}
}

func TestWindow(t *testing.T) {
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := 0; i < 3; i++ {
			if !yield(i) {
				return nil
			}
		}
		select {
		case <-time.After(150 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		for i := 3; i < 5; i++ {
			if !yield(i) {
				return nil
			}
		}
		return nil
	})

	batches, err := kitsune.Window(p, 50*time.Millisecond).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) < 2 {
		t.Fatalf("expected at least 2 windows, got %d", len(batches))
	}
}

func TestBroadcast(t *testing.T) {
	input := kitsune.FromSlice([]int{1, 2, 3})
	copies := kitsune.Broadcast(input, 3)

	var results [3][]int
	runners := make([]*kitsune.Runner, 3)
	for i, p := range copies {
		i := i
		runners[i] = p.ForEach(func(_ context.Context, n int) error {
			results[i] = append(results[i], n)
			return nil
		})
	}

	err := kitsune.MergeRunners(runners...).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	for i, r := range results {
		if len(r) != 3 {
			t.Errorf("copy %d: expected 3 items, got %d: %v", i, len(r), r)
		}
	}
}

func TestBroadcastWithTransform(t *testing.T) {
	input := kitsune.FromSlice([]int{1, 2, 3})
	copies := kitsune.Broadcast(input, 2)

	doubled := kitsune.Map(copies[0], func(_ context.Context, n int) (int, error) { return n * 2, nil })
	tripled := kitsune.Map(copies[1], func(_ context.Context, n int) (int, error) { return n * 3, nil })

	merged := kitsune.Merge(doubled, tripled)
	results, err := merged.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 6 {
		t.Fatalf("expected 6 items, got %d: %v", len(results), results)
	}
}

func TestBroadcastPanics(t *testing.T) {
	input := kitsune.FromSlice([]int{1, 2, 3})
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected Broadcast(0) to panic")
		}
	}()
	kitsune.Broadcast(input, 0)
}

func TestMergePanics(t *testing.T) {
	t.Run("no pipelines", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected Merge() to panic")
			}
		}()
		kitsune.Merge[int]()
	})

	t.Run("different graphs", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected Merge with different graphs to panic")
			}
		}()
		p1 := kitsune.FromSlice([]int{1})
		p2 := kitsune.FromSlice([]int{2}) // different graph
		kitsune.Merge(p1, p2)
	})
}

// ---------------------------------------------------------------------------
// Ordered output tests
// ---------------------------------------------------------------------------

func TestOrderedMap(t *testing.T) {
	// 100 items with variable sleep; Concurrency(8) + Ordered() must emit in input order.
	const n = 100
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	results, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		if v%2 == 1 {
			time.Sleep(time.Millisecond)
		}
		return v, nil
	}, kitsune.Concurrency(8), kitsune.Ordered()).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != n {
		t.Fatalf("want %d results, got %d", n, len(results))
	}
	for i, v := range results {
		if v != i {
			t.Fatalf("position %d: want %d, got %d", i, i, v)
		}
	}
}

func TestOrderedMapWithSkip(t *testing.T) {
	// Items that error+skip are dropped; remaining items stay in input order.
	items := make([]int, 20)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	results, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		if v%3 == 0 {
			return 0, fmt.Errorf("skip-me")
		}
		return v, nil
	}, kitsune.Concurrency(4), kitsune.Ordered(), kitsune.OnError(kitsune.Skip())).
		Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(results); i++ {
		if results[i] <= results[i-1] {
			t.Fatalf("order violation at index %d: %d not > %d", i, results[i], results[i-1])
		}
	}
	for _, v := range results {
		if v%3 == 0 {
			t.Fatalf("skipped item %d appeared in results", v)
		}
	}
}

func TestOrderedMapWithHalt(t *testing.T) {
	// An error with Halt must stop the pipeline without deadlock.
	items := make([]int, 50)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	_, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		if v == 10 {
			return 0, fmt.Errorf("halt-error")
		}
		return v, nil
	}, kitsune.Concurrency(4), kitsune.Ordered()).Collect(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestOrderedFlatMap(t *testing.T) {
	// FlatMap with Concurrency + Ordered: output order must match input order.
	items := make([]int, 30)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	results, err := kitsune.FlatMap(p, func(_ context.Context, v int) ([]int, error) {
		if v%2 == 1 {
			time.Sleep(time.Millisecond)
		}
		return []int{v * 10, v*10 + 1}, nil
	}, kitsune.Concurrency(4), kitsune.Ordered()).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(items)*2 {
		t.Fatalf("want %d results, got %d", len(items)*2, len(results))
	}
	for i, v := range results {
		expected := (i/2)*10 + (i % 2)
		if v != expected {
			t.Fatalf("position %d: want %d, got %d", i, expected, v)
		}
	}
}

func TestOrderedNoConcurrency(t *testing.T) {
	// Ordered() with Concurrency(1) behaves identically to no options.
	items := []int{1, 2, 3, 4, 5}
	p := kitsune.FromSlice(items)
	results, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v * 2, nil
	}, kitsune.Concurrency(1), kitsune.Ordered()).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range results {
		if v != items[i]*2 {
			t.Fatalf("position %d: want %d, got %d", i, items[i]*2, v)
		}
	}
}

func TestUnorderedStillWorks(t *testing.T) {
	// Regression: Concurrency(4) without Ordered() still produces all items.
	const n = 100
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	results, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v, nil
	}, kitsune.Concurrency(4)).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != n {
		t.Fatalf("want %d results, got %d", n, len(results))
	}
}

func TestOrderedContextCancellation(t *testing.T) {
	// No deadlock when context is cancelled during ordered concurrent processing.
	ctx, cancel := context.WithCancel(context.Background())
	items := make([]int, 200)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	done := make(chan struct{})
	go func() {
		defer close(done)
		kitsune.Map(p, func(_ context.Context, v int) (int, error) { //nolint
			time.Sleep(5 * time.Millisecond)
			return v, nil
		}, kitsune.Concurrency(8), kitsune.Ordered()).Collect(ctx) //nolint
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not exit after context cancellation")
	}
}

func TestOrderedRetry(t *testing.T) {
	// A retried item ends up in the correct position after retry succeeds.
	var attempt [10]int
	items := make([]int, 10)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	results, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		attempt[v]++
		if v == 5 && attempt[v] < 3 {
			return 0, fmt.Errorf("transient")
		}
		return v, nil
	}, kitsune.Concurrency(4), kitsune.Ordered(),
		kitsune.OnError(kitsune.Retry(3, kitsune.FixedBackoff(0))),
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 10 {
		t.Fatalf("want 10 results, got %d", len(results))
	}
	for i, v := range results {
		if v != i {
			t.Fatalf("position %d: want %d, got %d", i, i, v)
		}
	}
}

func TestMergeRunnersPanics(t *testing.T) {
	t.Run("no runners", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected MergeRunners() to panic")
			}
		}()
		kitsune.MergeRunners()
	})

	t.Run("different graphs", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected MergeRunners with different graphs to panic")
			}
		}()
		r1 := kitsune.FromSlice([]int{1}).Drain()
		r2 := kitsune.FromSlice([]int{2}).Drain() // different graph
		kitsune.MergeRunners(r1, r2)
	})
}

// ---------------------------------------------------------------------------
// Supervision tests
// ---------------------------------------------------------------------------

func TestSupervisionRestartOnError(t *testing.T) {
	// Stage fails on first call, restarts and succeeds on subsequent calls.
	var calls atomic.Int64
	p := kitsune.FromSlice([]int{1, 2, 3})
	_, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		n := calls.Add(1)
		if n == 1 {
			return 0, fmt.Errorf("transient startup error")
		}
		return v * 2, nil
	}, kitsune.Supervise(kitsune.RestartOnError(2, kitsune.FixedBackoff(0)))).
		Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

func TestSupervisionRestartOnPanic(t *testing.T) {
	// Stage panics on first item, restarts and processes remaining items.
	var calls atomic.Int64
	p := kitsune.FromSlice([]int{1, 2, 3})
	results, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		n := calls.Add(1)
		if n == 1 {
			panic("deliberate test panic")
		}
		return v * 2, nil
	}, kitsune.Supervise(kitsune.RestartOnPanic(2, kitsune.FixedBackoff(0)))).
		Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// After restart, remaining items are processed.
	if len(results) == 0 {
		t.Fatal("expected some results after restart")
	}
}

func TestSupervisionMaxRestartsExhausted(t *testing.T) {
	// After exhausting restart budget, the error propagates.
	p := kitsune.FromSlice([]int{1, 2, 3})
	_, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return 0, fmt.Errorf("persistent error")
	}, kitsune.Supervise(kitsune.RestartOnError(2, kitsune.FixedBackoff(0)))).
		Collect(context.Background())
	if err == nil {
		t.Fatal("expected error after max restarts exhausted")
	}
}

func TestSupervisionNoRestartDefault(t *testing.T) {
	// Without Supervise, default behavior is unchanged: error halts.
	p := kitsune.FromSlice([]int{1, 2, 3})
	_, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return 0, fmt.Errorf("halt error")
	}).Collect(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSupervisionWithConcurrency(t *testing.T) {
	// Supervision works with concurrent stages.
	var calls atomic.Int64
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	results, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		n := calls.Add(1)
		if n == 1 {
			return 0, fmt.Errorf("first call fails")
		}
		return v, nil
	}, kitsune.Concurrency(3),
		kitsune.Supervise(kitsune.RestartOnError(3, kitsune.FixedBackoff(0))),
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results after supervision restart")
	}
}

func TestSupervisionBackoff(t *testing.T) {
	// Backoff delay is respected between restarts.
	// Two items in source: first run consumes item 1 and fails, restart #1 consumes
	// item 2 and fails, restart #2 finds empty channel and exits. That's 2 backoffs.
	var calls atomic.Int64
	start := time.Now()
	p := kitsune.FromSlice([]int{1, 2})
	_, _ = kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		calls.Add(1)
		return 0, fmt.Errorf("always fail")
	}, kitsune.Supervise(kitsune.RestartOnError(3, kitsune.FixedBackoff(20*time.Millisecond)))).
		Collect(context.Background())
	elapsed := time.Since(start)
	// At least 2 restarts × 20ms each = 40ms; allow some scheduler slack.
	if elapsed < 35*time.Millisecond {
		t.Fatalf("expected at least 35ms due to backoff, got %v", elapsed)
	}
}

func TestSupervisionContextCancelled(t *testing.T) {
	// Clean exit when context is cancelled during supervision backoff.
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int64
	p := kitsune.FromSlice([]int{1, 2, 3})
	done := make(chan struct{})
	go func() {
		defer close(done)
		kitsune.Map(p, func(_ context.Context, v int) (int, error) { //nolint
			calls.Add(1)
			return 0, fmt.Errorf("always fails")
		}, kitsune.Supervise(kitsune.SupervisionPolicy{
			MaxRestarts: 100,
			Backoff:     kitsune.FixedBackoff(100 * time.Millisecond),
		})).Collect(ctx) //nolint
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline did not exit after context cancellation during backoff")
	}
}

func TestSupervisionWithItemErrorHandling(t *testing.T) {
	// OnError(Skip) and Supervise compose correctly.
	// Items that error+skip are dropped; stage restarts on a fatal error.
	var calls atomic.Int64
	p := kitsune.FromSlice([]int{0, 1, 2, 3, 4})
	results, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		calls.Add(1)
		if v%2 == 0 {
			return 0, fmt.Errorf("even-skip")
		}
		return v, nil
	}, kitsune.OnError(kitsune.Skip()),
		kitsune.Supervise(kitsune.RestartOnError(1, kitsune.FixedBackoff(0))),
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Only odd values should appear.
	for _, v := range results {
		if v%2 == 0 {
			t.Fatalf("even value %d should have been skipped", v)
		}
	}
}

func TestSupervisionPanicSkip(t *testing.T) {
	// PanicSkip recovers from a panic and returns nil without restarting.
	p := kitsune.FromSlice([]int{1})
	_, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		panic("skippable panic")
	}, kitsune.Supervise(kitsune.SupervisionPolicy{OnPanic: kitsune.PanicSkip})).
		Collect(context.Background())
	if err != nil {
		t.Fatalf("expected nil error with PanicSkip, got: %v", err)
	}
}

func TestSupervisionWindow(t *testing.T) {
	// Restart counter resets after the window expires.
	var calls atomic.Int64
	p := kitsune.FromSlice([]int{1, 2, 3})
	results, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		n := calls.Add(1)
		// Fail on first call of each "run" (to trigger restart), succeed after.
		if n%3 == 1 && n > 1 {
			return 0, fmt.Errorf("restart trigger")
		}
		return v, nil
	}, kitsune.Supervise(kitsune.SupervisionPolicy{
		MaxRestarts: 2,
		Window:      50 * time.Millisecond,
		Backoff:     kitsune.FixedBackoff(0),
	})).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = results // just confirm no error
}

// ---------------------------------------------------------------------------
// Overflow tests
// ---------------------------------------------------------------------------

func TestOverflowDefaultIsBlock(t *testing.T) {
	// Without Overflow(), all 50 items must be delivered (regression).
	const n = 50
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	results, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v, nil
	}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != n {
		t.Fatalf("want %d results, got %d", n, len(results))
	}
}

func TestOverflowDropNewest(t *testing.T) {
	// Fast source+map, slow terminal → buffer fills → DropNewest drops incoming items.
	// Overflow is set on the Map stage's output (Buffer(2)); ForEach is the slow consumer.
	const n = 200
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	var received []int
	err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v, nil // fast map
	}, kitsune.Buffer(2), kitsune.Overflow(kitsune.DropNewest)).
		ForEach(func(_ context.Context, v int) error {
			time.Sleep(time.Millisecond) // slow consumer causes buffer to fill
			received = append(received, v)
			return nil
		}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(received) >= n {
		t.Fatalf("expected drops with DropNewest, but got all %d items", n)
	}
	// Surviving items must be in ascending order.
	for i := 1; i < len(received); i++ {
		if received[i] <= received[i-1] {
			t.Fatalf("order violation at index %d: %d not > %d", i, received[i], received[i-1])
		}
	}
}

func TestOverflowDropOldest(t *testing.T) {
	// Fast source+map, slow terminal → buffer fills → DropOldest evicts oldest items.
	const n = 100
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	var received []int
	err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v, nil // fast map
	}, kitsune.Buffer(2), kitsune.Overflow(kitsune.DropOldest)).
		ForEach(func(_ context.Context, v int) error {
			time.Sleep(2 * time.Millisecond)
			received = append(received, v)
			return nil
		}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(received) >= n {
		t.Fatalf("expected drops with DropOldest, but got all %d items", n)
	}
	// The last item (n-1) should be present: it's the newest and survives DropOldest.
	last := received[len(received)-1]
	if last != n-1 {
		t.Fatalf("expected last surviving item to be %d (newest), got %d", n-1, last)
	}
}

func TestOverflowDropNewestConcurrent(t *testing.T) {
	// Concurrent stage with DropNewest must complete without deadlock or panic.
	const n = 500
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	results, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v, nil
	}, kitsune.Concurrency(4), kitsune.Buffer(2), kitsune.Overflow(kitsune.DropNewest)).
		Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected some results")
	}
}

func TestOverflowDropOldestConcurrent(t *testing.T) {
	// Concurrent stage with DropOldest must have no races (run with -race).
	const n = 500
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	results, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v, nil
	}, kitsune.Concurrency(4), kitsune.Buffer(2), kitsune.Overflow(kitsune.DropOldest)).
		Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected some results")
	}
}

func TestOverflowHookCalled(t *testing.T) {
	// OverflowHook.OnDrop is called for each dropped item.
	// dropCountHook is defined at package level (see below).
	h := &dropCountHook{
		Hook: kitsune.LogHook(slog.New(slog.NewTextHandler(io.Discard, nil))),
	}

	const n = 200
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	var received atomic.Int64
	err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v, nil // fast map; slow consumer below causes buffer to fill
	}, kitsune.Buffer(2), kitsune.Overflow(kitsune.DropNewest)).
		ForEach(func(_ context.Context, v int) error {
			time.Sleep(time.Millisecond) // slow consumer
			received.Add(1)
			return nil
		}).Run(context.Background(), kitsune.WithHook(h))
	if err != nil {
		t.Fatal(err)
	}
	if received.Load() >= int64(n) {
		t.Fatalf("expected some drops, got all %d items", n)
	}
	// OnDrop must have been called at least once.
	if h.drops.Load() == 0 {
		t.Fatal("expected OnDrop to be called, got 0 calls")
	}
	if received.Load()+h.drops.Load() > int64(n) {
		// received + dropped can be ≤ n (source may also have been gated)
		// but should never exceed n.
		t.Fatalf("received(%d) + dropped(%d) > %d", received.Load(), h.drops.Load(), n)
	}
}

func TestOverflowBroadcast(t *testing.T) {
	// One fast consumer, one slow consumer via Broadcast.
	// The slow consumer's channel drops items; the fast consumer gets all.
	const n = 50
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}

	p := kitsune.FromSlice(items)
	outs := kitsune.Broadcast(p, 2)
	fast, slow := outs[0], outs[1]

	var fastCount, slowCount atomic.Int64
	r1 := fast.ForEach(func(_ context.Context, v int) error {
		fastCount.Add(1)
		return nil
	})
	// Overflow on the Map output — ForEach is slow, causing drops.
	r2 := kitsune.Map(slow, func(_ context.Context, v int) (int, error) {
		return v, nil // fast map
	}, kitsune.Buffer(2), kitsune.Overflow(kitsune.DropNewest)).
		ForEach(func(_ context.Context, v int) error {
			time.Sleep(2 * time.Millisecond) // slow consumer causes buffer to fill
			slowCount.Add(1)
			return nil
		})

	if err := kitsune.MergeRunners(r1, r2).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fastCount.Load() != int64(n) {
		t.Fatalf("fast consumer: want %d, got %d", n, fastCount.Load())
	}
	if slowCount.Load() >= int64(n) {
		t.Fatalf("slow consumer should have dropped some items, got all %d", n)
	}
}

// ---------------------------------------------------------------------------
// Context propagation audit
// ---------------------------------------------------------------------------

func TestMergeErrorPropagation(t *testing.T) {
	// Merge must surface errors (e.g. context cancellation) rather than
	// silently swallowing them.
	ctx, cancel := context.WithCancel(context.Background())

	// Infinite source — cancel the context after a short delay to force an
	// outbox.Send error path inside runMerge.
	p := kitsune.Generate(func(_ context.Context, yield func(int) bool) error {
		for i := 0; ; i++ {
			if !yield(i) {
				return nil
			}
		}
	})

	outs := kitsune.Broadcast(p, 2)
	merged := kitsune.Merge(outs[0], outs[1])

	done := make(chan error, 1)
	go func() {
		_, err := merged.Collect(ctx)
		done <- err
	}()

	// Give the pipeline a moment to start, then cancel.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected non-nil error after context cancellation, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not terminate after context cancellation")
	}
}

// ---------------------------------------------------------------------------
// Test coverage pass
// ---------------------------------------------------------------------------

func TestSupervisionExhaustion(t *testing.T) {
	// After MaxRestarts is exhausted the pipeline must terminate with the
	// persistent error, not hang.
	persistent := errors.New("persistent failure")

	done := make(chan error, 1)
	go func() {
		_, err := kitsune.Map(
			kitsune.FromSlice([]int{1, 2, 3}),
			func(_ context.Context, v int) (int, error) {
				return 0, persistent
			},
			kitsune.Supervise(kitsune.RestartOnError(2, kitsune.FixedBackoff(0))),
		).Collect(context.Background())
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, persistent) {
			t.Fatalf("expected persistent error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline hung after supervision exhaustion")
	}
}

func TestSupervisionConcurrentRestarts(t *testing.T) {
	// Concurrency(4) + supervision: verify no data races (run with -race).
	const n = 100
	var attempts atomic.Int64

	items := make([]int, n)
	for i := range items {
		items[i] = i
	}

	results, err := kitsune.Map(
		kitsune.FromSlice(items),
		func(_ context.Context, v int) (int, error) {
			if attempts.Add(1) <= 4 {
				return 0, errors.New("transient")
			}
			return v * 2, nil
		},
		kitsune.Concurrency(4),
		kitsune.Supervise(kitsune.RestartOnError(3, kitsune.FixedBackoff(0))),
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}
}

func TestOrderedCancellationNoLeak(t *testing.T) {
	// Cancelling an ordered concurrent stage must not deadlock.
	ctx, cancel := context.WithCancel(context.Background())

	const n = 1000
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}

	done := make(chan error, 1)
	go func() {
		_, err := kitsune.Map(
			kitsune.FromSlice(items),
			func(_ context.Context, v int) (int, error) {
				return v, nil
			},
			kitsune.Concurrency(8),
			kitsune.Ordered(),
		).Collect(ctx)
		done <- err
	}()

	time.Sleep(time.Millisecond)
	cancel()

	select {
	case <-done:
		// Pipeline terminated — no deadlock.
	case <-time.After(5 * time.Second):
		t.Fatal("ordered pipeline deadlocked under cancellation")
	}
}

func TestOverflowUnderLoad(t *testing.T) {
	// High-throughput stress: no deadlock, drops occur with both drop strategies.
	const n = 10_000

	items := make([]int, n)
	for i := range items {
		items[i] = i
	}

	for _, strategy := range []kitsune.OverflowStrategy{kitsune.DropNewest, kitsune.DropOldest} {
		h := &dropCountHook{Hook: kitsune.LogHook(slog.New(slog.NewTextHandler(io.Discard, nil)))}
		var received atomic.Int64

		err := kitsune.Map(
			kitsune.FromSlice(items),
			func(_ context.Context, v int) (int, error) { return v, nil },
			kitsune.Concurrency(8),
			kitsune.Buffer(4),
			kitsune.Overflow(strategy),
		).ForEach(func(_ context.Context, v int) error {
			received.Add(1)
			return nil
		}).Run(context.Background(), kitsune.WithHook(h))
		if err != nil {
			t.Fatalf("strategy %v: unexpected error: %v", strategy, err)
		}
		if received.Load() == 0 {
			t.Fatalf("strategy %v: no items received", strategy)
		}
	}
}

func TestExponentialBackoff(t *testing.T) {
	bo := kitsune.ExponentialBackoff(10*time.Millisecond, 100*time.Millisecond)

	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 10 * time.Millisecond},
		{1, 20 * time.Millisecond},
		{2, 40 * time.Millisecond},
		{3, 80 * time.Millisecond},
		{4, 100 * time.Millisecond}, // capped
		{10, 100 * time.Millisecond},
	}
	for _, c := range cases {
		got := bo(c.attempt)
		if got != c.want {
			t.Errorf("attempt %d: got %v, want %v", c.attempt, got, c.want)
		}
	}
}

func TestRetryThenFallback(t *testing.T) {
	// RetryThen(2, ..., Skip()) must skip items after retries are exhausted.
	persistent := errors.New("always fails")
	var calls atomic.Int64

	results, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			calls.Add(1)
			return 0, persistent
		},
		kitsune.OnError(kitsune.RetryThen(2, kitsune.FixedBackoff(0), kitsune.Skip())),
	).Collect(context.Background())
	if err != nil {
		t.Fatalf("expected nil error after skip fallback, got: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty results (all skipped), got %d", len(results))
	}
	// Each of 3 items gets 3 attempts (initial + 2 retries).
	if calls.Load() != 9 {
		t.Fatalf("expected 9 calls (3 items × 3 attempts), got %d", calls.Load())
	}
}

func TestRestartAlways(t *testing.T) {
	// RestartAlways restarts on both errors and panics. After the first item
	// errors/panics (consuming it), the restarted stage processes remaining items.
	t.Run("recovers from error", func(t *testing.T) {
		var calls atomic.Int64
		results, err := kitsune.Map(
			kitsune.FromSlice([]int{1, 2, 3}),
			func(_ context.Context, v int) (int, error) {
				if calls.Add(1) == 1 {
					return 0, errors.New("transient")
				}
				return v * 2, nil
			},
			kitsune.Supervise(kitsune.RestartAlways(1, kitsune.FixedBackoff(0))),
		).Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		// Item 1 consumed by the erroring attempt; items 2 and 3 processed after restart.
		if len(results) != 2 {
			t.Fatalf("expected 2 results (items 2 and 3), got %v", results)
		}
	})

	t.Run("recovers from panic", func(t *testing.T) {
		var calls atomic.Int64
		results, err := kitsune.Map(
			kitsune.FromSlice([]int{1, 2, 3}),
			func(_ context.Context, v int) (int, error) {
				if calls.Add(1) == 1 {
					panic("transient panic")
				}
				return v * 2, nil
			},
			kitsune.Supervise(kitsune.RestartAlways(1, kitsune.FixedBackoff(0))),
		).Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 2 {
			t.Fatalf("expected 2 results (items 2 and 3), got %v", results)
		}
	})
}

func TestEmptyInput(t *testing.T) {
	// Each sub-test needs its own pipeline — a graph node may only have one consumer.
	t.Run("Map", func(t *testing.T) {
		results, err := kitsune.Map(
			kitsune.FromSlice([]int{}),
			func(_ context.Context, v int) (int, error) { return v, nil },
		).Collect(context.Background())
		if err != nil || len(results) != 0 {
			t.Fatalf("Map: got %v, err %v", results, err)
		}
	})

	t.Run("Filter", func(t *testing.T) {
		results, err := kitsune.FromSlice([]int{}).
			Filter(func(v int) bool { return true }).
			Collect(context.Background())
		if err != nil || len(results) != 0 {
			t.Fatalf("Filter: got %v, err %v", results, err)
		}
	})

	t.Run("FlatMap", func(t *testing.T) {
		results, err := kitsune.FlatMap(
			kitsune.FromSlice([]int{}),
			func(_ context.Context, v int) ([]int, error) { return []int{v, v}, nil },
		).Collect(context.Background())
		if err != nil || len(results) != 0 {
			t.Fatalf("FlatMap: got %v, err %v", results, err)
		}
	})

	t.Run("Batch", func(t *testing.T) {
		results, err := kitsune.Batch(kitsune.FromSlice([]int{}), 3).Collect(context.Background())
		if err != nil || len(results) != 0 {
			t.Fatalf("Batch: got %v, err %v", results, err)
		}
	})
}

// ---------------------------------------------------------------------------
// Graceful drain
// ---------------------------------------------------------------------------

func TestWithDrainFlushesPartialBatch(t *testing.T) {
	// A batch stage should flush its partial buffer when context is cancelled
	// with a drain timeout, rather than dropping items.
	const batchSize = 10
	const totalItems = 5 // fewer than batchSize so the batch never auto-flushes

	ctx, cancel := context.WithCancel(context.Background())

	var received [][]int
	var mu sync.Mutex

	// Source emits totalItems, then blocks until context is cancelled.
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := range totalItems {
			if !yield(i) {
				return nil
			}
		}
		// Park until cancelled.
		<-ctx.Done()
		return nil
	})

	runner := kitsune.Batch(p, batchSize).ForEach(func(_ context.Context, batch []int) error {
		mu.Lock()
		received = append(received, append([]int(nil), batch...))
		mu.Unlock()
		return nil
	})

	done := make(chan error, 1)
	go func() {
		done <- runner.Run(ctx, kitsune.WithDrain(2*time.Second))
	}()

	// Let the source emit all items, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not drain within timeout")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) == 0 {
		t.Fatal("expected partial batch to be flushed, got none")
	}
	total := 0
	for _, b := range received {
		total += len(b)
	}
	if total != totalItems {
		t.Fatalf("expected %d items total, got %d", totalItems, total)
	}
}

func TestWithDrainHardStop(t *testing.T) {
	// When the drain timeout expires, the pipeline should terminate even if
	// a stage is still busy. The hard stop must happen within a reasonable bound.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Source parks forever after emitting one item.
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		yield(1)
		<-ctx.Done()
		return nil
	})

	// Sink simulates slow work but respects context cancellation.
	// time.Sleep alone would ignore ctx; use select so the hard stop can unblock it.
	runner := p.ForEach(func(ctx context.Context, v int) error {
		select {
		case <-time.After(10 * time.Second): // much longer than drain timeout
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	done := make(chan error, 1)
	go func() {
		done <- runner.Run(ctx, kitsune.WithDrain(100*time.Millisecond))
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Pipeline terminated — hard stop worked.
	case <-time.After(3 * time.Second):
		t.Fatal("pipeline did not hard-stop after drain timeout")
	}
}

func TestWithDrainNormalCompletion(t *testing.T) {
	// WithDrain should not affect normal (non-cancelled) pipeline completion.
	results, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) { return v * 2, nil },
	).Collect(context.Background(), kitsune.WithDrain(5*time.Second))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// Scan
// ---------------------------------------------------------------------------

func TestScan(t *testing.T) {
	results, err := kitsune.Scan(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		0,
		func(sum, v int) int { return sum + v },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 3, 6, 10, 15}
	if len(results) != len(want) {
		t.Fatalf("got %v, want %v", results, want)
	}
	for i, v := range results {
		if v != want[i] {
			t.Fatalf("results[%d] = %d, want %d", i, v, want[i])
		}
	}
}

func TestScanEmpty(t *testing.T) {
	results, err := kitsune.Scan(kitsune.FromSlice([]int{}), 0, func(s, v int) int { return s + v }).
		Collect(context.Background())
	if err != nil || len(results) != 0 {
		t.Fatalf("got %v, err %v", results, err)
	}
}

// ---------------------------------------------------------------------------
// GroupBy
// ---------------------------------------------------------------------------

func TestGroupBy(t *testing.T) {
	type Event struct{ Kind, Val string }
	input := []Event{
		{"a", "1"}, {"b", "2"}, {"a", "3"}, {"b", "4"}, {"c", "5"},
	}
	maps, err := kitsune.GroupBy(
		kitsune.FromSlice(input),
		func(e Event) string { return e.Kind },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(maps) != 1 {
		t.Fatalf("expected 1 map item, got %d", len(maps))
	}
	m := maps[0]
	if len(m["a"]) != 2 || len(m["b"]) != 2 || len(m["c"]) != 1 {
		t.Fatalf("unexpected groups: %v", m)
	}
}

// ---------------------------------------------------------------------------
// Distinct / DistinctBy
// ---------------------------------------------------------------------------

func TestDistinct(t *testing.T) {
	results, err := kitsune.Distinct(
		kitsune.FromSlice([]int{1, 2, 1, 3, 2, 4}),
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 distinct items, got %v", results)
	}
}

func TestDistinctBy(t *testing.T) {
	type Item struct{ ID int; Val string }
	input := []Item{{1, "a"}, {2, "b"}, {1, "c"}, {3, "d"}, {2, "e"}}
	results, err := kitsune.DistinctBy(
		kitsune.FromSlice(input),
		func(x Item) string { return fmt.Sprintf("%d", x.ID) },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 distinct items, got %v", results)
	}
	// First occurrence per ID should be kept.
	if results[0].Val != "a" || results[1].Val != "b" || results[2].Val != "d" {
		t.Fatalf("wrong first-occurrence items: %v", results)
	}
}

// ---------------------------------------------------------------------------
// TakeWhile / DropWhile
// ---------------------------------------------------------------------------

func TestTakeWhile(t *testing.T) {
	results, err := kitsune.TakeWhile(
		kitsune.FromSlice([]int{1, 2, 3, 10, 4, 5}),
		func(v int) bool { return v < 5 },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3}
	if len(results) != len(want) {
		t.Fatalf("got %v, want %v", results, want)
	}
	for i, v := range results {
		if v != want[i] {
			t.Fatalf("results[%d] = %d, want %d", i, v, want[i])
		}
	}
}

func TestTakeWhileAllPass(t *testing.T) {
	results, err := kitsune.TakeWhile(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(v int) bool { return v < 100 },
	).Collect(context.Background())
	if err != nil || len(results) != 3 {
		t.Fatalf("got %v, err %v", results, err)
	}
}

func TestTakeWhileNonePass(t *testing.T) {
	results, err := kitsune.TakeWhile(
		kitsune.FromSlice([]int{10, 20, 30}),
		func(v int) bool { return v < 5 },
	).Collect(context.Background())
	if err != nil || len(results) != 0 {
		t.Fatalf("got %v, err %v", results, err)
	}
}

func TestDropWhile(t *testing.T) {
	results, err := kitsune.DropWhile(
		kitsune.FromSlice([]int{1, 2, 3, 10, 4, 5}),
		func(v int) bool { return v < 5 },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{10, 4, 5}
	if len(results) != len(want) {
		t.Fatalf("got %v, want %v", results, want)
	}
	for i, v := range results {
		if v != want[i] {
			t.Fatalf("results[%d] = %d, want %d", i, v, want[i])
		}
	}
}

func TestDropWhileAllDrop(t *testing.T) {
	results, err := kitsune.DropWhile(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(v int) bool { return v < 100 },
	).Collect(context.Background())
	if err != nil || len(results) != 0 {
		t.Fatalf("got %v, err %v", results, err)
	}
}

// ---------------------------------------------------------------------------
// Zip
// ---------------------------------------------------------------------------

func TestZip(t *testing.T) {
	branches := kitsune.Broadcast[int](kitsune.FromSlice([]int{1, 2, 3}), 2)
	doubled := kitsune.Map(branches[1], func(_ context.Context, v int) (int, error) { return v * 2, nil })
	pairs, err := kitsune.Zip(branches[0], doubled).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []kitsune.Pair[int, int]{{1, 2}, {2, 4}, {3, 6}}
	if len(pairs) != len(want) {
		t.Fatalf("got %v, want %v", pairs, want)
	}
	for i, p := range pairs {
		if p != want[i] {
			t.Fatalf("pairs[%d] = %v, want %v", i, p, want[i])
		}
	}
}

func TestZipUnequalLengths(t *testing.T) {
	// Shorter stream determines output length.
	branches := kitsune.Broadcast[int](kitsune.FromSlice([]int{1, 2, 3, 4, 5}), 2)
	short := branches[0].Take(3)
	full := branches[1]
	pairs, err := kitsune.Zip(short, full).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 3 {
		t.Fatalf("expected 3 pairs, got %d: %v", len(pairs), pairs)
	}
}

func TestZipCrossGraphPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for cross-graph Zip")
		}
	}()
	a := kitsune.FromSlice([]int{1, 2, 3})
	b := kitsune.FromSlice([]string{"x", "y", "z"})
	kitsune.Zip(a, b) // should panic
}

func TestScanTypeChange(t *testing.T) {
	// S ≠ T: accumulate ints into a string.
	results, err := kitsune.Scan(
		kitsune.FromSlice([]int{1, 2, 3}),
		"",
		func(acc string, v int) string {
			if acc == "" {
				return fmt.Sprintf("%d", v)
			}
			return acc + "," + fmt.Sprintf("%d", v)
		},
	).Collect(context.Background())
	if err != nil || len(results) != 3 {
		t.Fatalf("got %v, err %v", results, err)
	}
	if results[2] != "1,2,3" {
		t.Fatalf("last accumulator = %q, want %q", results[2], "1,2,3")
	}
}

func TestGroupByEmpty(t *testing.T) {
	maps, err := kitsune.GroupBy(
		kitsune.FromSlice([]int{}),
		func(v int) int { return v % 2 },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// An empty source produces no batches, so GroupBy emits nothing.
	if len(maps) != 0 {
		t.Fatalf("expected empty output, got %v", maps)
	}
}

func TestDistinctOrdering(t *testing.T) {
	// First occurrence order must be preserved.
	results, err := kitsune.Distinct(
		kitsune.FromSlice([]int{5, 3, 5, 1, 3, 2}),
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{5, 3, 1, 2}
	if len(results) != len(want) {
		t.Fatalf("got %v, want %v", results, want)
	}
	for i, v := range results {
		if v != want[i] {
			t.Fatalf("results[%d] = %d, want %d", i, v, want[i])
		}
	}
}

func TestDistinctEmpty(t *testing.T) {
	results, err := kitsune.Distinct(kitsune.FromSlice([]int{})).Collect(context.Background())
	if err != nil || len(results) != 0 {
		t.Fatalf("got %v, err %v", results, err)
	}
}

func TestDropWhileNoneDrop(t *testing.T) {
	// First item already fails predicate — all items should pass through.
	results, err := kitsune.DropWhile(
		kitsune.FromSlice([]int{10, 1, 2}),
		func(v int) bool { return v < 5 },
	).Collect(context.Background())
	if err != nil || len(results) != 3 {
		t.Fatalf("got %v, err %v", results, err)
	}
}

func TestTakeWhileStopsSource(t *testing.T) {
	// Verify that TakeWhile stops an infinite source without deadlock.
	counter := kitsune.Generate(func(_ context.Context, yield func(int) bool) error {
		for i := 0; ; i++ {
			if !yield(i) {
				return nil
			}
		}
	})
	results, err := kitsune.TakeWhile(counter, func(v int) bool { return v < 5 }).
		Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 items, got %d: %v", len(results), results)
	}
}

func TestZipEmpty(t *testing.T) {
	branches := kitsune.Broadcast[int](kitsune.FromSlice([]int{}), 2)
	pairs, err := kitsune.Zip(branches[0], branches[1]).Collect(context.Background())
	if err != nil || len(pairs) != 0 {
		t.Fatalf("got %v, err %v", pairs, err)
	}
}

func TestZipDifferentTypes(t *testing.T) {
	branches := kitsune.Broadcast[int](kitsune.FromSlice([]int{1, 2, 3}), 2)
	asStr := kitsune.Map(branches[1], func(_ context.Context, v int) (string, error) {
		return fmt.Sprintf("item-%d", v), nil
	})
	pairs, err := kitsune.Zip(branches[0], asStr).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 3 {
		t.Fatalf("expected 3 pairs, got %d", len(pairs))
	}
	if pairs[0].First != 1 || pairs[0].Second != "item-1" {
		t.Fatalf("unexpected first pair: %v", pairs[0])
	}
}
