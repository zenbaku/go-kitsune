package kitsune

import (
	"context"
	"fmt"
	"time"
)

// RunOutcome classifies how a pipeline run ended. It is computed at the end
// of [Runner.Run] from [Effect] stage results and the pipeline-level error.
type RunOutcome int

const (
	// RunSuccess means the pipeline finished without a fatal error and every
	// [Effect] stage either produced no failures or has no failures attributed
	// to it (no effects in the graph also yields RunSuccess).
	RunSuccess RunOutcome = iota

	// RunPartialSuccess means the pipeline finished without a fatal error and
	// every required [Effect] succeeded, but at least one best-effort [Effect]
	// (configured with [BestEffort]) had terminal failures.
	RunPartialSuccess

	// RunFailure means the pipeline returned a fatal error, or at least one
	// required [Effect] (the default; or explicitly [Required]) had terminal
	// failures.
	RunFailure
)

// String returns a stable human-readable name for the outcome.
func (o RunOutcome) String() string {
	switch o {
	case RunSuccess:
		return "RunSuccess"
	case RunPartialSuccess:
		return "RunPartialSuccess"
	case RunFailure:
		return "RunFailure"
	default:
		return fmt.Sprintf("RunOutcome(%d)", int(o))
	}
}

// RunSummary is the structured result of one pipeline run. It is returned by
// [Runner.Run], [ForEachRunner.Run], [DrainRunner.Run], and [RunHandle.Wait].
//
// Outcome derives from per-[Effect]-stage success/failure counts together
// with the pipeline-level error: see the package documentation for the
// precise derivation rules.
//
// Err mirrors the second return value of Run; it is the first fatal error
// from the stage graph (or nil when the run completes cleanly). FinalizerErrs
// holds errors from finalizers attached via [Runner.WithFinalizer], in
// registration order, with nil entries for finalizers that returned nil.
// Finalizer errors do not change Outcome.
//
// Metrics is a point-in-time snapshot taken at the moment the pipeline
// finished. It is non-empty (carries Timestamp, Elapsed, and an empty Stages
// map) even when no [MetricsHook] is attached. When a MetricsHook is
// attached via [WithHook], Metrics is the hook's snapshot.
type RunSummary struct {
	Outcome       RunOutcome      `json:"outcome"`
	Err           error           `json:"-"`
	Metrics       MetricsSnapshot `json:"metrics"`
	Duration      time.Duration   `json:"duration_ns"`
	CompletedAt   time.Time       `json:"completed_at"`
	FinalizerErrs []error         `json:"-"`
}

// RunSummaryHook is an optional extension of [Hook]. If the hook passed to
// [WithHook] implements RunSummaryHook, [Runner.Run] calls OnRunComplete once
// at the end of the run, after the [RunSummary] is computed and after any
// finalizers attached via [Runner.WithFinalizer] have run. The summary
// passed to OnRunComplete is the same value Run returns to the caller.
//
// Hooks that observe per-item events implement [Hook]; hooks that observe
// the run as a whole implement RunSummaryHook. A single hook may implement
// both.
//
// Checked via type assertion; existing Hook implementations need not
// implement this.
type RunSummaryHook interface {
	OnRunComplete(ctx context.Context, summary RunSummary)
}

// deriveRunOutcome computes the run's outcome from the pipeline error and
// the per-Effect-stage counters in rc. It is pure: it does not mutate rc.
//
// Rules:
//   - If pipelineErr is non-nil, return RunFailure (the error overrides
//     Effect-level analysis: a stage-graph error means the pipeline itself
//     failed, regardless of what Effects did before that point).
//   - Else if any required-Effect stage had at least one terminal failure,
//     return RunFailure.
//   - Else if any best-effort-Effect stage had at least one terminal
//     failure, return RunPartialSuccess.
//   - Else return RunSuccess.
//
// A pipeline with no Effect stages at all yields RunSuccess on a clean exit
// and RunFailure on a stage-graph error.
func deriveRunOutcome(rc *runCtx, pipelineErr error) RunOutcome {
	if pipelineErr != nil {
		return RunFailure
	}
	sawBestEffortFailure := false
	for _, s := range rc.effectStats {
		if s.failure.Load() == 0 {
			continue
		}
		if s.required {
			return RunFailure
		}
		sawBestEffortFailure = true
	}
	if sawBestEffortFailure {
		return RunPartialSuccess
	}
	return RunSuccess
}
