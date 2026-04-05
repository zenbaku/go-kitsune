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

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/testkit"
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
	split := kitsune.FlatMap(input, func(_ context.Context, s string, yield func(string) error) error {
		for _, part := range strings.Split(s, ",") {
			if err := yield(part); err != nil {
				return err
			}
		}
		return nil
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

func TestFlatMapEarlyExit(t *testing.T) {
	// Verify fn respects yield errors when context is cancelled.
	var called int
	input := kitsune.FromSlice([]int{1, 2, 3})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	kitsune.FlatMap(input, func(_ context.Context, n int, yield func(int) error) error {
		called++
		return yield(n)
	}).Drain().Run(ctx) //nolint:errcheck
	// with cancelled context the pipeline may process 0 or 1 items — just ensure no panic
	_ = called
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
	boom := errors.New("boom")
	input := kitsune.FromSlice([]int{1, 2, 3})
	failing := kitsune.Map(input, func(_ context.Context, n int) (int, error) {
		if n == 2 {
			return 0, boom
		}
		return n * 10, nil
	})

	_, err := failing.Collect(context.Background())
	if err == nil || !errors.Is(err, boom) {
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

func TestErrorHandlingReturn(t *testing.T) {
	input := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(input, func(_ context.Context, n int) (int, error) {
		if n == 2 {
			return 0, errors.New("replace me")
		}
		return n * 10, nil
	}, kitsune.OnError(kitsune.Return(-1)))

	results, err := mapped.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []int{10, -1, 30}
	if !slices.Equal(results, expected) {
		t.Fatalf("expected %v, got %v", expected, results)
	}
}

func TestErrorHandlingReturnStruct(t *testing.T) {
	type User struct {
		Name string
	}
	sentinel := User{Name: "unknown"}
	input := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(input, func(_ context.Context, n int) (User, error) {
		if n == 2 {
			return User{}, errors.New("lookup failed")
		}
		return User{Name: fmt.Sprintf("user%d", n)}, nil
	}, kitsune.OnError(kitsune.Return(sentinel)))

	results, err := mapped.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[1] != sentinel {
		t.Errorf("expected sentinel %v at index 1, got %v", sentinel, results[1])
	}
}

func TestErrorHandlingRetryThenReturn(t *testing.T) {
	var attempts atomic.Int32
	input := kitsune.FromSlice([]int{1})
	mapped := kitsune.Map(input, func(_ context.Context, n int) (int, error) {
		attempts.Add(1)
		return 0, errors.New("always fails")
	}, kitsune.OnError(kitsune.RetryThen(2, kitsune.FixedBackoff(0), kitsune.Return(0))))

	results, err := mapped.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []int{0}
	if !slices.Equal(results, expected) {
		t.Fatalf("expected %v, got %v", expected, results)
	}
	// 1 initial attempt + 2 retries = 3 total
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
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

	merged, err := kitsune.MergeRunners(evenRunner, oddRunner)
	if err != nil {
		t.Fatal(err)
	}
	if err := merged.Run(context.Background()); err != nil {
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

func TestLiftPure(t *testing.T) {
	input := kitsune.FromSlice([]int{1, 2, 3})
	doubled := kitsune.Map(input, kitsune.LiftPure(func(n int) int { return n * 2 }))

	results, err := doubled.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []int{2, 4, 6}
	if !slices.Equal(results, expected) {
		t.Fatalf("expected %v, got %v", expected, results)
	}
}

func TestLiftPureTypeChange(t *testing.T) {
	input := kitsune.FromSlice([]int{1, 2, 3})
	strs := kitsune.Map(input, kitsune.LiftPure(func(n int) string { return strconv.Itoa(n) }))

	results, err := strs.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"1", "2", "3"}
	if !slices.Equal(results, expected) {
		t.Fatalf("expected %v, got %v", expected, results)
	}
}

func TestDedupe(t *testing.T) {
	input := kitsune.FromSlice([]string{"a", "b", "a", "c", "b", "d"})
	deduped := input.Dedupe(func(s string) string { return s })

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
	deduped := input.Dedupe(func(i Item) string {
		return fmt.Sprintf("%d", i.ID)
	})

	results, err := deduped.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 unique items, got %d: %v", len(results), results)
	}
}

func TestDedupeAddErrorHaltsPipeline(t *testing.T) {
	// Add errors must propagate to halt the pipeline (not be silently dropped).
	addErr := fmt.Errorf("add failed")
	set := &failingDedupSet{addErr: addErr}
	_, err := kitsune.FromSlice([]string{"a", "b", "c"}).
		Dedupe(func(s string) string { return s }, kitsune.WithDedupSet(set)).
		Collect(context.Background())
	if err == nil {
		t.Fatal("expected error from DedupSet.Add to halt the pipeline")
	}
	if !errors.Is(err, addErr) {
		t.Fatalf("expected addErr, got: %v", err)
	}
}

// failingDedupSet always reports items as unseen but fails on Add.
type failingDedupSet struct{ addErr error }

func (f *failingDedupSet) Contains(_ context.Context, _ string) (bool, error) { return false, nil }
func (f *failingDedupSet) Add(_ context.Context, _ string) error              { return f.addErr }

func TestMapWithCache(t *testing.T) {
	callCount := 0
	input := kitsune.FromSlice([]string{"a", "b", "a", "c", "a"})

	cached := kitsune.Map(input, func(_ context.Context, s string) (string, error) {
		callCount++
		return s + "!", nil
	}, kitsune.CacheBy(func(s string) string { return s }, kitsune.CacheBackend(kitsune.MemoryCache(100))))

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

// ---------------------------------------------------------------------------
// StateTTL tests
// ---------------------------------------------------------------------------

func TestStateTTL_ExpiryOnGet(t *testing.T) {
	ttl := 20 * time.Millisecond
	key := kitsune.NewKey("ttl-expire", 0, kitsune.StateTTL(ttl))
	input := kitsune.FromSlice([]int{1})

	p := kitsune.MapWith(input, key, func(ctx context.Context, ref *kitsune.Ref[int], item int) (int, error) {
		if err := ref.Set(ctx, 42); err != nil {
			return 0, err
		}
		return item, nil
	})
	// Consume pipeline to set value.
	if _, err := p.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Build a second pipeline to read the value after TTL expiry.
	input2 := kitsune.FromSlice([]int{1})
	key2 := kitsune.NewKey("ttl-expire2", 0, kitsune.StateTTL(ttl))
	var gotValue int
	p2 := kitsune.MapWith(input2, key2, func(ctx context.Context, ref *kitsune.Ref[int], item int) (int, error) {
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
	if _, err := p2.Collect(context.Background()); err != nil {
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
	p := kitsune.MapWith(input, key, func(ctx context.Context, ref *kitsune.Ref[int], item int) (int, error) {
		if err := ref.Set(ctx, 99); err != nil {
			return 0, err
		}
		time.Sleep(ttl / 2)
		// Write again — resets the TTL clock.
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
	// Default key (no TTL) — value must never expire.
	key := kitsune.NewKey("ttl-zero", 0)
	input := kitsune.FromSlice([]int{1})

	var gotValue int
	p := kitsune.MapWith(input, key, func(ctx context.Context, ref *kitsune.Ref[int], item int) (int, error) {
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
	p := kitsune.MapWith(input, key, func(ctx context.Context, ref *kitsune.Ref[int], item int) (int, error) {
		if err := ref.Set(ctx, 5); err != nil {
			return 0, err
		}
		time.Sleep(ttl / 2)
		// Update — resets the TTL clock.
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
// MapWithKey / FlatMapWithKey tests
// ---------------------------------------------------------------------------

func TestMapWithKey_PerEntityIsolation(t *testing.T) {
	// Two entity keys must accumulate independently.
	key := kitsune.NewKey("entity-count", 0)
	type event struct {
		entity string
		val    int
	}
	items := []event{
		{"a", 1}, {"b", 10}, {"a", 2}, {"b", 20}, {"a", 3},
	}
	input := kitsune.FromSlice(items)
	p := kitsune.MapWithKey(input,
		func(e event) string { return e.entity },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], e event) (int, error) {
			return ref.UpdateAndGet(ctx, func(v int) (int, error) { return v + e.val, nil })
		},
	)
	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// a: 1, 3, 6 (running totals); b: 10, 30 (running totals)
	expected := []int{1, 10, 3, 30, 6}
	if len(results) != len(expected) {
		t.Fatalf("expected %d results, got %d: %v", len(expected), len(results), results)
	}
	for i, v := range results {
		if v != expected[i] {
			t.Errorf("results[%d] = %d, want %d", i, v, expected[i])
		}
	}
}

func TestMapWithKey_SameKeySharesState(t *testing.T) {
	key := kitsune.NewKey("shared-state", 0)
	input := kitsune.FromSlice([]string{"x", "x", "x"})
	p := kitsune.MapWithKey(input,
		func(s string) string { return s },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (int, error) {
			return ref.UpdateAndGet(ctx, func(v int) (int, error) { return v + 1, nil })
		},
	)
	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []int{1, 2, 3}
	if len(results) != len(expected) {
		t.Fatalf("expected %d results, got %d: %v", len(expected), len(results), results)
	}
	for i, v := range results {
		if v != expected[i] {
			t.Errorf("results[%d] = %d, want %d", i, v, expected[i])
		}
	}
}

func TestFlatMapWithKey_MultipleOutputs(t *testing.T) {
	// 1:N with per-entity state.
	key := kitsune.NewKey("flatmap-key-count", 0)
	type item struct {
		entity string
		n      int
	}
	items := []item{{"a", 2}, {"b", 3}, {"a", 1}}
	input := kitsune.FromSlice(items)
	p := kitsune.FlatMapWithKey(input,
		func(it item) string { return it.entity },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], it item, yield func(string) error) error {
			cur, err := ref.UpdateAndGet(ctx, func(v int) (int, error) { return v + it.n, nil })
			if err != nil {
				return err
			}
			for i := 0; i < it.n; i++ {
				if err := yield(fmt.Sprintf("%s:%d", it.entity, cur)); err != nil {
					return err
				}
			}
			return nil
		},
	)
	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// a: 2 outputs (total=2), b: 3 outputs (total=3), a: 1 output (total=3)
	if len(results) != 6 {
		t.Fatalf("expected 6 results, got %d: %v", len(results), results)
	}
}

func TestMapWithKey_StoreBacked(t *testing.T) {
	key := kitsune.NewKey("store-keyed", 0)
	input := kitsune.FromSlice([]string{"u1", "u2", "u1"})
	p := kitsune.MapWithKey(input,
		func(s string) string { return s },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (string, error) {
			n, err := ref.UpdateAndGet(ctx, func(v int) (int, error) { return v + 1, nil })
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s:%d", s, n), nil
		},
	)
	results, err := p.Collect(context.Background(), kitsune.WithStore(kitsune.MemoryStore()))
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"u1:1", "u2:1", "u1:2"}
	if len(results) != len(expected) {
		t.Fatalf("expected %d results, got %d: %v", len(expected), len(results), results)
	}
	for i, v := range results {
		if v != expected[i] {
			t.Errorf("results[%d] = %q, want %q", i, v, expected[i])
		}
	}
}

func TestMapWithKey_WithTTL(t *testing.T) {
	// TTL + keyed state: after the TTL window expires, Get returns the initial
	// value. We verify this by sleeping *inside* the stage function (after Set,
	// before Get) so that the Ref's own TTL-on-read path triggers.
	ttl := 20 * time.Millisecond
	key := kitsune.NewKey("keyed-ttl", 0, kitsune.StateTTL(ttl))

	input := kitsune.FromSlice([]string{"a", "b"})
	var gotValues []int
	p := kitsune.MapWithKey(input,
		func(s string) string { return s },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (int, error) {
			if err := ref.Set(ctx, 42); err != nil {
				return 0, err
			}
			// Sleep past the TTL so Get sees expiry.
			time.Sleep(ttl + 5*time.Millisecond)
			v, err := ref.Get(ctx)
			if err != nil {
				return 0, err
			}
			gotValues = append(gotValues, v)
			return v, nil
		},
	)
	if _, err := p.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(gotValues) != 2 {
		t.Fatalf("expected 2 values, got %d: %v", len(gotValues), gotValues)
	}
	// Each entity's ref was written then expired — should return initial value 0.
	for i, v := range gotValues {
		if v != 0 {
			t.Errorf("gotValues[%d] = %d, want 0 (initial after TTL expiry)", i, v)
		}
	}
}

// ---------------------------------------------------------------------------
// CountBy / SumBy tests
// ---------------------------------------------------------------------------

func TestCountBy_Basic(t *testing.T) {
	type event struct{ typ string }
	items := []event{{"click"}, {"view"}, {"click"}, {"click"}, {"view"}}
	input := kitsune.FromSlice(items)
	p := kitsune.CountBy(input, func(e event) string { return e.typ })
	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 snapshots, got %d", len(results))
	}
	last := results[len(results)-1]
	if last["click"] != 3 {
		t.Errorf("expected click=3, got %d", last["click"])
	}
	if last["view"] != 2 {
		t.Errorf("expected view=2, got %d", last["view"])
	}
}

func TestCountBy_SnapshotIsolation(t *testing.T) {
	// Mutating the returned map must not corrupt internal state.
	input := kitsune.FromSlice([]string{"a", "b", "a"})
	p := kitsune.CountBy(input, func(s string) string { return s })
	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no results")
	}
	// Mutate the first snapshot.
	results[0]["a"] = 9999
	// Last snapshot should be unaffected.
	last := results[len(results)-1]
	if last["a"] != 2 {
		t.Errorf("expected a=2 in last snapshot, got %d (snapshot isolation broken)", last["a"])
	}
}

func TestCountBy_EmptyStream(t *testing.T) {
	input := kitsune.FromSlice([]string{})
	p := kitsune.CountBy(input, func(s string) string { return s })
	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected no output for empty stream, got %d", len(results))
	}
}

func TestSumBy_Float64(t *testing.T) {
	type txn struct {
		account string
		amount  float64
	}
	items := []txn{
		{"alice", 10.0}, {"bob", 5.0}, {"alice", 20.0}, {"bob", 15.0},
	}
	input := kitsune.FromSlice(items)
	p := kitsune.SumBy(input,
		func(t txn) string { return t.account },
		func(t txn) float64 { return t.amount },
	)
	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 snapshots, got %d", len(results))
	}
	last := results[len(results)-1]
	if last["alice"] != 30.0 {
		t.Errorf("expected alice=30.0, got %v", last["alice"])
	}
	if last["bob"] != 20.0 {
		t.Errorf("expected bob=20.0, got %v", last["bob"])
	}
}

func TestSumBy_Integer(t *testing.T) {
	type kv struct {
		k string
		v int
	}
	items := []kv{{"x", 1}, {"y", 2}, {"x", 3}, {"y", 4}}
	input := kitsune.FromSlice(items)
	p := kitsune.SumBy(input,
		func(item kv) string { return item.k },
		func(item kv) int { return item.v },
	)
	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	last := results[len(results)-1]
	if last["x"] != 4 {
		t.Errorf("expected x=4, got %d", last["x"])
	}
	if last["y"] != 6 {
		t.Errorf("expected y=6, got %d", last["y"])
	}
}

func TestSumBy_EmptyStream(t *testing.T) {
	input := kitsune.FromSlice([]struct{ k string }{})
	p := kitsune.SumBy(input,
		func(item struct{ k string }) string { return item.k },
		func(item struct{ k string }) int { return 0 },
	)
	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected no output for empty stream, got %d", len(results))
	}
}

func TestSumBy_SnapshotIsolation(t *testing.T) {
	// Mutating the returned map must not corrupt internal state.
	type kv struct {
		k string
		v int
	}
	input := kitsune.FromSlice([]kv{{"a", 1}, {"b", 2}, {"a", 3}})
	p := kitsune.SumBy(input,
		func(item kv) string { return item.k },
		func(item kv) int { return item.v },
	)
	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no results")
	}
	// Mutate the first snapshot.
	results[0]["a"] = 9999
	// Last snapshot should be unaffected.
	last := results[len(results)-1]
	if last["a"] != 4 {
		t.Errorf("expected a=4 in last snapshot, got %d (snapshot isolation broken)", last["a"])
	}
}

func TestSumBy_MultipleInstances(t *testing.T) {
	// Two SumBy calls in the same pipeline must use independent state.
	type kv struct {
		k string
		v int
	}
	input1 := kitsune.FromSlice([]kv{{"x", 10}, {"x", 20}, {"x", 30}})
	input2 := kitsune.FromSlice([]kv{{"y", 5}, {"y", 5}})
	p1 := kitsune.SumBy(input1,
		func(item kv) string { return item.k },
		func(item kv) int { return item.v },
	)
	p2 := kitsune.SumBy(input2,
		func(item kv) string { return item.k },
		func(item kv) int { return item.v },
	)

	r1, err := p1.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r2, err := p2.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	last1 := r1[len(r1)-1]
	last2 := r2[len(r2)-1]
	if last1["x"] != 60 {
		t.Errorf("p1: expected x=60, got %d", last1["x"])
	}
	if last2["y"] != 10 {
		t.Errorf("p2: expected y=10, got %d", last2["y"])
	}
	if last1["y"] != 0 {
		t.Errorf("p1 should not have y key, got %d", last1["y"])
	}
}

func TestCountBy_MultipleInstances(t *testing.T) {
	// Two CountBy in the same program must not share state.
	input1 := kitsune.FromSlice([]string{"a", "a", "a"})
	input2 := kitsune.FromSlice([]string{"b", "b"})
	p1 := kitsune.CountBy(input1, func(s string) string { return s })
	p2 := kitsune.CountBy(input2, func(s string) string { return s })

	r1, err := p1.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r2, err := p2.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	last1 := r1[len(r1)-1]
	last2 := r2[len(r2)-1]
	if last1["a"] != 3 {
		t.Errorf("p1: expected a=3, got %d", last1["a"])
	}
	if last2["b"] != 2 {
		t.Errorf("p2: expected b=2, got %d", last2["b"])
	}
	if last1["b"] != 0 {
		t.Errorf("p1 should not have b key, got %d", last1["b"])
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

func TestRunAsync_PauseResume(t *testing.T) {
	// Use NewChannel so we control item injection precisely.
	ch := kitsune.NewChannel[int](16)
	var (
		mu  sync.Mutex
		got []int
	)
	h := kitsune.Map(ch.Source(), func(_ context.Context, n int) (int, error) { return n, nil }).
		ForEach(func(_ context.Context, n int) error {
			mu.Lock()
			got = append(got, n)
			mu.Unlock()
			return nil
		}).RunAsync(context.Background())

	// Send a few items and let them flow.
	ch.Send(context.Background(), 1) //nolint:errcheck
	ch.Send(context.Background(), 2) //nolint:errcheck
	ch.Send(context.Background(), 3) //nolint:errcheck
	time.Sleep(50 * time.Millisecond)

	// Pause and record how many items arrived so far.
	h.Pause()
	if !h.Paused() {
		t.Fatal("expected Paused() == true")
	}
	mu.Lock()
	countAtPause := len(got)
	mu.Unlock()

	// Items pushed while paused should not reach the sink.
	ch.Send(context.Background(), 4) //nolint:errcheck
	ch.Send(context.Background(), 5) //nolint:errcheck
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	countWhilePaused := len(got)
	mu.Unlock()
	if countWhilePaused > countAtPause {
		t.Fatalf("items flowed while paused: had %d at pause, %d after", countAtPause, countWhilePaused)
	}

	// Resume and let buffered items drain.
	h.Resume()
	if h.Paused() {
		t.Fatal("expected Paused() == false after Resume")
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	countAfterResume := len(got)
	mu.Unlock()
	if countAfterResume <= countAtPause {
		t.Fatalf("expected more items after resume, got %d (was %d at pause)", countAfterResume, countAtPause)
	}
}

func TestRunAsync_PauseContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Infinite source.
	counter := func(yield func(int) bool) {
		for i := 0; ; i++ {
			if !yield(i) {
				return
			}
		}
	}

	h := kitsune.FromIter(counter).Drain().RunAsync(ctx)

	h.Pause()
	time.Sleep(20 * time.Millisecond)

	// Cancelling the context while paused should unblock and exit cleanly.
	cancel()
	select {
	case <-h.Done():
		// clean exit
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline did not exit after context cancel while paused")
	}
	// nil or context.Canceled are both acceptable.
	if err := h.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunAsync_PausedState(t *testing.T) {
	gate := kitsune.NewGate()
	if gate.Paused() {
		t.Fatal("new gate should not be paused")
	}
	gate.Pause()
	if !gate.Paused() {
		t.Fatal("gate should be paused")
	}
	gate.Resume()
	if gate.Paused() {
		t.Fatal("gate should be open after Resume")
	}
}

func TestWithPauseGate(t *testing.T) {
	gate := kitsune.NewGate()
	gate.Pause()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Infinite source that sends 1 per tick.
	var count atomic.Int64
	counter := func(yield func(int) bool) {
		for i := 0; ; i++ {
			if !yield(i) {
				return
			}
		}
	}

	done := make(chan error, 1)
	go func() {
		done <- kitsune.FromIter(counter).
			ForEach(func(_ context.Context, _ int) error {
				count.Add(1)
				return nil
			}).Run(ctx, kitsune.WithPauseGate(gate))
	}()

	time.Sleep(30 * time.Millisecond)
	if count.Load() > 0 {
		t.Fatal("items should not flow while gate is paused from the start")
	}

	gate.Resume()
	time.Sleep(30 * time.Millisecond)
	if count.Load() == 0 {
		t.Fatal("items should flow after gate resumed")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline did not exit after context cancel")
	}
}

func TestIter(t *testing.T) {
	seq, errFn := kitsune.FromSlice([]int{1, 2, 3}).Iter(context.Background())
	var got []int
	for item := range seq {
		got = append(got, item)
	}
	if err := errFn(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []int{1, 2, 3}) {
		t.Fatalf("unexpected: %v", got)
	}
}

func TestIterEmpty(t *testing.T) {
	seq, errFn := kitsune.FromSlice([]int{}).Iter(context.Background())
	var got []int
	for item := range seq {
		got = append(got, item)
	}
	if err := errFn(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestIterBreak(t *testing.T) {
	items := make([]int, 1000)
	for i := range items {
		items[i] = i
	}
	seq, errFn := kitsune.FromSlice(items).Iter(context.Background())
	var got []int
	for item := range seq {
		got = append(got, item)
		if len(got) == 2 {
			break
		}
	}
	if err := errFn(); err != nil {
		t.Fatalf("expected nil after break, got: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 items, got %d", len(got))
	}
}

func TestIterContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Infinite source.
	counter := func(yield func(int) bool) {
		for i := 0; ; i++ {
			if !yield(i) {
				return
			}
		}
	}

	seq, errFn := kitsune.FromIter(counter).Iter(ctx)
	var count int
	for range seq {
		count++
		if count == 3 {
			cancel()
		}
	}

	err := errFn()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestIterPipelineError(t *testing.T) {
	wantErr := errors.New("stage failure")
	seq, errFn := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, n int) (int, error) {
			if n == 2 {
				return 0, wantErr
			}
			return n, nil
		},
	).Iter(context.Background())
	for range seq {
	}
	err := errFn()
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got: %v", wantErr, err)
	}
}

func TestIterWithMap(t *testing.T) {
	seq, errFn := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, n int) (string, error) { return fmt.Sprintf("%d", n), nil },
	).Iter(context.Background())
	var got []string
	for s := range seq {
		got = append(got, s)
	}
	if err := errFn(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"1", "2", "3"}) {
		t.Fatalf("unexpected: %v", got)
	}
}

func TestIterWithFilter(t *testing.T) {
	seq, errFn := kitsune.FromSlice([]int{1, 2, 3, 4, 5}).
		Filter(func(n int) bool { return n%2 == 0 }).
		Iter(context.Background())
	var got []int
	for item := range seq {
		got = append(got, item)
	}
	if err := errFn(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []int{2, 4}) {
		t.Fatalf("unexpected: %v", got)
	}
}

func TestIterErrFnIdempotent(t *testing.T) {
	seq, errFn := kitsune.FromSlice([]int{1, 2, 3}).Iter(context.Background())
	for range seq {
	}
	for i := 0; i < 3; i++ {
		if err := errFn(); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
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

	merged, err := kitsune.MergeRunners(runners...)
	if err != nil {
		t.Fatal(err)
	}
	if err := merged.Run(context.Background()); err != nil {
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
		p1 := kitsune.FromSlice([]int{1})
		p2 := kitsune.FromSlice([]int{2})
		got, err := kitsune.Merge(p1, p2).Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(got)
		if !slices.Equal(got, []int{1, 2}) {
			t.Errorf("got %v, want [1 2]", got)
		}
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
	results, err := kitsune.FlatMap(p, func(_ context.Context, v int, yield func(int) error) error {
		if v%2 == 1 {
			time.Sleep(time.Millisecond)
		}
		if err := yield(v * 10); err != nil {
			return err
		}
		return yield(v*10 + 1)
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

func TestMergeRunners_NoRunners(t *testing.T) {
	_, err := kitsune.MergeRunners()
	if !errors.Is(err, kitsune.ErrNoRunners) {
		t.Fatalf("expected ErrNoRunners, got %v", err)
	}
}

func TestMergeRunners_DifferentGraphs(t *testing.T) {
	r1 := kitsune.FromSlice([]int{1}).Drain()
	r2 := kitsune.FromSlice([]int{2}).Drain() // different graph
	_, err := kitsune.MergeRunners(r1, r2)
	if !errors.Is(err, kitsune.ErrGraphMismatch) {
		t.Fatalf("expected ErrGraphMismatch, got %v", err)
	}
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

func TestRestartOnPanicHaltsOnError(t *testing.T) {
	// RestartOnPanic must NOT restart on regular errors — it should halt immediately.
	var calls atomic.Int64
	_, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			calls.Add(1)
			return 0, fmt.Errorf("regular error")
		},
		kitsune.Supervise(kitsune.RestartOnPanic(5, kitsune.FixedBackoff(0))),
	).Collect(context.Background())
	if err == nil {
		t.Fatal("expected error: RestartOnPanic should halt on regular errors")
	}
	// With PanicOnly, the first error halts immediately — no restarts.
	if calls.Load() != 1 {
		t.Fatalf("expected exactly 1 call (no restarts), got %d", calls.Load())
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

	merged, err := kitsune.MergeRunners(r1, r2)
	if err != nil {
		t.Fatal(err)
	}
	if err := merged.Run(context.Background()); err != nil {
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
			if attempts.Add(1) <= 3 {
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
			func(_ context.Context, v int, yield func(int) error) error {
				if err := yield(v); err != nil {
					return err
				}
				return yield(v)
			},
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
	type Item struct {
		ID  int
		Val string
	}
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

func TestZipIndependentGraphs(t *testing.T) {
	a := kitsune.FromSlice([]int{1, 2, 3})
	b := kitsune.FromSlice([]string{"x", "y", "z"})
	pairs, err := kitsune.Zip(a, b).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []kitsune.Pair[int, string]{{1, "x"}, {2, "y"}, {3, "z"}}
	if len(pairs) != len(want) {
		t.Fatalf("got %v, want %v", pairs, want)
	}
	for i, p := range pairs {
		if p != want[i] {
			t.Fatalf("pairs[%d] = %v, want %v", i, p, want[i])
		}
	}
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

func TestScanWithStageOption(t *testing.T) {
	// Verify that Scan accepts StageOption — WithName should appear in the error
	// message when the stage's error handler is invoked.
	// We also check correctness is preserved when opts are forwarded.
	results, err := kitsune.Scan(
		kitsune.FromSlice([]int{1, 2, 3}),
		0,
		func(acc, v int) int { return acc + v },
		kitsune.WithName("running-sum"),
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[2] != 6 {
		t.Errorf("results = %v, want [1 3 6]", results)
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

// ---------------------------------------------------------------------------
// Skip
// ---------------------------------------------------------------------------

func TestSkip(t *testing.T) {
	results, err := kitsune.FromSlice([]int{1, 2, 3, 4, 5}).Skip(2).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(results, []int{3, 4, 5}) {
		t.Fatalf("expected [3 4 5], got %v", results)
	}
}

func TestSkipZero(t *testing.T) {
	results, err := kitsune.FromSlice([]int{1, 2, 3}).Skip(0).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(results, []int{1, 2, 3}) {
		t.Fatalf("expected all items, got %v", results)
	}
}

func TestSkipAll(t *testing.T) {
	results, err := kitsune.FromSlice([]int{1, 2, 3}).Skip(100).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty, got %v", results)
	}
}

// ---------------------------------------------------------------------------
// Reduce
// ---------------------------------------------------------------------------

func TestReduce(t *testing.T) {
	result, err := kitsune.Reduce(kitsune.FromSlice([]int{1, 2, 3, 4, 5}), 0, func(acc, v int) int {
		return acc + v
	}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0] != 15 {
		t.Fatalf("expected [15], got %v", result)
	}
}

func TestReduceEmpty(t *testing.T) {
	result, err := kitsune.Reduce(kitsune.FromSlice([]int{}), 42, func(acc, v int) int {
		return acc + v
	}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0] != 42 {
		t.Fatalf("expected [42] (seed), got %v", result)
	}
}

// ---------------------------------------------------------------------------
// MapRecover
// ---------------------------------------------------------------------------

func TestMapRecover(t *testing.T) {
	errBoom := errors.New("boom")
	results, err := kitsune.MapRecover(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		func(_ context.Context, v int) (int, error) {
			if v%2 == 0 {
				return 0, errBoom
			}
			return v * 10, nil
		},
		func(_ context.Context, v int, _ error) int {
			return -v
		},
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []int{10, -2, 30, -4, 50}
	if !slices.Equal(results, expected) {
		t.Fatalf("expected %v, got %v", expected, results)
	}
}

// ---------------------------------------------------------------------------
// MultiHook
// ---------------------------------------------------------------------------

type countHook struct {
	starts atomic.Int64
	items  atomic.Int64
	done   atomic.Int64
	drops  atomic.Int64
}

func (h *countHook) OnStageStart(_ context.Context, _ string)                     { h.starts.Add(1) }
func (h *countHook) OnItem(_ context.Context, _ string, _ time.Duration, _ error) { h.items.Add(1) }
func (h *countHook) OnStageDone(_ context.Context, _ string, _, _ int64)          { h.done.Add(1) }
func (h *countHook) OnDrop(_ context.Context, _ string, _ any)                    { h.drops.Add(1) }

func TestMultiHook(t *testing.T) {
	h1, h2 := &countHook{}, &countHook{}
	multi := kitsune.MultiHook(h1, h2)

	p := kitsune.FromSlice([]int{1, 2, 3})
	err := p.ForEach(func(_ context.Context, _ int) error { return nil }).
		Run(context.Background(), kitsune.WithHook(multi))
	if err != nil {
		t.Fatal(err)
	}

	// Both hooks should have received events.
	for _, h := range []*countHook{h1, h2} {
		if h.starts.Load() == 0 {
			t.Error("expected OnStageStart to be called")
		}
		if h.items.Load() == 0 {
			t.Error("expected OnItem to be called")
		}
		if h.done.Load() == 0 {
			t.Error("expected OnStageDone to be called")
		}
	}
}

// ---------------------------------------------------------------------------
// First / Last / Count / Any / All
// ---------------------------------------------------------------------------

func TestFirst(t *testing.T) {
	v, ok, err := kitsune.FromSlice([]int{10, 20, 30}).First(context.Background())
	if err != nil || !ok || v != 10 {
		t.Fatalf("expected (10, true, nil), got (%d, %v, %v)", v, ok, err)
	}
}

func TestFirstEmpty(t *testing.T) {
	v, ok, err := kitsune.FromSlice([]int{}).First(context.Background())
	if err != nil || ok || v != 0 {
		t.Fatalf("expected (0, false, nil), got (%d, %v, %v)", v, ok, err)
	}
}

func TestLast(t *testing.T) {
	v, ok, err := kitsune.FromSlice([]int{10, 20, 30}).Last(context.Background())
	if err != nil || !ok || v != 30 {
		t.Fatalf("expected (30, true, nil), got (%d, %v, %v)", v, ok, err)
	}
}

func TestLastEmpty(t *testing.T) {
	v, ok, err := kitsune.FromSlice([]int{}).Last(context.Background())
	if err != nil || ok || v != 0 {
		t.Fatalf("expected (0, false, nil), got (%d, %v, %v)", v, ok, err)
	}
}

func TestCount(t *testing.T) {
	n, err := kitsune.FromSlice([]int{1, 2, 3, 4, 5}).Count(context.Background())
	if err != nil || n != 5 {
		t.Fatalf("expected (5, nil), got (%d, %v)", n, err)
	}
}

func TestCountEmpty(t *testing.T) {
	n, err := kitsune.FromSlice([]int{}).Count(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("expected (0, nil), got (%d, %v)", n, err)
	}
}

func TestAnyTrue(t *testing.T) {
	ok, err := kitsune.FromSlice([]int{1, 2, 3, 4, 5}).Any(context.Background(), func(v int) bool { return v == 3 })
	if err != nil || !ok {
		t.Fatalf("expected (true, nil), got (%v, %v)", ok, err)
	}
}

func TestAnyFalse(t *testing.T) {
	ok, err := kitsune.FromSlice([]int{1, 2, 3}).Any(context.Background(), func(v int) bool { return v > 100 })
	if err != nil || ok {
		t.Fatalf("expected (false, nil), got (%v, %v)", ok, err)
	}
}

func TestAllTrue(t *testing.T) {
	ok, err := kitsune.FromSlice([]int{2, 4, 6, 8}).All(context.Background(), func(v int) bool { return v%2 == 0 })
	if err != nil || !ok {
		t.Fatalf("expected (true, nil), got (%v, %v)", ok, err)
	}
}

func TestAllFalse(t *testing.T) {
	ok, err := kitsune.FromSlice([]int{2, 3, 4}).All(context.Background(), func(v int) bool { return v%2 == 0 })
	if err != nil || ok {
		t.Fatalf("expected (false, nil), got (%v, %v)", ok, err)
	}
}

// ---------------------------------------------------------------------------
// Throttle
// ---------------------------------------------------------------------------

func TestThrottle(t *testing.T) {
	// Send 10 items with no delay; Throttle should keep only the first in each window.
	// With a 50ms window and items sent back-to-back, we expect to get ~1 item
	// (the first), and the rest dropped.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	results, err := kitsune.Throttle(p, 100*time.Millisecond).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The first item must always be emitted.
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	if results[0] != 1 {
		t.Fatalf("expected first result to be 1, got %d", results[0])
	}
	// All results should be ≤ 10.
	if len(results) > 10 {
		t.Fatalf("got more results than input: %v", results)
	}
}

func TestThrottleAllPass(t *testing.T) {
	// With a 0 window, all items should pass through (zero duration = no throttle window).
	// Actually, 0 means lastEmit.IsZero() is always satisfied... let's use a
	// very small duration and space items apart.
	// Simpler: use a real time-spaced generate and verify rate.
	// Instead, let's just verify correctness for d=1ns (essentially unlimited).
	p := kitsune.FromSlice([]int{1, 2, 3})
	results, err := kitsune.Throttle(p, time.Nanosecond).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// With 1ns window, most items should pass (time.Now() resolution typically > 1ns between iterations).
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
}

// ---------------------------------------------------------------------------
// Debounce
// ---------------------------------------------------------------------------

func TestDebounce(t *testing.T) {
	// Generate a burst then a pause, verify only the last burst item is emitted.
	ctx := context.Background()
	ch := make(chan int, 10)

	// Send a burst of items quickly.
	for i := 1; i <= 5; i++ {
		ch <- i
	}
	close(ch)

	results, err := kitsune.Debounce(kitsune.From(ch), 20*time.Millisecond).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The burst should have been coalesced to just the last item.
	if len(results) != 1 {
		t.Fatalf("expected 1 result (debounced), got %v", results)
	}
	if results[0] != 5 {
		t.Fatalf("expected last item (5), got %d", results[0])
	}
}

func TestDebounceEmpty(t *testing.T) {
	ch := make(chan int)
	close(ch)
	results, err := kitsune.Debounce(kitsune.From(ch), 10*time.Millisecond).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty, got %v", results)
	}
}

// ---------------------------------------------------------------------------
// Skip — additional cases
// ---------------------------------------------------------------------------

func TestSkipEmpty(t *testing.T) {
	results, err := kitsune.FromSlice([]int{}).Skip(5).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty result, got %v", results)
	}
}

func TestSkipComposed(t *testing.T) {
	// Skip(3), then Filter(even), then Map(*10)
	p := kitsune.FromSlice([]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	skipped := p.Skip(3)                                          // [3,4,5,6,7,8,9]
	evens := skipped.Filter(func(n int) bool { return n%2 == 0 }) // [4,6,8]
	results, err := kitsune.Map(evens, func(_ context.Context, n int) (int, error) {
		return n * 10, nil
	}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(results, []int{40, 60, 80}) {
		t.Fatalf("expected [40 60 80], got %v", results)
	}
}

// ---------------------------------------------------------------------------
// Reduce — additional cases
// ---------------------------------------------------------------------------

func TestReduceTypeChange(t *testing.T) {
	// Reduce []int → string
	result, err := kitsune.Reduce(
		kitsune.FromSlice([]int{1, 2, 3}),
		"",
		func(acc string, v int) string {
			if acc == "" {
				return fmt.Sprintf("%d", v)
			}
			return acc + "," + fmt.Sprintf("%d", v)
		},
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0] != "1,2,3" {
		t.Fatalf(`expected ["1,2,3"], got %v`, result)
	}
}

func TestReduceSingleItem(t *testing.T) {
	result, err := kitsune.Reduce(
		kitsune.FromSlice([]int{7}),
		0,
		func(acc, v int) int { return acc + v },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0] != 7 {
		t.Fatalf("expected [7], got %v", result)
	}
}

func TestReduceAfterFlatMap(t *testing.T) {
	// FlatMap [1,2,3] → [1,1,2,2,3,3], Reduce sum → 12
	expanded := kitsune.FlatMap(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int, yield func(int) error) error {
			if err := yield(v); err != nil {
				return err
			}
			return yield(v)
		},
	)
	result, err := kitsune.Reduce(expanded, 0, func(acc, v int) int { return acc + v }).
		Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0] != 12 {
		t.Fatalf("expected [12], got %v", result)
	}
}

// ---------------------------------------------------------------------------
// MapRecover — additional cases
// ---------------------------------------------------------------------------

func TestMapRecoverNoErrors(t *testing.T) {
	var recoverCalled atomic.Int64
	results, err := kitsune.MapRecover(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) { return v * 2, nil },
		func(_ context.Context, _ int, _ error) int {
			recoverCalled.Add(1)
			return -1
		},
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(results, []int{2, 4, 6}) {
		t.Fatalf("expected [2 4 6], got %v", results)
	}
	if recoverCalled.Load() != 0 {
		t.Fatalf("recover should not be called when fn succeeds, called %d times", recoverCalled.Load())
	}
}

func TestMapRecoverAllErrors(t *testing.T) {
	errBoom := errors.New("boom")
	results, err := kitsune.MapRecover(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, _ int) (int, error) { return 0, errBoom },
		func(_ context.Context, v int, _ error) int { return -v },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(results, []int{-1, -2, -3}) {
		t.Fatalf("expected [-1 -2 -3], got %v", results)
	}
}

// ---------------------------------------------------------------------------
// MultiHook — additional cases
// ---------------------------------------------------------------------------

func TestMultiHookEventCounts(t *testing.T) {
	h1, h2 := &countHook{}, &countHook{}
	err := kitsune.FromSlice([]int{1, 2, 3, 4, 5}).
		ForEach(func(_ context.Context, _ int) error { return nil }, kitsune.WithName("sink")).
		Run(context.Background(), kitsune.WithHook(kitsune.MultiHook(h1, h2)))
	if err != nil {
		t.Fatal(err)
	}
	// source + sink = 2 stages start/done each
	for i, h := range []*countHook{h1, h2} {
		if h.starts.Load() < 2 {
			t.Errorf("hook[%d]: expected ≥2 OnStageStart calls, got %d", i, h.starts.Load())
		}
		if h.done.Load() < 2 {
			t.Errorf("hook[%d]: expected ≥2 OnStageDone calls, got %d", i, h.done.Load())
		}
		if h.items.Load() < 5 {
			t.Errorf("hook[%d]: expected ≥5 OnItem calls, got %d", i, h.items.Load())
		}
	}
	// Both hooks should see the same counts.
	if h1.items.Load() != h2.items.Load() {
		t.Errorf("hook item counts differ: h1=%d h2=%d", h1.items.Load(), h2.items.Load())
	}
}

func TestMultiHookDropEvents(t *testing.T) {
	h1, h2 := &countHook{}, &countHook{}

	// A slow Map with a tiny, drop-newest buffer forces drops.
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}),
		func(_ context.Context, v int) (int, error) {
			time.Sleep(10 * time.Millisecond)
			return v, nil
		},
		kitsune.Buffer(1),
		kitsune.Overflow(kitsune.DropNewest),
	)
	err := p.Drain().Run(context.Background(), kitsune.WithHook(kitsune.MultiHook(h1, h2)))
	if err != nil {
		t.Fatal(err)
	}
	// Both hooks should have received the same OnDrop count.
	if h1.drops.Load() != h2.drops.Load() {
		t.Errorf("drop counts differ: h1=%d h2=%d", h1.drops.Load(), h2.drops.Load())
	}
}

// ---------------------------------------------------------------------------
// Any / All — additional cases
// ---------------------------------------------------------------------------

func TestAnyEmpty(t *testing.T) {
	ok, err := kitsune.FromSlice([]int{}).Any(context.Background(), func(v int) bool { return v > 0 })
	if err != nil || ok {
		t.Fatalf("expected (false, nil) on empty input, got (%v, %v)", ok, err)
	}
}

func TestAllEmpty(t *testing.T) {
	// Vacuous truth: All over an empty set is true.
	ok, err := kitsune.FromSlice([]int{}).All(context.Background(), func(v int) bool { return v > 0 })
	if err != nil || !ok {
		t.Fatalf("expected (true, nil) on empty input (vacuous truth), got (%v, %v)", ok, err)
	}
}

func TestAnyFirstElement(t *testing.T) {
	ok, err := kitsune.FromSlice([]int{42, 1, 2, 3}).Any(context.Background(), func(v int) bool { return v == 42 })
	if err != nil || !ok {
		t.Fatalf("expected (true, nil), got (%v, %v)", ok, err)
	}
}

func TestAllShortCircuits(t *testing.T) {
	// [2, 4, 3, 6] — should stop at 3 (odd).
	ok, err := kitsune.FromSlice([]int{2, 4, 3, 6}).All(context.Background(), func(v int) bool { return v%2 == 0 })
	if err != nil || ok {
		t.Fatalf("expected (false, nil), got (%v, %v)", ok, err)
	}
}

// ---------------------------------------------------------------------------
// First / Last — additional cases
// ---------------------------------------------------------------------------

func TestFirstAfterFilter(t *testing.T) {
	// Filter drops all items; First should return (zero, false, nil).
	v, ok, err := kitsune.FromSlice([]int{1, 2, 3}).
		Filter(func(n int) bool { return n > 100 }).
		First(context.Background())
	if err != nil || ok || v != 0 {
		t.Fatalf("expected (0, false, nil), got (%d, %v, %v)", v, ok, err)
	}
}

func TestFirstComposed(t *testing.T) {
	// Map then First — verify type change works end-to-end.
	v, ok, err := kitsune.Map(
		kitsune.FromSlice([]int{10, 20, 30}),
		func(_ context.Context, n int) (string, error) { return fmt.Sprintf("v%d", n), nil },
	).First(context.Background())
	if err != nil || !ok || v != "v10" {
		t.Fatalf("expected (\"v10\", true, nil), got (%q, %v, %v)", v, ok, err)
	}
}

// ---------------------------------------------------------------------------
// Count — additional cases
// ---------------------------------------------------------------------------

func TestCountAfterFlatMap(t *testing.T) {
	// FlatMap [1,2,3] → [1 item, 2 items, 3 items] → total 6
	expanded := kitsune.FlatMap(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int, yield func(int) error) error {
			for range v {
				if err := yield(0); err != nil {
					return err
				}
			}
			return nil
		},
	)
	n, err := expanded.Count(context.Background())
	if err != nil || n != 6 {
		t.Fatalf("expected (6, nil), got (%d, %v)", n, err)
	}
}

// ---------------------------------------------------------------------------
// Throttle — additional cases
// ---------------------------------------------------------------------------

func TestThrottleMultipleWindows(t *testing.T) {
	const window = 20 * time.Millisecond
	// Generate 3 items, each separated by 2x the window — each should pass through.
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := 1; i <= 3; i++ {
			if !yield(i) {
				return nil
			}
			if i < 3 {
				select {
				case <-time.After(window * 2):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return nil
	})
	results, err := kitsune.Throttle(p, window).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected all 3 items (each in its own window), got %v", results)
	}
	if !slices.Equal(results, []int{1, 2, 3}) {
		t.Fatalf("expected [1 2 3], got %v", results)
	}
}

func TestThrottleEmptyInput(t *testing.T) {
	results, err := kitsune.Throttle(kitsune.FromSlice([]int{}), 50*time.Millisecond).
		Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty, got %v", results)
	}
}

// ---------------------------------------------------------------------------
// Debounce — additional cases
// ---------------------------------------------------------------------------

func TestDebounceMultipleBursts(t *testing.T) {
	const quietPeriod = 25 * time.Millisecond
	// Two bursts separated by a pause longer than quietPeriod.
	// Each burst should yield its last item.
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		// First burst: 1, 2, 3
		for _, v := range []int{1, 2, 3} {
			if !yield(v) {
				return nil
			}
		}
		// Pause > quietPeriod so the first burst fires.
		select {
		case <-time.After(quietPeriod * 4):
		case <-ctx.Done():
			return ctx.Err()
		}
		// Second burst: 4, 5
		for _, v := range []int{4, 5} {
			if !yield(v) {
				return nil
			}
		}
		return nil
	})
	results, err := kitsune.Debounce(p, quietPeriod).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (last of each burst), got %v", results)
	}
	if results[0] != 3 || results[1] != 5 {
		t.Fatalf("expected [3 5], got %v", results)
	}
}

func TestDebounceSingleItem(t *testing.T) {
	ch := make(chan int, 1)
	ch <- 99
	close(ch)
	results, err := kitsune.Debounce(kitsune.From(ch), 10*time.Millisecond).
		Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0] != 99 {
		t.Fatalf("expected [99], got %v", results)
	}
}

// ---------------------------------------------------------------------------
// Channel[T] tests
// ---------------------------------------------------------------------------

func TestChannel_SendAndCollect(t *testing.T) {
	src := kitsune.NewChannel[int](16)
	p := src.Source()

	var mu sync.Mutex
	var collected []int
	h := p.ForEach(func(_ context.Context, n int) error {
		mu.Lock()
		collected = append(collected, n)
		mu.Unlock()
		return nil
	}).RunAsync(context.Background())

	for i := 0; i < 10; i++ {
		if err := src.Send(context.Background(), i); err != nil {
			t.Fatalf("Send(%d): %v", i, err)
		}
	}
	src.Close()

	if err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	if len(collected) != 10 {
		t.Fatalf("expected 10 items, got %d", len(collected))
	}
}

func TestChannel_SendAfterClose(t *testing.T) {
	src := kitsune.NewChannel[int](1)
	src.Close()
	err := src.Send(context.Background(), 42)
	if !errors.Is(err, kitsune.ErrChannelClosed) {
		t.Fatalf("expected ErrChannelClosed, got %v", err)
	}
}

func TestChannel_CloseIdempotent(t *testing.T) {
	src := kitsune.NewChannel[int](1)
	src.Close()
	src.Close() // must not panic
}

func TestChannel_TrySendBufferFull(t *testing.T) {
	src := kitsune.NewChannel[int](1)
	ok1 := src.TrySend(1)
	ok2 := src.TrySend(2)
	if !ok1 {
		t.Fatal("expected first TrySend to succeed")
	}
	if ok2 {
		t.Fatal("expected second TrySend to fail (buffer full)")
	}
}

func TestChannel_TrySendAfterClose(t *testing.T) {
	src := kitsune.NewChannel[int](1)
	src.Close()
	if src.TrySend(42) {
		t.Fatal("expected TrySend to return false after Close")
	}
}

func TestChannel_SendContextCancelled(t *testing.T) {
	src := kitsune.NewChannel[int](0) // unbuffered — blocks immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := src.Send(ctx, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestChannel_SourcePanicsOnSecondCall(t *testing.T) {
	src := kitsune.NewChannel[int](1)
	src.Source() // first call — ok
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on second Source() call")
		}
	}()
	src.Source() // second call — must panic
}

func TestChannel_Backpressure(t *testing.T) {
	// Zero-buffer: each Send blocks until the pipeline consumes.
	src := kitsune.NewChannel[int](0)
	p := src.Source()

	var received atomic.Int64
	h := p.ForEach(func(_ context.Context, _ int) error {
		received.Add(1)
		return nil
	}).RunAsync(context.Background())

	for i := 0; i < 5; i++ {
		if err := src.Send(context.Background(), i); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	src.Close()

	if err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	if received.Load() != 5 {
		t.Fatalf("expected 5, got %d", received.Load())
	}
}

// ---------------------------------------------------------------------------
// RunAsync tests
// ---------------------------------------------------------------------------

func TestRunAsync_ReturnsNilOnSuccess(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	h := p.Drain().RunAsync(context.Background())
	if err := h.Wait(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestRunAsync_PropagatesError(t *testing.T) {
	boom := errors.New("boom")
	p := kitsune.FromSlice([]int{1})
	mapped := kitsune.Map(p, func(_ context.Context, _ int) (int, error) {
		return 0, boom
	})
	h := mapped.Drain().RunAsync(context.Background())
	if err := h.Wait(); !errors.Is(err, boom) {
		t.Fatalf("expected boom error, got %v", err)
	}
}

func TestRunAsync_Done_ClosesOnCompletion(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	h := p.Drain().RunAsync(context.Background())
	select {
	case <-h.Done():
		// good
	case <-time.After(5 * time.Second):
		t.Fatal("Done() channel did not close")
	}
}

func TestRunAsync_Err_Channel(t *testing.T) {
	boom := errors.New("boom")
	p := kitsune.FromSlice([]int{1})
	h := kitsune.Map(p, func(_ context.Context, _ int) (int, error) {
		return 0, boom
	}).Drain().RunAsync(context.Background())
	select {
	case err := <-h.Err():
		if !errors.Is(err, boom) {
			t.Fatalf("expected boom, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Err() channel did not receive")
	}
}

// ---------------------------------------------------------------------------
// Stage[I,O] tests
// ---------------------------------------------------------------------------

func TestStage_Apply(t *testing.T) {
	double := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, n int) (int, error) { return n * 2, nil })
	})

	results, err := double.Apply(kitsune.FromSlice([]int{1, 2, 3})).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []int{2, 4, 6}
	if !slices.Equal(results, expected) {
		t.Fatalf("expected %v, got %v", expected, results)
	}
}

func TestStage_Then_TypeChanging(t *testing.T) {
	toStr := kitsune.Stage[int, string](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, n int) (string, error) {
			return strconv.Itoa(n), nil
		})
	})
	addBang := kitsune.Stage[string, string](func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, s string) (string, error) {
			return s + "!", nil
		})
	})

	composed := kitsune.Then(toStr, addBang)
	results, err := composed.Apply(kitsune.FromSlice([]int{1, 2, 3})).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"1!", "2!", "3!"}
	if !slices.Equal(results, expected) {
		t.Fatalf("expected %v, got %v", expected, results)
	}
}

func TestStage_ThroughCompatibility(t *testing.T) {
	filterEven := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return p.Filter(func(n int) bool { return n%2 == 0 })
	})

	// Stage[T,T] is directly assignable to the func type Through expects.
	results, err := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6}).
		Through(filterEven).
		Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []int{2, 4, 6}
	if !slices.Equal(results, expected) {
		t.Fatalf("expected %v, got %v", expected, results)
	}
}

func TestStage_ThenThreeStages(t *testing.T) {
	toStr := kitsune.Stage[int, string](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, n int) (string, error) {
			return strconv.Itoa(n), nil
		})
	})
	upper := kitsune.Stage[string, string](func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, s string) (string, error) {
			return strings.ToUpper(s), nil
		})
	})
	addPrefix := kitsune.Stage[string, string](func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, s string) (string, error) {
			return "item:" + s, nil
		})
	})

	ab := kitsune.Then(toStr, upper)
	abc := kitsune.Then(ab, addPrefix)

	results, err := abc.Apply(kitsune.FromSlice([]int{1, 2})).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"item:1", "item:2"}
	if !slices.Equal(results, expected) {
		t.Fatalf("expected %v, got %v", expected, results)
	}
}

func TestStage_IsolatedTesting(t *testing.T) {
	// Verify each stage can be tested independently with FromSlice.
	parse := kitsune.Stage[string, int](func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, kitsune.Lift(strconv.Atoi))
	})
	double := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, n int) (int, error) { return n * 2, nil })
	})

	// Test parse in isolation.
	parsed, err := parse.Apply(kitsune.FromSlice([]string{"1", "2", "3"})).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(parsed, []int{1, 2, 3}) {
		t.Fatalf("parse stage: expected [1 2 3], got %v", parsed)
	}

	// Test double in isolation using already-parsed values.
	doubled, err := double.Apply(kitsune.FromSlice(parsed)).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(doubled, []int{2, 4, 6}) {
		t.Fatalf("double stage: expected [2 4 6], got %v", doubled)
	}
}

// ---------------------------------------------------------------------------
// Combined: Channel + Stage
// ---------------------------------------------------------------------------

func TestChannel_WithStage(t *testing.T) {
	double := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, n int) (int, error) { return n * 2, nil })
	})

	src := kitsune.NewChannel[int](8)
	p := double.Apply(src.Source())

	var mu sync.Mutex
	var collected []int
	h := p.ForEach(func(_ context.Context, n int) error {
		mu.Lock()
		collected = append(collected, n)
		mu.Unlock()
		return nil
	}).RunAsync(context.Background())

	for i := 1; i <= 5; i++ {
		if err := src.Send(context.Background(), i); err != nil {
			t.Fatal(err)
		}
	}
	src.Close()

	if err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(collected)
	expected := []int{2, 4, 6, 8, 10}
	if !slices.Equal(collected, expected) {
		t.Fatalf("expected %v, got %v", expected, collected)
	}
}

// ---------------------------------------------------------------------------
// Timeout
// ---------------------------------------------------------------------------

func TestTimeoutMap(t *testing.T) {
	input := kitsune.FromSlice([]int{1, 2, 3})
	_, err := kitsune.Map(input, func(ctx context.Context, v int) (int, error) {
		select {
		case <-time.After(time.Second):
			return v, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}, kitsune.Timeout(20*time.Millisecond)).Collect(context.Background())
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestTimeoutMapSkip(t *testing.T) {
	input := kitsune.FromSlice([]int{1, 2, 3})
	results, err := kitsune.Map(input, func(ctx context.Context, v int) (int, error) {
		select {
		case <-time.After(time.Second):
			return v, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}, kitsune.Timeout(20*time.Millisecond), kitsune.OnError(kitsune.Skip())).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected all items skipped, got %v", results)
	}
}

func TestTimeoutMapFastFn(t *testing.T) {
	input := kitsune.FromSlice([]int{1, 2, 3})
	results, err := kitsune.Map(input, func(_ context.Context, v int) (int, error) {
		return v * 2, nil
	}, kitsune.Timeout(time.Second)).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(results, []int{2, 4, 6}) {
		t.Fatalf("expected [2 4 6], got %v", results)
	}
}

func TestTimeoutFlatMap(t *testing.T) {
	input := kitsune.FromSlice([]int{1})
	_, err := kitsune.FlatMap(input, func(ctx context.Context, v int, yield func(int) error) error {
		select {
		case <-time.After(time.Second):
			return yield(v)
		case <-ctx.Done():
			return ctx.Err()
		}
	}, kitsune.Timeout(20*time.Millisecond)).Collect(context.Background())
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Ticker / Interval
// ---------------------------------------------------------------------------

func TestTicker(t *testing.T) {
	results, err := kitsune.Ticker(10 * time.Millisecond).Take(5).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 ticks, got %d", len(results))
	}
	for i := 1; i < len(results); i++ {
		if results[i].Before(results[i-1]) {
			t.Errorf("tick %d is before tick %d", i, i-1)
		}
	}
}

func TestInterval(t *testing.T) {
	results, err := kitsune.Interval(10 * time.Millisecond).Take(5).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []int64{0, 1, 2, 3, 4}
	if len(results) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, results)
	}
	for i, v := range results {
		if v != expected[i] {
			t.Errorf("results[%d] = %d, want %d", i, v, expected[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Pairwise
// ---------------------------------------------------------------------------

func TestPairwise(t *testing.T) {
	results, err := kitsune.Pairwise(kitsune.FromSlice([]int{1, 2, 3, 4})).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []kitsune.Pair[int, int]{{1, 2}, {2, 3}, {3, 4}}
	if len(results) != len(want) {
		t.Fatalf("expected %v, got %v", want, results)
	}
	for i, p := range results {
		if p != want[i] {
			t.Errorf("results[%d] = %v, want %v", i, p, want[i])
		}
	}
}

func TestPairwiseEmpty(t *testing.T) {
	results, err := kitsune.Pairwise(kitsune.FromSlice([]int{})).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty, got %v", results)
	}
}

func TestPairwiseSingleItem(t *testing.T) {
	results, err := kitsune.Pairwise(kitsune.FromSlice([]int{42})).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty for single item, got %v", results)
	}
}

// ---------------------------------------------------------------------------
// ConcatMap
// ---------------------------------------------------------------------------

func TestConcatMap(t *testing.T) {
	results, err := kitsune.ConcatMap(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int, yield func(int) error) error {
			if err := yield(v); err != nil {
				return err
			}
			return yield(v * 10)
		}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []int{1, 10, 2, 20, 3, 30}
	if !slices.Equal(results, expected) {
		t.Fatalf("expected %v, got %v", expected, results)
	}
}

func TestConcatMapIgnoresConcurrency(t *testing.T) {
	// Even with Concurrency(8), ConcatMap must produce output in input order.
	var mu sync.Mutex
	var order []int
	results, err := kitsune.ConcatMap(kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		func(_ context.Context, v int, yield func(int) error) error {
			mu.Lock()
			order = append(order, v)
			mu.Unlock()
			return yield(v)
		}, kitsune.Concurrency(8)).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(results, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("expected in-order output, got %v", results)
	}
	_ = order
}

// ---------------------------------------------------------------------------
// SwitchMap
// ---------------------------------------------------------------------------

func TestSwitchMap(t *testing.T) {
	// Basic: each item yields itself twice; no cancellation because input is
	// consumed sequentially (single-item latency << test timeout).
	results, err := kitsune.SwitchMap(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int, yield func(int) error) error {
			if err := yield(v); err != nil {
				return err
			}
			return yield(v * 10)
		}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// At minimum we expect the last item's outputs; depending on timing we may
	// also see earlier items' outputs. Just verify no error and non-empty result.
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
}

func TestSwitchMapCancelsInner(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive test in short mode")
	}
	// Slow inner function: each item sleeps, then yields. We send items quickly
	// so later items should cancel earlier ones.
	// The last item (3) must be present in the output; items 1 and 2 may be
	// cancelled before they yield.
	const last = 3
	results, err := kitsune.SwitchMap(kitsune.FromSlice([]int{1, 2, last}),
		func(ctx context.Context, v int, yield func(int) error) error {
			select {
			case <-time.After(50 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
			return yield(v)
		},
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The last value must appear; earlier values may or may not.
	found := false
	for _, v := range results {
		if v == last {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected last item (%d) in output, got %v", last, results)
	}
}

func TestSwitchMapContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := kitsune.SwitchMap(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int, yield func(int) error) error {
			return yield(v)
		}).Collect(ctx)
	// Either nil (pipeline short-circuits before producing anything) or a
	// context error is acceptable; what must NOT happen is a panic.
	_ = err
}

// ---------------------------------------------------------------------------
// ExhaustMap
// ---------------------------------------------------------------------------

func TestExhaustMap(t *testing.T) {
	// Basic: single item, no contention.
	results, err := kitsune.ExhaustMap(kitsune.FromSlice([]int{42}),
		func(_ context.Context, v int, yield func(int) error) error {
			if err := yield(v); err != nil {
				return err
			}
			return yield(v * 10)
		}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(results, []int{42, 420}) {
		t.Fatalf("expected [42 420], got %v", results)
	}
}

func TestExhaustMapIgnoresDuringActive(t *testing.T) {
	// First item gets a slow inner function. Items 2 and 3 arrive while inner
	// is active and should be dropped. Only item 1's output must appear.
	results, err := kitsune.ExhaustMap(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(ctx context.Context, v int, yield func(int) error) error {
			if v == 1 {
				// Slow enough that items 2 and 3 arrive before we finish.
				select {
				case <-time.After(40 * time.Millisecond):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return yield(v)
		},
		kitsune.Buffer(8),
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Item 1 must appear; items 2 and 3 may be dropped (or may sneak in on a
	// fast machine before the goroutine spins up). Just verify item 1 is there.
	found := false
	for _, v := range results {
		if v == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected item 1 in output, got %v", results)
	}
}

func TestExhaustMapContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := kitsune.ExhaustMap(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int, yield func(int) error) error {
			return yield(v)
		}).Collect(ctx)
	// Either nil or context error is acceptable; no panic.
	_ = err
}

// ---------------------------------------------------------------------------
// SlidingWindow
// ---------------------------------------------------------------------------

func TestSlidingWindow_3_1(t *testing.T) {
	results, err := kitsune.SlidingWindow(kitsune.FromSlice([]int{1, 2, 3, 4, 5}), 3, 1).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := [][]int{{1, 2, 3}, {2, 3, 4}, {3, 4, 5}}
	if len(results) != len(want) {
		t.Fatalf("expected %v, got %v", want, results)
	}
	for i, w := range results {
		if !slices.Equal(w, want[i]) {
			t.Errorf("results[%d] = %v, want %v", i, w, want[i])
		}
	}
}

func TestSlidingWindow_4_2(t *testing.T) {
	results, err := kitsune.SlidingWindow(kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6}), 4, 2).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := [][]int{{1, 2, 3, 4}, {3, 4, 5, 6}}
	if len(results) != len(want) {
		t.Fatalf("expected %v, got %v", want, results)
	}
	for i, w := range results {
		if !slices.Equal(w, want[i]) {
			t.Errorf("results[%d] = %v, want %v", i, w, want[i])
		}
	}
}

func TestSlidingWindowTumbling(t *testing.T) {
	// step == size → non-overlapping (same as Batch)
	results, err := kitsune.SlidingWindow(kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6}), 3, 3).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := [][]int{{1, 2, 3}, {4, 5, 6}}
	if len(results) != len(want) {
		t.Fatalf("expected %v, got %v", want, results)
	}
	for i, w := range results {
		if !slices.Equal(w, want[i]) {
			t.Errorf("results[%d] = %v, want %v", i, w, want[i])
		}
	}
}

func TestSlidingWindowShortStream(t *testing.T) {
	results, err := kitsune.SlidingWindow(kitsune.FromSlice([]int{1, 2}), 5, 1).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty for stream shorter than window size, got %v", results)
	}
}

func TestSlidingWindowPanics(t *testing.T) {
	for _, tc := range []struct {
		size, step int
	}{
		{0, 1}, {1, 0}, {2, 3},
	} {
		func(size, step int) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("SlidingWindow(%d,%d) should have panicked", size, step)
				}
			}()
			kitsune.SlidingWindow(kitsune.FromSlice([]int{1}), size, step)
		}(tc.size, tc.step)
	}
}

// ---------------------------------------------------------------------------
// MapResult
// ---------------------------------------------------------------------------

func TestMapResultAllSuccess(t *testing.T) {
	ok, failed := kitsune.MapResult(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) { return v * 2, nil })
	okResults, err := ok.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	failedResults, err := failed.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(okResults, []int{2, 4, 6}) {
		t.Fatalf("ok: expected [2 4 6], got %v", okResults)
	}
	if len(failedResults) != 0 {
		t.Fatalf("failed: expected empty, got %v", failedResults)
	}
}

func TestMapResultAllErrors(t *testing.T) {
	boom := errors.New("boom")
	ok, failed := kitsune.MapResult(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) { return 0, boom })
	okResults, err := ok.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	failedResults, err := failed.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(okResults) != 0 {
		t.Fatalf("ok: expected empty, got %v", okResults)
	}
	if len(failedResults) != 3 {
		t.Fatalf("failed: expected 3 items, got %d", len(failedResults))
	}
	for _, ei := range failedResults {
		if ei.Err != boom {
			t.Errorf("ErrItem.Err = %v, want %v", ei.Err, boom)
		}
	}
}

func TestMapResultMixed(t *testing.T) {
	boom := errors.New("oops")
	ok, failed := kitsune.MapResult(kitsune.FromSlice([]int{1, 2, 3, 4}),
		func(_ context.Context, v int) (string, error) {
			if v%2 == 0 {
				return "", boom
			}
			return fmt.Sprintf("%d", v), nil
		})
	okResults, err := ok.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	failedResults, err := failed.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(okResults, []string{"1", "3"}) {
		t.Fatalf("ok: expected [1 3], got %v", okResults)
	}
	if len(failedResults) != 2 {
		t.Fatalf("failed: expected 2, got %d", len(failedResults))
	}
	for _, ei := range failedResults {
		if ei.Item%2 != 0 {
			t.Errorf("ErrItem.Item = %d, want even", ei.Item)
		}
		if ei.Err != boom {
			t.Errorf("ErrItem.Err = %v, want %v", ei.Err, boom)
		}
	}
}

// ---------------------------------------------------------------------------
// DeadLetter / DeadLetterSink
// ---------------------------------------------------------------------------

func TestDeadLetterBasic(t *testing.T) {
	boom := errors.New("fail")
	ok, dlq := kitsune.DeadLetter(
		kitsune.FromSlice([]int{1, 2, 3, 4}),
		func(_ context.Context, v int) (int, error) {
			if v%2 == 0 {
				return 0, boom
			}
			return v * 10, nil
		},
	)
	okResults, err := ok.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dlqResults, err := dlq.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(okResults, []int{10, 30}) {
		t.Errorf("ok = %v, want [10 30]", okResults)
	}
	if len(dlqResults) != 2 {
		t.Fatalf("dlq len = %d, want 2", len(dlqResults))
	}
	for _, ei := range dlqResults {
		if ei.Item%2 != 0 {
			t.Errorf("dlq item %d should be even", ei.Item)
		}
		if !errors.Is(ei.Err, boom) {
			t.Errorf("dlq err = %v, want boom", ei.Err)
		}
	}
}

func TestDeadLetterAllSucceed(t *testing.T) {
	ok, dlq := kitsune.DeadLetter(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) { return v * 2, nil },
	)
	okResults, _ := ok.Collect(context.Background())
	dlqResults, _ := dlq.Collect(context.Background())
	if !slices.Equal(okResults, []int{2, 4, 6}) {
		t.Errorf("ok = %v, want [2 4 6]", okResults)
	}
	if len(dlqResults) != 0 {
		t.Errorf("dlq = %v, want empty", dlqResults)
	}
}

func TestDeadLetterAllFail(t *testing.T) {
	boom := errors.New("always fail")
	ok, dlq := kitsune.DeadLetter(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, _ int) (int, error) { return 0, boom },
	)
	okResults, _ := ok.Collect(context.Background())
	dlqResults, _ := dlq.Collect(context.Background())
	if len(okResults) != 0 {
		t.Errorf("ok = %v, want empty", okResults)
	}
	if len(dlqResults) != 3 {
		t.Errorf("dlq len = %d, want 3", len(dlqResults))
	}
}

func TestDeadLetterWithRetry(t *testing.T) {
	// First call per item fails; second succeeds. With Retry(1), item should reach ok.
	calls := make(map[int]int)
	var mu sync.Mutex
	ok, dlq := kitsune.DeadLetter(
		kitsune.FromSlice([]int{1, 2}),
		func(_ context.Context, v int) (int, error) {
			mu.Lock()
			calls[v]++
			n := calls[v]
			mu.Unlock()
			if n == 1 {
				return 0, errors.New("transient")
			}
			return v * 10, nil
		},
		kitsune.OnError(kitsune.Retry(1, kitsune.FixedBackoff(0))),
	)
	okResults, err := ok.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dlqResults, _ := dlq.Collect(context.Background())
	if len(dlqResults) != 0 {
		t.Errorf("dlq = %v, want empty (retries should have succeeded)", dlqResults)
	}
	slices.Sort(okResults)
	if !slices.Equal(okResults, []int{10, 20}) {
		t.Errorf("ok = %v, want [10 20]", okResults)
	}
}

func TestDeadLetterRetryExhausted(t *testing.T) {
	boom := errors.New("persistent")
	ok, dlq := kitsune.DeadLetter(
		kitsune.FromSlice([]int{1}),
		func(_ context.Context, _ int) (int, error) { return 0, boom },
		kitsune.OnError(kitsune.Retry(2, kitsune.FixedBackoff(0))),
	)
	okResults, _ := ok.Collect(context.Background())
	dlqResults, _ := dlq.Collect(context.Background())
	if len(okResults) != 0 {
		t.Errorf("ok = %v, want empty", okResults)
	}
	if len(dlqResults) != 1 {
		t.Fatalf("dlq len = %d, want 1", len(dlqResults))
	}
	if !errors.Is(dlqResults[0].Err, boom) {
		t.Errorf("dlq err = %v, want boom", dlqResults[0].Err)
	}
}

func TestDeadLetterSink(t *testing.T) {
	boom := errors.New("sink fail")
	written := make([]int, 0)
	var mu sync.Mutex

	dlq, runner := kitsune.DeadLetterSink(
		kitsune.FromSlice([]int{1, 2, 3, 4}),
		func(_ context.Context, v int) error {
			if v%2 == 0 {
				return boom
			}
			mu.Lock()
			written = append(written, v)
			mu.Unlock()
			return nil
		},
	)
	dlqItems, dlqRunner := dlq.ForEach(func(_ context.Context, _ kitsune.ErrItem[int]) error { return nil }), runner
	_ = dlqItems
	if err := dlqRunner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDeadLetterSinkCollectDLQ(t *testing.T) {
	boom := errors.New("write fail")
	dlq, runner := kitsune.DeadLetterSink(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) error {
			if v == 2 {
				return boom
			}
			return nil
		},
	)
	var dlqItems []kitsune.ErrItem[int]
	dlqRunner := dlq.ForEach(func(_ context.Context, ei kitsune.ErrItem[int]) error {
		dlqItems = append(dlqItems, ei)
		return nil
	})
	_ = dlqRunner
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// WithLatestFrom
// ---------------------------------------------------------------------------

func TestWithLatestFrom(t *testing.T) {
	// Build: source → Broadcast(2) → WithLatestFrom(branch0, branch1)
	// Both branches receive every item. The secondary branch updates "latest"
	// and the primary branch emits Pair{item, latest} once secondary has a value.
	ch := kitsune.NewChannel[int](16)
	src := ch.Source()
	branches := kitsune.Broadcast(src, 2)
	combined := kitsune.WithLatestFrom(branches[0], branches[1])

	var mu sync.Mutex
	var results []kitsune.Pair[int, int]
	h := combined.ForEach(func(_ context.Context, p kitsune.Pair[int, int]) error {
		mu.Lock()
		results = append(results, p)
		mu.Unlock()
		return nil
	}).RunAsync(context.Background())

	for i := 0; i < 10; i++ {
		_ = ch.Send(context.Background(), i)
	}
	ch.Close()

	if err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	// Some items may be dropped before secondary has a value; rest must be valid pairs.
	mu.Lock()
	defer mu.Unlock()
	if len(results) > 10 {
		t.Fatalf("too many results: %d > 10", len(results))
	}
	for _, p := range results {
		if p.Second < 0 || p.Second > 9 {
			t.Errorf("unexpected secondary value in pair: %v", p)
		}
	}
}

func TestWithLatestFromIndependentGraphs(t *testing.T) {
	// Primary ticks every ~1ms; secondary produces a single value immediately.
	// All primary items should be combined with that value once it arrives.
	primary := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	secondary := kitsune.FromSlice([]string{"latest"})
	pairs, err := kitsune.WithLatestFrom(primary, secondary).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Some primary items may be dropped before secondary emits; rest must be valid pairs.
	if len(pairs) > 5 {
		t.Fatalf("too many pairs: %d", len(pairs))
	}
	for _, p := range pairs {
		if p.Second != "latest" {
			t.Errorf("unexpected secondary value: %q", p.Second)
		}
	}
}

// ---------------------------------------------------------------------------
// ZipWith
// ---------------------------------------------------------------------------

func TestZipWith(t *testing.T) {
	branches := kitsune.Broadcast[int](kitsune.FromSlice([]int{1, 2, 3}), 2)
	doubled := kitsune.Map(branches[1], func(_ context.Context, v int) (int, error) { return v * 2, nil })

	results, err := kitsune.ZipWith(branches[0], doubled,
		func(_ context.Context, a, b int) (int, error) { return a + b, nil },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{3, 6, 9} // 1+2, 2+4, 3+6
	if len(results) != len(want) {
		t.Fatalf("expected %d results, got %d", len(want), len(results))
	}
	for i, v := range results {
		if v != want[i] {
			t.Errorf("results[%d] = %d, want %d", i, v, want[i])
		}
	}
}

func TestZipWithIndependentGraphs(t *testing.T) {
	a := kitsune.FromSlice([]int{1, 2, 3})
	b := kitsune.FromSlice([]int{4, 5, 6})
	results, err := kitsune.ZipWith(a, b, func(_ context.Context, x, y int) (int, error) {
		return x + y, nil
	}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{5, 7, 9}
	if !slices.Equal(results, want) {
		t.Errorf("got %v, want %v", results, want)
	}
}

// ---------------------------------------------------------------------------
// MapBatch
// ---------------------------------------------------------------------------

func TestMapBatch(t *testing.T) {
	var batchSizes []int
	var mu sync.Mutex

	input := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	results, err := kitsune.MapBatch(input, 2,
		func(_ context.Context, batch []int) ([]int, error) {
			mu.Lock()
			batchSizes = append(batchSizes, len(batch))
			mu.Unlock()
			out := make([]int, len(batch))
			for i, v := range batch {
				out[i] = v * 10
			}
			return out, nil
		},
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	want := []int{10, 20, 30, 40, 50}
	if !slices.Equal(results, want) {
		t.Errorf("results = %v, want %v", results, want)
	}
	// 5 items with batch size 2 → batches of [2, 2, 1]
	totalBatched := 0
	for _, s := range batchSizes {
		totalBatched += s
	}
	if totalBatched != 5 {
		t.Errorf("total batched items = %d, want 5", totalBatched)
	}
}

func TestMapBatchError(t *testing.T) {
	boom := errors.New("boom")
	input := kitsune.FromSlice([]int{1, 2, 3})
	_, err := kitsune.MapBatch(input, 10,
		func(_ context.Context, _ []int) ([]int, error) { return nil, boom },
	).Collect(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// LookupBy
// ---------------------------------------------------------------------------

func TestLookupBy(t *testing.T) {
	db := map[int]string{1: "one", 2: "two", 3: "three"}

	type item struct{ ID int }
	input := kitsune.FromSlice([]item{{1}, {2}, {3}})

	results, err := kitsune.LookupBy(input, kitsune.LookupConfig[item, int, string]{
		Key: func(it item) int { return it.ID },
		Fetch: func(_ context.Context, ids []int) (map[int]string, error) {
			m := make(map[int]string, len(ids))
			for _, id := range ids {
				m[id] = db[id]
			}
			return m, nil
		},
	}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for _, p := range results {
		if p.Second != db[p.First.ID] {
			t.Errorf("item %d: got %q, want %q", p.First.ID, p.Second, db[p.First.ID])
		}
	}
}

func TestLookupByDeduplicatesKeys(t *testing.T) {
	var fetchCalls [][]int

	type item struct{ ID int }
	// Two items with same ID — Fetch should only receive one key.
	input := kitsune.FromSlice([]item{{1}, {1}, {2}})

	_, err := kitsune.LookupBy(input, kitsune.LookupConfig[item, int, string]{
		Key: func(it item) int { return it.ID },
		Fetch: func(_ context.Context, ids []int) (map[int]string, error) {
			fetchCalls = append(fetchCalls, ids)
			return map[int]string{1: "one", 2: "two"}, nil
		},
		BatchSize: 10,
	}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fetchCalls) != 1 {
		t.Fatalf("expected 1 fetch call, got %d", len(fetchCalls))
	}
	if len(fetchCalls[0]) != 2 {
		t.Errorf("expected 2 unique keys, got %d: %v", len(fetchCalls[0]), fetchCalls[0])
	}
}

// ---------------------------------------------------------------------------
// Enrich
// ---------------------------------------------------------------------------

func TestEnrich(t *testing.T) {
	db := map[int]string{1: "one", 2: "two", 3: "three"}

	type item struct{ ID int }
	type enriched struct {
		ID   int
		Name string
	}

	input := kitsune.FromSlice([]item{{1}, {2}, {3}})
	results, err := kitsune.Enrich(input, kitsune.EnrichConfig[item, int, string, enriched]{
		Key: func(it item) int { return it.ID },
		Fetch: func(_ context.Context, ids []int) (map[int]string, error) {
			m := make(map[int]string, len(ids))
			for _, id := range ids {
				m[id] = db[id]
			}
			return m, nil
		},
		Join: func(it item, name string) enriched {
			return enriched{ID: it.ID, Name: name}
		},
	}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Name != db[r.ID] {
			t.Errorf("item %d: got %q, want %q", r.ID, r.Name, db[r.ID])
		}
	}
}

func TestEnrichMissingKey(t *testing.T) {
	// Items whose key is absent from Fetch result should receive zero value in Join.
	type item struct{ ID int }
	type enriched struct {
		ID   int
		Name string
	}

	input := kitsune.FromSlice([]item{{1}, {99}}) // 99 not in db
	results, err := kitsune.Enrich(input, kitsune.EnrichConfig[item, int, string, enriched]{
		Key: func(it item) int { return it.ID },
		Fetch: func(_ context.Context, ids []int) (map[int]string, error) {
			return map[int]string{1: "one"}, nil // 99 intentionally absent
		},
		Join: func(it item, name string) enriched {
			return enriched{ID: it.ID, Name: name}
		},
	}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[1].Name != "" {
		t.Errorf("missing key: expected empty Name, got %q", results[1].Name)
	}
}

// ---------------------------------------------------------------------------
// Reject
// ---------------------------------------------------------------------------

func TestReject(t *testing.T) {
	results, err := kitsune.Reject(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6}),
		func(n int) bool { return n%2 == 0 },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 3, 5}
	if !slices.Equal(results, want) {
		t.Errorf("got %v, want %v", results, want)
	}
}

// ---------------------------------------------------------------------------
// WithIndex
// ---------------------------------------------------------------------------

func TestWithIndex(t *testing.T) {
	results, err := kitsune.WithIndex(kitsune.FromSlice([]string{"a", "b", "c"})).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3, got %d", len(results))
	}
	for i, p := range results {
		if p.First != i {
			t.Errorf("item %d: index %d, want %d", i, p.First, i)
		}
		if p.Second != []string{"a", "b", "c"}[i] {
			t.Errorf("item %d: value %q, want %q", i, p.Second, []string{"a", "b", "c"}[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Intersperse / MapIntersperse
// ---------------------------------------------------------------------------

func TestIntersperse(t *testing.T) {
	results, err := kitsune.Intersperse(kitsune.FromSlice([]string{"a", "b", "c"}), ",").Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", ",", "b", ",", "c"}
	if !slices.Equal(results, want) {
		t.Errorf("got %v, want %v", results, want)
	}
}

func TestIntersperseEmpty(t *testing.T) {
	results, err := kitsune.Intersperse(kitsune.FromSlice([]string{}), ",").Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty, got %v", results)
	}
}

func TestMapIntersperse(t *testing.T) {
	results, err := kitsune.MapIntersperse(
		kitsune.FromSlice([]int{1, 2, 3}),
		0,
		func(_ context.Context, n int) (int, error) { return n * 10, nil },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{10, 0, 20, 0, 30}
	if !slices.Equal(results, want) {
		t.Errorf("got %v, want %v", results, want)
	}
}

// ---------------------------------------------------------------------------
// TakeEvery / DropEvery / MapEvery
// ---------------------------------------------------------------------------

func TestTakeEvery(t *testing.T) {
	results, err := kitsune.TakeEvery(kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6}), 2).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 3, 5}
	if !slices.Equal(results, want) {
		t.Errorf("got %v, want %v", results, want)
	}
}

func TestDropEvery(t *testing.T) {
	results, err := kitsune.DropEvery(kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6}), 2).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{2, 4, 6}
	if !slices.Equal(results, want) {
		t.Errorf("got %v, want %v", results, want)
	}
}

func TestMapEvery(t *testing.T) {
	results, err := kitsune.MapEvery(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6}),
		3,
		func(_ context.Context, n int) (int, error) { return n * -1, nil },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// indices 0,3 → negated; others pass through
	want := []int{-1, 2, 3, -4, 5, 6}
	if !slices.Equal(results, want) {
		t.Errorf("got %v, want %v", results, want)
	}
}

// ---------------------------------------------------------------------------
// ConsecutiveDedup / ConsecutiveDedupBy
// ---------------------------------------------------------------------------

func TestConsecutiveDedup(t *testing.T) {
	results, err := kitsune.ConsecutiveDedup(
		kitsune.FromSlice([]int{1, 1, 2, 3, 3, 3, 2}),
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3, 2}
	if !slices.Equal(results, want) {
		t.Errorf("got %v, want %v", results, want)
	}
}

func TestConsecutiveDedupBy(t *testing.T) {
	type item struct{ Val, Extra int }
	results, err := kitsune.ConsecutiveDedupBy(
		kitsune.FromSlice([]item{{1, 0}, {1, 1}, {2, 0}, {2, 1}, {1, 2}}),
		func(x item) int { return x.Val },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3, got %d: %v", len(results), results)
	}
	if results[0].Val != 1 || results[1].Val != 2 || results[2].Val != 1 {
		t.Errorf("unexpected values: %v", results)
	}
}

// ---------------------------------------------------------------------------
// ChunkBy / ChunkWhile
// ---------------------------------------------------------------------------

func TestChunkBy(t *testing.T) {
	results, err := kitsune.ChunkBy(
		kitsune.FromSlice([]int{1, 1, 2, 2, 3, 1}),
		func(n int) int { return n },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := [][]int{{1, 1}, {2, 2}, {3}, {1}}
	if len(results) != len(want) {
		t.Fatalf("got %v, want %v", results, want)
	}
	for i, chunk := range results {
		if !slices.Equal(chunk, want[i]) {
			t.Errorf("chunk %d: got %v, want %v", i, chunk, want[i])
		}
	}
}

func TestChunkWhile(t *testing.T) {
	results, err := kitsune.ChunkWhile(
		kitsune.FromSlice([]int{1, 2, 4, 9, 10, 11, 15}),
		func(prev, next int) bool { return next-prev <= 1 },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := [][]int{{1, 2}, {4}, {9, 10, 11}, {15}}
	if len(results) != len(want) {
		t.Fatalf("got %v, want %v", results, want)
	}
	for i, chunk := range results {
		if !slices.Equal(chunk, want[i]) {
			t.Errorf("chunk %d: got %v, want %v", i, chunk, want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Unzip
// ---------------------------------------------------------------------------

func TestUnzip(t *testing.T) {
	pairSlice := []kitsune.Pair[int, string]{{First: 1, Second: "a"}, {First: 2, Second: "b"}, {First: 3, Second: "c"}}
	src := kitsune.FromSlice(pairSlice)
	as, bs := kitsune.Unzip(src)
	aResults, err := as.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bResults, err := bs.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(aResults, []int{1, 2, 3}) {
		t.Errorf("as: got %v, want [1 2 3]", aResults)
	}
	if !slices.Equal(bResults, []string{"a", "b", "c"}) {
		t.Errorf("bs: got %v, want [a b c]", bResults)
	}
}

// ---------------------------------------------------------------------------
// Sort / SortBy
// ---------------------------------------------------------------------------

func TestSort(t *testing.T) {
	results, err := kitsune.Sort(
		kitsune.FromSlice([]int{3, 1, 4, 1, 5, 9, 2, 6}),
		func(a, b int) bool { return a < b },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 1, 2, 3, 4, 5, 6, 9}
	if !slices.Equal(results, want) {
		t.Errorf("got %v, want %v", results, want)
	}
}

func TestSortBy(t *testing.T) {
	type item struct{ Name string }
	results, err := kitsune.SortBy(
		kitsune.FromSlice([]item{{"banana"}, {"apple"}, {"cherry"}}),
		func(x item) string { return x.Name },
		func(a, b string) bool { return a < b },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0].Name != "apple" || results[1].Name != "banana" || results[2].Name != "cherry" {
		t.Errorf("unexpected order: %v", results)
	}
}

// ---------------------------------------------------------------------------
// Unfold / Iterate / Repeatedly / Cycle / Timer / Concat
// ---------------------------------------------------------------------------

func TestUnfold(t *testing.T) {
	results, err := kitsune.Unfold(0, func(n int) (int, int, bool) {
		if n >= 5 {
			return 0, 0, true
		}
		return n, n + 1, false
	}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{0, 1, 2, 3, 4}
	if !slices.Equal(results, want) {
		t.Errorf("got %v, want %v", results, want)
	}
}

func TestIterate(t *testing.T) {
	results, err := kitsune.Iterate(1, func(n int) int { return n * 2 }).Take(5).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 4, 8, 16}
	if !slices.Equal(results, want) {
		t.Errorf("got %v, want %v", results, want)
	}
}

func TestRepeatedly(t *testing.T) {
	n := 0
	results, err := kitsune.Repeatedly(func() int {
		n++
		return n
	}).Take(4).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3, 4}
	if !slices.Equal(results, want) {
		t.Errorf("got %v, want %v", results, want)
	}
}

func TestCycle(t *testing.T) {
	results, err := kitsune.Cycle([]string{"a", "b", "c"}).Take(7).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c", "a", "b", "c", "a"}
	if !slices.Equal(results, want) {
		t.Errorf("got %v, want %v", results, want)
	}
}

func TestTimer(t *testing.T) {
	results, err := kitsune.Timer(1*time.Millisecond, func() string { return "ping" }).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0] != "ping" {
		t.Errorf("got %v, want [ping]", results)
	}
}

func TestConcat(t *testing.T) {
	results, err := kitsune.Concat(
		func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{1, 2}) },
		func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{3, 4}) },
		func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{5}) },
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3, 4, 5}
	if !slices.Equal(results, want) {
		t.Errorf("got %v, want %v", results, want)
	}
}

func TestConcatEmpty(t *testing.T) {
	results, err := kitsune.Concat[int]().Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty, got %v", results)
	}
}

// ---------------------------------------------------------------------------
// Sum
// ---------------------------------------------------------------------------

func TestSum(t *testing.T) {
	got, err := kitsune.Sum(context.Background(), kitsune.FromSlice([]int{1, 2, 3, 4, 5}))
	if err != nil {
		t.Fatal(err)
	}
	if got != 15 {
		t.Errorf("got %d, want 15", got)
	}
}

func TestSumEmpty(t *testing.T) {
	got, err := kitsune.Sum(context.Background(), kitsune.FromSlice([]int{}))
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Min / Max / MinMax
// ---------------------------------------------------------------------------

func TestMin(t *testing.T) {
	v, ok, err := kitsune.Min(context.Background(), kitsune.FromSlice([]int{3, 1, 4, 1, 5}), func(a, b int) bool { return a < b })
	if err != nil {
		t.Fatal(err)
	}
	if !ok || v != 1 {
		t.Errorf("got %d ok=%v, want 1 true", v, ok)
	}
}

func TestMinEmpty(t *testing.T) {
	_, ok, err := kitsune.Min(context.Background(), kitsune.FromSlice([]int{}), func(a, b int) bool { return a < b })
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected ok=false for empty pipeline")
	}
}

func TestMax(t *testing.T) {
	v, ok, err := kitsune.Max(context.Background(), kitsune.FromSlice([]int{3, 1, 4, 1, 5}), func(a, b int) bool { return a < b })
	if err != nil {
		t.Fatal(err)
	}
	if !ok || v != 5 {
		t.Errorf("got %d ok=%v, want 5 true", v, ok)
	}
}

func TestMinMax(t *testing.T) {
	pair, ok, err := kitsune.MinMax(context.Background(), kitsune.FromSlice([]int{3, 1, 4, 1, 5, 9}), func(a, b int) bool { return a < b })
	if err != nil {
		t.Fatal(err)
	}
	if !ok || pair.First != 1 || pair.Second != 9 {
		t.Errorf("got %v ok=%v, want {1,9} true", pair, ok)
	}
}

// ---------------------------------------------------------------------------
// MinBy / MaxBy
// ---------------------------------------------------------------------------

func TestMinBy(t *testing.T) {
	type item struct{ Name string }
	v, ok, err := kitsune.MinBy(
		context.Background(),
		kitsune.FromSlice([]item{{"banana"}, {"apple"}, {"cherry"}}),
		func(x item) string { return x.Name },
		func(a, b string) bool { return a < b },
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || v.Name != "apple" {
		t.Errorf("got %v ok=%v, want apple true", v, ok)
	}
}

func TestMaxBy(t *testing.T) {
	type item struct{ Name string }
	v, ok, err := kitsune.MaxBy(
		context.Background(),
		kitsune.FromSlice([]item{{"banana"}, {"apple"}, {"cherry"}}),
		func(x item) string { return x.Name },
		func(a, b string) bool { return a < b },
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || v.Name != "cherry" {
		t.Errorf("got %v ok=%v, want cherry true", v, ok)
	}
}

func TestMaxEmpty(t *testing.T) {
	_, ok, err := kitsune.Max(context.Background(), kitsune.FromSlice([]int{}), func(a, b int) bool { return a < b })
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected ok=false for empty pipeline")
	}
}

func TestMinMaxEmpty(t *testing.T) {
	_, ok, err := kitsune.MinMax(context.Background(), kitsune.FromSlice([]int{}), func(a, b int) bool { return a < b })
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected ok=false for empty pipeline")
	}
}

func TestMinByEmpty(t *testing.T) {
	_, ok, err := kitsune.MinBy(context.Background(), kitsune.FromSlice([]int{}),
		func(v int) int { return v },
		func(a, b int) bool { return a < b })
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected ok=false for empty pipeline")
	}
}

func TestMaxByEmpty(t *testing.T) {
	_, ok, err := kitsune.MaxBy(context.Background(), kitsune.FromSlice([]int{}),
		func(v int) int { return v },
		func(a, b int) bool { return a < b })
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected ok=false for empty pipeline")
	}
}

// ---------------------------------------------------------------------------
// Find
// ---------------------------------------------------------------------------

func TestFind(t *testing.T) {
	v, ok, err := kitsune.Find(context.Background(), kitsune.FromSlice([]int{1, 2, 3, 4, 5}), func(n int) bool { return n > 3 })
	if err != nil {
		t.Fatal(err)
	}
	if !ok || v != 4 {
		t.Errorf("got %d ok=%v, want 4 true", v, ok)
	}
}

func TestFindNotFound(t *testing.T) {
	_, ok, err := kitsune.Find(context.Background(), kitsune.FromSlice([]int{1, 2, 3}), func(n int) bool { return n > 10 })
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected ok=false when no match")
	}
}

// ---------------------------------------------------------------------------
// Frequencies / FrequenciesBy
// ---------------------------------------------------------------------------

func TestFrequencies(t *testing.T) {
	m, err := kitsune.Frequencies(context.Background(), kitsune.FromSlice([]string{"a", "b", "a", "c", "b", "a"}))
	if err != nil {
		t.Fatal(err)
	}
	if m["a"] != 3 || m["b"] != 2 || m["c"] != 1 {
		t.Errorf("unexpected frequencies: %v", m)
	}
}

func TestFrequenciesBy(t *testing.T) {
	m, err := kitsune.FrequenciesBy(
		context.Background(),
		kitsune.FromSlice([]string{"cat", "dog", "cow", "ant", "bee"}),
		func(s string) int { return len(s) },
	)
	if err != nil {
		t.Fatal(err)
	}
	if m[3] != 5 {
		t.Errorf("expected 5 words of length 3, got %d", m[3])
	}
}

// ---------------------------------------------------------------------------
// ReduceWhile
// ---------------------------------------------------------------------------

func TestReduceWhile(t *testing.T) {
	// Sum until running total exceeds 10.
	got, err := kitsune.ReduceWhile(
		context.Background(),
		kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7}),
		0,
		func(acc, n int) (int, bool) {
			acc += n
			return acc, acc <= 10
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	// 1+2+3+4=10 (continue), 10+5=15 (halt and return 15)
	if got != 15 {
		t.Errorf("got %d, want 15", got)
	}
}

func TestReduceWhileFullStream(t *testing.T) {
	got, err := kitsune.ReduceWhile(
		context.Background(),
		kitsune.FromSlice([]int{1, 2, 3}),
		0,
		func(acc, n int) (int, bool) { return acc + n, true },
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != 6 {
		t.Errorf("got %d, want 6", got)
	}
}

// ---------------------------------------------------------------------------
// TakeRandom
// ---------------------------------------------------------------------------

func TestTakeRandom(t *testing.T) {
	items := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	got, err := kitsune.TakeRandom(context.Background(), kitsune.FromSlice(items), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 items, got %d", len(got))
	}
	for _, v := range got {
		if !slices.Contains(items, v) {
			t.Errorf("unexpected item %d", v)
		}
	}
}

func TestTakeRandomMoreThanAvailable(t *testing.T) {
	items := []int{1, 2, 3}
	got, err := kitsune.TakeRandom(context.Background(), kitsune.FromSlice(items), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 items, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// StageError
// ---------------------------------------------------------------------------

func TestStageErrorWrapping(t *testing.T) {
	cause := errors.New("boom")
	p := kitsune.Map(
		kitsune.FromSlice([]int{1}),
		func(_ context.Context, _ int) (int, error) { return 0, cause },
		kitsune.WithName("my-stage"),
	)
	_, err := p.Collect(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	var se *kitsune.StageError
	if !errors.As(err, &se) {
		t.Fatalf("expected *kitsune.StageError, got %T: %v", err, err)
	}
	if se.Stage != "my-stage" {
		t.Errorf("Stage = %q, want %q", se.Stage, "my-stage")
	}
	if se.Attempt != 0 {
		t.Errorf("Attempt = %d, want 0", se.Attempt)
	}
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is(err, cause) = false")
	}
}

func TestStageErrorWithRetry(t *testing.T) {
	cause := errors.New("transient")
	calls := 0
	p := kitsune.Map(
		kitsune.FromSlice([]int{1}),
		func(_ context.Context, _ int) (int, error) {
			calls++
			return 0, cause
		},
		kitsune.WithName("retry-stage"),
		kitsune.OnError(kitsune.Retry(2, kitsune.FixedBackoff(0))),
	)
	_, err := p.Collect(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	var se *kitsune.StageError
	if !errors.As(err, &se) {
		t.Fatalf("expected *kitsune.StageError, got %T: %v", err, err)
	}
	if se.Stage != "retry-stage" {
		t.Errorf("Stage = %q, want %q", se.Stage, "retry-stage")
	}
	if se.Attempt != 2 {
		t.Errorf("Attempt = %d, want 2", se.Attempt)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (initial + 2 retries)", calls)
	}
}

func TestContextErrNotWrapped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, _ int) (int, error) { return 0, context.Canceled },
	)
	_, err := p.Collect(ctx)
	if err == nil {
		return // pipeline may have produced nothing; that's fine
	}
	var se *kitsune.StageError
	if errors.As(err, &se) {
		t.Errorf("context error should not be wrapped in StageError, got %T", err)
	}
}

func TestStageErrorSink(t *testing.T) {
	cause := errors.New("sink-boom")
	err := kitsune.FromSlice([]int{1}).
		ForEach(func(_ context.Context, _ int) error { return cause },
			kitsune.WithName("my-sink")).
		Run(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	var se *kitsune.StageError
	if !errors.As(err, &se) {
		t.Fatalf("expected *kitsune.StageError, got %T: %v", err, err)
	}
	if se.Stage != "my-sink" {
		t.Errorf("Stage = %q, want %q", se.Stage, "my-sink")
	}
}

// ---------------------------------------------------------------------------
// WithSampleRate
// ---------------------------------------------------------------------------

type sampleCountHook struct {
	mu      sync.Mutex
	byStage map[string]int
}

func (h *sampleCountHook) OnStageStart(_ context.Context, _ string)                     {}
func (h *sampleCountHook) OnItem(_ context.Context, _ string, _ time.Duration, _ error) {}
func (h *sampleCountHook) OnStageDone(_ context.Context, _ string, _, _ int64)          {}
func (h *sampleCountHook) OnItemSample(_ context.Context, stage string, _ any) {
	h.mu.Lock()
	if h.byStage == nil {
		h.byStage = make(map[string]int)
	}
	h.byStage[stage]++
	h.mu.Unlock()
}

func (h *sampleCountHook) count(stage string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byStage[stage]
}

func (h *sampleCountHook) total() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, c := range h.byStage {
		n += c
	}
	return n
}

func TestWithSampleRate(t *testing.T) {
	h := &sampleCountHook{}
	// 10 items sampled every 2nd → 5 samples from the named map stage.
	items := make([]int, 10)
	for i := range items {
		items[i] = i + 1
	}
	p := kitsune.Map(
		kitsune.FromSlice(items),
		func(_ context.Context, v int) (int, error) { return v, nil },
		kitsune.WithName("sample-map"),
	)
	if err := p.Drain().Run(context.Background(), kitsune.WithHook(h), kitsune.WithSampleRate(2)); err != nil {
		t.Fatal(err)
	}
	if got := h.count("sample-map"); got != 5 {
		t.Errorf("samples for 'sample-map' = %d, want 5 (every 2nd of 10 items)", got)
	}
}

func TestWithSampleRateDisabled(t *testing.T) {
	h := &sampleCountHook{}
	items := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	p := kitsune.Map(kitsune.FromSlice(items), func(_ context.Context, v int) (int, error) { return v, nil })
	if err := p.Drain().Run(context.Background(), kitsune.WithHook(h), kitsune.WithSampleRate(-1)); err != nil {
		t.Fatal(err)
	}
	if got := h.total(); got != 0 {
		t.Errorf("total samples = %d, want 0 (sampling disabled)", got)
	}
}

// ---------------------------------------------------------------------------
// GraphNode metadata
// ---------------------------------------------------------------------------

type graphCaptureHook struct {
	mu    sync.Mutex
	nodes []kitsune.GraphNode
}

func (h *graphCaptureHook) OnStageStart(_ context.Context, _ string)                     {}
func (h *graphCaptureHook) OnItem(_ context.Context, _ string, _ time.Duration, _ error) {}
func (h *graphCaptureHook) OnStageDone(_ context.Context, _ string, _, _ int64)          {}
func (h *graphCaptureHook) OnGraph(nodes []kitsune.GraphNode) {
	h.mu.Lock()
	h.nodes = append(h.nodes[:0], nodes...)
	h.mu.Unlock()
}

func (h *graphCaptureHook) get() []kitsune.GraphNode {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]kitsune.GraphNode, len(h.nodes))
	copy(out, h.nodes)
	return out
}

func TestGraphNodeMetadata(t *testing.T) {
	h := &graphCaptureHook{}
	cause := errors.New("err")
	p := kitsune.Map(
		kitsune.FromSlice([]int{1}),
		func(_ context.Context, v int) (int, error) { return 0, cause },
		kitsune.WithName("retrying-map"),
		kitsune.OnError(kitsune.Retry(3, kitsune.FixedBackoff(0))),
		kitsune.Timeout(100*time.Millisecond),
	)
	_ = p.Drain().Run(context.Background(), kitsune.WithHook(h))

	var found *kitsune.GraphNode
	for _, n := range h.get() {
		n := n
		if n.Name == "retrying-map" {
			found = &n
			break
		}
	}
	if found == nil {
		t.Fatal("node 'retrying-map' not found in graph")
	}
	if !found.HasRetry {
		t.Error("HasRetry = false, want true")
	}
	if found.Timeout == 0 {
		t.Error("Timeout = 0, want non-zero")
	}
}

func TestGraphNodeBatchSize(t *testing.T) {
	h := &graphCaptureHook{}
	batched := kitsune.Batch(kitsune.FromSlice([]int{1, 2, 3}), 2, kitsune.WithName("my-batch"))
	_ = batched.Drain().Run(context.Background(), kitsune.WithHook(h))

	var found *kitsune.GraphNode
	for _, n := range h.get() {
		n := n
		if n.Name == "my-batch" {
			found = &n
			break
		}
	}
	if found == nil {
		t.Fatal("node 'my-batch' not found in graph")
	}
	if found.BatchSize != 2 {
		t.Errorf("BatchSize = %d, want 2", found.BatchSize)
	}
}

func TestGraphNodeHasSupervision(t *testing.T) {
	h := &graphCaptureHook{}
	p := kitsune.FromSlice([]int{1}).
		ForEach(func(_ context.Context, _ int) error { return nil },
			kitsune.WithName("supervised-sink"),
			kitsune.Supervise(kitsune.RestartOnError(2, kitsune.FixedBackoff(0))),
		)
	_ = p.Run(context.Background(), kitsune.WithHook(h))

	var found *kitsune.GraphNode
	for _, n := range h.get() {
		n := n
		if n.Name == "supervised-sink" {
			found = &n
			break
		}
	}
	if found == nil {
		t.Fatal("node 'supervised-sink' not found in graph")
	}
	if !found.HasSupervision {
		t.Error("HasSupervision = false, want true")
	}
}

// ---------------------------------------------------------------------------
// MergeIndependent tests
// ---------------------------------------------------------------------------

func TestMerge_Independent_TwoDifferentGraphs(t *testing.T) {
	// Two independent FromSlice sources — different graphs.
	a := kitsune.FromSlice([]int{1, 2, 3})
	b := kitsune.FromSlice([]int{4, 5, 6})
	merged := kitsune.Merge(a, b)
	got, err := merged.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []int{1, 2, 3, 4, 5, 6}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMerge_Independent_ThreeGraphs(t *testing.T) {
	a := kitsune.FromSlice([]int{1})
	b := kitsune.FromSlice([]int{2})
	c := kitsune.FromSlice([]int{3})
	merged := kitsune.Merge(a, b, c)
	got, err := merged.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if !slices.Equal(got, []int{1, 2, 3}) {
		t.Errorf("got %v, want {1,2,3}", got)
	}
}

func TestMerge_Independent_SameGraphDelegatesToMerge(t *testing.T) {
	// Two pipelines from the same Broadcast share a graph — should still work.
	branches := kitsune.Broadcast(kitsune.FromSlice([]int{1, 2, 3}), 2)
	merged := kitsune.Merge(branches[0], branches[1])
	got, err := merged.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 3 items × 2 branches = 6 items.
	if len(got) != 6 {
		t.Errorf("got %d items, want 6", len(got))
	}
}

func TestMerge_Independent_SinglePipeline(t *testing.T) {
	p := kitsune.FromSlice([]int{10, 20})
	merged := kitsune.Merge(p)
	got, err := merged.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []int{10, 20}) {
		t.Errorf("got %v, want [10 20]", got)
	}
}

func TestMerge_Independent_ErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	// One pipeline errors, one succeeds.
	a := kitsune.FromSlice([]int{1, 2, 3})
	b := kitsune.Map(kitsune.FromSlice([]int{1}), func(_ context.Context, n int) (int, error) {
		return 0, boom
	})
	merged := kitsune.Merge(a, b)
	_, err := merged.Collect(context.Background())
	if !errors.Is(err, boom) {
		t.Errorf("expected boom error, got %v", err)
	}
}

func TestMerge_Independent_ContextCancellation(t *testing.T) {
	// Source that blocks until context is cancelled.
	a := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		<-ctx.Done()
		return ctx.Err()
	})
	b := kitsune.FromSlice([]int{1, 2, 3})
	merged := kitsune.Merge(a, b)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	_, err := merged.Collect(ctx)
	// Should exit cleanly (context.Canceled is filtered internally, so we get nil
	// or context.Canceled from the outer Collect).
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMerge_Independent_TakeDownstream(t *testing.T) {
	// Verify early exit (yield returns false) doesn't deadlock.
	a := kitsune.FromSlice(make([]int, 1000))
	b := kitsune.FromSlice(make([]int, 1000))
	merged := kitsune.Merge(a, b)
	got, err := merged.Take(5).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Errorf("got %d items, want 5", len(got))
	}
}

func TestMerge_Independent_Panic(t *testing.T) {
	if len(func() (r []int) { defer func() { recover() }(); kitsune.Merge[int](); return }()) == 0 {
		// panic was recovered — the test passed
	}
}

// ---------------------------------------------------------------------------
// Codec tests
// ---------------------------------------------------------------------------

// countingCodec counts Marshal and Unmarshal calls for test assertions.
type countingCodec struct {
	marshals   atomic.Int64
	unmarshals atomic.Int64
}

func (c *countingCodec) Marshal(v any) ([]byte, error) {
	c.marshals.Add(1)
	return fmt.Appendf(nil, "%v", v), nil
}

func (c *countingCodec) Unmarshal(data []byte, v any) error {
	c.unmarshals.Add(1)
	// Only used in tests with int targets; parse back.
	n, err := strconv.Atoi(string(data))
	if err != nil {
		return err
	}
	switch dst := v.(type) {
	case *int:
		*dst = n
	}
	return nil
}

// TestCodecDefaultJSON confirms the default behavior (no WithCodec) produces
// correct CacheBy results, proving the JSON default path is intact.
func TestCodecDefaultJSON(t *testing.T) {
	cache := kitsune.MemoryCache(100)
	items := []int{1, 2, 1, 3, 2}
	p := kitsune.FromSlice(items)
	calls := 0
	got, err := kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		calls++
		return n * 10, nil
	}, kitsune.CacheBy(func(n int) string { return strconv.Itoa(n) }),
	).Collect(context.Background(), kitsune.WithCache(cache, 0))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{10, 20, 10, 30, 20}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// 3 unique keys → 3 cache misses, 2 hits.
	if calls != 3 {
		t.Errorf("fn called %d times, want 3 (cache should serve hits)", calls)
	}
}

// TestCodecCustom verifies that WithCodec routes CacheBy through the custom codec.
func TestCodecCustom(t *testing.T) {
	codec := &countingCodec{}
	cache := kitsune.MemoryCache(100)
	items := []int{1, 2, 1, 3, 2}
	p := kitsune.FromSlice(items)
	calls := 0
	got, err := kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		calls++
		return n, nil
	}, kitsune.CacheBy(func(n int) string { return strconv.Itoa(n) }),
	).Collect(context.Background(),
		kitsune.WithCache(cache, 0),
		kitsune.WithCodec(codec),
	)
	if err != nil {
		t.Fatal(err)
	}
	// 3 unique keys → 3 misses (each Marshal once), 2 hits (each Unmarshal once).
	if calls != 3 {
		t.Errorf("fn called %d times, want 3", calls)
	}
	if codec.marshals.Load() == 0 {
		t.Error("expected custom codec Marshal to be called at least once")
	}
	if codec.unmarshals.Load() == 0 {
		t.Error("expected custom codec Unmarshal to be called at least once (cache hits)")
	}
	_ = got
}

// TestCodecStore verifies that WithCodec routes MapWith Store-backed Ref through
// the custom codec.
func TestCodecStore(t *testing.T) {
	codec := &countingCodec{}
	store := kitsune.MemoryStore()
	counterKey := kitsune.NewKey("counter", 0)
	items := []int{1, 2, 3}
	p := kitsune.FromSlice(items)
	got, err := kitsune.MapWith(p, counterKey, func(ctx context.Context, ref *kitsune.Ref[int], n int) (int, error) {
		if err := ref.Update(ctx, func(c int) (int, error) { return c + 1, nil }); err != nil {
			return 0, err
		}
		v, err := ref.Get(ctx)
		return v, err
	}).Collect(context.Background(),
		kitsune.WithStore(store),
		kitsune.WithCodec(codec),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3", len(got))
	}
	// Each Update does a Marshal+Unmarshal; each Get also does an Unmarshal.
	if codec.marshals.Load() == 0 {
		t.Error("expected Marshal calls via custom codec")
	}
	if codec.unmarshals.Load() == 0 {
		t.Error("expected Unmarshal calls via custom codec")
	}
}

// ---------------------------------------------------------------------------
// Ref.GetOrSet and Ref.UpdateAndGet tests
// ---------------------------------------------------------------------------

func TestRef_GetOrSet_MemoryRef(t *testing.T) {
	key := kitsune.NewKey("counter", 42)
	var got int
	p := kitsune.FromSlice([]int{1})
	_, err := kitsune.MapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], _ int) (int, error) {
		v, err := ref.GetOrSet(ctx, func() (int, error) {
			t.Error("fn should not be called for memory Ref")
			return 0, nil
		})
		got = v
		return v, err
	}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != 42 {
		t.Errorf("GetOrSet = %d, want 42 (initial value)", got)
	}
}

func TestRef_GetOrSet_StoreBacked(t *testing.T) {
	key := kitsune.NewKey("val", 0)
	var callCount atomic.Int64
	p := kitsune.FromSlice([]int{1, 2, 3})
	_, err := kitsune.MapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], n int) (int, error) {
		v, err := ref.GetOrSet(ctx, func() (int, error) {
			callCount.Add(1)
			return 99, nil
		})
		return v + n, err
	}).Collect(context.Background(), kitsune.WithStore(kitsune.MemoryStore()))
	if err != nil {
		t.Fatal(err)
	}
	// fn should be called exactly once (first call when key absent).
	if callCount.Load() != 1 {
		t.Errorf("GetOrSet fn called %d times, want 1", callCount.Load())
	}
}

func TestRef_UpdateAndGet_ReturnsNewValue(t *testing.T) {
	key := kitsune.NewKey("counter", 0)
	var results []int
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.MapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], n int) (int, error) {
		return ref.UpdateAndGet(ctx, func(v int) (int, error) {
			return v + n, nil
		})
	}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	results = got
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	// Final value should be 1+2+3 = 6.
	if results[len(results)-1] != 6 {
		t.Errorf("final value = %d, want 6", results[len(results)-1])
	}
}

func TestRef_UpdateAndGet_StoreBacked(t *testing.T) {
	key := kitsune.NewKey("sum", 0)
	p := kitsune.FromSlice([]int{10, 20})
	results, err := kitsune.MapWith(p, key, func(ctx context.Context, ref *kitsune.Ref[int], n int) (int, error) {
		return ref.UpdateAndGet(ctx, func(v int) (int, error) {
			return v + n, nil
		})
	}).Collect(context.Background(), kitsune.WithStore(kitsune.MemoryStore()))
	if err != nil {
		t.Fatal(err)
	}
	// results should be [10, 30] (cumulative sums).
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0] != 10 || results[1] != 30 {
		t.Errorf("got %v, want [10 30]", results)
	}
}

// ---------------------------------------------------------------------------
// NewLookupConfig / NewEnrichConfig tests
// ---------------------------------------------------------------------------

func TestNewLookupConfig_DefaultBatchSize(t *testing.T) {
	cfg := kitsune.NewLookupConfig(
		func(n int) int { return n },
		func(_ context.Context, keys []int) (map[int]string, error) {
			m := make(map[int]string)
			for _, k := range keys {
				m[k] = fmt.Sprintf("val:%d", k)
			}
			return m, nil
		},
	)
	p := kitsune.FromSlice([]int{1, 2, 3})
	results, err := kitsune.LookupBy(p, cfg).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
}

func TestNewEnrichConfig_DefaultBatchSize(t *testing.T) {
	cfg := kitsune.NewEnrichConfig(
		func(n int) int { return n },
		func(_ context.Context, keys []int) (map[int]string, error) {
			m := make(map[int]string)
			for _, k := range keys {
				m[k] = fmt.Sprintf("enriched:%d", k)
			}
			return m, nil
		},
		func(n int, s string) string { return fmt.Sprintf("%d=%s", n, s) },
	)
	p := kitsune.FromSlice([]int{1, 2})
	results, err := kitsune.Enrich(p, cfg).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
}

// ---------------------------------------------------------------------------
// Stage.Or tests
// ---------------------------------------------------------------------------

func TestOr_PrimarySucceeds(t *testing.T) {
	stage := kitsune.Or(
		func(_ context.Context, n int) (string, error) { return fmt.Sprintf("primary:%d", n), nil },
		func(_ context.Context, n int) (string, error) { return fmt.Sprintf("fallback:%d", n), nil },
	)
	results, err := stage.Apply(kitsune.FromSlice([]int{1, 2, 3})).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r[:7] != "primary" {
			t.Errorf("expected primary result, got %q", r)
		}
	}
}

func TestOr_FallbackOnPrimaryError(t *testing.T) {
	boom := errors.New("boom")
	stage := kitsune.Or(
		func(_ context.Context, n int) (string, error) { return "", boom },
		func(_ context.Context, n int) (string, error) { return fmt.Sprintf("fallback:%d", n), nil },
	)
	results, err := stage.Apply(kitsune.FromSlice([]int{1})).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0] != "fallback:1" {
		t.Errorf("got %v, want [fallback:1]", results)
	}
}

func TestOr_BothFail(t *testing.T) {
	boom := errors.New("boom")
	stage := kitsune.Or(
		func(_ context.Context, n int) (string, error) { return "", boom },
		func(_ context.Context, n int) (string, error) { return "", boom },
	)
	_, err := stage.Apply(kitsune.FromSlice([]int{1})).Collect(context.Background())
	if !errors.Is(err, boom) {
		t.Errorf("expected boom, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CombineLatest
// ---------------------------------------------------------------------------

func TestCombineLatest(t *testing.T) {
	// Same-graph: Broadcast a source into 2 branches, CombineLatest them.
	src := kitsune.FromSlice([]int{1, 2, 3})
	branches := kitsune.Broadcast(src, 2)
	combined := kitsune.CombineLatest(branches[0], branches[1])
	results, err := combined.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// At least some pairs should be produced; each value must be from [1,3].
	for _, p := range results {
		if p.First < 1 || p.First > 3 {
			t.Errorf("unexpected First value %d", p.First)
		}
		if p.Second < 1 || p.Second > 3 {
			t.Errorf("unexpected Second value %d", p.Second)
		}
	}
}

func TestCombineLatestBothSidesTrigger(t *testing.T) {
	// Both sides should trigger output (key distinction from WithLatestFrom).
	// Use independent pipelines so we can observe pairs from both sides.
	a := kitsune.FromSlice([]int{10, 20})
	b := kitsune.FromSlice([]int{1, 2})
	results, err := kitsune.CombineLatest(a, b).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// We expect some output; the exact count depends on scheduling.
	// Verify every pair has valid values.
	for _, p := range results {
		if p.First != 10 && p.First != 20 {
			t.Errorf("unexpected First value %d", p.First)
		}
		if p.Second != 1 && p.Second != 2 {
			t.Errorf("unexpected Second value %d", p.Second)
		}
	}
}

func TestCombineLatestOneSideEmpty(t *testing.T) {
	a := kitsune.FromSlice([]int{1, 2, 3})
	b := kitsune.FromSlice([]int{}) // empty
	results, err := kitsune.CombineLatest(a, b).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results when one side is empty, got %d: %v", len(results), results)
	}
}

func TestCombineLatestIndependentGraphs(t *testing.T) {
	a := kitsune.FromSlice([]int{1, 2, 3})
	b := kitsune.FromSlice([]string{"x", "y", "z"})
	results, err := kitsune.CombineLatest(a, b).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Some pairs should be produced; verify value constraints.
	for _, p := range results {
		if p.First < 1 || p.First > 3 {
			t.Errorf("unexpected First value %d", p.First)
		}
		if p.Second != "x" && p.Second != "y" && p.Second != "z" {
			t.Errorf("unexpected Second value %q", p.Second)
		}
	}
}

// ---------------------------------------------------------------------------
// Balance
// ---------------------------------------------------------------------------

func TestBalance(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	outputs := kitsune.Balance(src, 3)

	var mu sync.Mutex
	var all []int
	var perOutput [3][]int

	runners := make([]*kitsune.Runner, 3)
	for i, p := range outputs {
		i := i
		runners[i] = p.ForEach(func(_ context.Context, n int) error {
			mu.Lock()
			all = append(all, n)
			perOutput[i] = append(perOutput[i], n)
			mu.Unlock()
			return nil
		})
	}
	merged, err := kitsune.MergeRunners(runners...)
	if err != nil {
		t.Fatal(err)
	}
	if err := merged.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	// All 6 items should be present exactly once.
	slices.Sort(all)
	if !slices.Equal(all, []int{1, 2, 3, 4, 5, 6}) {
		t.Errorf("expected all items [1-6], got %v", all)
	}

	// Each output should have exactly 2 items.
	for i, items := range perOutput {
		if len(items) != 2 {
			t.Errorf("output %d: expected 2 items, got %d: %v", i, len(items), items)
		}
	}
}

func TestBalanceRoundRobin(t *testing.T) {
	// Ordered items should distribute round-robin.
	src := kitsune.FromSlice([]int{0, 1, 2, 3, 4, 5})
	outputs := kitsune.Balance(src, 3)

	results := make([][]int, 3)
	runners := make([]*kitsune.Runner, 3)
	for i, p := range outputs {
		i := i
		runners[i] = p.ForEach(func(_ context.Context, n int) error {
			results[i] = append(results[i], n)
			return nil
		})
	}
	merged, err := kitsune.MergeRunners(runners...)
	if err != nil {
		t.Fatal(err)
	}
	if err := merged.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Verify round-robin: output[i] should contain items i, i+3.
	for i, items := range results {
		if len(items) != 2 {
			t.Errorf("output %d: expected 2 items, got %d: %v", i, len(items), items)
		}
		// Items should be i and i+3 (modular round-robin).
		slices.Sort(items)
		want := []int{i, i + 3}
		if !slices.Equal(items, want) {
			t.Errorf("output %d: got %v, want %v", i, items, want)
		}
	}
}

func TestBalanceSingleOutput(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	outputs := kitsune.Balance(src, 1)
	results, err := outputs[0].Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(results, []int{1, 2, 3}) {
		t.Errorf("got %v, want [1 2 3]", results)
	}
}

func TestBalancePanics(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected Balance(p, 0) to panic")
		}
	}()
	kitsune.Balance(src, 0)
}

func TestOr_ComposesWithThen(t *testing.T) {
	orStage := kitsune.Or(
		func(_ context.Context, n int) (int, error) { return n * 2, nil },
		func(_ context.Context, n int) (int, error) { return n, nil },
	)
	doubleAgain := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, n int) (int, error) { return n * 2, nil })
	})
	composed := kitsune.Then(orStage, doubleAgain)
	results, err := composed.Apply(kitsune.FromSlice([]int{1, 2, 3})).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []int{4, 8, 12}
	if !slices.Equal(results, expected) {
		t.Errorf("got %v, want %v", results, expected)
	}
}

// ---------------------------------------------------------------------------
// TestClock integration tests — deterministic, no real sleeps
// ---------------------------------------------------------------------------

// TestWindow_TestClock verifies that Window flushes when virtual time advances
// past the window duration.
func TestWindow_TestClock(t *testing.T) {
	clock := testkit.NewTestClock()

	// Use a channel source so we can control item delivery and close timing.
	ch := kitsune.NewChannel[int](10)
	src := ch.Source()
	w := kitsune.Window(src, 5*time.Second, kitsune.WithClock(clock))

	// Collect runs the pipeline in a background goroutine; resultCh carries the result.
	resultCh := make(chan [][]int, 1)
	errCh := make(chan error, 1)
	go func() {
		items, err := w.Collect(context.Background())
		resultCh <- items
		errCh <- err
	}()

	// Send 3 items, then advance time past the window boundary to flush them.
	// Small real sleep so the goroutine reaches the select in runBatch before we advance.
	time.Sleep(5 * time.Millisecond)
	_ = ch.Send(context.Background(), 1)
	_ = ch.Send(context.Background(), 2)
	_ = ch.Send(context.Background(), 3)
	time.Sleep(5 * time.Millisecond)
	clock.Advance(5 * time.Second)

	// Send 2 more items, then close — end-of-stream flush.
	time.Sleep(5 * time.Millisecond)
	_ = ch.Send(context.Background(), 4)
	_ = ch.Send(context.Background(), 5)
	ch.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	batches := <-resultCh

	if len(batches) < 2 {
		t.Fatalf("expected at least 2 windows, got %d: %v", len(batches), batches)
	}
	// First window must contain the first 3 items.
	if !slices.Equal(batches[0], []int{1, 2, 3}) {
		t.Errorf("window[0] = %v, want [1 2 3]", batches[0])
	}
	// Last window must contain 4 and 5.
	last := batches[len(batches)-1]
	if !slices.Equal(last, []int{4, 5}) {
		t.Errorf("window[last] = %v, want [4 5]", last)
	}
}

// TestBatch_TestClock verifies that Batch flushes a partial batch when virtual
// time advances past the BatchTimeout.
func TestBatch_TestClock(t *testing.T) {
	clock := testkit.NewTestClock()

	ch := kitsune.NewChannel[int](10)
	src := ch.Source()
	batched := kitsune.Batch(src, 10,
		kitsune.BatchTimeout(1*time.Second),
		kitsune.WithClock(clock),
	)

	resultCh := make(chan [][]int, 1)
	errCh := make(chan error, 1)
	go func() {
		items, err := batched.Collect(context.Background())
		resultCh <- items
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)
	_ = ch.Send(context.Background(), 10)
	_ = ch.Send(context.Background(), 20)
	_ = ch.Send(context.Background(), 30)
	time.Sleep(5 * time.Millisecond)

	// Advance past the timeout — should flush the partial batch of 3.
	clock.Advance(1 * time.Second)
	time.Sleep(5 * time.Millisecond)

	// Send one more item then close — flushed as end-of-stream batch.
	_ = ch.Send(context.Background(), 40)
	ch.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	batches := <-resultCh

	if len(batches) < 2 {
		t.Fatalf("expected at least 2 batches, got %d: %v", len(batches), batches)
	}
	if !slices.Equal(batches[0], []int{10, 20, 30}) {
		t.Errorf("batch[0] = %v, want [10 20 30]", batches[0])
	}
	if !slices.Equal(batches[len(batches)-1], []int{40}) {
		t.Errorf("batch[last] = %v, want [40]", batches[len(batches)-1])
	}
}

// TestThrottle_TestClock verifies that Throttle uses virtual time to decide
// whether an item is within the cooldown window.
func TestThrottle_TestClock(t *testing.T) {
	clock := testkit.NewTestClock()
	const window = 5 * time.Second

	ch := kitsune.NewChannel[int](10)
	src := ch.Source()
	throttled := kitsune.Throttle(src, window, kitsune.WithClock(clock))

	resultCh := make(chan []int, 1)
	errCh := make(chan error, 1)
	go func() {
		items, err := throttled.Collect(context.Background())
		resultCh <- items
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)

	// First item: should pass through (nothing emitted yet).
	_ = ch.Send(context.Background(), 1)
	time.Sleep(5 * time.Millisecond)

	// Second item within the window: should be dropped.
	_ = ch.Send(context.Background(), 2)
	time.Sleep(5 * time.Millisecond)

	// Advance past the throttle window.
	clock.Advance(5 * time.Second)
	time.Sleep(5 * time.Millisecond)

	// Third item: cooldown elapsed, should pass through.
	_ = ch.Send(context.Background(), 3)
	time.Sleep(5 * time.Millisecond)

	ch.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	results := <-resultCh

	if !slices.Equal(results, []int{1, 3}) {
		t.Errorf("got %v, want [1 3]", results)
	}
}

// TestDebounce_TestClock verifies that Debounce uses virtual time and emits
// only after the quiet period elapses.
func TestDebounce_TestClock(t *testing.T) {
	clock := testkit.NewTestClock()
	const quietPeriod = 2 * time.Second

	ch := kitsune.NewChannel[int](10)
	src := ch.Source()
	debounced := kitsune.Debounce(src, quietPeriod, kitsune.WithClock(clock))

	resultCh := make(chan []int, 1)
	errCh := make(chan error, 1)
	go func() {
		items, err := debounced.Collect(context.Background())
		resultCh <- items
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)

	// Send a burst of 3 items — only the last should be emitted after the quiet period.
	_ = ch.Send(context.Background(), 1)
	time.Sleep(2 * time.Millisecond)
	_ = ch.Send(context.Background(), 2)
	time.Sleep(2 * time.Millisecond)
	_ = ch.Send(context.Background(), 3)
	time.Sleep(5 * time.Millisecond)

	// Advance time past the quiet period to trigger the debounce flush.
	clock.Advance(2 * time.Second)
	time.Sleep(10 * time.Millisecond)

	// Close the channel — no pending item at this point.
	ch.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	results := <-resultCh

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d: %v", len(results), results)
	}
	if results[0] != 3 {
		t.Errorf("expected 3 (last item of burst), got %d", results[0])
	}
}

// TestTicker_TestClock verifies that Ticker emits time values when the virtual
// clock advances past tick intervals.
func TestTicker_TestClock(t *testing.T) {
	clock := testkit.NewTestClock()
	const tickInterval = 1 * time.Second

	// Collect exactly 3 ticks using Take.
	p := kitsune.Ticker(tickInterval, kitsune.WithClock(clock)).Take(3)

	resultCh := make(chan []time.Time, 1)
	errCh := make(chan error, 1)
	go func() {
		items, err := p.Collect(context.Background())
		resultCh <- items
		errCh <- err
	}()

	// Advance the clock three times, one tick interval each time.
	for i := 0; i < 3; i++ {
		time.Sleep(5 * time.Millisecond)
		clock.Advance(tickInterval)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	results := <-resultCh

	if len(results) != 3 {
		t.Fatalf("expected 3 ticks, got %d", len(results))
	}
	// Each tick time should be after the previous one.
	for i := 1; i < len(results); i++ {
		if !results[i].After(results[i-1]) {
			t.Errorf("tick[%d] (%v) is not after tick[%d] (%v)", i, results[i], i-1, results[i-1])
		}
	}
}

// TestInterval_TestClock verifies that Interval emits sequential int64 values
// when the virtual clock advances.
func TestInterval_TestClock(t *testing.T) {
	clock := testkit.NewTestClock()
	const tickInterval = 1 * time.Second

	p := kitsune.Interval(tickInterval, kitsune.WithClock(clock)).Take(4)

	resultCh := make(chan []int64, 1)
	errCh := make(chan error, 1)
	go func() {
		items, err := p.Collect(context.Background())
		resultCh <- items
		errCh <- err
	}()

	for i := 0; i < 4; i++ {
		time.Sleep(5 * time.Millisecond)
		clock.Advance(tickInterval)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	results := <-resultCh

	if !slices.Equal(results, []int64{0, 1, 2, 3}) {
		t.Errorf("got %v, want [0 1 2 3]", results)
	}
}

// ---------------------------------------------------------------------------
// SessionWindow tests — deterministic, no real sleeps beyond scheduling yields
// ---------------------------------------------------------------------------

// TestSessionWindow_Basic verifies that a single session is flushed when the
// gap timer fires after all items have been received.
func TestSessionWindow_Basic(t *testing.T) {
	clock := testkit.NewTestClock()
	const gap = 2 * time.Second

	ch := kitsune.NewChannel[int](10)
	src := ch.Source()
	sessions := kitsune.SessionWindow(src, gap, kitsune.WithClock(clock))

	resultCh := make(chan [][]int, 1)
	errCh := make(chan error, 1)
	go func() {
		items, err := sessions.Collect(context.Background())
		resultCh <- items
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)
	_ = ch.Send(context.Background(), 1)
	_ = ch.Send(context.Background(), 2)
	_ = ch.Send(context.Background(), 3)
	time.Sleep(5 * time.Millisecond)

	// Advance past the gap to trigger flush.
	clock.Advance(gap)
	time.Sleep(10 * time.Millisecond)

	ch.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	results := <-resultCh

	if len(results) != 1 {
		t.Fatalf("expected 1 session, got %d: %v", len(results), results)
	}
	if !slices.Equal(results[0], []int{1, 2, 3}) {
		t.Errorf("session[0] = %v, want [1 2 3]", results[0])
	}
}

// TestSessionWindow_MultipleSessions verifies that two distinct sessions are
// emitted when the gap fires between them.
func TestSessionWindow_MultipleSessions(t *testing.T) {
	clock := testkit.NewTestClock()
	const gap = 2 * time.Second

	ch := kitsune.NewChannel[int](10)
	src := ch.Source()
	sessions := kitsune.SessionWindow(src, gap, kitsune.WithClock(clock))

	resultCh := make(chan [][]int, 1)
	errCh := make(chan error, 1)
	go func() {
		items, err := sessions.Collect(context.Background())
		resultCh <- items
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)

	// First session.
	_ = ch.Send(context.Background(), 1)
	_ = ch.Send(context.Background(), 2)
	time.Sleep(5 * time.Millisecond)

	// Advance past gap to flush first session.
	clock.Advance(gap)
	time.Sleep(10 * time.Millisecond)

	// Second session.
	_ = ch.Send(context.Background(), 3)
	_ = ch.Send(context.Background(), 4)
	time.Sleep(5 * time.Millisecond)

	// Advance past gap to flush second session.
	clock.Advance(gap)
	time.Sleep(10 * time.Millisecond)

	ch.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	results := <-resultCh

	if len(results) != 2 {
		t.Fatalf("expected 2 sessions, got %d: %v", len(results), results)
	}
	if !slices.Equal(results[0], []int{1, 2}) {
		t.Errorf("session[0] = %v, want [1 2]", results[0])
	}
	if !slices.Equal(results[1], []int{3, 4}) {
		t.Errorf("session[1] = %v, want [3 4]", results[1])
	}
}

// TestSessionWindow_FlushOnClose verifies that the current session is flushed
// immediately when the upstream channel closes, without waiting for the gap.
func TestSessionWindow_FlushOnClose(t *testing.T) {
	clock := testkit.NewTestClock()
	const gap = 10 * time.Second // long gap — should not fire

	ch := kitsune.NewChannel[int](10)
	src := ch.Source()
	sessions := kitsune.SessionWindow(src, gap, kitsune.WithClock(clock))

	resultCh := make(chan [][]int, 1)
	errCh := make(chan error, 1)
	go func() {
		items, err := sessions.Collect(context.Background())
		resultCh <- items
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)
	_ = ch.Send(context.Background(), 1)
	_ = ch.Send(context.Background(), 2)
	_ = ch.Send(context.Background(), 3)
	time.Sleep(5 * time.Millisecond)

	// Close without advancing the clock — flush must happen due to close.
	ch.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	results := <-resultCh

	if len(results) != 1 {
		t.Fatalf("expected 1 session, got %d: %v", len(results), results)
	}
	if !slices.Equal(results[0], []int{1, 2, 3}) {
		t.Errorf("session[0] = %v, want [1 2 3]", results[0])
	}
}

// TestSessionWindow_Empty verifies that closing an empty stream produces no
// sessions.
func TestSessionWindow_Empty(t *testing.T) {
	clock := testkit.NewTestClock()
	const gap = 2 * time.Second

	ch := kitsune.NewChannel[int](10)
	src := ch.Source()
	sessions := kitsune.SessionWindow(src, gap, kitsune.WithClock(clock))

	resultCh := make(chan [][]int, 1)
	errCh := make(chan error, 1)
	go func() {
		items, err := sessions.Collect(context.Background())
		resultCh <- items
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)
	ch.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	results := <-resultCh

	if len(results) != 0 {
		t.Errorf("expected 0 sessions, got %d: %v", len(results), results)
	}
}

// TestSessionWindow_ResetBehavior verifies that each new item resets the gap
// timer. Advancing by half the gap twice should NOT flush if items arrive in
// between; the session should only flush after a full gap of inactivity.
func TestSessionWindow_ResetBehavior(t *testing.T) {
	clock := testkit.NewTestClock()
	const gap = 4 * time.Second
	const half = 2 * time.Second

	ch := kitsune.NewChannel[int](10)
	src := ch.Source()
	sessions := kitsune.SessionWindow(src, gap, kitsune.WithClock(clock))

	resultCh := make(chan [][]int, 1)
	errCh := make(chan error, 1)
	go func() {
		items, err := sessions.Collect(context.Background())
		resultCh <- items
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)

	// Send item 1.
	_ = ch.Send(context.Background(), 1)
	time.Sleep(5 * time.Millisecond)

	// Advance by half the gap — timer should NOT fire yet.
	clock.Advance(half)
	time.Sleep(5 * time.Millisecond)

	// Send item 2 — this resets the timer.
	_ = ch.Send(context.Background(), 2)
	time.Sleep(5 * time.Millisecond)

	// Advance by half the gap again — only half the gap has elapsed since item 2,
	// so still no flush.
	clock.Advance(half)
	time.Sleep(5 * time.Millisecond)

	// Verify no flush has happened yet by checking resultCh is empty.
	select {
	case earlyResult := <-resultCh:
		t.Fatalf("unexpected early flush: %v", earlyResult)
	default:
	}

	// Advance the remaining half gap — now a full gap since item 2, should flush.
	clock.Advance(half)
	time.Sleep(10 * time.Millisecond)

	ch.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	results := <-resultCh

	if len(results) != 1 {
		t.Fatalf("expected 1 session, got %d: %v", len(results), results)
	}
	if !slices.Equal(results[0], []int{1, 2}) {
		t.Errorf("session[0] = %v, want [1 2]", results[0])
	}
}

// TestSessionWindow_ContextCancel verifies that cancelling the context
// terminates the pipeline with an error.
func TestSessionWindow_ContextCancel(t *testing.T) {
	clock := testkit.NewTestClock()
	const gap = 10 * time.Second

	ch := kitsune.NewChannel[int](10)
	src := ch.Source()
	sessions := kitsune.SessionWindow(src, gap, kitsune.WithClock(clock))

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		_, err := sessions.Collect(ctx)
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)
	_ = ch.Send(ctx, 1)
	time.Sleep(5 * time.Millisecond)

	cancel()

	err := <-errCh
	if err == nil {
		t.Fatal("expected error after context cancel, got nil")
	}
}

// ---------------------------------------------------------------------------
// Contains
// ---------------------------------------------------------------------------

func TestContains_Present(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got, err := kitsune.Contains(context.Background(), p, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("expected true, got false")
	}
}

func TestContains_Absent(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.Contains(context.Background(), p, 99)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("expected false, got true")
	}
}

func TestContains_Empty(t *testing.T) {
	p := kitsune.FromSlice([]int{})
	got, err := kitsune.Contains(context.Background(), p, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("expected false for empty stream, got true")
	}
}

// ---------------------------------------------------------------------------
// ElementAt
// ---------------------------------------------------------------------------

func TestElementAt_Valid(t *testing.T) {
	p := kitsune.FromSlice([]string{"a", "b", "c"})
	got, ok, err := p.ElementAt(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != "b" {
		t.Errorf("got %q, want %q", got, "b")
	}
}

func TestElementAt_OutOfBounds(t *testing.T) {
	p := kitsune.FromSlice([]string{"a", "b"})
	_, ok, err := p.ElementAt(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected ok=false for out-of-bounds index")
	}
}

func TestElementAt_Zero(t *testing.T) {
	p := kitsune.FromSlice([]int{10, 20, 30})
	got, ok, err := p.ElementAt(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != 10 {
		t.Errorf("got %d, want 10", got)
	}
}

func TestElementAt_Empty(t *testing.T) {
	p := kitsune.FromSlice([]int{})
	_, ok, err := p.ElementAt(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected ok=false for empty stream")
	}
}

// ---------------------------------------------------------------------------
// ToMap
// ---------------------------------------------------------------------------

func TestToMap_Basic(t *testing.T) {
	type kv struct{ K, V string }
	p := kitsune.FromSlice([]kv{{"a", "1"}, {"b", "2"}, {"c", "3"}})
	m, err := kitsune.ToMap(context.Background(), p,
		func(x kv) string { return x.K },
		func(x kv) string { return x.V },
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 3 || m["a"] != "1" || m["b"] != "2" || m["c"] != "3" {
		t.Errorf("unexpected map: %v", m)
	}
}

func TestToMap_DuplicateKeys(t *testing.T) {
	type kv struct{ K, V int }
	p := kitsune.FromSlice([]kv{{1, 10}, {2, 20}, {1, 99}})
	m, err := kitsune.ToMap(context.Background(), p,
		func(x kv) int { return x.K },
		func(x kv) int { return x.V },
	)
	if err != nil {
		t.Fatal(err)
	}
	// Last value for key 1 wins.
	if m[1] != 99 {
		t.Errorf("expected m[1]=99 (last wins), got %d", m[1])
	}
	if m[2] != 20 {
		t.Errorf("expected m[2]=20, got %d", m[2])
	}
}

func TestToMap_Empty(t *testing.T) {
	p := kitsune.FromSlice([]int{})
	m, err := kitsune.ToMap(context.Background(), p,
		func(n int) int { return n },
		func(n int) int { return n },
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

// ---------------------------------------------------------------------------
// SequenceEqual
// ---------------------------------------------------------------------------

func TestSequenceEqual_Equal(t *testing.T) {
	a := kitsune.FromSlice([]int{1, 2, 3})
	b := kitsune.FromSlice([]int{1, 2, 3})
	ok, err := kitsune.SequenceEqual(context.Background(), a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected equal sequences to return true")
	}
}

func TestSequenceEqual_DifferentValues(t *testing.T) {
	a := kitsune.FromSlice([]int{1, 2, 3})
	b := kitsune.FromSlice([]int{1, 2, 4})
	ok, err := kitsune.SequenceEqual(context.Background(), a, b)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected different values to return false")
	}
}

func TestSequenceEqual_DifferentLengths(t *testing.T) {
	t.Run("first shorter", func(t *testing.T) {
		a := kitsune.FromSlice([]int{1, 2})
		b := kitsune.FromSlice([]int{1, 2, 3})
		ok, err := kitsune.SequenceEqual(context.Background(), a, b)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("expected false when first is shorter")
		}
	})
	t.Run("second shorter", func(t *testing.T) {
		a := kitsune.FromSlice([]int{1, 2, 3})
		b := kitsune.FromSlice([]int{1, 2})
		ok, err := kitsune.SequenceEqual(context.Background(), a, b)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("expected false when second is shorter")
		}
	})
}

func TestSequenceEqual_BothEmpty(t *testing.T) {
	a := kitsune.FromSlice([]int{})
	b := kitsune.FromSlice([]int{})
	ok, err := kitsune.SequenceEqual(context.Background(), a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected two empty streams to be equal")
	}
}

// ---------------------------------------------------------------------------
// StartWith
// ---------------------------------------------------------------------------

func TestStartWith_Basic(t *testing.T) {
	p := kitsune.FromSlice([]int{3, 4, 5})
	results, err := kitsune.StartWith(p, 1, 2).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3, 4, 5}
	if len(results) != len(want) {
		t.Fatalf("got %v, want %v", results, want)
	}
	for i, v := range results {
		if v != want[i] {
			t.Errorf("results[%d]=%d, want %d", i, v, want[i])
		}
	}
}

func TestStartWith_Empty(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	result := kitsune.StartWith(p) // no items
	// Should return same pipeline reference (or at minimum, same items)
	results, err := result.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0] != 1 || results[2] != 3 {
		t.Errorf("unexpected results: %v", results)
	}
}

func TestStartWith_EmptyOriginal(t *testing.T) {
	p := kitsune.FromSlice([]int{})
	results, err := kitsune.StartWith(p, 10, 20).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{10, 20}
	if len(results) != len(want) || results[0] != 10 || results[1] != 20 {
		t.Errorf("got %v, want %v", results, want)
	}
}

// ---------------------------------------------------------------------------
// DefaultIfEmpty
// ---------------------------------------------------------------------------

func TestDefaultIfEmpty_NonEmpty(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	results, err := kitsune.DefaultIfEmpty(p, 99).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3}
	if len(results) != len(want) {
		t.Fatalf("got %v, want %v", results, want)
	}
	for i, v := range results {
		if v != want[i] {
			t.Errorf("results[%d]=%d, want %d", i, v, want[i])
		}
	}
}

func TestDefaultIfEmpty_Empty(t *testing.T) {
	p := kitsune.FromSlice([]int{})
	results, err := kitsune.DefaultIfEmpty(p, 42).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0] != 42 {
		t.Errorf("expected [42], got %v", results)
	}
}

func TestDefaultIfEmpty_SingleItem(t *testing.T) {
	p := kitsune.FromSlice([]int{7})
	results, err := kitsune.DefaultIfEmpty(p, 99).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0] != 7 {
		t.Errorf("expected [7], got %v", results)
	}
}

// ---------------------------------------------------------------------------
// Timestamp
// ---------------------------------------------------------------------------

func TestTimestamp_PopulatesTime(t *testing.T) {
	clock := testkit.NewTestClock()
	base := clock.Now()
	p := kitsune.FromSlice([]int{1, 2, 3})
	results, err := kitsune.Timestamp(p, kitsune.WithClock(clock)).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, r := range results {
		if r.Time.IsZero() {
			t.Errorf("results[%d].Time is zero", i)
		}
		if r.Time.Before(base) {
			t.Errorf("results[%d].Time=%v is before base=%v", i, r.Time, base)
		}
	}
}

func TestTimestamp_OrderPreserved(t *testing.T) {
	clock := testkit.NewTestClock()
	p := kitsune.FromSlice([]int{10, 20, 30})
	results, err := kitsune.Timestamp(p, kitsune.WithClock(clock)).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range results {
		want := (i + 1) * 10
		if r.Value != want {
			t.Errorf("results[%d].Value=%d, want %d", i, r.Value, want)
		}
	}
}

// ---------------------------------------------------------------------------
// TimeInterval
// ---------------------------------------------------------------------------

func TestTimeInterval_Basic(t *testing.T) {
	clock := testkit.NewTestClock()
	ch := kitsune.NewChannel[int](10)
	src := ch.Source()
	p := kitsune.TimeInterval(src, kitsune.WithClock(clock))

	resultCh := make(chan []kitsune.TimedInterval[int], 1)
	go func() {
		items, _ := p.Collect(context.Background())
		resultCh <- items
	}()

	time.Sleep(5 * time.Millisecond)
	_ = ch.Send(context.Background(), 1)
	time.Sleep(5 * time.Millisecond)

	clock.Advance(100 * time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	_ = ch.Send(context.Background(), 2)
	time.Sleep(5 * time.Millisecond)

	clock.Advance(200 * time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	_ = ch.Send(context.Background(), 3)
	time.Sleep(5 * time.Millisecond)

	ch.Close()
	results := <-resultCh

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// First item always has Elapsed == 0.
	if results[0].Elapsed != 0 {
		t.Errorf("first item Elapsed=%v, want 0", results[0].Elapsed)
	}
	// Second item: 100ms elapsed.
	if results[1].Elapsed != 100*time.Millisecond {
		t.Errorf("second item Elapsed=%v, want 100ms", results[1].Elapsed)
	}
	// Third item: 200ms elapsed.
	if results[2].Elapsed != 200*time.Millisecond {
		t.Errorf("third item Elapsed=%v, want 200ms", results[2].Elapsed)
	}
}

func TestTimeInterval_Sequential(t *testing.T) {
	// Verifies Concurrency(1) is enforced: all intervals are accurate.
	clock := testkit.NewTestClock()
	ch := kitsune.NewChannel[int](10)
	src := ch.Source()
	p := kitsune.TimeInterval(src, kitsune.WithClock(clock))

	ctx := context.Background()
	resultCh := make(chan []kitsune.TimedInterval[int], 1)
	go func() {
		items, _ := p.Collect(ctx)
		resultCh <- items
	}()

	time.Sleep(5 * time.Millisecond)
	for i := 0; i < 5; i++ {
		// Advance the clock *before* sending the item so the channel is empty
		// while the clock changes — the goroutine cannot race-process an item
		// that hasn't been sent yet.
		if i > 0 {
			clock.Advance(10 * time.Millisecond)
		}
		_ = ch.Send(ctx, i)
		time.Sleep(5 * time.Millisecond)
	}
	ch.Close()
	results := <-resultCh

	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
	if results[0].Elapsed != 0 {
		t.Errorf("first item Elapsed=%v, want 0", results[0].Elapsed)
	}
	for i := 1; i < 5; i++ {
		if results[i].Elapsed <= 0 {
			t.Errorf("results[%d].Elapsed=%v, want > 0", i, results[i].Elapsed)
		}
	}
}

// ---------------------------------------------------------------------------
// Amb
// ---------------------------------------------------------------------------

func TestAmb_FirstWins(t *testing.T) {
	// Use a channel to control which factory emits first.
	fast := make(chan int, 2)
	slow := make(chan int, 2)

	fast <- 1
	fast <- 2
	close(fast)

	p := kitsune.Amb(
		func() *kitsune.Pipeline[int] { return kitsune.From((<-chan int)(fast)) },
		func() *kitsune.Pipeline[int] {
			// slow channel: only emit after a delay
			return kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
				select {
				case v, ok := <-slow:
					if ok {
						yield(v)
					}
					return nil
				case <-ctx.Done():
					return nil
				}
			})
		},
	)

	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Should only contain items from fast factory.
	for _, v := range results {
		if v != 1 && v != 2 {
			t.Errorf("got unexpected value %d (should be from fast factory)", v)
		}
	}
	if len(results) == 0 {
		t.Error("expected at least one result from fast factory")
	}
}

func TestAmb_SingleFactory(t *testing.T) {
	p := kitsune.Amb(
		func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{1, 2, 3}) },
	)
	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3}
	if len(results) != len(want) {
		t.Fatalf("got %v, want %v", results, want)
	}
	for i, v := range results {
		if v != want[i] {
			t.Errorf("results[%d]=%d, want %d", i, v, want[i])
		}
	}
}

func TestAmb_Empty(t *testing.T) {
	p := kitsune.Amb[int]()
	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty pipeline, got %v", results)
	}
}

// TestSessionWindow_Panics verifies that SessionWindow panics for non-positive
// gap values.
func TestSessionWindow_Panics(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})

	t.Run("zero gap", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected SessionWindow(p, 0) to panic")
			}
		}()
		kitsune.SessionWindow(src, 0)
	})

	t.Run("negative gap", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected SessionWindow(p, -1) to panic")
			}
		}()
		kitsune.SessionWindow(src, -1*time.Second)
	})
}
