package kitsune_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// ForEach: serial
// ---------------------------------------------------------------------------

func TestForEach_Serial(t *testing.T) {
	ctx := context.Background()
	var got []int
	_, err := kitsune.FromSlice([]int{1, 2, 3}).ForEach(func(_ context.Context, v int) error {
		got = append(got, v)
		return nil
	}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("position %d: got %d, want %d", i, got[i], w)
		}
	}
}

func TestForEach_Serial_OnError_Skip(t *testing.T) {
	ctx := context.Background()
	var got []int
	_, err := kitsune.FromSlice([]int{1, 2, 3, 4}).ForEach(func(_ context.Context, v int) error {
		if v%2 == 0 {
			return fmt.Errorf("even")
		}
		got = append(got, v)
		return nil
	}, kitsune.OnError(kitsune.ActionDrop())).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Errorf("got %v, want [1 3]", got)
	}
}

func TestForEach_Serial_OnError_Halt(t *testing.T) {
	ctx := context.Background()
	_, err := kitsune.FromSlice([]int{1, 2, 3}).ForEach(func(_ context.Context, v int) error {
		if v == 2 {
			return fmt.Errorf("boom")
		}
		return nil
	}).Run(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestForEach_Serial_Supervise_Restart(t *testing.T) {
	ctx := context.Background()
	attempt := 0
	var processed []int
	_, err := kitsune.FromSlice([]int{1, 2, 3}).ForEach(func(_ context.Context, v int) error {
		attempt++
		if attempt == 1 {
			return fmt.Errorf("trigger restart")
		}
		processed = append(processed, v)
		return nil
	}, kitsune.Supervise(kitsune.RestartOnError(3, nil))).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Item 1 lost on restart; items 2 and 3 processed after.
	if len(processed) != 2 || processed[0] != 2 || processed[1] != 3 {
		t.Errorf("got %v, want [2 3]", processed)
	}
}

// ---------------------------------------------------------------------------
// ForEach: concurrent
// ---------------------------------------------------------------------------

func TestForEach_Concurrent_AllProcessed(t *testing.T) {
	ctx := context.Background()
	n := 100
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	var count atomic.Int64
	_, err := kitsune.FromSlice(items).ForEach(func(_ context.Context, _ int) error {
		count.Add(1)
		return nil
	}, kitsune.Concurrency(4)).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count.Load() != int64(n) {
		t.Errorf("processed %d items, want %d", count.Load(), n)
	}
}

func TestForEach_Concurrent_OnError_Skip(t *testing.T) {
	ctx := context.Background()
	var count atomic.Int64
	_, err := kitsune.FromSlice([]int{1, 2, 3, 4, 5}).ForEach(func(_ context.Context, v int) error {
		if v%2 == 0 {
			return fmt.Errorf("even")
		}
		count.Add(1)
		return nil
	}, kitsune.Concurrency(3), kitsune.OnError(kitsune.ActionDrop())).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count.Load() != 3 {
		t.Errorf("processed %d odd items, want 3", count.Load())
	}
}

func TestForEach_Concurrent_Error_Halts(t *testing.T) {
	ctx := context.Background()
	_, err := kitsune.FromSlice([]int{1, 2, 3, 4, 5}).ForEach(func(_ context.Context, v int) error {
		if v == 3 {
			return fmt.Errorf("halt on 3")
		}
		return nil
	}, kitsune.Concurrency(4)).Run(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// ForEach: ordered concurrent
// ---------------------------------------------------------------------------

func TestForEach_Ordered_AllProcessed(t *testing.T) {
	ctx := context.Background()
	n := 50
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	var count atomic.Int64
	_, err := kitsune.FromSlice(items).ForEach(func(_ context.Context, _ int) error {
		count.Add(1)
		return nil
	}, kitsune.Concurrency(4), kitsune.Ordered()).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count.Load() != int64(n) {
		t.Errorf("processed %d items, want %d", count.Load(), n)
	}
}

func TestForEach_Ordered_ErrorPropagation(t *testing.T) {
	// In ordered mode, if item 3 errors while items 4 and 5 have already
	// completed, the error from item 3 is still reported (not suppressed by
	// later successes).
	ctx := context.Background()
	_, err := kitsune.FromSlice([]int{1, 2, 3, 4, 5}).ForEach(func(_ context.Context, v int) error {
		if v == 3 {
			return fmt.Errorf("item 3 failed")
		}
		return nil
	}, kitsune.Concurrency(4), kitsune.Ordered()).Run(ctx)
	if err == nil {
		t.Fatal("expected error from ordered ForEach, got nil")
	}
}

func TestForEach_Ordered_OnError_Skip(t *testing.T) {
	ctx := context.Background()
	var got []int
	var mu sync.Mutex
	_, err := kitsune.FromSlice([]int{1, 2, 3, 4, 5}).ForEach(func(_ context.Context, v int) error {
		if v%2 == 0 {
			return fmt.Errorf("even")
		}
		mu.Lock()
		got = append(got, v)
		mu.Unlock()
		return nil
	}, kitsune.Concurrency(3), kitsune.Ordered(), kitsune.OnError(kitsune.ActionDrop())).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sort.Ints(got)
	want := []int{1, 3, 5}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("index %d: got %d, want %d", i, got[i], w)
		}
	}
}

// ---------------------------------------------------------------------------
// ForEach: race detector
// ---------------------------------------------------------------------------

func TestForEach_Concurrent_Race(t *testing.T) {
	ctx := context.Background()
	var count atomic.Int64
	n := 500
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	_, err := kitsune.FromSlice(items).ForEach(func(_ context.Context, _ int) error {
		count.Add(1)
		return nil
	}, kitsune.Concurrency(8)).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count.Load() != int64(n) {
		t.Errorf("processed %d items, want %d", count.Load(), n)
	}
}
