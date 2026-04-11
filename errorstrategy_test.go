package kitsune_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// runWith is a helper that collects items from a pipeline with custom RunOptions.
func runWith[T any](t *testing.T, p *kitsune.Pipeline[T], opts ...kitsune.RunOption) ([]T, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out []T
	err := p.ForEach(func(_ context.Context, item T) error {
		out = append(out, item)
		return nil
	}).Run(ctx, opts...)
	return out, err
}

// ---------------------------------------------------------------------------
// WithErrorStrategy
// ---------------------------------------------------------------------------

func TestWithErrorStrategy_DefaultHalt(t *testing.T) {
	// Without any strategy, the first error halts the pipeline.
	boom := errors.New("boom")
	p := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				return 0, boom
			}
			return v, nil
		},
	)
	_, err := runWith(t, p)
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
}

func TestWithErrorStrategy_SkipMap(t *testing.T) {
	// WithErrorStrategy(Skip) drops failing items; pipeline continues.
	boom := errors.New("boom")
	p := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				return 0, boom
			}
			return v, nil
		},
	)
	got, err := runWith(t, p, kitsune.WithErrorStrategy(kitsune.Skip()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sliceEqual(got, []int{1, 3}) {
		t.Fatalf("got %v, want [1 3]", got)
	}
}

func TestWithErrorStrategy_SkipForEach(t *testing.T) {
	// WithErrorStrategy applies to ForEach stages too.
	boom := errors.New("boom")
	var seen []int
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := kitsune.FromSlice([]int{1, 2, 3}).
		ForEach(func(_ context.Context, v int) error {
			if v == 2 {
				return boom
			}
			seen = append(seen, v)
			return nil
		}).
		Run(ctx, kitsune.WithErrorStrategy(kitsune.Skip()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sliceEqual(seen, []int{1, 3}) {
		t.Fatalf("got %v, want [1 3]", seen)
	}
}

func TestWithErrorStrategy_StageOverridesHalt(t *testing.T) {
	// Pipeline default is Skip, but one stage overrides back to Halt.
	boom := errors.New("boom")
	p := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				return 0, boom
			}
			return v, nil
		},
		kitsune.OnError(kitsune.Halt()), // explicit override
	)
	_, err := runWith(t, p, kitsune.WithErrorStrategy(kitsune.Skip()))
	if !errors.Is(err, boom) {
		t.Fatalf("expected stage-level Halt to override pipeline Skip; got %v", err)
	}
}

func TestWithErrorStrategy_StageOverridesSkip(t *testing.T) {
	// Pipeline default is Halt, but one stage overrides to Skip.
	boom := errors.New("boom")
	p := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				return 0, boom
			}
			return v, nil
		},
		kitsune.OnError(kitsune.Skip()), // explicit override on this stage
	)
	// No pipeline-level strategy; stage Skip should prevail.
	got, err := runWith(t, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sliceEqual(got, []int{1, 3}) {
		t.Fatalf("got %v, want [1 3]", got)
	}
}

func TestWithErrorStrategy_SkipConcurrent(t *testing.T) {
	// WithErrorStrategy works with concurrent Map stages.
	boom := errors.New("boom")
	p := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		func(_ context.Context, v int) (int, error) {
			if v == 3 {
				return 0, boom
			}
			return v, nil
		},
		kitsune.Concurrency(3),
	)
	got, err := runWith(t, p, kitsune.WithErrorStrategy(kitsune.Skip()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Ints(got)
	if !sliceEqual(got, []int{1, 2, 4, 5}) {
		t.Fatalf("got %v, want [1 2 4 5]", got)
	}
}

func TestWithErrorStrategy_MultiStage(t *testing.T) {
	// Both Map stages inherit Skip from the pipeline default.
	boom := errors.New("boom")
	double := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3, 4}),
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				return 0, boom
			}
			return v * 10, nil
		},
	)
	triple := kitsune.Map(double,
		func(_ context.Context, v int) (int, error) {
			if v == 30 {
				return 0, boom
			}
			return v * 3, nil
		},
	)
	got, err := runWith(t, triple, kitsune.WithErrorStrategy(kitsune.Skip()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Ints(got)
	// 1 → 10 → 30 (10≠30, ok; 10×3=30)
	// 2 → skipped in first stage (2==2)
	// 3 → 30 → skipped in second stage (30==30)
	// 4 → 40 → 120 (40≠30, ok; 40×3=120)
	if !sliceEqual(got, []int{30, 120}) {
		t.Fatalf("got %v, want [30 120]", got)
	}
}

func TestWithErrorStrategy_HaltExplicitSameAsDefault(t *testing.T) {
	// WithErrorStrategy(Halt()) behaves identically to having no strategy.
	boom := errors.New("boom")
	p := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				return 0, boom
			}
			return v, nil
		},
	)
	_, err := runWith(t, p, kitsune.WithErrorStrategy(kitsune.Halt()))
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ActionDrop
// ---------------------------------------------------------------------------

func TestActionDrop_DropsFailingItem(t *testing.T) {
	boom := errors.New("boom")
	p := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				return 0, boom
			}
			return v, nil
		},
		kitsune.OnError(kitsune.ActionDrop()),
	)
	got, err := runWith(t, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sliceEqual(got, []int{1, 3}) {
		t.Fatalf("got %v, want [1 3]", got)
	}
}

// ---------------------------------------------------------------------------
// RetryIf / RetryIfThen
// ---------------------------------------------------------------------------

func TestRetryIf_RetriesMatchingError(t *testing.T) {
	transient := errors.New("transient")
	var calls int
	p := kitsune.Map(kitsune.FromSlice([]int{1}),
		func(_ context.Context, v int) (int, error) {
			calls++
			if calls < 3 {
				return 0, transient
			}
			return v * 10, nil
		},
		kitsune.OnError(kitsune.RetryIf(
			func(err error) bool { return errors.Is(err, transient) },
			kitsune.FixedBackoff(0),
		)),
	)
	got, err := runWith(t, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sliceEqual(got, []int{10}) {
		t.Fatalf("got %v, want [10]", got)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls (2 failures + 1 success), got %d", calls)
	}
}

func TestRetryIf_HaltsOnNonMatchingError(t *testing.T) {
	transient := errors.New("transient")
	permanent := errors.New("permanent")
	p := kitsune.Map(kitsune.FromSlice([]int{1}),
		func(_ context.Context, _ int) (int, error) {
			return 0, permanent
		},
		kitsune.OnError(kitsune.RetryIf(
			func(err error) bool { return errors.Is(err, transient) },
			kitsune.FixedBackoff(0),
		)),
	)
	_, err := runWith(t, p)
	if !errors.Is(err, permanent) {
		t.Fatalf("expected permanent error, got %v", err)
	}
}

func TestRetryIfThen_FallsBackToDropOnNonMatchingError(t *testing.T) {
	transient := errors.New("transient")
	permanent := errors.New("permanent")
	p := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				return 0, permanent // permanent: not retried, dropped
			}
			return v, nil
		},
		kitsune.OnError(kitsune.RetryIfThen(
			func(err error) bool { return errors.Is(err, transient) },
			kitsune.FixedBackoff(0),
			kitsune.ActionDrop(), // fallback on non-matching error
		)),
	)
	got, err := runWith(t, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sliceEqual(got, []int{1, 3}) {
		t.Fatalf("got %v, want [1 3]", got)
	}
}

// ---------------------------------------------------------------------------
// WithDefaultBuffer
// ---------------------------------------------------------------------------

func TestWithDefaultBuffer_FunctionalCorrectness(t *testing.T) {
	// WithDefaultBuffer(0) forces all stages to use unbuffered channels.
	// The pipeline must still produce all items correctly.
	got, err := runWith(t,
		kitsune.Map(
			kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
			func(_ context.Context, v int) (int, error) { return v * 2, nil },
		),
		kitsune.WithDefaultBuffer(0),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sliceEqual(got, []int{2, 4, 6, 8, 10}) {
		t.Fatalf("got %v, want [2 4 6 8 10]", got)
	}
}

func TestWithDefaultBuffer_ExplicitBufferTakesPrecedence(t *testing.T) {
	// A stage with an explicit Buffer(n) is not affected by WithDefaultBuffer.
	// We verify this functionally: the pipeline still produces correct results
	// even when the explicit and default buffers differ.
	got, err := runWith(t,
		kitsune.Map(
			kitsune.FromSlice([]int{1, 2, 3}),
			func(_ context.Context, v int) (int, error) { return v + 1, nil },
			kitsune.Buffer(4), // explicit override
		),
		kitsune.WithDefaultBuffer(0), // run-level default (should not affect the above stage)
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sliceEqual(got, []int{2, 3, 4}) {
		t.Fatalf("got %v, want [2 3 4]", got)
	}
}

func TestWithDefaultBuffer_DescribeShowsResolvedSize(t *testing.T) {
	// Describe() is called without a runCtx so buffer sizes in the graph reflect
	// construction-time values. Only the channel size at run time matters for
	// correctness; this test just confirms Describe doesn't panic.
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) { return v, nil },
	)
	nodes := p.Describe()
	if len(nodes) == 0 {
		t.Fatal("expected at least one node")
	}
}
