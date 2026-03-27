package kitsune_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
)

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
