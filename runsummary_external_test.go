package kitsune_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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

// TestRunSummary_EffectStats_EmptyForNonEffectPipeline verifies that a
// pipeline with no Effect stages still produces a non-nil EffectStats map
// with zero entries. Always-allocate avoids a nil check at every call site.
func TestRunSummary_EffectStats_EmptyForNonEffectPipeline(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1, 2, 3})
	runner := kitsune.Map(src, func(_ context.Context, v int) (int, error) { return v, nil }).
		ForEach(func(_ context.Context, _ int) error { return nil })

	summary, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if summary.EffectStats == nil {
		t.Fatalf("EffectStats is nil; want allocated empty map")
	}
	if len(summary.EffectStats) != 0 {
		t.Errorf("len(EffectStats)=%d, want 0; got %+v", len(summary.EffectStats), summary.EffectStats)
	}
}

// TestRunSummary_EffectStats_PopulatedWithSplit verifies that EffectStats
// reflects both the Required flag and the per-stage success/failure counts.
// Each effect runs in its own pipeline so the assertions stay focused; the
// invariant under test (population from rc.effectStats) is identical.
func TestRunSummary_EffectStats_PopulatedWithSplit(t *testing.T) {
	ctx := context.Background()

	// Required effect: succeeds on 1, 2, 3; fails on 4.
	srcA := kitsune.FromSlice([]int{1, 2, 3, 4})
	requiredFn := func(_ context.Context, v int) (int, error) {
		if v == 4 {
			return 0, errors.New("boom")
		}
		return v * 10, nil
	}
	required := kitsune.Effect(srcA, requiredFn, kitsune.EffectStageOption(kitsune.WithName("required-effect")))
	summaryA, err := required.ForEach(func(_ context.Context, _ kitsune.EffectOutcome[int, int]) error { return nil }).
		Run(ctx)
	if err != nil {
		t.Fatalf("required pipeline run: %v", err)
	}
	if len(summaryA.EffectStats) != 1 {
		t.Fatalf("required: len(EffectStats)=%d, want 1; got %+v", len(summaryA.EffectStats), summaryA.EffectStats)
	}
	r, ok := summaryA.EffectStats["required-effect"]
	if !ok {
		t.Fatalf("required: missing required-effect entry; got: %+v", summaryA.EffectStats)
	}
	if !r.Required {
		t.Errorf("required-effect Required=false, want true")
	}
	if r.Success != 3 {
		t.Errorf("required-effect Success=%d, want 3", r.Success)
	}
	if r.Failure != 1 {
		t.Errorf("required-effect Failure=%d, want 1", r.Failure)
	}

	// Best-effort effect: succeeds on all 2 items, no failures.
	srcB := kitsune.FromSlice([]int{10, 20})
	bestEffortFn := func(_ context.Context, v int) (int, error) { return v + 1, nil }
	bestEffort := kitsune.Effect(srcB, bestEffortFn, kitsune.BestEffort(), kitsune.EffectStageOption(kitsune.WithName("besteffort-effect")))
	summaryB, err := bestEffort.ForEach(func(_ context.Context, _ kitsune.EffectOutcome[int, int]) error { return nil }).
		Run(ctx)
	if err != nil {
		t.Fatalf("besteffort pipeline run: %v", err)
	}
	if len(summaryB.EffectStats) != 1 {
		t.Fatalf("besteffort: len(EffectStats)=%d, want 1; got %+v", len(summaryB.EffectStats), summaryB.EffectStats)
	}
	be, ok := summaryB.EffectStats["besteffort-effect"]
	if !ok {
		t.Fatalf("besteffort: missing besteffort-effect entry; got: %+v", summaryB.EffectStats)
	}
	if be.Required {
		t.Errorf("besteffort-effect Required=true, want false")
	}
	if be.Success != 2 {
		t.Errorf("besteffort-effect Success=%d, want 2", be.Success)
	}
	if be.Failure != 0 {
		t.Errorf("besteffort-effect Failure=%d, want 0", be.Failure)
	}
}

// TestRunSummary_EffectStats_JSONRoundTrip verifies that EffectStats
// serializes to JSON with the documented field names and survives a
// round-trip without losing data.
func TestRunSummary_EffectStats_JSONRoundTrip(t *testing.T) {
	original := kitsune.RunSummary{
		EffectStats: map[string]kitsune.EffectStats{
			"required-effect":   {Required: true, Success: 3, Failure: 1},
			"besteffort-effect": {Required: false, Success: 2, Failure: 0},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got kitsune.RunSummary
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got.EffectStats, original.EffectStats) {
		t.Errorf("EffectStats mismatch after round-trip:\n  got  = %+v\n  want = %+v",
			got.EffectStats, original.EffectStats)
	}

	// Verify the documented JSON shape: "effect_stats" key, lowercase field
	// names "required" / "success" / "failure".
	var asMap map[string]interface{}
	if err := json.Unmarshal(data, &asMap); err != nil {
		t.Fatalf("Unmarshal as map: %v", err)
	}
	es, ok := asMap["effect_stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected effect_stats key in JSON; got: %s", data)
	}
	req, ok := es["required-effect"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected required-effect entry; got: %+v", es)
	}
	for _, want := range []string{"required", "success", "failure"} {
		if _, ok := req[want]; !ok {
			t.Errorf("missing JSON field %q in required-effect entry; got entry: %+v", want, req)
		}
	}
}
