package kitsune_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zenbaku/go-kitsune"
)

// TestRunSummary_SuccessNoEffects verifies that a pipeline with no Effect
// stages and no fatal error returns RunSuccess and a populated Duration.
func TestRunSummary_SuccessNoEffects(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1, 2, 3})
	runner := kitsune.Map(src, func(_ context.Context, v int) (int, error) { return v * 2, nil }).
		ForEach(func(_ context.Context, _ int) error { return nil })

	summary, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if summary.Outcome != kitsune.RunSuccess {
		t.Errorf("Outcome=%v, want RunSuccess", summary.Outcome)
	}
	if summary.Err != nil {
		t.Errorf("summary.Err=%v, want nil", summary.Err)
	}
	if summary.Duration <= 0 {
		t.Errorf("Duration=%v, want > 0", summary.Duration)
	}
	if summary.CompletedAt.IsZero() {
		t.Errorf("CompletedAt is zero")
	}
}

// TestRunSummary_RequiredEffectFailureFails verifies that a required Effect
// with terminal failures yields RunFailure.
func TestRunSummary_RequiredEffectFailureFails(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1, 2})
	fn := func(_ context.Context, _ int) (int, error) { return 0, errors.New("boom") }
	p := kitsune.Effect(src, fn) // default Required
	runner := p.ForEach(func(_ context.Context, _ kitsune.EffectOutcome[int, int]) error { return nil })

	summary, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected pipeline err: %v", err)
	}
	if summary.Outcome != kitsune.RunFailure {
		t.Errorf("Outcome=%v, want RunFailure", summary.Outcome)
	}
}

// TestRunSummary_BestEffortFailureYieldsPartial verifies that a best-effort
// Effect with terminal failures yields RunPartialSuccess when no required
// Effect failed.
func TestRunSummary_BestEffortFailureYieldsPartial(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1, 2})
	fn := func(_ context.Context, _ int) (int, error) { return 0, errors.New("boom") }
	p := kitsune.Effect(src, fn, kitsune.BestEffort())
	runner := p.ForEach(func(_ context.Context, _ kitsune.EffectOutcome[int, int]) error { return nil })

	summary, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected pipeline err: %v", err)
	}
	if summary.Outcome != kitsune.RunPartialSuccess {
		t.Errorf("Outcome=%v, want RunPartialSuccess", summary.Outcome)
	}
}

// TestRunSummary_DryRunIsClean verifies that a dry-run of a normally-failing
// Effect still yields RunSuccess (no fn was called, no failures recorded).
func TestRunSummary_DryRunIsClean(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1, 2})
	fn := func(_ context.Context, _ int) (int, error) { return 0, errors.New("would-fail") }
	p := kitsune.Effect(src, fn) // Required
	runner := p.ForEach(func(_ context.Context, _ kitsune.EffectOutcome[int, int]) error { return nil })

	summary, err := runner.Run(ctx, kitsune.DryRun())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if summary.Outcome != kitsune.RunSuccess {
		t.Errorf("DryRun outcome=%v, want RunSuccess", summary.Outcome)
	}
}

// TestWithFinalizer_RunsInOrder verifies that multiple finalizers run in
// registration order and each receives the same summary.
func TestWithFinalizer_RunsInOrder(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1})
	var order []string

	runner := src.ForEach(func(_ context.Context, _ int) error { return nil }).
		WithFinalizer(func(_ context.Context, s kitsune.RunSummary) error {
			order = append(order, "first")
			if s.Outcome != kitsune.RunSuccess {
				t.Errorf("finalizer 1: Outcome=%v, want RunSuccess", s.Outcome)
			}
			return nil
		}).
		WithFinalizer(func(_ context.Context, _ kitsune.RunSummary) error {
			order = append(order, "second")
			return nil
		})

	summary, err := runner.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Errorf("finalizer order=%v, want [first second]", order)
	}
	if len(summary.FinalizerErrs) != 2 ||
		summary.FinalizerErrs[0] != nil || summary.FinalizerErrs[1] != nil {
		t.Errorf("FinalizerErrs=%v, want [nil nil]", summary.FinalizerErrs)
	}
}

// TestWithFinalizer_ErrorRecordedDoesNotChangeOutcome verifies that a
// finalizer error is captured but does not change the run's outcome.
func TestWithFinalizer_ErrorRecordedDoesNotChangeOutcome(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1})
	finalizerErr := errors.New("finalizer-failed")
	runner := src.ForEach(func(_ context.Context, _ int) error { return nil }).
		WithFinalizer(func(_ context.Context, _ kitsune.RunSummary) error {
			return finalizerErr
		})

	summary, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("pipeline err=%v, want nil", err)
	}
	if summary.Outcome != kitsune.RunSuccess {
		t.Errorf("Outcome=%v, want RunSuccess", summary.Outcome)
	}
	if len(summary.FinalizerErrs) != 1 || !errors.Is(summary.FinalizerErrs[0], finalizerErr) {
		t.Errorf("FinalizerErrs=%v, want [%v]", summary.FinalizerErrs, finalizerErr)
	}
}

// TestRunHandle_WaitReturnsSummary verifies that RunAsync's RunHandle.Wait
// returns the same summary that synchronous Run would have returned.
func TestRunHandle_WaitReturnsSummary(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1, 2, 3})
	runner := src.ForEach(func(_ context.Context, _ int) error { return nil })

	handle := runner.RunAsync(ctx)
	summary, err := handle.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Outcome != kitsune.RunSuccess {
		t.Errorf("Outcome=%v, want RunSuccess", summary.Outcome)
	}
	if summary.Duration <= 0 {
		t.Errorf("Duration=%v, want > 0", summary.Duration)
	}
}

// TestRunHandle_SummaryAccessor verifies that Summary() after completion
// returns the real summary; before completion it returns the zero value.
func TestRunHandle_SummaryAccessor(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1})
	runner := src.ForEach(func(_ context.Context, _ int) error { return nil })

	handle := runner.RunAsync(ctx)
	// Block until done to avoid racing on Summary().
	_, _ = handle.Wait()
	s := handle.Summary()
	if s.Outcome != kitsune.RunSuccess {
		t.Errorf("post-completion Summary().Outcome=%v, want RunSuccess", s.Outcome)
	}
}

// TestMergeRunners_FinalizersCombined verifies that finalizers attached to
// each individual runner run after MergeRunners, in input order, followed by
// any finalizer attached to the merged runner.
func TestMergeRunners_FinalizersCombined(t *testing.T) {
	ctx := context.Background()
	var order []string

	src := kitsune.FromSlice([]int{1, 2, 3})
	evens, odds := kitsune.Partition(src, func(v int) bool {
		return v%2 == 0
	})
	e := evens.ForEach(func(_ context.Context, _ int) error { return nil }).
		WithFinalizer(func(_ context.Context, _ kitsune.RunSummary) error {
			order = append(order, "evens")
			return nil
		})
	o := odds.ForEach(func(_ context.Context, _ int) error { return nil }).
		WithFinalizer(func(_ context.Context, _ kitsune.RunSummary) error {
			order = append(order, "odds")
			return nil
		})
	runner, err := kitsune.MergeRunners(e, o)
	if err != nil {
		t.Fatal(err)
	}
	runner.WithFinalizer(func(_ context.Context, _ kitsune.RunSummary) error {
		order = append(order, "merged")
		return nil
	})

	if _, err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(order) != 3 || order[0] != "evens" || order[1] != "odds" || order[2] != "merged" {
		t.Errorf("finalizer order=%v, want [evens odds merged]", order)
	}
}
