package kitsune_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// Map
// ---------------------------------------------------------------------------

func TestMapSerial(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v * 2, nil
	}))
	want := []int{2, 4, 6, 8, 10}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMapConcurrent(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8})
	got := collectAll(t, kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v * 2, nil
	}, kitsune.Concurrency(4)))
	sort.Ints(got)
	want := []int{2, 4, 6, 8, 10, 12, 14, 16}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMapConcurrentOrdered(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, kitsune.Map(p, func(_ context.Context, v int) (string, error) {
		return fmt.Sprintf("item-%d", v), nil
	}, kitsune.Concurrency(3), kitsune.Ordered()))
	want := []string{"item-1", "item-2", "item-3", "item-4", "item-5"}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMapError(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	boom := errors.New("boom")
	err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		if v == 2 {
			return 0, boom
		}
		return v, nil
	}).ForEach(func(_ context.Context, _ int) error { return nil }).Run(ctx)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestMapSkip(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		if v%2 == 0 {
			return 0, errors.New("even")
		}
		return v, nil
	}, kitsune.OnError(kitsune.ActionDrop())))
	want := []int{1, 3, 5}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMapReturn(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	got := collectAll(t, kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		if v == 2 {
			return 0, errors.New("bad")
		}
		return v * 10, nil
	}, kitsune.OnError(kitsune.Return(99))))
	want := []int{10, 99, 30}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMapTimeout(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := kitsune.Map(p, func(ctx context.Context, v int) (int, error) {
		if v == 2 {
			select {
			case <-time.After(10 * time.Second):
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		return v, nil
	}, kitsune.Timeout(50*time.Millisecond)).
		ForEach(func(_ context.Context, _ int) error { return nil }).
		Run(ctx)

	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// ---------------------------------------------------------------------------
// FlatMap
// ---------------------------------------------------------------------------

func TestFlatMapSerial(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	got := collectAll(t, kitsune.FlatMap(p, func(_ context.Context, v int, yield func(int) error) error {
		for i := 0; i < v; i++ {
			if err := yield(v*10 + i); err != nil {
				return err
			}
		}
		return nil
	}))
	want := []int{10, 20, 21, 30, 31, 32}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFlatMapConcurrentOrdered(t *testing.T) {
	p := kitsune.FromSlice([]int{3, 1, 2})
	got := collectAll(t, kitsune.FlatMap(p, func(_ context.Context, v int, yield func(int) error) error {
		for i := 0; i < v; i++ {
			if err := yield(v*100 + i); err != nil {
				return err
			}
		}
		return nil
	}, kitsune.Concurrency(3), kitsune.Ordered()))
	// Must be ordered: 3→{300,301,302}, 1→{100}, 2→{200,201}
	want := []int{300, 301, 302, 100, 200, 201}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Filter
// ---------------------------------------------------------------------------

func TestFilter(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	got := collectAll(t, kitsune.Filter(p, func(_ context.Context, v int) (bool, error) {
		return v%2 == 0, nil
	}))
	want := []int{2, 4, 6}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Tap
// ---------------------------------------------------------------------------

func TestTap(t *testing.T) {
	var tapped []int
	p := kitsune.FromSlice([]int{1, 2, 3})
	got := collectAll(t, kitsune.Tap(p, func(_ context.Context, v int) error {
		tapped = append(tapped, v*10)
		return nil
	}))
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Fatalf("tap changed items: got %v", got)
	}
	if !sliceEqual(tapped, []int{10, 20, 30}) {
		t.Fatalf("tap side effect: got %v", tapped)
	}
}

// ---------------------------------------------------------------------------
// TapError
// ---------------------------------------------------------------------------

func TestTapError_NoFireOnSuccess(t *testing.T) {
	var called bool
	p := kitsune.TapError(kitsune.FromSlice([]int{1, 2, 3}), func(_ context.Context, _ error) {
		called = true
	})
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Fatalf("items changed: got %v", got)
	}
	if called {
		t.Fatal("callback fired on success")
	}
}

func TestTapError_PassesThroughItems(t *testing.T) {
	p := kitsune.TapError(kitsune.FromSlice([]int{10, 20, 30}), func(_ context.Context, _ error) {})
	got := collectAll(t, p)
	if !sliceEqual(got, []int{10, 20, 30}) {
		t.Fatalf("items changed: got %v", got)
	}
}

func TestTapError_FiresOnError(t *testing.T) {
	boom := errors.New("boom")
	var callbackErr error
	p := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				return 0, boom
			}
			return v, nil
		},
	)
	p2 := kitsune.TapError(p, func(_ context.Context, err error) {
		callbackErr = err
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := p2.ForEach(func(_ context.Context, _ int) error { return nil }).Run(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(callbackErr, boom) {
		t.Fatalf("callback received %v, want boom", callbackErr)
	}
}

func TestTapError_DoesNotSuppressError(t *testing.T) {
	boom := errors.New("boom")
	p := kitsune.Map(kitsune.FromSlice([]int{1}),
		func(_ context.Context, _ int) (int, error) { return 0, boom },
	)
	p2 := kitsune.TapError(p, func(_ context.Context, _ error) {})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := p2.ForEach(func(_ context.Context, _ int) error { return nil }).Run(ctx)
	if !errors.Is(err, boom) {
		t.Fatalf("error not propagated: got %v", err)
	}
}

func TestTapError_ContextCancelDoesNotFire(t *testing.T) {
	var called bool
	ctx, cancel := context.WithCancel(context.Background())

	ch := kitsune.NewChannel[int](0)
	p := kitsune.TapError(ch.Source(), func(_ context.Context, _ error) {
		called = true
	})
	done := make(chan error, 1)
	go func() {
		done <- p.ForEach(func(_ context.Context, _ int) error { return nil }).Run(ctx)
	}()
	cancel()
	<-done
	if called {
		t.Fatal("callback fired on context cancellation")
	}
}

func TestTapError_MethodForm(t *testing.T) {
	boom := errors.New("boom")
	var callbackErr error
	p := kitsune.Map(kitsune.FromSlice([]int{1}),
		func(_ context.Context, _ int) (int, error) { return 0, boom },
	)
	p2 := p.TapError(func(err error) { callbackErr = err })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p2.ForEach(func(_ context.Context, _ int) error { return nil }).Run(ctx)
	if !errors.Is(callbackErr, boom) {
		t.Fatalf("method form: callback received %v, want boom", callbackErr)
	}
}

// ---------------------------------------------------------------------------
// Finally
// ---------------------------------------------------------------------------

func TestFinally_PassesThroughItems(t *testing.T) {
	p := kitsune.Finally(kitsune.FromSlice([]int{1, 2, 3}), func(_ context.Context, _ error) {})
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Fatalf("items changed: got %v", got)
	}
}

func TestFinally_FiresOnSuccess(t *testing.T) {
	var callbackErr error
	var called bool
	p := kitsune.Finally(kitsune.FromSlice([]int{1, 2, 3}), func(_ context.Context, err error) {
		called = true
		callbackErr = err
	})
	collectAll(t, p)
	if !called {
		t.Fatal("callback not fired on success")
	}
	if callbackErr != nil {
		t.Fatalf("expected nil error on success, got %v", callbackErr)
	}
}

func TestFinally_FiresOnError(t *testing.T) {
	boom := errors.New("boom")
	var callbackErr error
	p := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				return 0, boom
			}
			return v, nil
		},
	)
	p2 := kitsune.Finally(p, func(_ context.Context, err error) { callbackErr = err })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := p2.ForEach(func(_ context.Context, _ int) error { return nil }).Run(ctx)
	if !errors.Is(err, boom) {
		t.Fatalf("error not propagated: got %v", err)
	}
	if !errors.Is(callbackErr, boom) {
		t.Fatalf("callback received %v, want boom", callbackErr)
	}
}

func TestFinally_FiresOnContextCancel(t *testing.T) {
	var called bool
	ctx, cancel := context.WithCancel(context.Background())

	ch := kitsune.NewChannel[int](0)
	p := kitsune.Finally(ch.Source(), func(_ context.Context, _ error) {
		called = true
	})
	done := make(chan error, 1)
	go func() {
		done <- p.ForEach(func(_ context.Context, _ int) error { return nil }).Run(ctx)
	}()
	cancel()
	<-done
	if !called {
		t.Fatal("callback not fired on context cancellation")
	}
}

func TestFinally_FiresOnEarlyStop(t *testing.T) {
	// Take causes an early consumer stop; Finally should fire with nil.
	var callbackErr error
	var called bool
	p := kitsune.Finally(
		kitsune.Take(kitsune.FromSlice([]int{1, 2, 3, 4, 5}), 2),
		func(_ context.Context, err error) {
			called = true
			callbackErr = err
		},
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2}) {
		t.Fatalf("got %v, want [1 2]", got)
	}
	if !called {
		t.Fatal("callback not fired on early stop")
	}
	if callbackErr != nil {
		t.Fatalf("expected nil on early stop, got %v", callbackErr)
	}
}

func TestFinally_MethodForm(t *testing.T) {
	var called bool
	p := kitsune.FromSlice([]int{1, 2, 3}).Finally(func(_ error) { called = true })
	collectAll(t, p)
	if !called {
		t.Fatal("method form: callback not fired")
	}
}

// ---------------------------------------------------------------------------
// IgnoreElements
// ---------------------------------------------------------------------------

func TestIgnoreElements_NoItemsDownstream(t *testing.T) {
	got := collectAll(t, kitsune.IgnoreElements(kitsune.FromSlice([]int{1, 2, 3})))
	if len(got) != 0 {
		t.Fatalf("IgnoreElements emitted items: %v", got)
	}
}

func TestIgnoreElements_MethodForm(t *testing.T) {
	got := collectAll(t, kitsune.FromSlice([]int{1, 2, 3}).IgnoreElements())
	if len(got) != 0 {
		t.Fatalf("IgnoreElements method form emitted items: %v", got)
	}
}

func TestIgnoreElements_SideEffectsRun(t *testing.T) {
	var count int
	p := kitsune.Tap(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, _ int) error {
			count++
			return nil
		},
	)
	got := collectAll(t, kitsune.IgnoreElements(p))
	if len(got) != 0 {
		t.Fatalf("IgnoreElements emitted items: %v", got)
	}
	if count != 3 {
		t.Fatalf("Tap ran %d times, want 3", count)
	}
}

func TestIgnoreElements_ErrorPropagates(t *testing.T) {
	sentinel := errors.New("boom")
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, _ int) (int, error) { return 0, sentinel },
	)
	ctx := context.Background()
	err := kitsune.IgnoreElements(p).ForEach(func(_ context.Context, _ int) error {
		return nil
	}).Run(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIgnoreElements_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	var items []int
	err := kitsune.IgnoreElements(kitsune.Never[int]()).ForEach(func(_ context.Context, v int) error {
		items = append(items, v)
		return nil
	}).Run(ctx)

	if len(items) != 0 {
		t.Fatalf("IgnoreElements emitted items: %v", items)
	}
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ExpandMap
// ---------------------------------------------------------------------------

// expandTree is a helper that maps an int to its "children" in a simple tree:
// 1 → [2, 3], 2 → [4, 5], 3 → [], 4 → [], 5 → [].
func expandTree(_ context.Context, v int) *kitsune.Pipeline[int] {
	switch v {
	case 1:
		return kitsune.FromSlice([]int{2, 3})
	case 2:
		return kitsune.FromSlice([]int{4, 5})
	default:
		return nil // leaf node
	}
}

func TestExpandMap_SingleLevel(t *testing.T) {
	// fn always returns nil — only root items are emitted.
	p := kitsune.ExpandMap(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, _ int) *kitsune.Pipeline[int] { return nil },
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Fatalf("got %v, want [1 2 3]", got)
	}
}

func TestExpandMap_BFSOrder(t *testing.T) {
	// Tree: root emits [1]; 1 expands to [2,3]; 2 expands to [4,5]; 3,4,5 are leaves.
	// BFS order: 1, 2, 3, 4, 5.
	p := kitsune.ExpandMap(kitsune.FromSlice([]int{1}), expandTree)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("got %v, want [1 2 3 4 5]", got)
	}
}

func TestExpandMap_MultipleRoots(t *testing.T) {
	// Two roots: 1 and 2; each expands via expandTree.
	// BFS: 1, 2, then children of 1 (2,3), then children of 2 (4,5),
	// then children of those children (4,5 from expanding 2 again, but 3,4,5 are leaves).
	// Root: [1,2] → expand 1→[2,3], expand 2→[4,5] → expand 2→[4,5], expand 3→nil,
	//        expand 4→nil, expand 5→nil
	// Emit order: 1, 2, 2, 3, 4, 5, 4, 5
	p := kitsune.ExpandMap(kitsune.FromSlice([]int{1, 2}), expandTree)
	got := collectAll(t, p)
	want := []int{1, 2, 2, 3, 4, 5, 4, 5}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandMap_EarlyStop(t *testing.T) {
	// Take(2) stops expansion after 2 items.
	p := kitsune.Take(kitsune.ExpandMap(kitsune.FromSlice([]int{1}), expandTree), 2)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2}) {
		t.Fatalf("got %v, want [1 2]", got)
	}
}

func TestExpandMap_ErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	p := kitsune.ExpandMap(kitsune.FromSlice([]int{1}),
		func(_ context.Context, _ int) *kitsune.Pipeline[int] {
			return kitsune.Map(kitsune.FromSlice([]int{0}),
				func(_ context.Context, _ int) (int, error) { return 0, boom },
			)
		},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := p.ForEach(func(_ context.Context, _ int) error { return nil }).Run(ctx)
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
}

func TestExpandMap_NilChildSkipped(t *testing.T) {
	// Mix of nil and non-nil: only the non-nil child is followed.
	calls := 0
	p := kitsune.ExpandMap(kitsune.FromSlice([]int{1, 2}),
		func(_ context.Context, v int) *kitsune.Pipeline[int] {
			calls++
			if v == 1 {
				return kitsune.FromSlice([]int{10})
			}
			return nil
		},
	)
	got := collectAll(t, p)
	// 1 and 2 emitted; 1 expands to 10; 2 returns nil; 10 returns nil (calls=3)
	if !sliceEqual(got, []int{1, 2, 10}) {
		t.Fatalf("got %v, want [1 2 10]", got)
	}
	if calls != 3 {
		t.Fatalf("expected 3 fn calls, got %d", calls)
	}
}

func TestExpandMap_ExpandMapFunc(t *testing.T) {
	// ExpandMapFunc lifts a context-free fn.
	p := kitsune.ExpandMap(kitsune.FromSlice([]int{1}),
		kitsune.ExpandMapFunc(func(v int) *kitsune.Pipeline[int] {
			if v == 1 {
				return kitsune.FromSlice([]int{2, 3})
			}
			return nil
		}),
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Fatalf("got %v, want [1 2 3]", got)
	}
}

func TestExpandMap_WithName(t *testing.T) {
	// WithName should not affect correctness.
	p := kitsune.ExpandMap(
		kitsune.FromSlice([]int{1}),
		expandTree,
		kitsune.WithName("my_expand"),
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("got %v, want [1 2 3 4 5]", got)
	}
}

func TestExpandMap_VisitedBy_BreaksCycle(t *testing.T) {
	// Graph: 1→[2], 2→[3], 3→[1] (cycle). Without VisitedBy this never terminates.
	fn := func(_ context.Context, v int) *kitsune.Pipeline[int] {
		next := (v % 3) + 1 // 1→2, 2→3, 3→1
		return kitsune.FromSlice([]int{next})
	}
	p := kitsune.ExpandMap(
		kitsune.FromSlice([]int{1}),
		fn,
		kitsune.VisitedBy(func(v int) string { return fmt.Sprint(v) }),
	)
	got := collectAll(t, p)
	// 1 emitted → enqueues [2]; 2 emitted → enqueues [3]; 3 emitted → enqueues [1];
	// 1 already seen → skip. Output: [1, 2, 3].
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Fatalf("got %v, want [1 2 3]", got)
	}
}

func TestExpandMap_VisitedBy_SkipsSubtreeOfSeenItem(t *testing.T) {
	// DAG: 1→[2,3], 2→[4], 3→[2]. Without dedup: 1,2,3,4,2,4 (2 and 4 twice).
	// With VisitedBy: when 3 tries to expand 2, it's already seen — skip.
	fn := func(_ context.Context, v int) *kitsune.Pipeline[int] {
		switch v {
		case 1:
			return kitsune.FromSlice([]int{2, 3})
		case 2:
			return kitsune.FromSlice([]int{4})
		case 3:
			return kitsune.FromSlice([]int{2}) // points back at 2
		default:
			return nil
		}
	}
	p := kitsune.ExpandMap(
		kitsune.FromSlice([]int{1}),
		fn,
		kitsune.VisitedBy(func(v int) string { return fmt.Sprint(v) }),
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3, 4}) {
		t.Fatalf("got %v, want [1 2 3 4]", got)
	}
}

func TestExpandMap_VisitedBy_ExternalDedupSet(t *testing.T) {
	// WithDedupSet overrides the default MemoryDedupSet.
	set := kitsune.MemoryDedupSet()
	fn := func(_ context.Context, v int) *kitsune.Pipeline[int] {
		next := (v % 3) + 1
		return kitsune.FromSlice([]int{next})
	}
	p := kitsune.ExpandMap(
		kitsune.FromSlice([]int{1}),
		fn,
		kitsune.VisitedBy(func(v int) string { return fmt.Sprint(v) }),
		kitsune.WithDedupSet(set),
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Fatalf("got %v, want [1 2 3]", got)
	}
}

// ---------------------------------------------------------------------------
// ExpandMap — MaxDepth / MaxItems bounds
// ---------------------------------------------------------------------------

// depthTree is an infinite binary tree generator: every node v expands to
// [2v, 2v+1].
func depthTree(_ context.Context, v int) *kitsune.Pipeline[int] {
	return kitsune.FromSlice([]int{2 * v, 2*v + 1})
}

func TestExpandMap_MaxDepth_Zero(t *testing.T) {
	// MaxDepth(0): emit roots only; do not expand.
	p := kitsune.ExpandMap(
		kitsune.FromSlice([]int{1}),
		depthTree,
		kitsune.MaxDepth(0),
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1}) {
		t.Fatalf("got %v, want [1]", got)
	}
}

func TestExpandMap_MaxDepth_One(t *testing.T) {
	// MaxDepth(1): roots + one level of children.
	// Root 1 expands to [2,3]. BFS order: 1, 2, 3.
	p := kitsune.ExpandMap(
		kitsune.FromSlice([]int{1}),
		depthTree,
		kitsune.MaxDepth(1),
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Fatalf("got %v, want [1 2 3]", got)
	}
}

func TestExpandMap_MaxDepth_ExceedsTree(t *testing.T) {
	// MaxDepth larger than actual tree height: full tree is emitted.
	// expandTree (defined earlier): 1->[2,3], 2->[4,5], others leaves.
	// Full BFS order: 1, 2, 3, 4, 5.
	p := kitsune.ExpandMap(
		kitsune.FromSlice([]int{1}),
		expandTree,
		kitsune.MaxDepth(10),
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("got %v, want [1 2 3 4 5]", got)
	}
}

func TestExpandMap_MaxItems(t *testing.T) {
	// Infinite binary tree bounded to exactly 5 items.
	// BFS order on binary tree rooted at 1: 1, 2, 3, 4, 5.
	p := kitsune.ExpandMap(
		kitsune.FromSlice([]int{1}),
		depthTree,
		kitsune.MaxItems(5),
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("got %v, want [1 2 3 4 5]", got)
	}
}

func TestExpandMap_MaxItems_ExceedsTotal(t *testing.T) {
	// MaxItems larger than total tree: all items emitted.
	p := kitsune.ExpandMap(
		kitsune.FromSlice([]int{1}),
		expandTree,
		kitsune.MaxItems(100),
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("got %v, want [1 2 3 4 5]", got)
	}
}

func TestExpandMap_MaxDepth_And_MaxItems(t *testing.T) {
	// Both set. Binary tree rooted at 1, MaxDepth(3) would allow up to
	// 1+2+4+8=15 items. MaxItems(4) fires first.
	// BFS: 1, 2, 3, 4 (stop after 4 items).
	p := kitsune.ExpandMap(
		kitsune.FromSlice([]int{1}),
		depthTree,
		kitsune.MaxDepth(3),
		kitsune.MaxItems(4),
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3, 4}) {
		t.Fatalf("got %v, want [1 2 3 4]", got)
	}
}

// ---------------------------------------------------------------------------
// Take / Drop
// ---------------------------------------------------------------------------

func TestTake(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, kitsune.Take(p, 3))
	want := []int{1, 2, 3}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTakeMoreThanAvailable(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2})
	got := collectAll(t, kitsune.Take(p, 10))
	want := []int{1, 2}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDrop(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, kitsune.Drop(p, 2))
	want := []int{3, 4, 5}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSkip(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, p.Skip(2))
	want := []int{3, 4, 5}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// TakeWhile / DropWhile
// ---------------------------------------------------------------------------

func TestTakeWhile(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 1, 2})
	got := collectAll(t, kitsune.TakeWhile(p, func(v int) bool { return v < 4 }))
	want := []int{1, 2, 3}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDropWhile(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 1, 2})
	got := collectAll(t, kitsune.DropWhile(p, func(v int) bool { return v < 4 }))
	want := []int{4, 1, 2}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Batch / Unbatch / SlidingWindow
// ---------------------------------------------------------------------------

func TestBatch(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, kitsune.Batch(p, kitsune.BatchCount(2)))
	if len(got) != 3 {
		t.Fatalf("expected 3 batches, got %d: %v", len(got), got)
	}
	if !sliceEqual(got[0], []int{1, 2}) {
		t.Errorf("batch 0: got %v", got[0])
	}
	if !sliceEqual(got[1], []int{3, 4}) {
		t.Errorf("batch 1: got %v", got[1])
	}
	if !sliceEqual(got[2], []int{5}) {
		t.Errorf("batch 2 (partial): got %v", got[2])
	}
}

func TestUnbatch(t *testing.T) {
	p := kitsune.FromSlice([][]int{{1, 2}, {3, 4, 5}})
	got := collectAll(t, kitsune.Unbatch(p))
	want := []int{1, 2, 3, 4, 5}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBatch_DropPartial(t *testing.T) {
	// 7 items, size 3: full batches are [1,2,3] and [4,5,6]; trailing [7] is dropped.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7})
	got := collectAll(t, kitsune.Batch(p, kitsune.BatchCount(3), kitsune.DropPartial()))
	if len(got) != 2 {
		t.Fatalf("expected 2 full batches, got %d: %v", len(got), got)
	}
	if !sliceEqual(got[0], []int{1, 2, 3}) {
		t.Errorf("batch 0: %v", got[0])
	}
	if !sliceEqual(got[1], []int{4, 5, 6}) {
		t.Errorf("batch 1: %v", got[1])
	}
}

func TestBatch_BatchCount(t *testing.T) {
	ctx := context.Background()
	ch := kitsune.NewChannel[int](10)
	batches := make(chan []int, 10)
	done := make(chan error, 1)
	go func() {
		done <- kitsune.Batch(ch.Source(), kitsune.BatchCount(3)).
			ForEach(func(_ context.Context, b []int) error {
				batches <- b
				return nil
			}).Run(ctx)
	}()

	for _, v := range []int{1, 2, 3, 4, 5} {
		if err := ch.Send(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	ch.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	var got [][]int
	for len(batches) > 0 {
		got = append(got, <-batches)
	}
	if len(got) != 2 || len(got[0]) != 3 || len(got[1]) != 2 {
		t.Errorf("batches: got %v", got)
	}
}

func TestBatch_BatchMeasure(t *testing.T) {
	ctx := context.Background()
	ch := kitsune.NewChannel[string](10)
	batches := make(chan []string, 10)
	done := make(chan error, 1)
	go func() {
		// flush when total byte length >= 6
		done <- kitsune.Batch(ch.Source(),
			kitsune.BatchMeasure(func(s string) int { return len(s) }, 6),
		).ForEach(func(_ context.Context, b []string) error {
			batches <- b
			return nil
		}).Run(ctx)
	}()

	// "abc"=3, "def"=3 -> flush at 6; "gh"=2 -> partial on close
	for _, s := range []string{"abc", "def", "gh"} {
		if err := ch.Send(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	ch.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	var got [][]string
	for len(batches) > 0 {
		got = append(got, <-batches)
	}
	if len(got) != 2 || len(got[0]) != 2 || got[0][0] != "abc" {
		t.Errorf("batches: got %v", got)
	}
}

func TestBatch_PanicIfNoTrigger(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when no flush trigger provided")
		}
	}()
	ch := kitsune.NewChannel[int](1)
	kitsune.Batch(ch.Source()) // no BatchCount, BatchMeasure, or BatchTimeout
}

func TestSlidingWindow(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, kitsune.SlidingWindow(p, 3, 1))
	if len(got) != 3 {
		t.Fatalf("expected 3 windows, got %d: %v", len(got), got)
	}
	if !sliceEqual(got[0], []int{1, 2, 3}) {
		t.Errorf("window 0: %v", got[0])
	}
	if !sliceEqual(got[1], []int{2, 3, 4}) {
		t.Errorf("window 1: %v", got[1])
	}
	if !sliceEqual(got[2], []int{3, 4, 5}) {
		t.Errorf("window 2: %v", got[2])
	}
}

// ---------------------------------------------------------------------------
// Scan / Reduce
// ---------------------------------------------------------------------------

func TestScan(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4})
	got := collectAll(t, kitsune.Scan(p, 0, func(acc, v int) int { return acc + v }))
	want := []int{1, 3, 6, 10}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestReduce(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, kitsune.Reduce(p, 0, func(acc, v int) int { return acc + v }))
	want := []int{15}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestReduceEmpty(t *testing.T) {
	p := kitsune.FromSlice([]int{})
	got := collectAll(t, kitsune.Reduce(p, 42, func(acc, v int) int { return acc + v }))
	want := []int{42}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Distinct / DistinctBy / Dedupe / DedupeBy
// ---------------------------------------------------------------------------

func TestDistinct(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 1, 3, 2, 4})
	got := collectAll(t, kitsune.Distinct(p))
	want := []int{1, 2, 3, 4}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDedupe(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 1, 2, 2, 3, 1, 1})
	got := collectAll(t, kitsune.Dedupe(p))
	want := []int{1, 2, 3, 1}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// GroupBy / Frequencies
// ---------------------------------------------------------------------------

func TestGroupBy(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]string{"a", "b", "a", "c", "b", "a"})
	byKey, err := kitsune.GroupBy(ctx, p, func(s string) string { return s })
	if err != nil {
		t.Fatal(err)
	}
	if len(byKey["a"]) != 3 || len(byKey["b"]) != 2 || len(byKey["c"]) != 1 {
		t.Fatalf("unexpected groups: %v", byKey)
	}
}

func TestGroupByStream(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]string{"a", "b", "a", "c", "b", "a"})
	groups, err := kitsune.Collect(ctx, kitsune.GroupByStream(p, func(s string) string { return s }))
	if err != nil {
		t.Fatal(err)
	}
	// Expect three groups in first-seen order: a, b, c.
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d: %v", len(groups), groups)
	}
	if groups[0].Key != "a" || len(groups[0].Items) != 3 {
		t.Errorf("group[0]: got key=%q items=%v, want key=a items=[a a a]", groups[0].Key, groups[0].Items)
	}
	if groups[1].Key != "b" || len(groups[1].Items) != 2 {
		t.Errorf("group[1]: got key=%q items=%v, want key=b items=[b b]", groups[1].Key, groups[1].Items)
	}
	if groups[2].Key != "c" || len(groups[2].Items) != 1 {
		t.Errorf("group[2]: got key=%q items=%v, want key=c items=[c]", groups[2].Key, groups[2].Items)
	}
}

func TestGroupByStreamEmpty(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]string{})
	groups, err := kitsune.Collect(ctx, kitsune.GroupByStream(p, func(s string) string { return s }))
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Fatalf("expected empty result, got %v", groups)
	}
}

func TestFrequencies(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]string{"a", "b", "a", "c", "b", "a"})
	freq, err := kitsune.Frequencies(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if freq["a"] != 3 || freq["b"] != 2 || freq["c"] != 1 {
		t.Fatalf("unexpected frequencies: %v", freq)
	}
}

func TestRunningFrequencies(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]string{"a", "b", "a"})
	snapshots, err := kitsune.Collect(ctx, kitsune.RunningFrequencies(p))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 3 {
		t.Fatalf("expected 3 running snapshots, got %d", len(snapshots))
	}
	last := snapshots[len(snapshots)-1]
	if last["a"] != 2 || last["b"] != 1 {
		t.Fatalf("final snapshot: got %v, want {a:2, b:1}", last)
	}
}

func TestRunningFrequenciesBy(t *testing.T) {
	ctx := context.Background()
	type item struct{ key string }
	p := kitsune.FromSlice([]item{{"x"}, {"y"}, {"x"}, {"x"}})
	snapshots, err := kitsune.Collect(ctx,
		kitsune.RunningFrequenciesBy(p, func(i item) string { return i.key }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 4 {
		t.Fatalf("expected 4 running snapshots, got %d", len(snapshots))
	}
	last := snapshots[len(snapshots)-1]
	if last["x"] != 3 || last["y"] != 1 {
		t.Fatalf("final snapshot: got %v, want {x:3, y:1}", last)
	}
}

// ---------------------------------------------------------------------------
// Merge
// ---------------------------------------------------------------------------

func TestMerge(t *testing.T) {
	a := kitsune.FromSlice([]int{1, 2, 3})
	b := kitsune.FromSlice([]int{4, 5, 6})
	got := collectAll(t, kitsune.Merge(a, b))
	sort.Ints(got)
	want := []int{1, 2, 3, 4, 5, 6}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Partition
// ---------------------------------------------------------------------------

func TestPartition(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	evens, odds := kitsune.Partition(p, func(v int) bool { return v%2 == 0 })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var evenItems, oddItems []int
	evenRunner := evens.ForEach(func(_ context.Context, v int) error {
		evenItems = append(evenItems, v)
		return nil
	}).Build()
	oddRunner := odds.ForEach(func(_ context.Context, v int) error {
		oddItems = append(oddItems, v)
		return nil
	}).Build()

	runner, err := kitsune.MergeRunners(evenRunner, oddRunner)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	sort.Ints(evenItems)
	sort.Ints(oddItems)
	if !sliceEqual(evenItems, []int{2, 4, 6}) {
		t.Fatalf("evens: got %v", evenItems)
	}
	if !sliceEqual(oddItems, []int{1, 3, 5}) {
		t.Fatalf("odds: got %v", oddItems)
	}
}

// ---------------------------------------------------------------------------
// Broadcast
// ---------------------------------------------------------------------------

func TestBroadcast(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	branches := kitsune.Broadcast(p, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got0, got1 []int
	r0 := branches[0].ForEach(func(_ context.Context, v int) error {
		got0 = append(got0, v)
		return nil
	}).Build()
	r1 := branches[1].ForEach(func(_ context.Context, v int) error {
		got1 = append(got1, v)
		return nil
	}).Build()

	runner, err := kitsune.MergeRunners(r0, r1)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	if !sliceEqual(got0, []int{1, 2, 3}) {
		t.Fatalf("branch 0: got %v", got0)
	}
	if !sliceEqual(got1, []int{1, 2, 3}) {
		t.Fatalf("branch 1: got %v", got1)
	}
}

// ---------------------------------------------------------------------------
// Balance
// ---------------------------------------------------------------------------

func TestBalance(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	branches := kitsune.Balance(p, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var count0, count1 atomic.Int64
	r0 := branches[0].ForEach(func(_ context.Context, _ int) error {
		count0.Add(1)
		return nil
	}).Build()
	r1 := branches[1].ForEach(func(_ context.Context, _ int) error {
		count1.Add(1)
		return nil
	}).Build()

	runner, err := kitsune.MergeRunners(r0, r1)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	total := count0.Load() + count1.Load()
	if total != 6 {
		t.Fatalf("expected 6 total items, got %d (0=%d, 1=%d)", total, count0.Load(), count1.Load())
	}
	// Round-robin: 3 items each
	if count0.Load() != 3 || count1.Load() != 3 {
		t.Fatalf("expected 3/3 split, got %d/%d", count0.Load(), count1.Load())
	}
}

// ---------------------------------------------------------------------------
// KeyedBalance
// ---------------------------------------------------------------------------

func TestKeyedBalance(t *testing.T) {
	// Items with same key prefix must always go to the same branch.
	items := []string{
		"a#1", "b#1", "a#2", "c#1", "b#2", "a#3", "c#2", "b#3",
	}
	p := kitsune.FromSlice(items)
	branches := kitsune.KeyedBalance(p, 3, func(s string) string { return s[:1] })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var mu sync.Mutex
	keyToBranch := map[string]int{}
	counts := make([]int, 3)
	runners := make([]kitsune.Runnable, 3)
	for i := 0; i < 3; i++ {
		i := i
		runners[i] = branches[i].ForEach(func(_ context.Context, s string) error {
			mu.Lock()
			defer mu.Unlock()
			counts[i]++
			key := s[:1]
			if prev, ok := keyToBranch[key]; ok && prev != i {
				t.Errorf("key %q seen on branch %d and %d", key, prev, i)
			}
			keyToBranch[key] = i
			return nil
		}).Build()
	}

	runner, err := kitsune.MergeRunners(runners...)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	total := counts[0] + counts[1] + counts[2]
	if total != len(items) {
		t.Fatalf("expected %d total items, got %d (%v)", len(items), total, counts)
	}
	if len(keyToBranch) != 3 {
		t.Fatalf("expected 3 distinct keys, got %d", len(keyToBranch))
	}
}

func TestKeyedBalanceN2(t *testing.T) {
	p := kitsune.FromSlice([]string{"x1", "y1", "x2", "y2", "x3"})
	branches := kitsune.KeyedBalance(p, 2, func(s string) string { return s[:1] })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var total atomic.Int64
	r0 := branches[0].ForEach(func(_ context.Context, _ string) error {
		total.Add(1)
		return nil
	}).Build()
	r1 := branches[1].ForEach(func(_ context.Context, _ string) error {
		total.Add(1)
		return nil
	}).Build()

	runner, err := kitsune.MergeRunners(r0, r1)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if total.Load() != 5 {
		t.Fatalf("expected 5 items, got %d", total.Load())
	}
}

func TestKeyedBalanceWithName(t *testing.T) {
	p := kitsune.FromSlice([]string{"a", "b"})
	branches := kitsune.KeyedBalance(p, 2, func(s string) string { return s }, kitsune.WithName("shards"))
	if len(branches) != 2 {
		t.Fatalf("expected 2 branches, got %d", len(branches))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r0 := branches[0].ForEach(func(_ context.Context, _ string) error { return nil }).Build()
	r1 := branches[1].ForEach(func(_ context.Context, _ string) error { return nil }).Build()
	runner, err := kitsune.MergeRunners(r0, r1)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Share
// ---------------------------------------------------------------------------

func TestShare_BasicMulticast(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	subscribe := kitsune.Share(p)
	a := subscribe()
	b := subscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var gotA, gotB []int
	rA := a.ForEach(func(_ context.Context, v int) error {
		gotA = append(gotA, v)
		return nil
	}).Build()
	rB := b.ForEach(func(_ context.Context, v int) error {
		gotB = append(gotB, v)
		return nil
	}).Build()

	runner, err := kitsune.MergeRunners(rA, rB)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	if !sliceEqual(gotA, []int{1, 2, 3}) {
		t.Fatalf("branch A: got %v, want [1 2 3]", gotA)
	}
	if !sliceEqual(gotB, []int{1, 2, 3}) {
		t.Fatalf("branch B: got %v, want [1 2 3]", gotB)
	}
}

func TestShare_SingleSubscriber(t *testing.T) {
	// Unlike Broadcast (requires n>=2), Share allows a single subscriber.
	p := kitsune.FromSlice([]int{10, 20, 30})
	subscribe := kitsune.Share(p)
	only := subscribe()

	got := collectAll(t, only)
	if !sliceEqual(got, []int{10, 20, 30}) {
		t.Fatalf("got %v, want [10 20 30]", got)
	}
}

func TestShare_ThreeSubscribers(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	subscribe := kitsune.Share(p)
	a := subscribe()
	b := subscribe()
	c := subscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var gotA, gotB, gotC []int
	rA := a.ForEach(func(_ context.Context, v int) error { gotA = append(gotA, v); return nil }).Build()
	rB := b.ForEach(func(_ context.Context, v int) error { gotB = append(gotB, v); return nil }).Build()
	rC := c.ForEach(func(_ context.Context, v int) error { gotC = append(gotC, v); return nil }).Build()

	runner, err := kitsune.MergeRunners(rA, rB, rC)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	want := []int{1, 2, 3, 4, 5}
	if !sliceEqual(gotA, want) {
		t.Fatalf("branch A: got %v", gotA)
	}
	if !sliceEqual(gotB, want) {
		t.Fatalf("branch B: got %v", gotB)
	}
	if !sliceEqual(gotC, want) {
		t.Fatalf("branch C: got %v", gotC)
	}
}

func TestShare_PerBranchBuffer(t *testing.T) {
	// Each branch can have an independent buffer size.
	p := kitsune.FromSlice([]int{1, 2, 3})
	subscribe := kitsune.Share(p)
	small := subscribe(kitsune.Buffer(1))
	large := subscribe(kitsune.Buffer(64))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var gotSmall, gotLarge []int
	rSmall := small.ForEach(func(_ context.Context, v int) error { gotSmall = append(gotSmall, v); return nil }).Build()
	rLarge := large.ForEach(func(_ context.Context, v int) error { gotLarge = append(gotLarge, v); return nil }).Build()

	runner, err := kitsune.MergeRunners(rSmall, rLarge)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	want := []int{1, 2, 3}
	if !sliceEqual(gotSmall, want) {
		t.Fatalf("small-buffer branch: got %v", gotSmall)
	}
	if !sliceEqual(gotLarge, want) {
		t.Fatalf("large-buffer branch: got %v", gotLarge)
	}
}

func TestShare_FactoryDefaultOpts(t *testing.T) {
	// Opts passed to Share act as defaults for all branches.
	p := kitsune.FromSlice([]int{1, 2})
	subscribe := kitsune.Share(p, kitsune.Buffer(32))
	a := subscribe()
	b := subscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var gotA, gotB []int
	rA := a.ForEach(func(_ context.Context, v int) error { gotA = append(gotA, v); return nil }).Build()
	rB := b.ForEach(func(_ context.Context, v int) error { gotB = append(gotB, v); return nil }).Build()

	runner, err := kitsune.MergeRunners(rA, rB)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	want := []int{1, 2}
	if !sliceEqual(gotA, want) {
		t.Fatalf("branch A: got %v", gotA)
	}
	if !sliceEqual(gotB, want) {
		t.Fatalf("branch B: got %v", gotB)
	}
}

func TestShare_LateSubscribePanics(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	subscribe := kitsune.Share(p)
	branch := subscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run the pipeline in a goroutine; the build closure will set frozen=true.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = branch.ForEach(func(_ context.Context, _ int) error { return nil }).Run(ctx)
	}()
	// Wait for the pipeline to complete (frozen is set during build, before items flow).
	<-done

	// Now subscribe() must panic because frozen=true.
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when subscribing after Run() started")
		}
	}()
	subscribe()
}

// ---------------------------------------------------------------------------
// Zip / ZipWith
// ---------------------------------------------------------------------------

func TestZip(t *testing.T) {
	a := kitsune.FromSlice([]int{1, 2, 3})
	b := kitsune.FromSlice([]string{"a", "b", "c"})
	got := collectAll(t, kitsune.Zip(a, b))
	want := []kitsune.Pair[int, string]{
		{First: 1, Second: "a"},
		{First: 2, Second: "b"},
		{First: 3, Second: "c"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("[%d]: got %v want %v", i, g, want[i])
		}
	}
}

func TestZipStopsOnShortInput(t *testing.T) {
	a := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	b := kitsune.FromSlice([]string{"a", "b"})
	got := collectAll(t, kitsune.Zip(a, b))
	if len(got) != 2 {
		t.Fatalf("expected 2 pairs (b is shorter), got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// CombineLatest
// ---------------------------------------------------------------------------

func TestCombineLatest(t *testing.T) {
	a := kitsune.FromSlice([]int{1, 2})
	b := kitsune.FromSlice([]string{"x", "y", "z"})

	got := collectAll(t, kitsune.CombineLatest(a, b))
	// Must emit at least one pair combining latest from each stream.
	if len(got) == 0 {
		t.Fatal("no output from CombineLatest")
	}
}

// ---------------------------------------------------------------------------
// LatestFrom
// ---------------------------------------------------------------------------

func TestLatestFrom(t *testing.T) {
	// other emits two values; main waits 20ms so the background goroutine in
	// LatestFrom has time to consume at least one value from other before
	// main items arrive (LatestFrom drops main items until other has emitted).
	other := kitsune.Generate(func(ctx context.Context, yield func(string) bool) error {
		yield("v1")
		yield("v2")
		return nil
	})
	main := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		time.Sleep(20 * time.Millisecond) // let other emit first
		for _, v := range []int{1, 2, 3} {
			if !yield(v) {
				return nil
			}
		}
		return nil
	})

	got := collectAll(t, kitsune.LatestFrom(main, other))
	if len(got) != 3 {
		t.Fatalf("expected 3 pairs (main has 3 items), got %d: %v", len(got), got)
	}
	for _, p := range got {
		if p.Second == "" {
			t.Errorf("empty Second in pair: %v", p)
		}
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// SlidingWindow edge cases (6d)
// ---------------------------------------------------------------------------

func TestSlidingWindowTumbling(t *testing.T) {
	// step == size → non-overlapping (tumbling) windows, same as Window.
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	got, err := kitsune.Collect(ctx, kitsune.SlidingWindow(p, 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]int{{1, 2}, {3, 4}, {5, 6}}
	if len(got) != len(want) {
		t.Fatalf("got %d windows, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if !sliceEqual(got[i], w) {
			t.Errorf("window[%d]: got %v, want %v", i, got[i], w)
		}
	}
}

func TestSlidingWindowShortStream(t *testing.T) {
	// Stream shorter than window size — no windows emitted (partial windows dropped).
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2})
	got, err := kitsune.Collect(ctx, kitsune.SlidingWindow(p, 5, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 windows for short stream, got %d: %v", len(got), got)
	}
}

func TestSlidingWindowPanics(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})

	t.Run("step zero", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for step=0")
			}
		}()
		kitsune.SlidingWindow(src, 3, 0)
	})

	t.Run("step negative", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for step=-1")
			}
		}()
		kitsune.SlidingWindow(src, 3, -1)
	})

	t.Run("step greater than size", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for step>size")
			}
		}()
		kitsune.SlidingWindow(src, 3, 4)
	})
}

func TestOrderedNoConcurrency(t *testing.T) {
	// Ordered() with Concurrency(1) should produce the same output as serial execution.
	items := []int{1, 2, 3, 4, 5}
	results, err := kitsune.Map(
		kitsune.FromSlice(items),
		func(_ context.Context, v int) (int, error) { return v * 2, nil },
		kitsune.Concurrency(1), kitsune.Ordered(),
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(items) {
		t.Fatalf("want %d results, got %d", len(items), len(results))
	}
	for i, v := range results {
		if want := items[i] * 2; v != want {
			t.Fatalf("results[%d] = %d, want %d", i, v, want)
		}
	}
}

// ---------------------------------------------------------------------------
// TakeUntil / DropUntil
// ---------------------------------------------------------------------------

func TestTakeUntil_SourceFinishesBeforeBoundary(t *testing.T) {
	// Boundary never fires; p is finite — all items should be emitted.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	// Boundary that blocks until ctx is cancelled (i.e. never fires on its own).
	boundary := kitsune.Generate(func(ctx context.Context, yield func(struct{}) bool) error {
		<-ctx.Done()
		return nil
	})
	got := collectAll(t, kitsune.TakeUntil(p, boundary))
	want := []int{1, 2, 3, 4, 5}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTakeUntil_BoundaryFiresImmediately(t *testing.T) {
	// Empty boundary closes immediately — TakeUntil should stop early.
	// p has 1000 items; we only verify that fewer than 1000 are emitted.
	ctx := context.Background()
	items := make([]int, 1000)
	for i := range items {
		items[i] = i
	}
	p := kitsune.FromSlice(items)
	boundary := kitsune.FromSlice([]struct{}{{}}) // one item, fires immediately
	got, _ := kitsune.Collect(ctx, kitsune.TakeUntil(p, boundary))
	if len(got) >= 1000 {
		t.Fatalf("expected fewer than 1000 items, got %d", len(got))
	}
}

func TestTakeUntil_BoundaryChannel(t *testing.T) {
	// Control both p and boundary via NewChannel. Send 3 items, fire boundary,
	// close p — expect exactly 3 items (boundary fires before remaining sends).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pCh := kitsune.NewChannel[int](10)
	bCh := kitsune.NewChannel[struct{}](1)

	resultCh := make(chan []int, 1)
	go func() {
		got, _ := kitsune.Collect(ctx, kitsune.TakeUntil(pCh.Source(), bCh.Source()))
		resultCh <- got
	}()

	for _, v := range []int{1, 2, 3} {
		if err := pCh.Send(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	// Fire boundary then close p — no more items should arrive.
	if err := bCh.Send(ctx, struct{}{}); err != nil {
		t.Fatal(err)
	}
	bCh.Close()
	pCh.Close()

	select {
	case got := <-resultCh:
		// Must have stopped at or before item 3.
		if len(got) > 3 {
			t.Fatalf("expected ≤3 items, got %v", got)
		}
		for i, v := range got {
			if v != i+1 {
				t.Fatalf("item[%d]=%d, want %d", i, v, i+1)
			}
		}
	case <-ctx.Done():
		t.Fatal("test timed out")
	}
}

func TestDropUntil_BoundaryNeverFires(t *testing.T) {
	// Boundary never fires; p is finite — no items should be emitted.
	p := kitsune.FromSlice([]int{1, 2, 3})
	boundary := kitsune.Generate(func(ctx context.Context, yield func(struct{}) bool) error {
		<-ctx.Done()
		return nil
	})
	got := collectAll(t, kitsune.DropUntil(p, boundary))
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestDropUntil_EmptySource(t *testing.T) {
	// p is empty; boundary fires immediately. Output must be empty and error-free.
	// (No race: p emits nothing regardless of when boundary fires.)
	p := kitsune.FromSlice([]int{})
	boundary := kitsune.FromSlice([]struct{}{{}})
	got := collectAll(t, kitsune.DropUntil(p, boundary))
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestDropUntil_BoundaryChannel(t *testing.T) {
	// Fire boundary after 3 items; the subsequent items must appear in output.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pCh := kitsune.NewChannel[int](10)
	bCh := kitsune.NewChannel[struct{}](1)

	resultCh := make(chan []int, 1)
	go func() {
		got, _ := kitsune.Collect(ctx, kitsune.DropUntil(pCh.Source(), bCh.Source()))
		resultCh <- got
	}()

	// These items arrive before the gate opens — they should be skipped.
	for _, v := range []int{1, 2, 3} {
		if err := pCh.Send(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	// Open the gate.
	if err := bCh.Send(ctx, struct{}{}); err != nil {
		t.Fatal(err)
	}
	bCh.Close()
	// Give the stage goroutine time to process the boundary signal before
	// sending post-gate items — otherwise items may arrive before gate opens.
	time.Sleep(pipelineStartup)
	// These items arrive after the gate opens — they must be emitted.
	for _, v := range []int{10, 20, 30} {
		if err := pCh.Send(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	pCh.Close()

	select {
	case got := <-resultCh:
		// The post-gate items must all be present.
		if len(got) < 3 {
			t.Fatalf("expected at least 3 items, got %d: %v", len(got), got)
		}
		last3 := got[len(got)-3:]
		if !sliceEqual(last3, []int{10, 20, 30}) {
			t.Fatalf("last 3 items: got %v, want [10 20 30] (full: %v)", last3, got)
		}
	case <-ctx.Done():
		t.Fatal("test timed out")
	}
}

// ---------------------------------------------------------------------------
// DropLast
// ---------------------------------------------------------------------------

func TestDropLast_Basic(t *testing.T) {
	got := collectAll(t, kitsune.DropLast(kitsune.FromSlice([]int{1, 2, 3, 4, 5}), 2))
	want := []int{1, 2, 3}
	if !sliceEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDropLast_ZeroN(t *testing.T) {
	// n=0 should forward all items unchanged.
	got := collectAll(t, kitsune.DropLast(kitsune.FromSlice([]int{1, 2, 3}), 0))
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Errorf("got %v, want [1 2 3]", got)
	}
}

func TestDropLast_NGreaterThanLen(t *testing.T) {
	// n ≥ len → empty output.
	got := collectAll(t, kitsune.DropLast(kitsune.FromSlice([]int{1, 2}), 5))
	if len(got) != 0 {
		t.Errorf("got %v, want []", got)
	}
}

func TestDropLast_ExactN(t *testing.T) {
	// n == len → empty output.
	got := collectAll(t, kitsune.DropLast(kitsune.FromSlice([]int{1, 2, 3}), 3))
	if len(got) != 0 {
		t.Errorf("got %v, want []", got)
	}
}

func TestDropLast_One(t *testing.T) {
	// n=1 should drop only the last item.
	got := collectAll(t, kitsune.DropLast(kitsune.FromSlice([]int{10, 20, 30}), 1))
	if !sliceEqual(got, []int{10, 20}) {
		t.Errorf("got %v, want [10 20]", got)
	}
}

func sliceEqual[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
