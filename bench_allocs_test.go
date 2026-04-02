package kitsune_test

import (
	"context"
	"testing"

	kitsune "github.com/jonathan/go-kitsune"
)

// TestAllocBounds enforces per-pipeline allocation ceilings for the
// highest-traffic operators. Each subtest runs a full pipeline on 10,000 items
// and fails if the total allocation count regresses past the measured baseline
// plus a ~5% margin.
//
// Baselines measured on Apple M1, darwin/arm64, Go 1.26.1.
func TestAllocBounds(t *testing.T) {
	const n = 10_000
	items := makeItems(n)
	ctx := context.Background()

	t.Run("Map", func(t *testing.T) {
		allocs := testing.AllocsPerRun(3, func() {
			p := kitsune.FromSlice(items)
			kitsune.Map(p, func(_ context.Context, x int) (int, error) {
				return x * 2, nil
			}).Drain().Run(ctx)
		})
		const ceiling = 21_000 // baseline ~19,675
		if allocs > ceiling {
			t.Errorf("Map allocs/run = %.0f, want <= %d (regression from ~19,675 baseline)", allocs, ceiling)
		}
	})

	t.Run("FlatMap", func(t *testing.T) {
		// 1K items, each yielding 10 outputs = 10K outputs total.
		small := makeItems(n / 10)
		allocs := testing.AllocsPerRun(3, func() {
			p := kitsune.FromSlice(small)
			kitsune.FlatMap(p, func(_ context.Context, x int, yield func(int) error) error {
				for i := range 10 {
					if err := yield(x + i); err != nil {
						return err
					}
				}
				return nil
			}).Drain().Run(ctx)
		})
		const ceiling = 11_000 // baseline ~10,288
		if allocs > ceiling {
			t.Errorf("FlatMap allocs/run = %.0f, want <= %d (regression from ~10,288 baseline)", allocs, ceiling)
		}
	})

	t.Run("Batch", func(t *testing.T) {
		allocs := testing.AllocsPerRun(3, func() {
			p := kitsune.FromSlice(items)
			kitsune.Batch(p, 100).Drain().Run(ctx)
		})
		const ceiling = 10_500 // baseline ~10,003
		if allocs > ceiling {
			t.Errorf("Batch allocs/run = %.0f, want <= %d (regression from ~10,003 baseline)", allocs, ceiling)
		}
	})

	t.Run("RateLimit", func(t *testing.T) {
		// Rate 1e9/s with full burst — effectively unlimited — isolates
		// framework overhead rather than throttling behavior.
		allocs := testing.AllocsPerRun(3, func() {
			p := kitsune.FromSlice(items)
			kitsune.RateLimit(p, 1e9, kitsune.Burst(n)).Drain().Run(ctx)
		})
		const ceiling = 21_000 // baseline ~19,556
		if allocs > ceiling {
			t.Errorf("RateLimit allocs/run = %.0f, want <= %d (regression from ~19,556 baseline)", allocs, ceiling)
		}
	})

	t.Run("CircuitBreaker", func(t *testing.T) {
		// All items succeed — circuit stays closed, measuring closed-state
		// per-item overhead of the two-phase ref.Update state machine.
		allocs := testing.AllocsPerRun(3, func() {
			p := kitsune.FromSlice(items)
			kitsune.CircuitBreaker(p, func(_ context.Context, x int) (int, error) {
				return x * 2, nil
			}).Drain().Run(ctx)
		})
		const ceiling = 21_000 // baseline ~19,683
		if allocs > ceiling {
			t.Errorf("CircuitBreaker allocs/run = %.0f, want <= %d (regression from ~19,683 baseline)", allocs, ceiling)
		}
	})
}
