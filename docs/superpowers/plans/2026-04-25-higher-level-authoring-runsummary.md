# Higher-Level Authoring Layer — M3 (RunSummary + WithFinalizer) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade `Runner.Run` (and `RunHandle.Wait`, `ForEachRunner.Run`, `DrainRunner.Run`) to return a `(RunSummary, error)` tuple, where `RunSummary` carries the run's outcome class (success / partial / failure) derived from `Effect` stages, plus duration, completion timestamp, and a metrics snapshot. Add `WithFinalizer(fn)` on `*Runner` and `*ForEachRunner[T]` for post-run callbacks that observe the summary.

**Architecture:** A new `runsummary.go` defines `RunOutcome` (`RunSuccess` / `RunPartialSuccess` / `RunFailure`) and `RunSummary`. Per-effect-stage success/failure counts live in `runCtx.effectStats` (an `int64`-based atomic map keyed by stage ID), populated by `Effect.build` and incremented inside the per-item loop. After the stage graph finishes, `Runner.Run` walks `effectStats` plus the pipeline error to derive `RunOutcome`, builds the `RunSummary`, runs registered finalizers in order, captures any finalizer errors, and returns the summary. The signature change is hard-breaking; pre-1.0 with no external users so the cost is internal sweeps only.

**Tech Stack:** Go generics, `sync/atomic` counters, the existing `MetricsHook`/`MetricsSnapshot` infrastructure.

**Module path:** `github.com/zenbaku/go-kitsune`

---

## Spec source

This plan implements Section 3 of `docs/superpowers/specs/2026-04-15-higher-level-authoring-design.md` (approved 2026-04-15). M1 (Segment) and M2 (Effect) shipped in 2026-04-24/2026-04-25; M4 (DevStore) is out of scope here.

### Spec deltas applied during M3 design

These are pragmatic scoping decisions captured during plan-writing. They are the source of truth for the implementation; the spec text is superseded where they conflict.

1. **`RunSummary.FinalizerErrs []error`.** The spec says "A finalizer error is recorded in `RunSummary` but does not affect `RunOutcome`" without naming a field. Use a slice rather than a single `errors.Join`'d error: it lets callers iterate finalizers individually without having to pull a joined error apart, and matches the `WithFinalizer`-can-be-called-multiple-times surface. Length is the count of registered finalizers; entries are nil for finalizers that returned nil.

2. **`RunHandle.Wait` signature changes to `(RunSummary, error)`** in lockstep with `Runner.Run`. The spec only mentions `Runner.Run`; `RunHandle` was added post-spec. Keeping `Wait` returning just `error` while `Run` returns the tuple would be confusing. `Err()` (non-blocking) keeps its `error` shape. Add `Summary()` for non-blocking access to the summary (returns the zero value before completion).

3. **Effect operator does NOT call `hook.OnItem`** in this milestone. `RunOutcome` derivation uses `runCtx.effectStats` directly. As a consequence, `MetricsHook.Snapshot().Stages["my-effect"]` may not exist if no `OnItem` calls happen against that stage name. This is a known limitation of v1, called out in the operators.md docs. A follow-up can wire `Effect → hook.OnItem` so MetricsHook reflects effect counts; not in M3 to keep scope tight.

4. **`DrainRunner.Run` signature follows `Runner.Run`** (returns `(RunSummary, error)`). `DrainRunner` is deprecated but still callable. Updating its signature keeps the API consistent.

5. **`WithFinalizer` accumulates rather than replaces** when called multiple times. Calling order = registration order; finalizers run in registration order after the pipeline completes. Each finalizer receives the summary computed from the previous finalizer's outputs (i.e. the outcome / duration is fixed before the first finalizer runs; finalizers cannot mutate the outcome).

6. **`MergeRunners` collects finalizers from input runners.** If finalizers were attached to individual runners before merging, the merged Runner runs all of them in input order. If `WithFinalizer` is called on the merged Runner, those finalizers run after the collected ones.

---

## RunOutcome derivation rules

After stages exit and before finalizers run:

```
fatal pipeline err (Run's stage-graph error is non-nil and not a context cancellation suppressed by drain)
  → RunFailure

else any required-effect stage with effectStats.failure > 0
  → RunFailure

else any best-effort-effect stage with effectStats.failure > 0
  → RunPartialSuccess

else
  → RunSuccess
```

Context-cancelled-but-no-actual-error: if `runWithDrain` suppresses a `context.Canceled` to nil, the pipeline error is nil and outcome derivation proceeds normally. The summary's `Err` field follows the returned error.

Finalizer errors do NOT affect `RunOutcome`; they are recorded separately in `FinalizerErrs`.

---

## File structure

| File | Change |
|---|---|
| `runsummary.go` (new) | `RunOutcome` + `String()`; `RunSummary`; `deriveRunOutcome` |
| `pipeline.go` | Add `effectStats map[int64]*effectStat` to `runCtx`; `effectStat` struct; `runCtx.registerEffectStat`, `runCtx.recordEffectOutcome` helpers |
| `effect.go` | In `Effect.build`: register stage in `rc.effectStats`; in per-item loop: call `recordEffectOutcome` after each outcome |
| `kitsune.go` | `Runner` gains `finalizers []func(...) error`; `Runner.Run` signature changes to `(RunSummary, error)` and runs finalizers; `Runner.WithFinalizer` method; `Runner.RunAsync` keeps returning `*RunHandle` but the goroutine stores the summary; `MergeRunners` collects finalizers |
| `kitsune.go` (RunHandle) | `RunHandle` gains `summary RunSummary`; `Wait()` returns `(RunSummary, error)`; new `Summary() RunSummary` non-blocking accessor; `Err()` unchanged; `collect.go:672` caller updated |
| `terminal.go` | `ForEachRunner.Run` returns `(RunSummary, error)`; `ForEachRunner.WithFinalizer`; `DrainRunner.Run` and `DrainRunner.RunAsync` (still deprecated) follow the new signatures |
| `errorstrategy_test.go` | `runWith` helper updated to discard summary |
| All other `*_test.go` in main package | Sweep `err := …Run(ctx` and `…Run(ctx); err` patterns |
| `examples/*/main.go` | Sweep |
| `inspector/*` if any callers | Sweep |
| `tails/kkafka/kkafka_test.go` | Sweep one occurrence |
| `runsummary_test.go` (new) | Tests for derivation, finalizer ordering, finalizer-errors-don't-change-outcome, finalizer-error-recorded, MergeRunners-finalizer-merge |
| `doc/operators.md` | Add `RunSummary` / `RunOutcome` / `WithFinalizer` reference; note Effect docs caveat that MetricsHook does not yet reflect Effect outcomes |
| `doc/api-matrix.md` | New rows; update Run signatures throughout |
| `doc/getting-started.md` | Update Run examples |
| `doc/roadmap.md` | Mark M3 complete |
| `MEMORY.md` (user memory) | Update `project_higher_level_authoring.md` |

---

## Tasks

> Each task ends with a commit. Run `task test` after each task; run `task test:race` before the documentation task.

---

### Task 1: Define `RunOutcome` and `RunSummary`

Pure type definitions; no behaviour wired up yet.

**Files:**
- Create: `runsummary.go`

- [ ] **Step 1: Create `runsummary.go`**

```go
package kitsune

import (
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
```

- [ ] **Step 2: Build to verify**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add runsummary.go
git commit -m "feat(runsummary): add RunOutcome and RunSummary types"
```

---

### Task 2: Add `runCtx.effectStats` accounting

Adds the per-effect-stage atomic counters used by `RunOutcome` derivation. Effect operator wiring comes in Task 3.

**Files:**
- Modify: `pipeline.go`

- [ ] **Step 1: Add `effectStat` struct and `effectStats` field to `runCtx`**

In `pipeline.go`, locate `runCtx` (around line 114). After the `dryRun` field, add:

```go
type runCtx struct {
    // ... existing fields unchanged ...
    dryRun bool

    // effectStats accumulates per-Effect-stage outcome counts during a run.
    // Keyed by the Effect stage's ID. Populated by Effect.build via
    // registerEffectStat; incremented from the Effect goroutine via
    // recordEffectOutcome after each emitted outcome. Read by Runner.Run at
    // shutdown to derive RunOutcome.
    //
    // Map writes (registerEffectStat) all happen during the build phase,
    // before any stage goroutine starts. Counter increments
    // (recordEffectOutcome) use atomic operations and read the entry pointer
    // from a map already populated at build time, so no lock is needed.
    effectStats map[int64]*effectStat
}

// effectStat tracks success and terminal-failure counts for a single Effect
// stage during a run.
type effectStat struct {
    name     string       // stage name (cf. stageMeta.name); used for logging
    required bool         // mirrors stageMeta.effectRequired
    success  atomic.Int64
    failure  atomic.Int64
}
```

- [ ] **Step 2: Initialise the map in `newRunCtx`**

Modify `newRunCtx` (around line 172):

```go
func newRunCtx() *runCtx {
    done := make(chan struct{})
    var once sync.Once
    return &runCtx{
        chans:       make(map[int64]any),
        drainNotify: make(map[int64]*drainEntry),
        segmentByID: make(map[int64]string),
        effectStats: make(map[int64]*effectStat),
        refs:        newRefRegistry(),
        done:        done,
        signalDone:  func() { once.Do(func() { close(done) }) },
    }
}
```

- [ ] **Step 3: Add register/record helpers**

Append after `(rc *runCtx) signalDrain` (around line 244):

```go
// registerEffectStat creates and stores an effectStat entry for an Effect
// stage. Called from Effect.build during the build phase, before any stage
// goroutine starts. Safe without a lock because all build closures run
// sequentially in a single goroutine before Run starts the stage workers.
func (rc *runCtx) registerEffectStat(id int64, name string, required bool) {
    rc.effectStats[id] = &effectStat{name: name, required: required}
}

// recordEffectOutcome increments the success or failure counter for an
// Effect stage. Called from inside the Effect goroutine after each emitted
// outcome. Safe for concurrent use because the entry pointer is read from a
// map that was fully populated during the build phase.
func (rc *runCtx) recordEffectOutcome(id int64, applied bool) {
    s, ok := rc.effectStats[id]
    if !ok {
        return
    }
    if applied {
        s.success.Add(1)
    } else {
        s.failure.Add(1)
    }
}
```

- [ ] **Step 4: Build to verify**

Run: `go build ./...`
Expected: clean. (No callers yet; additive plumbing.)

- [ ] **Step 5: Commit**

```bash
git add pipeline.go
git commit -m "feat(runsummary): add per-effect-stage success/failure accounting in runCtx"
```

---

### Task 3: Wire `Effect` to record outcomes

The Effect operator now registers itself in `runCtx.effectStats` during build and increments after each emitted outcome.

**Files:**
- Modify: `effect.go`

- [ ] **Step 1: Add `registerEffectStat` call in `Effect.build`**

In `effect.go`, locate the Effect operator's `build` closure (around line 221). After `rc.initDrainNotify(id, out.consumerCount.Load())` (around line 234), add:

```go
rc.initDrainNotify(id, out.consumerCount.Load())
rc.registerEffectStat(id, meta.name, cfg.required)
drainCh := rc.drainCh(id)
```

(Insert the new line between `initDrainNotify` and `drainCh := rc.drainCh(id)`. The `meta.name` and `cfg.required` are already in scope.)

- [ ] **Step 2: Add `recordEffectOutcome` call after producing each outcome**

In the same file, in the per-item loop (around line 255-275), after the `outcome` is determined but before the inner select that sends it downstream, add the record call:

```go
case item, ok := <-inCh:
    if !ok {
        return nil
    }
    var outcome EffectOutcome[I, R]
    outcome.Input = item

    if !dryRun {
        outcome = runEffectAttempts(ctx, item, fn, localCfg)
    }
    rc.recordEffectOutcome(id, outcome.Applied)

    select {
    case ch <- outcome:
    // ... unchanged ...
    }
```

The `Applied` field is true on success and on dry-run-skipped (which the spec considers neither success nor failure for outcome derivation purposes — see note below). Re-check: dry-run sets `Applied: false`, so dry-run outcomes are counted as failures by `recordEffectOutcome`. This is wrong for the derivation logic.

**Correction:** dry-run mode should not contribute to either counter. Adjust:

```go
    if !dryRun {
        outcome = runEffectAttempts(ctx, item, fn, localCfg)
        rc.recordEffectOutcome(id, outcome.Applied)
    }
```

When dry-run is on, no fn was called; counting it as success or failure misrepresents the run. Skipping the call entirely yields 0 successes and 0 failures for that stage, which means `RunOutcome` becomes `RunSuccess` for a dry-run pipeline (assuming no fatal pipeline error). This is the correct semantic.

- [ ] **Step 3: Build to verify**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 4: Run the existing Effect tests**

Run: `go test -run TestEffect_ ./... -race`
Expected: PASS — the new accounting is invisible to existing tests (they don't check `effectStats`).

- [ ] **Step 5: Commit**

```bash
git add effect.go
git commit -m "feat(effect): record per-stage success/failure counts in runCtx"
```

---

### Task 4: Implement `deriveRunOutcome`

Pure function from `runCtx` state plus pipeline error to a `RunOutcome`. Lives in `runsummary.go` next to the type. Includes a small unit test.

**Files:**
- Modify: `runsummary.go`
- Create: `runsummary_test.go`

- [ ] **Step 1: Append `deriveRunOutcome` to `runsummary.go`**

```go
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
```

- [ ] **Step 2: Create `runsummary_test.go` with unit tests**

```go
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
```

Note: this test file is `package kitsune` (not `kitsune_test`) so it can reach the unexported `runCtx` and helpers. Existing internal tests in the package use the same convention; check `grep -l "^package kitsune$" *.go` if unsure.

- [ ] **Step 3: Run the new tests**

Run: `go test -run "TestDeriveRunOutcome_|TestRunOutcome_" ./...`
Expected: all PASS.

- [ ] **Step 4: Run the full short suite**

Run: `task test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add runsummary.go runsummary_test.go
git commit -m "feat(runsummary): add deriveRunOutcome with unit coverage"
```

---

### Task 5: Add finalizers to `*Runner` and change `Runner.Run` signature

This is the breaking change. After this commit the rest of the package will not compile until Tasks 6-8 sweep the callers. Be ready to do them in sequence; do not push between Task 5 and the sweep tasks.

**Files:**
- Modify: `kitsune.go`

- [ ] **Step 1: Add `finalizers` field to `Runner`**

Locate `Runner` struct (around line 185) and add the field:

```go
type Runner struct {
    // terminal builds the full stage graph into rc when called.
    terminal func(rc *runCtx)

    // finalizers run in registration order after the pipeline completes.
    // Each receives the RunSummary computed before any finalizer ran;
    // their errors are recorded in RunSummary.FinalizerErrs but do not
    // change RunSummary.Outcome.
    finalizers []func(ctx context.Context, s RunSummary) error
}
```

- [ ] **Step 2: Add `Runner.WithFinalizer`**

Append after `Build` (around line 193):

```go
// WithFinalizer registers fn to run after the pipeline completes. Multiple
// finalizers run in registration order. Each finalizer receives the
// [RunSummary] for the run; its return error (if any) is captured in
// RunSummary.FinalizerErrs. Finalizer errors do not change RunSummary.Outcome
// (finalizers observe the run's outcome, they do not influence it).
//
// The same context that was passed to Run is passed to the finalizer.
//
// WithFinalizer returns r so callers can chain attach calls.
func (r *Runner) WithFinalizer(fn func(ctx context.Context, s RunSummary) error) *Runner {
    r.finalizers = append(r.finalizers, fn)
    return r
}
```

- [ ] **Step 3: Change `Runner.Run` signature and wire summary computation**

Replace the existing `Run` (around line 247-314) with this version. The body keeps the same pipeline-execution flow but bookends it with timing and post-run derivation.

```go
// Run executes the pipeline. It blocks until the pipeline completes,
// the context is cancelled, or an unhandled error occurs. Run may be called
// multiple times; each call builds a fresh channel graph.
//
// Returns a [RunSummary] describing the run plus the same fatal error (if
// any) that previously was the only return value. The summary is populated
// even when the error is non-nil. Finalizers attached via [Runner.WithFinalizer]
// run after the stage graph completes (and after the summary is computed)
// in registration order; their errors are recorded in RunSummary.FinalizerErrs
// without affecting RunSummary.Outcome.
func (r *Runner) Run(ctx context.Context, opts ...RunOption) (RunSummary, error) {
    cfg := buildRunConfig(opts)

    codec := cfg.codec
    if codec == nil {
        codec = internal.JSONCodec{}
    }

    hook := cfg.hook
    if hook == nil {
        hook = internal.NoopHook{}
    }

    rc := newRunCtx()
    rc.cache = cfg.defaultCache
    rc.cacheTTL = cfg.defaultCacheTTL
    rc.codec = codec
    rc.hook = hook
    rc.gate = cfg.gate
    rc.defaultErrorHandler = cfg.defaultErrorHandler
    rc.defaultBuffer = cfg.defaultBuffer
    rc.defaultKeyTTL = cfg.defaultKeyTTL
    rc.dryRun = cfg.dryRun
    r.terminal(rc)

    rc.refs.init(cfg.store, codec)

    if gh, ok := hook.(internal.GraphHook); ok {
        gh.OnGraph(metasToGraphNodes(rc.metas))
    }

    if bh, ok := hook.(internal.BufferHook); ok {
        metas := rc.metas
        bh.OnBuffers(func() []internal.BufferStatus {
            out := make([]internal.BufferStatus, 0, len(metas))
            for _, m := range metas {
                if m.getChanLen == nil {
                    continue
                }
                out = append(out, internal.BufferStatus{
                    Stage:    m.name,
                    Length:   m.getChanLen(),
                    Capacity: m.getChanCap(),
                })
            }
            return out
        })
    }

    wrappers := make([]func(context.Context) error, len(rc.stages))
    for i, s := range rc.stages {
        s := s
        wrappers[i] = func(ctx context.Context) error { return s(ctx) }
    }

    started := time.Now()
    var pipelineErr error
    if cfg.drainTimeout > 0 {
        pipelineErr = runWithDrain(ctx, cfg.drainTimeout, rc.signalDone, wrappers)
    } else {
        pipelineErr = internal.RunStages(ctx, wrappers)
    }
    finishedAt := time.Now()

    summary := RunSummary{
        Outcome:     deriveRunOutcome(rc, pipelineErr),
        Err:         pipelineErr,
        Duration:    finishedAt.Sub(started),
        CompletedAt: finishedAt,
    }
    if mh, ok := hook.(*MetricsHook); ok {
        summary.Metrics = mh.Snapshot()
    } else {
        summary.Metrics = MetricsSnapshot{Timestamp: finishedAt, Elapsed: summary.Duration}
    }

    if len(r.finalizers) > 0 {
        summary.FinalizerErrs = make([]error, len(r.finalizers))
        for i, fn := range r.finalizers {
            summary.FinalizerErrs[i] = fn(ctx, summary)
        }
    }

    return summary, pipelineErr
}
```

- [ ] **Step 4: Update `RunHandle` to carry the summary**

Replace the existing `RunHandle` (around line 200-242) with:

```go
type RunHandle struct {
    done    chan struct{}
    err     error
    summary RunSummary // written before done is closed; safe to read after <-done returns
    gate    *Gate
}

// Wait blocks until the pipeline completes and returns its [RunSummary] and
// fatal error (if any). Safe to call from multiple goroutines concurrently.
func (h *RunHandle) Wait() (RunSummary, error) {
    <-h.done
    return h.summary, h.err
}

// Done returns a channel that is closed when the pipeline completes.
// Use this in a select alongside other channels.
func (h *RunHandle) Done() <-chan struct{} {
    return h.done
}

// Err returns the pipeline's terminal error. It is non-blocking: if the
// pipeline has not yet completed it returns nil. Use [RunHandle.Done] in a
// select and then call Err to retrieve the result, or use [RunHandle.Wait]
// to block until done. Safe to call from multiple goroutines concurrently.
func (h *RunHandle) Err() error {
    select {
    case <-h.done:
        return h.err
    default:
        return nil
    }
}

// Summary returns the run's summary. It is non-blocking: if the pipeline has
// not yet completed it returns the zero-valued [RunSummary] (Outcome:
// RunSuccess, all other fields zero). Use [RunHandle.Done] in a select and
// then call Summary to retrieve the result, or use [RunHandle.Wait] to block
// until done. Safe to call from multiple goroutines concurrently.
func (h *RunHandle) Summary() RunSummary {
    select {
    case <-h.done:
        return h.summary
    default:
        return RunSummary{}
    }
}

// Pause stops sources from emitting new items. In-flight items continue
// draining through downstream stages. Safe to call multiple times. Has no
// effect after the pipeline has completed.
func (h *RunHandle) Pause() { h.gate.Pause() }

// Resume allows sources to emit items again after a [RunHandle.Pause]. Safe
// to call multiple times. Has no effect if the pipeline is not paused.
func (h *RunHandle) Resume() { h.gate.Resume() }

// Paused reports whether the pipeline is currently paused.
func (h *RunHandle) Paused() bool { return h.gate.Paused() }
```

- [ ] **Step 5: Update `Runner.RunAsync` to capture the summary**

Replace `RunAsync` (around line 366-379) with:

```go
// RunAsync starts the pipeline in a background goroutine and returns a
// [RunHandle] for observing completion and controlling execution.
// A [Gate] is created automatically and exposed via [RunHandle.Pause] and
// [RunHandle.Resume]. Pass [WithPauseGate] to supply your own gate instead.
func (r *Runner) RunAsync(ctx context.Context, opts ...RunOption) *RunHandle {
    cfg := buildRunConfig(opts)
    gate := cfg.gate
    if gate == nil {
        gate = internal.NewGate()
        opts = append(opts, WithPauseGate(gate))
    }
    h := &RunHandle{done: make(chan struct{}), gate: gate}
    go func() {
        h.summary, h.err = r.Run(ctx, opts...)
        close(h.done)
    }()
    return h
}
```

- [ ] **Step 6: Update `MergeRunners` to combine finalizers**

Replace `MergeRunners` (around line 420-438) with:

```go
func MergeRunners(runners ...Runnable) (*Runner, error) {
    if len(runners) == 0 {
        return nil, ErrNoRunners
    }
    terminals := make([]func(*runCtx), len(runners))
    var finalizers []func(ctx context.Context, s RunSummary) error
    for i, r := range runners {
        built := r.Build()
        terminals[i] = built.terminal
        finalizers = append(finalizers, built.finalizers...)
    }
    return &Runner{
        terminal: func(rc *runCtx) {
            for _, t := range terminals {
                t(rc)
            }
        },
        finalizers: finalizers,
    }, nil
}
```

- [ ] **Step 7: Update the one internal `Wait()` caller in `collect.go`**

In `/Users/jonathan/projects/go-kitsune/collect.go` find line 672 (`errVal = handle.Wait()`). It needs to discard the summary:

```go
_, errVal = handle.Wait()
```

(There may be no other call sites; verify with `grep -n "handle\.Wait()\|\.Wait()" *.go internal/*.go terminal.go` from the repo root.)

- [ ] **Step 8: Build to confirm `kitsune.go` and `collect.go` compile**

Run: `go build .`
Expected: build will most likely FAIL because `terminal.go` (`ForEachRunner.Run` and `DrainRunner.Run` still return `error`) and tests/examples still use the old signature. That is expected at this commit boundary; the next tasks fix them. Save the build error log; the sweep is guided by the compiler's complaints.

If `kitsune.go` itself does not compile, fix it before moving on. The expected pattern of remaining errors is "cannot use … as error" or "too many return values" in `terminal.go` and downstream callers.

- [ ] **Step 9: Commit anyway, marking the breaking change**

The package is broken at this commit; that is intentional (the next tasks repair it). Use a clear commit message:

```bash
git add kitsune.go collect.go
git commit -m "feat(runsummary)!: change Runner.Run and RunHandle.Wait to return (RunSummary, error)"
```

(The `!` after the type denotes a breaking change.)

---

### Task 6: Sweep `terminal.go`

Updates `ForEachRunner.Run`, `DrainRunner.Run`, and adds `ForEachRunner.WithFinalizer` so the package compiles before the test sweep.

**Files:**
- Modify: `terminal.go`

- [ ] **Step 1: Update `ForEachRunner.Run` and add `RunAsync` no-op change (signature still returns `*RunHandle`)**

In `/Users/jonathan/projects/go-kitsune/terminal.go`, replace `ForEachRunner.Run` (around line 390-392) and `RunAsync` (around line 397-399) with:

```go
// Run executes the pipeline, blocking until completion. See [Runner.Run].
func (r *ForEachRunner[T]) Run(ctx context.Context, opts ...RunOption) (RunSummary, error) {
    return r.runner.Run(ctx, opts...)
}

// RunAsync starts the pipeline in a background goroutine and returns a
// [RunHandle] for observing completion and controlling execution.
// See [Runner.RunAsync] for details.
func (r *ForEachRunner[T]) RunAsync(ctx context.Context, opts ...RunOption) *RunHandle {
    return r.runner.RunAsync(ctx, opts...)
}
```

- [ ] **Step 2: Add `ForEachRunner.WithFinalizer`**

After `RunAsync` (around line 400), add:

```go
// WithFinalizer registers a finalizer on the underlying [*Runner].
// Multiple finalizers run in registration order after the pipeline completes.
// See [Runner.WithFinalizer] for details.
//
// WithFinalizer returns r so callers can chain attach calls.
func (r *ForEachRunner[T]) WithFinalizer(fn func(ctx context.Context, s RunSummary) error) *ForEachRunner[T] {
    r.runner.WithFinalizer(fn)
    return r
}
```

- [ ] **Step 3: Update `DrainRunner.Run`**

Replace (around line 430-432) with:

```go
// Run registers the Drain terminal stage and executes the pipeline.
//
// Deprecated: use [Pipeline.ForEach] with a no-op function instead.
func (r *DrainRunner[T]) Run(ctx context.Context, opts ...RunOption) (RunSummary, error) {
    return r.Build().Run(ctx, opts...)
}
```

(`DrainRunner.RunAsync` keeps its signature; it returns `*RunHandle` and that's now updated to carry the summary. No edit needed.)

- [ ] **Step 4: Build the package**

Run: `go build .`
Expected: PASSES for the kitsune package itself. Tests will not yet build; that is the next task.

- [ ] **Step 5: Commit**

```bash
git add terminal.go
git commit -m "feat(runsummary): update ForEachRunner.Run / DrainRunner.Run; add ForEachRunner.WithFinalizer"
```

---

### Task 7: Sweep core `*_test.go` files

The signature change ripples into ~140 test calls. The pattern is mechanical: any line `err := …Run(ctx[, opts]…)` or `…Run(ctx[, opts]…)` (statement form) becomes `_, err := …Run(ctx[, opts]…)`. Lines that assign to a named `err` variable in declaration form (like `if err := …Run(ctx); err != nil`) become `if _, err := …Run(ctx); err != nil`.

Common variations to handle:
- `err := runner.Run(ctx)`
- `if err := runner.Run(ctx); err != nil`
- `runner.Run(ctx)` (statement: discard error too — keep as `_, _ = runner.Run(ctx)` or wrap in t.Fatal). For Go style, the bare statement `runner.Run(ctx)` discards the return; under M3 it now returns two values, so it has to become `_, _ = runner.Run(ctx)` or be reshaped. Most existing bare statements were `runner.Run(ctx) //nolint:errcheck` (in tests that don't care).

The `runWith` helper in `errorstrategy_test.go:16-26` is widely used — fix it once and dozens of tests follow.

**Files (high-traffic):**
- Modify: `errorstrategy_test.go` (the helper)
- Modify: every other `*_test.go` in the kitsune root package (around 30 files)

**Approach:** sweep file-by-file rather than mass-regex; run `go build ./...` after each batch to confirm progress.

- [ ] **Step 1: Update the `runWith` helper in `errorstrategy_test.go`**

Replace lines 15-26 with:

```go
// runWith is a helper that collects items from a pipeline with custom RunOptions.
func runWith[T any](t *testing.T, p *kitsune.Pipeline[T], opts ...kitsune.RunOption) ([]T, error) {
    t.Helper()
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    var out []T
    _, err := p.ForEach(func(_ context.Context, item T) error {
        out = append(out, item)
        return nil
    }).Run(ctx, opts...)
    return out, err
}
```

(The change is `err :=` → `_, err :=` on line 24.)

- [ ] **Step 2: Build to find remaining test-file failures**

Run: `go vet ./...` (faster than `go build ./...` for finding compilation errors and gives clearer messages).

You will get a list of `cannot use … as error` and `assignment mismatch: 1 variable but Run returns 2 values` errors. Use these messages as the worklist.

For each error, edit the file in place. Patterns:

| Old | New |
|---|---|
| `err := runner.Run(ctx)` | `_, err := runner.Run(ctx)` |
| `err = runner.Run(ctx)` | `_, err = runner.Run(ctx)` |
| `if err := runner.Run(ctx); err != nil` | `if _, err := runner.Run(ctx); err != nil` |
| `runner.Run(ctx)` (bare statement) | `_, _ = runner.Run(ctx)` (or refactor to capture the err) |
| `return runner.Run(ctx)` (in a func returning error) | `_, err := runner.Run(ctx); return err` (or whatever the surrounding func expects) |
| `errors.Is(runner.Run(ctx), expected)` | requires reshape: assign to a tmp first |

If a test was using `// := … runner.Run(ctx)` to test what error is returned, it should still work after the rewrite because the second return is the same `error` value Run used to return.

- [ ] **Step 3: Run `go vet ./...` repeatedly until clean**

After each file edit, re-run `go vet ./...`. The error count should monotonically decrease.

- [ ] **Step 4: Run the test suite**

Run: `task test`
Expected: PASS. The signature change should not affect any test's logic; only the form of the call changed.

If a test fails because of the new behaviour (e.g. a finalizer-related test that doesn't exist yet), defer that until Task 9.

- [ ] **Step 5: Commit**

Add every modified test file. The simplest invocation is `git add -u '*_test.go'` from the repo root (this stages modifications to tracked test files only and won't add untracked files). Then commit:

```bash
git add -u '*_test.go'
git commit -m "test(runsummary): sweep core test callers for new Run signature"
```

Run `git status -s` after staging to confirm only `*_test.go` files in the kitsune root package are staged. If anything unexpected appears (e.g. an example or an inspector file you weren't expecting to have edited), unstage it with `git restore --staged <file>` and inspect.

(If the sweep is large, you may break this into two commits — one for the helper, one for the file sweep — but a single commit is fine.)

---

### Task 8: Sweep examples, inspector, and tail tests

Same pattern as Task 7, applied to non-test main-package files and the one tail test that calls Run.

**Files:**
- Modify: `examples/*/main.go` (all of them; check each)
- Modify: `inspector/*.go` if any call Run
- Modify: `tails/kkafka/kkafka_test.go:69`

- [ ] **Step 1: List affected example files**

Run:

```bash
grep -rln "\.Run(ctx" /Users/jonathan/projects/go-kitsune/examples
```

For each file in the output, edit. Most examples use `if err := runner.Run(ctx); err != nil { panic(err) }` or `runner.Run(ctx)` bare. Apply the patterns from Task 7.

- [ ] **Step 2: Check inspector**

Run:

```bash
grep -rn "\.Run(ctx" /Users/jonathan/projects/go-kitsune/inspector
```

Apply the same patterns.

- [ ] **Step 3: Update `tails/kkafka/kkafka_test.go:69`**

Open the file and read lines ~60-75 to get the surrounding context. The current line is:

```go
}).Run(ctx) //nolint:errcheck
```

`Run` now returns `(RunSummary, error)`, so a bare statement is a Go compile error ("multiple-value … in single-value context"). Wrap the call so both returns are explicitly discarded:

```go
_, _ = ... .Run(ctx) //nolint:errcheck
```

The exact form depends on the prefix expression on line 67-69; preserve it verbatim and only change `}).Run(ctx)` to `_, _ = ...).Run(ctx)`. After editing, re-read the surrounding lines to confirm the `_, _ =` parses correctly (it usually requires the call to be a complete statement on its own line).

If the prefix happens to be a multi-line builder chain that's awkward to prefix, an alternative shape is:

```go
_, runErr := ... .Run(ctx)
_ = runErr //nolint:errcheck
```

Pick the shape that minimises diff churn.

- [ ] **Step 4: Verify all tests compile**

Run: `go build ./...` from the repo root. Expected: clean (kitsune root package). The tail packages are separate modules that may have their own go.mod — `task test:ext` is the way to verify them. (See Task 11 note about pre-existing `GOWORK` setup quirk.)

For the example smoke test:

Run: `go test -run TestExamples -timeout 120s .`
Expected: PASS.

- [ ] **Step 5: Run the full short suite plus race**

```bash
task test && task test:race
```
Expected: PASS, no races.

- [ ] **Step 6: Commit**

```bash
git add examples/ inspector/ tails/kkafka/kkafka_test.go
git commit -m "test(runsummary): sweep examples / inspector / tail callers for new Run signature"
```

---

### Task 9: Add integration tests for `RunSummary` end-to-end

These tests exercise the full `Runner.Run → derivation → finalizer` flow, not just `deriveRunOutcome` in isolation.

**Files:**
- Modify: `runsummary_test.go`

- [ ] **Step 1: Add a `package kitsune_test` test file or extend the existing one**

The unit tests in `runsummary_test.go` use the `kitsune` package directly (white-box). The integration tests should use the public surface: `package kitsune_test`. To keep both in one file, use the `_test` package convention. Add a SECOND file `runsummary_external_test.go` with `package kitsune_test`:

```go
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

// TestRunHandle_SummaryNonBlocking verifies that Summary() before completion
// returns the zero summary, and after completion returns the real one.
func TestRunHandle_SummaryNonBlocking(t *testing.T) {
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
// each individual runner run after MergeRunners, in input order.
func TestMergeRunners_FinalizersCombined(t *testing.T) {
    ctx := context.Background()
    var order []string

    src := kitsune.FromSlice([]int{1, 2, 3})
    evens, odds := kitsune.Partition(src, func(_ context.Context, v int) (bool, error) {
        return v%2 == 0, nil
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
```

- [ ] **Step 2: Run the new tests**

Run: `go test -run "TestRunSummary_|TestWithFinalizer_|TestRunHandle_|TestMergeRunners_" ./... -race -v`
Expected: all PASS, no races.

- [ ] **Step 3: Run the full short suite**

Run: `task test`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add runsummary_external_test.go
git commit -m "test(runsummary): add end-to-end tests for outcome derivation, finalizers, and async"
```

---

### Task 10: Documentation

Add user-facing reference for `RunSummary`, `RunOutcome`, and `WithFinalizer`. Update existing docs that show `Run(ctx)` examples.

**Files:**
- Modify: `doc/operators.md`
- Modify: `doc/api-matrix.md`
- Modify: `doc/getting-started.md`
- Modify: `doc/roadmap.md`

- [ ] **Step 1: Append a "Run Summary" section to `doc/operators.md`**

Locate the new "Effects" section (added in M2; around line 2880-2980). After the Effects `---` separator, insert a new top-level section before "Error Handling Options":

````markdown
## :material-clipboard-text-clock-outline: Run Summary { #run-summary }

Every call to `Runner.Run`, `ForEachRunner.Run`, `DrainRunner.Run`, and `RunHandle.Wait` returns a `RunSummary` alongside the fatal error. The summary classifies the run's outcome, captures duration and completion timestamp, exposes a metrics snapshot, and records any finalizer errors.

### RunSummary

```go
type RunSummary struct {
    Outcome       RunOutcome
    Err           error
    Metrics       MetricsSnapshot
    Duration      time.Duration
    CompletedAt   time.Time
    FinalizerErrs []error
}
```

`Outcome` is one of:

| Value | Meaning |
|---|---|
| `RunSuccess` | Pipeline finished without a fatal error and every [`Effect`](#effect) stage either succeeded or has no failures. |
| `RunPartialSuccess` | Pipeline finished cleanly and every required `Effect` succeeded, but at least one [`BestEffort`](#effect) `Effect` had terminal failures. |
| `RunFailure` | Pipeline returned a fatal error, OR at least one required `Effect` had terminal failures. |

`Err` is the fatal pipeline error (or `nil`). `Metrics` is a `MetricsSnapshot` taken at the moment the pipeline finished. When a [`MetricsHook`](#metricshook) is attached via [`WithHook`](#withhook), `Metrics` is the hook's snapshot; otherwise it is a minimal snapshot containing `Timestamp` and `Elapsed`.

`Duration` is the wall-clock time between `Run` starting the stage graph and the last stage exiting. `CompletedAt` is the wall-clock time at the end of the run.

`FinalizerErrs` has length equal to the number of registered finalizers (see [`WithFinalizer`](#withfinalizer)); entries are nil for finalizers that returned nil. Finalizer errors do not change `Outcome`.

### Caveat: MetricsHook does not yet reflect Effect outcomes

`Effect` does not currently report its per-input outcomes through the [`Hook`](#withhook) interface. As a result, `summary.Metrics.Stages["my-effect"]` will not appear in the snapshot even when an `Effect` stage with that name is in the pipeline. `Outcome` derivation does not depend on the metrics snapshot; it uses an internal per-stage counter populated by the `Effect` operator. Wiring `Effect → Hook.OnItem` is tracked as a follow-up.

### WithFinalizer

```go
func (r *Runner) WithFinalizer(fn func(ctx context.Context, s RunSummary) error) *Runner
func (r *ForEachRunner[T]) WithFinalizer(fn func(ctx context.Context, s RunSummary) error) *ForEachRunner[T]
```

Registers `fn` to run after the pipeline completes. Multiple finalizers run in registration order; each receives the same `RunSummary` (computed before any finalizer ran). A finalizer's return error is recorded in `RunSummary.FinalizerErrs[i]`; finalizer errors do not change `Outcome`.

`MergeRunners` collects finalizers from input runners and prepends them, so:

```go
runner, _ := kitsune.MergeRunners(a, b) // a's and b's finalizers carry over
runner.WithFinalizer(persistRunSummary) // runs after a's and b's
```

**Example: persist last-run timestamp**

```go
store := openLastRunStore()

runner := pipeline.
    Through(normalize).
    Through(enrich).
    ForEach(processItem).
    WithFinalizer(func(ctx context.Context, s kitsune.RunSummary) error {
        return store.Set(ctx, "last_run", s.CompletedAt)
    })

summary, err := runner.Run(ctx)
if err != nil {
    log.Printf("run failed: %v (outcome=%v)", err, summary.Outcome)
}
```

---
````

- [ ] **Step 2: Add api-matrix entries**

In `/Users/jonathan/projects/go-kitsune/doc/api-matrix.md`, after section 12.5 Effects, insert a new section 12.7 Run Summary (skipping 12.6 to leave room for an Effects-related future addition):

```markdown
## 12.7 · Run Summary

| Symbol | Signature | Notes |
|--------|-----------|-------|
| `RunOutcome` | `RunSuccess / RunPartialSuccess / RunFailure` | iota-typed enum returned in [`RunSummary.Outcome`](operators.md#run-summary) |
| `RunSummary` | `struct{ Outcome; Err; Metrics; Duration; CompletedAt; FinalizerErrs }` | Returned from every Run / Wait |
| `(r *Runner).WithFinalizer` | `WithFinalizer(fn func(ctx, RunSummary) error) *Runner` | Registers a post-run callback; multiple allowed |
| `(r *ForEachRunner[T]).WithFinalizer` | `WithFinalizer(fn func(ctx, RunSummary) error) *ForEachRunner[T]` | Same; chains to underlying *Runner |

---
```

Also update existing rows that mention `Runner.Run`, `ForEachRunner.Run`, `DrainRunner.Run`, `RunHandle.Wait`, `RunAsync` if their signatures appear inline in the matrix. Run `grep -n "Run(ctx)" doc/api-matrix.md` to find them.

- [ ] **Step 3: Update `doc/getting-started.md`**

Run `grep -n "Run(ctx)\|err := .*\.Run\|err = .*\.Run" doc/getting-started.md` to find code-block instances. Convert each to the new form. Be careful not to update prose that refers to "Run(ctx)" as a name (e.g. "calling Run(ctx) starts the pipeline" — that prose is still accurate).

- [ ] **Step 4: Update `doc/roadmap.md`**

Locate the M3 bullet inside "Higher-level authoring layer M2-M4" (around line 96). Replace its prose to reflect shipped status:

```markdown
    - **M3 — `RunSummary` + `WithFinalizer`.** *(shipped 2026-04-25.)* `Runner.Run`, `ForEachRunner.Run`, `DrainRunner.Run`, and `RunHandle.Wait` now return `(RunSummary, error)`. `RunSummary` carries `Outcome` (RunSuccess / RunPartialSuccess / RunFailure derived from per-`Effect`-stage success/failure counters), `Err`, `Metrics` (a `MetricsSnapshot`), `Duration`, `CompletedAt`, and `FinalizerErrs`. `(*Runner).WithFinalizer` and `(*ForEachRunner[T]).WithFinalizer` register post-run callbacks; multiple finalizers run in registration order. `MergeRunners` collects finalizers from input runners. Source-breaking change to ~150 call sites; pre-1.0 with no external users so the sweep was acceptable.
```

(M2 and M4 lines remain untouched.)

- [ ] **Step 5: Run docs sanity grep**

Confirm no leftover `Run(ctx) error` references in user-facing docs:

```bash
grep -rn "Run(ctx) error\|\.Run(ctx) error" doc/
```

Expected: no matches in user-facing docs.

- [ ] **Step 6: Run all tests once more**

```bash
task test && task test:race && task test:property
```
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add doc/operators.md doc/api-matrix.md doc/getting-started.md doc/roadmap.md
git commit -m "docs(runsummary): add RunSummary, RunOutcome, WithFinalizer reference"
```

---

### Task 11: Memory update + final verification

Updates the user's auto-memory and does a final pass.

**Files:**
- Modify: `~/.claude/projects/-Users-jonathan-projects-go-kitsune/memory/project_higher_level_authoring.md`
- Modify: `~/.claude/projects/-Users-jonathan-projects-go-kitsune/memory/MEMORY.md`

- [ ] **Step 1: Update the higher-level-authoring memory**

Mark M3 as shipped 2026-04-25 (or the actual date if different). The M2 caveat about MetricsHook reflecting Effect outcomes can move from a "future M3 problem" to a "future enhancement of Effect" since M3 explicitly chose not to wire it. Keep M4 unchanged.

- [ ] **Step 2: Update `MEMORY.md` index summary**

Reflect that M1, M2, M3 have all shipped and M4 remains.

(Memory files are not committed to git.)

- [ ] **Step 3: Final test pass**

```bash
task test
task test:race
task test:property
task test:examples
```

All four must PASS. (`task test:ext` has the pre-existing `GOWORK` configuration issue from M2; not this milestone's concern.)

- [ ] **Step 4: Inform the user**

Report concisely: how many commits, which test gates passed, and what M4 remains.

---

## Self-review notes (for the plan author, not the engineer)

- **Spec coverage:** every M3 surface from the spec is mapped to a task: `RunOutcome` (Task 1), `RunSummary` (Tasks 1, 5), derivation (Task 4), `Runner.Run` signature (Task 5), `ForEachRunner.Run` (Task 6), `DrainRunner.Run` (Task 6), `WithFinalizer` (Tasks 5, 6), `RunAsync`/`RunHandle.Wait` (Task 5). Spec deltas (`FinalizerErrs []error`, `Wait` signature change, no Effect→Hook in v1, `MergeRunners` finalizer collection) are documented up front.
- **Type-name consistency:** `RunOutcome`, `RunSummary`, `effectStat`, `effectStats`, `registerEffectStat`, `recordEffectOutcome`, `deriveRunOutcome`, `WithFinalizer`, `FinalizerErrs` are used consistently across tasks.
- **Sequencing:** Task 5 intentionally leaves the package non-compiling for a brief window (between Tasks 5 and 6); this is called out explicitly. Tasks 6, 7, 8 are the breaking-change sweep, ordered by blast radius (terminal.go → core tests → examples/inspector/tails). The sweep tasks rely on `go vet` to find remaining errors rather than prescribing every line edit.
- **No placeholders:** every code step shows the actual content. The sweep tasks (7, 8) describe the mechanical pattern explicitly with a worked-examples table; the per-file edits are too numerous to enumerate but the pattern is mechanical.
- **Frequent commits:** 11 task commits.
- **No rollback paths:** the breaking change cannot be backed out incrementally — once Task 5 lands the package won't compile until Task 6 lands. The plan acknowledges this and recommends not pushing between Task 5 and Task 8. If this proves uncomfortable, the alternative is to merge Tasks 5-8 into a single commit; the plan keeps them separate so each is reviewable independently.
