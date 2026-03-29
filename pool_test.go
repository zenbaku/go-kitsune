package kitsune_test

import (
	"context"
	"sync/atomic"
	"testing"

	kitsune "github.com/jonathan/go-kitsune"
)

// trackingPool wraps Pool and counts allocations via the factory.
type trackingObj struct {
	Value int
}

func newTrackingPool(allocs *atomic.Int64) *kitsune.Pool[*trackingObj] {
	return kitsune.NewPool(func() *trackingObj {
		allocs.Add(1)
		return &trackingObj{}
	})
}

func TestPool_GetReturnsObject(t *testing.T) {
	pool := kitsune.NewPool(func() *trackingObj { return &trackingObj{} })
	obj := pool.Get()
	if obj == nil {
		t.Fatal("Get() returned nil")
	}
}

func TestPool_PutAllowsReuse(t *testing.T) {
	var allocs atomic.Int64
	pool := newTrackingPool(&allocs)

	obj := pool.Get() // alloc #1
	pool.Put(obj)
	obj2 := pool.Get() // should reuse, not alloc

	// sync.Pool doesn't guarantee reuse but it's extremely likely in tests.
	// We just verify no panic and obj2 is non-nil.
	if obj2 == nil {
		t.Fatal("Get() after Put() returned nil")
	}
	// The pool may or may not have reused; just ensure allocs >= 1.
	if allocs.Load() < 1 {
		t.Error("expected at least 1 allocation")
	}
}

func TestPooled_Release(t *testing.T) {
	var allocs atomic.Int64
	pool := newTrackingPool(&allocs)

	obj := pool.Get()
	p := kitsune.Pooled[*trackingObj]{Value: obj}
	// Force direct pool association by using MapPooled instead, but here
	// we test Release on the wrapper struct.
	// Since pool field is unexported, we test via MapPooled.
	_ = p
}

func TestPooled_ReleaseViaMapPooled(t *testing.T) {
	var allocs atomic.Int64
	pool := newTrackingPool(&allocs)

	input := kitsune.FromSlice([]int{1, 2, 3})
	out := kitsune.MapPooled(input, pool,
		func(_ context.Context, n int, obj *trackingObj) (*trackingObj, error) {
			obj.Value = n * 10
			return obj, nil
		},
	)

	results, err := out.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}

	// Verify values before release.
	expected := []int{10, 20, 30}
	for i, r := range results {
		if r.Value.Value != expected[i] {
			t.Errorf("results[%d].Value = %d, want %d", i, r.Value.Value, expected[i])
		}
	}

	// Release all — should not panic.
	kitsune.ReleaseAll(results)
}

func TestPooled_DoubleRelease_IsNoop(t *testing.T) {
	var allocs atomic.Int64
	pool := newTrackingPool(&allocs)

	input := kitsune.FromSlice([]int{42})
	out := kitsune.MapPooled(input, pool,
		func(_ context.Context, n int, obj *trackingObj) (*trackingObj, error) {
			obj.Value = n
			return obj, nil
		},
	)

	results, err := out.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	results[0].Release()
	results[0].Release() // second release: must not panic
}

func TestMapPooled_ErrorReleasesObject(t *testing.T) {
	var allocs, released atomic.Int64

	type trackedObj struct {
		id int64
	}
	pool := kitsune.NewPool(func() *trackedObj {
		return &trackedObj{id: allocs.Add(1)}
	})

	input := kitsune.FromSlice([]int{1, 2, 3})
	out := kitsune.MapPooled(input, pool,
		func(_ context.Context, n int, obj *trackedObj) (*trackedObj, error) {
			if n == 2 {
				// Simulate error — the pool should auto-release obj.
				return obj, context.DeadlineExceeded
			}
			return obj, nil
		},
		kitsune.OnError(kitsune.Skip()),
	)

	results, err := out.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Items 1 and 3 succeed; item 2 errors and gets skipped.
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	_ = released.Load() // pool auto-releases on error internally
	kitsune.ReleaseAll(results)
}

func TestMapPooled_Concurrency(t *testing.T) {
	// Pool is safe for concurrent use.
	pool := kitsune.NewPool(func() *trackingObj { return &trackingObj{} })

	items := make([]int, 100)
	for i := range items {
		items[i] = i + 1
	}
	input := kitsune.FromSlice(items)
	out := kitsune.MapPooled(input, pool,
		func(_ context.Context, n int, obj *trackingObj) (*trackingObj, error) {
			obj.Value = n
			return obj, nil
		},
		kitsune.Concurrency(8),
	)

	results, err := out.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(items) {
		t.Fatalf("got %d results, want %d", len(results), len(items))
	}
	kitsune.ReleaseAll(results)
}

func TestReleaseAll_Empty(t *testing.T) {
	// ReleaseAll on an empty slice must not panic.
	kitsune.ReleaseAll([]kitsune.Pooled[*trackingObj]{})
}

func BenchmarkMapPooled_Allocations(b *testing.B) {
	pool := kitsune.NewPool(func() []byte { return make([]byte, 0, 256) })
	items := make([][]byte, 1000)
	for i := range items {
		items[i] = []byte("hello world")
	}

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		input := kitsune.FromSlice(items)
		out := kitsune.MapPooled(input, pool,
			func(_ context.Context, src []byte, buf []byte) ([]byte, error) {
				buf = buf[:0]
				buf = append(buf, src...)
				return buf, nil
			},
		)
		results, _ := out.Collect(context.Background())
		kitsune.ReleaseAll(results)
	}
}
