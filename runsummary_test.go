package kitsune

import (
	"errors"
	"testing"
)

// TestDeriveRunOutcome_NoEffects verifies that a run with no Effect stages
// yields RunSuccess on a clean exit and RunFailure on a fatal error.
func TestDeriveRunOutcome_NoEffects(t *testing.T) {
	rc := newRunCtx()

	if got := deriveRunOutcome(rc, nil); got != RunSuccess {
		t.Errorf("clean exit: got %v, want RunSuccess", got)
	}
	if got := deriveRunOutcome(rc, errors.New("boom")); got != RunFailure {
		t.Errorf("fatal err: got %v, want RunFailure", got)
	}
}

// TestDeriveRunOutcome_RequiredFailureBeatsClean verifies that a required
// Effect failure produces RunFailure even when the pipeline error is nil.
func TestDeriveRunOutcome_RequiredFailureBeatsClean(t *testing.T) {
	rc := newRunCtx()
	rc.registerEffectStat(1, "publish", true)
	rc.effectStats[1].failure.Add(1)

	if got := deriveRunOutcome(rc, nil); got != RunFailure {
		t.Errorf("required failure: got %v, want RunFailure", got)
	}
}

// TestDeriveRunOutcome_BestEffortYieldsPartial verifies that a best-effort
// Effect failure (with all required Effects clean) yields RunPartialSuccess.
func TestDeriveRunOutcome_BestEffortYieldsPartial(t *testing.T) {
	rc := newRunCtx()
	rc.registerEffectStat(1, "audit", false) // best-effort
	rc.effectStats[1].failure.Add(1)
	rc.registerEffectStat(2, "publish", true) // required, clean
	rc.effectStats[2].success.Add(3)

	if got := deriveRunOutcome(rc, nil); got != RunPartialSuccess {
		t.Errorf("best-effort failure: got %v, want RunPartialSuccess", got)
	}
}

// TestDeriveRunOutcome_RequiredOverridesBestEffort verifies that a required
// failure plus best-effort failure still yields RunFailure (severity wins).
func TestDeriveRunOutcome_RequiredOverridesBestEffort(t *testing.T) {
	rc := newRunCtx()
	rc.registerEffectStat(1, "audit", false)
	rc.effectStats[1].failure.Add(1)
	rc.registerEffectStat(2, "publish", true)
	rc.effectStats[2].failure.Add(1)

	if got := deriveRunOutcome(rc, nil); got != RunFailure {
		t.Errorf("required + best-effort: got %v, want RunFailure", got)
	}
}

// TestDeriveRunOutcome_PipelineErrBeatsEverything verifies that a fatal
// pipeline error produces RunFailure regardless of Effect stats.
func TestDeriveRunOutcome_PipelineErrBeatsEverything(t *testing.T) {
	rc := newRunCtx()
	rc.registerEffectStat(1, "publish", true)
	rc.effectStats[1].success.Add(5) // all clean

	if got := deriveRunOutcome(rc, errors.New("ctx cancelled")); got != RunFailure {
		t.Errorf("pipeline err override: got %v, want RunFailure", got)
	}
}

// TestRunOutcome_String verifies the stable string form for each outcome.
func TestRunOutcome_String(t *testing.T) {
	cases := map[RunOutcome]string{
		RunSuccess:        "RunSuccess",
		RunPartialSuccess: "RunPartialSuccess",
		RunFailure:        "RunFailure",
		RunOutcome(99):    "RunOutcome(99)",
	}
	for o, want := range cases {
		if got := o.String(); got != want {
			t.Errorf("(%d).String() = %q, want %q", int(o), got, want)
		}
	}
}
