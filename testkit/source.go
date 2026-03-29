package testkit

import (
	"context"
	"sync/atomic"
	"time"
)

// FailAt returns a Map function that returns err for items at the specified
// 0-based positions (counted from the first invocation). All other items pass
// through unchanged. The counter uses an atomic so the function is safe for
// concurrent [kitsune.Concurrency] stages.
//
//	fn := testkit.FailAt[int](boom, 1, 3) // fail 2nd and 4th items
//	p := kitsune.Map(kitsune.FromSlice(items), fn, kitsune.OnError(kitsune.Skip()))
func FailAt[T any](err error, positions ...int) func(context.Context, T) (T, error) {
	set := make(map[int]struct{}, len(positions))
	for _, p := range positions {
		set[p] = struct{}{}
	}
	var counter atomic.Int64
	return func(_ context.Context, v T) (T, error) {
		idx := int(counter.Add(1) - 1)
		if _, fail := set[idx]; fail {
			var zero T
			return zero, err
		}
		return v, nil
	}
}

// FailEvery returns a Map function that returns err for every nth item
// (0-indexed: positions 0, n, 2n, …). All other items pass through unchanged.
// The counter uses an atomic so the function is safe for concurrent stages.
//
//	fn := testkit.FailEvery[int](boom, 3) // fail items 0, 3, 6, …
//	p := kitsune.Map(kitsune.FromSlice(items), fn, kitsune.OnError(kitsune.Skip()))
func FailEvery[T any](err error, n int) func(context.Context, T) (T, error) {
	if n <= 0 {
		panic("testkit.FailEvery: n must be > 0")
	}
	var counter atomic.Int64
	return func(_ context.Context, v T) (T, error) {
		idx := int(counter.Add(1) - 1)
		if idx%n == 0 {
			var zero T
			return zero, err
		}
		return v, nil
	}
}

// SlowMap returns a Map function that sleeps for d before returning the item
// unchanged. Use this to simulate I/O-bound work in tests and benchmarks.
//
//	p := kitsune.Map(src, testkit.SlowMap[int](10*time.Millisecond), kitsune.Concurrency(4))
func SlowMap[T any](d time.Duration) func(context.Context, T) (T, error) {
	return func(ctx context.Context, v T) (T, error) {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
		return v, nil
	}
}

// SlowSink returns a ForEach function that sleeps for d per item. Use this to
// simulate a slow consumer in backpressure tests.
//
//	p.ForEach(testkit.SlowSink[int](5*time.Millisecond)).Run(ctx)
func SlowSink[T any](d time.Duration) func(context.Context, T) error {
	return func(ctx context.Context, _ T) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
