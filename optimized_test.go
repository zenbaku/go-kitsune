package kitsune_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func noopFn(_ context.Context, v int) (int, error)    { return v, nil }
func noopPred(_ context.Context, _ int) (bool, error) { return true, nil }

// allFastPath asserts that every fast-path-capable stage in reports is on the
// fast path and returns the failing report (if any) for test output.
func allFastPath(t *testing.T, reports []kitsune.OptimizationReport) {
	t.Helper()
	for _, r := range reports {
		if r.SupportsFastPath && !r.FastPath {
			t.Errorf("stage %q (%s): expected FastPath=true, reasons: %v", r.Name, r.Kind, r.Reasons)
		}
	}
}

// allFused asserts that every fast-path-capable stage in reports is fused.
func allFused(t *testing.T, reports []kitsune.OptimizationReport) {
	t.Helper()
	for _, r := range reports {
		if r.SupportsFastPath && !r.Fused {
			t.Errorf("stage %q (%s): expected Fused=true, reasons: %v", r.Name, r.Kind, r.Reasons)
		}
	}
}

// ---------------------------------------------------------------------------
// Fast-path eligibility
// ---------------------------------------------------------------------------

func TestIsOptimized_DefaultConfig_FastPath(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, noopFn)
	_ = mapped.ForEach(func(_ context.Context, _ int) error { return nil })

	reports := mapped.IsOptimized()
	allFastPath(t, reports)
}

func TestIsOptimized_MapFilter_FastPath(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, noopFn)
	filtered := kitsune.Filter(mapped, noopPred)
	_ = filtered.ForEach(func(_ context.Context, _ int) error { return nil })

	reports := filtered.IsOptimized()
	allFastPath(t, reports)
}

func TestIsOptimized_MapFilter_Fused(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, noopFn)
	filtered := kitsune.Filter(mapped, noopPred)
	_ = filtered.ForEach(func(_ context.Context, _ int) error { return nil })

	reports := filtered.IsOptimized()
	allFused(t, reports)
}

func TestIsOptimized_Concurrency_DisablesFastPath(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, noopFn, kitsune.Concurrency(2))
	_ = mapped.ForEach(func(_ context.Context, _ int) error { return nil })

	reports := mapped.IsOptimized()
	for _, r := range reports {
		if r.Kind == "map" {
			if r.FastPath {
				t.Error("expected FastPath=false with Concurrency(2)")
			}
			found := false
			for _, reason := range r.Reasons {
				if containsStr(reason, "Concurrency") {
					found = true
				}
			}
			if !found {
				t.Errorf("expected reason mentioning Concurrency, got %v", r.Reasons)
			}
		}
	}
}

func TestIsOptimized_OnError_DisablesFastPath(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, noopFn, kitsune.OnError(kitsune.ActionDrop()))
	_ = mapped.ForEach(func(_ context.Context, _ int) error { return nil })

	reports := mapped.IsOptimized()
	for _, r := range reports {
		if r.Kind == "map" {
			if r.FastPath {
				t.Error("expected FastPath=false with OnError")
			}
			found := false
			for _, reason := range r.Reasons {
				if containsStr(reason, "OnError") {
					found = true
				}
			}
			if !found {
				t.Errorf("expected reason mentioning OnError, got %v", r.Reasons)
			}
		}
	}
}

func TestIsOptimized_Timeout_DisablesFastPath(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, noopFn, kitsune.Timeout(100*time.Millisecond))
	_ = mapped.ForEach(func(_ context.Context, _ int) error { return nil })

	reports := mapped.IsOptimized()
	for _, r := range reports {
		if r.Kind == "map" {
			if r.FastPath {
				t.Error("expected FastPath=false with Timeout")
			}
			found := false
			for _, reason := range r.Reasons {
				if containsStr(reason, "Timeout") {
					found = true
				}
			}
			if !found {
				t.Errorf("expected reason mentioning Timeout, got %v", r.Reasons)
			}
		}
	}
}

func TestIsOptimized_DropNewest_DisablesFastPath(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, noopFn, kitsune.Overflow(kitsune.DropNewest))
	_ = mapped.ForEach(func(_ context.Context, _ int) error { return nil })

	reports := mapped.IsOptimized()
	for _, r := range reports {
		if r.Kind == "map" {
			if r.FastPath {
				t.Error("expected FastPath=false with DropNewest")
			}
			found := false
			for _, reason := range r.Reasons {
				if containsStr(reason, "DropNewest") {
					found = true
				}
			}
			if !found {
				t.Errorf("expected reason mentioning DropNewest, got %v", r.Reasons)
			}
		}
	}
}

func TestIsOptimized_Supervise_DisablesFastPath(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, noopFn, kitsune.Supervise(kitsune.RestartOnError(3, nil)))
	_ = mapped.ForEach(func(_ context.Context, _ int) error { return nil })

	reports := mapped.IsOptimized()
	for _, r := range reports {
		if r.Kind == "map" {
			if r.FastPath {
				t.Error("expected FastPath=false with Supervise")
			}
			found := false
			for _, reason := range r.Reasons {
				if containsStr(reason, "Supervise") {
					found = true
				}
			}
			if !found {
				t.Errorf("expected reason mentioning Supervise, got %v", r.Reasons)
			}
		}
	}
}

func TestIsOptimized_WithHook_DisablesFastPath(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, noopFn)
	_ = mapped.ForEach(func(_ context.Context, _ int) error { return nil })

	// WithHook at run time should disable fast path.
	reports := mapped.IsOptimized(kitsune.WithHook(kitsune.LogHook(slog.Default())))
	for _, r := range reports {
		if r.Kind == "map" {
			if r.FastPath {
				t.Error("expected FastPath=false with WithHook")
			}
			found := false
			for _, reason := range r.Reasons {
				if containsStr(reason, "WithHook") {
					found = true
				}
			}
			if !found {
				t.Errorf("expected reason mentioning WithHook, got %v", r.Reasons)
			}
		}
	}
}

func TestIsOptimized_WithErrorStrategy_DisablesFastPath(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, noopFn)
	_ = mapped.ForEach(func(_ context.Context, _ int) error { return nil })

	reports := mapped.IsOptimized(kitsune.WithErrorStrategy(kitsune.ActionDrop()))
	for _, r := range reports {
		if r.Kind == "map" {
			if r.FastPath {
				t.Error("expected FastPath=false with WithErrorStrategy")
			}
			found := false
			for _, reason := range r.Reasons {
				if containsStr(reason, "WithErrorStrategy") {
					found = true
				}
			}
			if !found {
				t.Errorf("expected reason mentioning WithErrorStrategy, got %v", r.Reasons)
			}
		}
	}
}

func TestIsOptimized_CacheBy_DisablesFastPath(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, noopFn,
		kitsune.CacheBy(func(v int) string { return fmt.Sprint(v) }),
	)
	_ = mapped.ForEach(func(_ context.Context, _ int) error { return nil })

	reports := mapped.IsOptimized()
	for _, r := range reports {
		if r.Kind == "map" {
			if r.FastPath {
				t.Error("expected FastPath=false with CacheBy")
			}
			found := false
			for _, reason := range r.Reasons {
				if containsStr(reason, "CacheBy") || containsStr(reason, "OnError") {
					found = true
				}
			}
			if !found {
				t.Errorf("expected reason mentioning CacheBy or OnError, got %v", r.Reasons)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Fusion
// ---------------------------------------------------------------------------

func TestIsOptimized_FanOut_DisablesFusion(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, noopFn)

	// Two direct ForEach consumers of the same pipeline increment
	// mapped.consumerCount to 2, which prevents fusion.
	r1 := mapped.ForEach(func(_ context.Context, _ int) error { return nil }).Build()
	r2 := mapped.ForEach(func(_ context.Context, _ int) error { return nil }).Build()
	_, _ = kitsune.MergeRunners(r1, r2)

	reports := mapped.IsOptimized()
	for _, r := range reports {
		if r.Kind == "map" {
			if r.Fused {
				t.Error("expected Fused=false with fan-out (2 consumers)")
			}
			found := false
			for _, reason := range r.Reasons {
				if containsStr(reason, "fan-out") || containsStr(reason, "consumer") {
					found = true
				}
			}
			if !found {
				t.Errorf("expected reason mentioning fan-out/consumers, got %v", r.Reasons)
			}
		}
	}
}

func TestIsOptimized_NoConsumer_NotFused(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	// No ForEach attached — consumerCount == 0.
	mapped := kitsune.Map(src, noopFn)

	reports := mapped.IsOptimized()
	for _, r := range reports {
		if r.Kind == "map" && r.Fused {
			t.Error("expected Fused=false when no consumer is attached")
		}
	}
}

// ---------------------------------------------------------------------------
// IsFastPath
// ---------------------------------------------------------------------------

func TestIsFastPath_DefaultPipeline(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, noopFn)
	_ = mapped.ForEach(func(_ context.Context, _ int) error { return nil })

	if !mapped.IsFastPath() {
		t.Error("expected IsFastPath() == true for default pipeline")
	}
}

func TestIsFastPath_WithConcurrency(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, noopFn, kitsune.Concurrency(2))
	_ = mapped.ForEach(func(_ context.Context, _ int) error { return nil })

	if mapped.IsFastPath() {
		t.Error("expected IsFastPath() == false with Concurrency(2)")
	}
}

func TestIsFastPath_SourceAloneIsTrue(t *testing.T) {
	// A source pipeline has no fast-path-capable stages; IsFastPath returns true
	// because there are no failing stages (sources are not checked).
	src := kitsune.FromSlice([]int{1, 2, 3})
	if !src.IsFastPath() {
		t.Error("expected IsFastPath() == true for source-only pipeline (no fast-path stages to fail)")
	}
}

// ---------------------------------------------------------------------------
// Report completeness
// ---------------------------------------------------------------------------

func TestIsOptimized_ReportsAllStages(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, noopFn)
	filtered := kitsune.Filter(mapped, noopPred)
	_ = filtered.ForEach(func(_ context.Context, _ int) error { return nil })

	reports := filtered.IsOptimized()
	// Expect: source + map + filter = 3 reports
	if len(reports) != 3 {
		t.Errorf("expected 3 reports (source, map, filter), got %d: %v",
			len(reports), reportsKinds(reports))
	}
}

func TestIsOptimized_SourceNotFastPathCapable(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	_ = src.ForEach(func(_ context.Context, _ int) error { return nil })

	reports := src.IsOptimized()
	for _, r := range reports {
		if r.Kind == "source" && r.SupportsFastPath {
			t.Errorf("source stage should not support fast path: %+v", r)
		}
	}
}

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

func containsStr(s, sub string) bool {
	return strings.Contains(s, sub)
}

func reportsKinds(reports []kitsune.OptimizationReport) []string {
	kinds := make([]string, len(reports))
	for i, r := range reports {
		kinds[i] = r.Kind
	}
	return kinds
}
