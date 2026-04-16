# Design: Higher-Level Pipeline Authoring Layer

**Date:** 2026-04-15
**Status:** Approved
**Roadmap item:** Higher-level authoring layer (new)

---

## Context

go-kitsune's core is strong: type-safe operators, composable stages, rich concurrency primitives, hooks, and inspector support. The gap exposed by real-world pipeline authoring (particularly scheduler-shaped and bounded-job-shaped workflows) is not missing operators — it is a missing higher-level authoring surface.

Today, a complex pipeline reads as a sequence of low-level operator calls. Business intent is invisible at the declaration site. Reusable units exist (`Stage[I,O]`) but carry no semantic metadata. There is no graph-visible name for a group of steps, no first-class model for externally-visible effects, no structured run summary, and no developer-experience affordances for iterating on subsections of a pipeline without re-running the whole thing.

This design adds four coherent additions to the main `kitsune` package that together enable pipelines written as named, composable, graph-visible business steps:

1. `Segment[I,O]` — named, graph-stamped stage group
2. `Effect[I,R]` — effectful step with typed outcome, policy, and required/best-effort semantics
3. `RunSummary` + `WithFinalizer` — structured run result and post-run hook
4. `DevStore` + `FromCheckpoint` — local development checkpoint/replay

All additions are strictly additive. Every existing operator, `Stage[I,O]`, `Then`, `ForEach`, `Runner`, and `Pipeline[T]` continues to work without modification.

---

## Section 1: `Segment[I,O]`

### Problem

A pipeline that is 15 operator calls wide is hard to read and hard to test in parts. There is no way to give a business name to a group of related steps that is visible in the graph or inspector.

### Design

```go
type Segment[I, O any] struct {
    name    string
    stage   Stage[I, O]
    options []SegmentOption
}

func NewSegment[I, O any](name string, stage Stage[I, O], opts ...SegmentOption) Segment[I, O]
```

`Segment[I,O]` wraps a `Stage[I,O]`. When applied to a pipeline, it stamps `SegmentName: name` on every `stageMeta` node created by the inner stage. This makes the group visible in `Describe()`, the inspector, and hooks — collapsed as one business unit or expanded into constituent operators.

`Segment[I,O]` implements `Apply(*Pipeline[I]) *Pipeline[O]`, the same interface as `Stage[I,O]`. This means it composes with `Then` without any API change:

```go
fetchPages   := NewSegment("fetch-pages",   stage.FetchPages)
enrichPages  := NewSegment("enrich-pages",  stage.EnrichPages)
buildObjects := NewSegment("build-objects", stage.BuildObjects)

pipeline := Then(Then(fetchPages, enrichPages), buildObjects)
```

Or using `Apply` directly:

```go
p = fetchPages.Apply(source)
p = enrichPages.Apply(p)
p = buildObjects.Apply(p)
```

### `Then` interface change

`Stage[I,O]` is `func(*Pipeline[I]) *Pipeline[O]`. `Segment[I,O]` is a struct. To make both work with `Then`, the `Then` signature is changed to accept an interface:

```go
// Before (still valid — Stage satisfies the interface implicitly):
func Then[I, M, O any](first Stage[I, M], second Stage[M, O]) Stage[I, O]

// After:
type Composable[I, O any] interface {
    Apply(*Pipeline[I]) *Pipeline[O]
}

func Then[I, M, O any](first Composable[I, M], second Composable[M, O]) Stage[I, O]
```

`Stage[I,O]` already has an `Apply` method, so every existing call to `Then` with `Stage` arguments continues to compile unchanged. `Segment[I,O]` also has `Apply`. Both now compose with `Then`.

### `stageMeta` changes

```go
type stageMeta struct {
    // existing fields unchanged
    SegmentName string // "" if not inside a segment
}
```

`GraphNode` gains the same field. The inspector and `Describe()` can use it to group nodes visually.

### SegmentOption

```go
type SegmentOption interface{ applySegment(*segmentConfig) }
```

Initial set: none required for v1. Placeholder for future options (description, tags, owner, metrics labeling).

### Testing

Because `Segment[I,O]` is just `Stage[I,O]` with a name, it is testable identically — call `.Apply(source)` and collect results with `Collect`.

---

## Section 2: `Effect[I,R]`

### Problem

Externally-visible state changes (publish to SNS, write to Redis, persist run metadata) are currently modeled as `ForEach` terminal sinks or as plain `Map` calls. Neither representation expresses:

- whether the effect is required or best-effort for overall run success
- retry/timeout/idempotency policy
- a typed outcome carrying both the effect result and application status
- branchable success/failure outputs

### Design

```go
type EffectOutcome[I, R any] struct {
    Input   I
    Result  R
    Err     error
    Applied bool // false if attempt failed before application; ambiguous on timeout
}

func Effect[I, R any](
    p        *Pipeline[I],
    fn       func(ctx context.Context, item I) (R, error),
    opts     ...EffectOption,
) (*Pipeline[EffectOutcome[I, R]])
```

`Effect` returns a single pipeline of `EffectOutcome[I,R]`. Callers route success and failure from that:

```go
outcomes := kitsune.Effect(p, publishToSNS, kitsune.Required(), kitsune.RetryN(3))
ok     := kitsune.Filter(outcomes, func(o EffectOutcome[Msg, string]) bool { return o.Err == nil })
failed := kitsune.Filter(outcomes, func(o EffectOutcome[Msg, string]) bool { return o.Err != nil })
```

This mirrors the `MapResult` two-output pattern while keeping the branch split in user code. A `TryEffect` convenience helper is provided:

```go
func TryEffect[I, R any](
    p    *Pipeline[I],
    fn   func(ctx context.Context, item I) (R, error),
    opts ...EffectOption,
) (ok *Pipeline[EffectOutcome[I, R]], failed *Pipeline[EffectOutcome[I, R]])
```

### `EffectPolicy`

A named, reusable policy value that implements `EffectOption`:

```go
type EffectPolicy struct {
    Required    bool
    MaxAttempts int
    Timeout     time.Duration
    Idempotent  bool
    IdempotencyKey func(any) string // typed via generic helper
}

func (p EffectPolicy) applyEffect(c *effectConfig) { /* set fields */ }
```

Call-site options override policy fields using last-write-wins:

```go
var SNSPolicy = kitsune.EffectPolicy{Required: true, MaxAttempts: 3, Timeout: 5 * time.Second}

// Override timeout only at this call site:
outcomes := kitsune.Effect(p, publish, SNSPolicy, kitsune.WithTimeout(10*time.Second))
```

### Effect options (call-site)

| Option | Description |
|---|---|
| `Required()` | Run failure if this effect fails after exhausting retries |
| `BestEffort()` | Run continues even if this effect fails; failure recorded in summary |
| `RetryN(n)` | Retry up to n times on error |
| `WithTimeout(d)` | Per-attempt timeout |
| `WithIdempotencyKey(fn)` | Key function for idempotent retry |
| `DryRunNoop()` | Override: skip fn call when `DryRun` RunOption is active |

Default when no `Required`/`BestEffort` is set: `Required`.

### `stageMeta` changes

```go
type stageMeta struct {
    // existing fields unchanged
    isEffect       bool
    effectRequired bool
}
```

`RunOutcome` (see Section 3) is derived from these flags combined with `MetricsSnapshot` error counts.

### `DryRun` RunOption

```go
kitsune.DryRun() RunOption
```

When active, all `Effect` calls skip the provided function and return `EffectOutcome{Applied: false}` with no error. Pure and stateful stages run normally. Useful for validating pipeline graph wiring without side effects.

### Graph visibility

Effect nodes appear in `Describe()` and the inspector with `kind: "effect"`, `required: true/false`, and any policy metadata attached. Segment grouping works identically — an effect inside a `Segment` inherits the segment name.

---

## Section 3: `RunSummary` and `WithFinalizer`

### Problem

`Runner.Run(ctx) error` returns a single `error`. There is no structured way to get per-stage metrics, item counts, effect outcomes, run duration, or overall success/partial-success status from a bounded pipeline run. Post-run cleanup (persist last-run timestamps, emit summary metrics) currently lives outside the pipeline entirely.

### `RunSummary`

```go
type RunOutcome int

const (
    RunSuccess        RunOutcome = iota // all required effects succeeded
    RunPartialSuccess                   // required effects ok; some best-effort effects failed
    RunFailure                          // one or more required effects failed, or pipeline error
)

type RunSummary struct {
    Outcome     RunOutcome
    Err         error          // first fatal error, if any
    Metrics     MetricsSnapshot
    Duration    time.Duration
    CompletedAt time.Time
}
```

`Runner.Run(ctx)` is upgraded:

```go
// Before:
func (r *Runner) Run(ctx context.Context) error

// After:
func (r *Runner) Run(ctx context.Context) (RunSummary, error)
```

`error` in the return is the same error that was previously the only return — a fatal pipeline error or context cancellation. `RunSummary.Err` mirrors it. The upgrade is a source-breaking change to callers that only capture `error`; the migration path is `summary, err := runner.Run(ctx)` with `_ = summary` for callers that don't need the summary yet.

`RunOutcome` is derived at `Run` completion by inspecting `stageMeta.isEffect`, `stageMeta.effectRequired`, and `MetricsSnapshot` error counts for each stage.

### `WithFinalizer`

Finalizers are attached to a `*Runner` or `*ForEachRunner[T]`, not passed as a `RunOption`. This keeps them part of the pipeline definition and makes them testable:

```go
func (r *Runner) WithFinalizer(fn func(ctx context.Context, s RunSummary) error) *Runner
func (r *ForEachRunner[T]) WithFinalizer(fn func(ctx context.Context, s RunSummary) error) *ForEachRunner[T]
```

Multiple finalizers may be attached; they run in registration order after the pipeline completes. A finalizer error is recorded in `RunSummary` but does not affect `RunOutcome` (finalizers observe the outcome, they do not change it).

Example:

```go
runner := kitsune.ForEach(p, process).
    WithFinalizer(func(ctx context.Context, s kitsune.RunSummary) error {
        return store.PersistLastRun(ctx, s.CompletedAt, s.Outcome)
    }).
    Build()

summary, err := runner.Run(ctx)
```

---

## Section 4: `DevStore` and `FromCheckpoint`

### Problem

When iterating on a subsection of a pipeline, developers do not want to re-run upstream stages (which may make expensive API calls) on every iteration. There is no built-in way to freeze a segment's output and replay it as input on the next run.

### Design

```go
type DevStore interface {
    Save(ctx context.Context, segment string, items []json.RawMessage) error
    Load(ctx context.Context, segment string) ([]json.RawMessage, error)
}
```

`FileDevStore` is the built-in implementation, writing one JSON file per segment name under a configurable directory:

```go
func NewFileDevStore(dir string) *FileDevStore
```

### `WithDevStore` RunOption

```go
kitsune.WithDevStore(store DevStore) RunOption
```

When a `DevStore` is attached to a run, each `Segment` automatically captures its output items (serialized via the run's `Codec`, defaulting to `JSONCodec`) at segment exit and saves them under the segment's name.

On subsequent runs with the same store, the behavior per segment is:
- **Snapshot exists:** segment is bypassed; its stored items are replayed as its output
- **Snapshot missing:** segment runs live and its fresh output is captured to the store

This means the first run always runs everything live and populates all snapshots. Subsequent runs replay segments whose snapshots exist. To force a segment to re-run and refresh its snapshot, delete its snapshot file from the store directory.

### `ReplayThrough` RunOption

```go
kitsune.ReplayThrough(segmentName string) RunOption
```

Replay all segments up to and including `segmentName` from stored snapshots, then run subsequent segments live. If any required segment snapshot is missing, `Run` returns an error with the name of the missing segment.

### `FromCheckpoint[T]`

For test-time use, load a stored segment output directly as a pipeline source:

```go
func FromCheckpoint[T any](store DevStore, segment string, opts ...StageOption) *Pipeline[T]
```

This allows writing a unit test that starts from a frozen segment output:

```go
store := kitsune.NewFileDevStore("testdata/checkpoints")
p := kitsune.FromCheckpoint[EnrichedPage](store, "enrich-pages")
// test the build-objects segment in isolation
```

### Codec

Serialization uses the existing `Codec` interface (already present in the library). `JSONCodec` is the default. Custom codecs can be set via `WithCodec(c Codec)` RunOption, which is already planned.

### Safety

`DevStore` is a development-only affordance. It is intentionally not production-safe: snapshots may become stale, serialization may lose type fidelity for complex types, and there is no version or schema tracking. The documentation makes this clear. Attaching a `DevStore` in a production run is an explicit choice by the author.

---

## Section 5: `Then` interface change — backwards compatibility

The change from `Stage[I,O]` to `Composable[I,O]` in `Then`'s signature is the only modification to an existing exported API.

**Before:**
```go
func Then[I, M, O any](first Stage[I, M], second Stage[M, O]) Stage[I, O]
```

**After:**
```go
type Composable[I, O any] interface {
    Apply(*Pipeline[I]) *Pipeline[O]
}
func Then[I, M, O any](first Composable[I, M], second Composable[M, O]) Stage[I, O]
```

`Stage[I,O]` already has `Apply(*Pipeline[I]) *Pipeline[O]`. It satisfies `Composable[I,O]` without modification. Every existing call to `Then(stage1, stage2)` compiles unchanged.

`Segment[I,O]` also implements `Apply`, so `Then(segment1, segment2)` and `Then(segment1, stage2)` both work.

---

## Section 6: Interaction model and graph visibility

### Graph (`Describe()` and inspector)

Each `stageMeta` now carries:
- `SegmentName string` — set by `Segment.Apply`; empty for stages not inside a segment
- `isEffect bool` — set by `Effect`
- `effectRequired bool` — set by `Required()` / `BestEffort()` options

`GraphNode` exposes the same fields. The inspector can:
- Group nodes by `SegmentName` for a collapsed business view
- Expand groups to show individual operator nodes
- Mark effect nodes with required/best-effort badges
- Show `RunOutcome` and per-segment error counts in run summaries

### `Describe()` output (text)

Existing text output is unchanged when `SegmentName` is empty. When segments are present, nodes are annotated with `[segment: name]`. A future `DescribeSegments()` call can render a collapsed view.

### Hooks

`HookEvent` gains `SegmentName string` (empty for non-segment stages). Hook authors can filter by segment for coarse-grained observability without changing existing hooks.

---

## What is NOT in this design

The following were discussed and explicitly excluded:

- **`SortBy` / `ReduceByKey` / barrier operators**: Naming conflicts with existing non-barrier operators (`GroupBy`, `DistinctBy`). Windowed sort should share the same name. Bounded materialization operators need a separate design pass that resolves naming and windowed-vs-bounded semantics first. Added as a roadmap item.
- **`MergeRunners` accepting `Runnable` interface**: `Build()` currently leaks into user-facing code only because `MergeRunners` requires `*Runner`. This is addressed as a separate roadmap item: introduce `Runnable` interface, change `MergeRunners` to accept `...Runnable`, add `RunAsync` to `ForEachRunner[T]`.
- **`Step` type hierarchy as a public type**: `PureStep` / `StatefulStep` / `EffectStep` as explicit public types add noise without value. Kind is derivable from `stageMeta` flags. The three-kind taxonomy is reflected in documentation, not in exported types.
- **Compensation / saga patterns**: Out of scope for this design. The effect model does not block future compensation support.
- **Page-oriented sources, keyed accumulation with final emission, barriered prioritization**: Valuable for scheduler workflows but require dedicated design work. Filed as separate roadmap items.

---

## Implementation order

1. `stageMeta` + `GraphNode` field additions (`SegmentName`, `isEffect`, `effectRequired`)
2. `Composable[I,O]` interface + `Then` signature update
3. `Segment[I,O]` struct + `NewSegment` + `Apply` method
4. `EffectOutcome[I,R]`, `EffectPolicy`, `EffectOption`, `Effect`, `TryEffect`
5. `RunSummary`, `RunOutcome`, `RunOutcome` derivation at `Run` time
6. `Runner.Run` return type upgrade to `(RunSummary, error)`
7. `ForEachRunner.WithFinalizer` + `Runner.WithFinalizer`
8. `DevStore`, `FileDevStore`, `WithDevStore`, `ReplayThrough`, `FromCheckpoint`
9. `DryRun` RunOption
10. Inspector + `Describe()` segment grouping
11. Documentation, examples, operator reference updates

Steps 1-3 are purely additive and can ship independently. Steps 4-7 form a coherent second milestone. Steps 8-9 form the dev-experience milestone. Step 10 follows after the data model is stable.

---

## Files affected

| File | Change |
|---|---|
| `pipeline.go` | `stageMeta`: add `SegmentName`, `isEffect`, `effectRequired`; `runCtx`: add `currentSegment`, `devStore`, `replayThrough`, `dryRun` |
| `stage.go` | Add `Composable[I,O]` interface; update `Then` signature |
| `stage.go` (new type) | `Segment[I,O]`, `NewSegment`, `SegmentOption` |
| `advanced.go` | `Effect[I,R]`, `TryEffect[I,R]`, `EffectOutcome[I,R]`, `EffectPolicy`, `EffectOption` implementations |
| `metrics.go` | `RunSummary`, `RunOutcome` |
| `terminal.go` | `Runner.Run` return type; `WithFinalizer` on `Runner` and `ForEachRunner` |
| `collect.go` | `FromCheckpoint[T]` |
| `devstore.go` (new) | `DevStore` interface, `FileDevStore`, `WithDevStore`, `ReplayThrough` RunOptions |
| `kitsune.go` | `DryRun` RunOption |
| `doc/roadmap.md` | New items: bounded barrier operators (separate design), `MergeRunners` Runnable refactor |
| `doc/operators.md` | `Segment`, `Effect`, `TryEffect` sections |
| `doc/api-matrix.md` | New rows |
| `examples/segment/`, `examples/effect/` | New examples |
