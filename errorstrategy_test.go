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
