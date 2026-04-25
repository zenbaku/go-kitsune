# Run summaries and finalizers

Every `Run` and `Wait` in kitsune returns a `RunSummary` alongside the fatal error. The summary classifies the run's outcome (`RunSuccess`, `RunPartialSuccess`, or `RunFailure`), records duration and completion time, exposes a metrics snapshot, and captures errors from any registered finalizers. `WithFinalizer` registers callbacks that run after the pipeline completes and observe the summary.

This page is the hands-on guide. For the type-by-type reference, see [Run Summary in the operator catalog](operators.md#run-summary).

---

## Why a structured summary?

A pipeline run can succeed, fail, or partially succeed in ways that the single error return value of an older `Run() error` shape cannot express:

- The pipeline finished cleanly, but a best-effort `Effect` had two terminal failures out of a hundred items. Run-as-a-whole succeeded, but downstream alerting wants to know.
- The pipeline returned a fatal error, but you want to record duration and partial metrics for post-mortem analysis.
- A finalizer that persists "last successful run timestamp" needs the actual completion time, not just success/failure.
- Async runs via `RunAsync` need a way to surface the same structured result that synchronous `Run` provides.

`RunSummary` covers all of those without requiring callers to thread a hook around the runner or to re-derive metrics state after the fact.

---

## Anatomy of `RunSummary`

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

Field by field:

- **`Outcome`** is one of `RunSuccess`, `RunPartialSuccess`, `RunFailure`. See [derivation rules](#runoutcome-derivation) below. The string form (`Outcome.String()`) is stable: `"RunSuccess"`, `"RunPartialSuccess"`, `"RunFailure"`.
- **`Err`** mirrors the second return value of `Run`. It is the first fatal error from the stage graph (or `nil` when the run completes cleanly). A non-nil `Err` always implies `Outcome: RunFailure`.
- **`Metrics`** is a [`MetricsSnapshot`](operators.md#metricshook) taken at the moment the pipeline finished. When a `MetricsHook` is attached via `WithHook`, this is the hook's snapshot. Otherwise it is a minimal snapshot containing `Timestamp` and `Elapsed` (so callers can always read `.Duration` and `.CompletedAt` without a nil check).
- **`Duration`** is the wall-clock time between `Run` starting the stage graph and the last stage exiting. Always populated.
- **`CompletedAt`** is the wall-clock time at the end of the run. Always populated.
- **`FinalizerErrs`** has length equal to the number of registered finalizers, with `nil` entries for finalizers that returned `nil`. Empty when no finalizers were registered. Finalizer errors do NOT change `Outcome`.

The combination `Err == nil && Outcome == RunSuccess` is the only "fully clean" state. Anything else is worth inspecting.

---

## `RunOutcome` derivation

The `Outcome` is derived at the end of `Run`, after the stage graph finishes but before finalizers run. The rules in priority order:

```
1. pipelineErr != nil
     → RunFailure
2. any required-Effect stage has at least one terminal failure
     → RunFailure
3. any best-effort-Effect stage has at least one terminal failure
     → RunPartialSuccess
4. otherwise
     → RunSuccess
```

A pipeline with no `Effect` stages at all yields `RunSuccess` on a clean exit and `RunFailure` only if the pipeline itself errors out.

A few worked examples:

| Pipeline shape | Result | Outcome |
|---|---|---|
| `Map → Filter → ForEach`, no errors | `(_, nil)` | `RunSuccess` |
| `Map → Filter → ForEach`, `Map` halts on error | `(_, err)` | `RunFailure` |
| `Effect (Required)` with all items succeeding | `(_, nil)` | `RunSuccess` |
| `Effect (Required)` with 3 items failing after retries | `(_, nil)` | `RunFailure` |
| `Effect (BestEffort)` with 3 items failing after retries | `(_, nil)` | `RunPartialSuccess` |
| Both required and best-effort effects, only best-effort fails | `(_, nil)` | `RunPartialSuccess` |
| Both fail | `(_, nil)` | `RunFailure` (required wins) |
| Required effect succeeds, pipeline ctx cancelled | `(_, ctx.Err())` | `RunFailure` (pipelineErr wins) |
| Run started with `DryRun()`; `Effect` skipped entirely | `(_, nil)` | `RunSuccess` (no fn was called, no failures recorded) |

Per-effect counters are kept in `runCtx.effectStats`, keyed by stage ID. Each `Effect` increments either `success` or `failure` per emitted outcome. Dry-run outcomes do NOT count toward either counter.

---

## Reading the summary

Synchronous case:

```go
summary, err := runner.Run(ctx)
log.Printf("run finished in %v at %v: outcome=%v",
    summary.Duration, summary.CompletedAt, summary.Outcome)
if err != nil {
    log.Printf("fatal error: %v", err)
}
```

Async case:

```go
handle := runner.RunAsync(ctx)
// ... do other work ...
summary, err := handle.Wait()
```

Or non-blocking polling:

```go
handle := runner.RunAsync(ctx)
select {
case <-handle.Done():
    summary := handle.Summary()
    log.Printf("done: %v", summary.Outcome)
case <-time.After(30 * time.Second):
    log.Print("still running")
}
```

`handle.Summary()` is non-blocking: before completion it returns the zero-valued `RunSummary` (with `Outcome: RunSuccess`, `Duration: 0`, etc.). Use `<-handle.Done()` to gate the read, or block on `handle.Wait()` which returns both the summary and the error.

---

## `WithFinalizer`: post-run callbacks

`WithFinalizer(fn)` registers `fn` to run after the stage graph completes and after the summary is computed:

```go
runner := pipeline.ForEach(handle).
    WithFinalizer(func(ctx context.Context, s kitsune.RunSummary) error {
        return store.PersistLastRun(ctx, s.CompletedAt, s.Outcome)
    })

summary, err := runner.Run(ctx)
```

A few rules:

- **Multiple finalizers run in registration order.** Each gets the same `RunSummary` (computed before any finalizer ran). Finalizers cannot mutate `Outcome` or any other summary field.
- **Each finalizer's error goes into `FinalizerErrs[i]`.** Length is the count of registered finalizers; entries are `nil` for finalizers that returned `nil`.
- **Finalizer errors do not change `Outcome`.** A finalizer that returns an error does not flip a `RunSuccess` to `RunFailure`. The whole point: finalizers observe the run, they do not influence its outcome.
- **The same `ctx` passed to `Run` is passed to the finalizer.** If the run was cancelled, the finalizer receives a cancelled context. Finalizers can choose to do best-effort cleanup with their own background context.
- **Finalizers run inline in the goroutine that called `Run`.** They block the return from `Run`. If a finalizer is slow (e.g. flushing buffered metrics), `Run` blocks too.

Both `*Runner` and `*ForEachRunner[T]` have `WithFinalizer`; the latter delegates to the underlying runner. They return the receiver for chaining.

---

## `MergeRunners` propagates finalizers

When you build multiple terminal runners (e.g. via `Partition` or `TryEffect`) and then merge them, finalizers attached to each input runner carry over to the merged runner. They run in input order. A finalizer attached to the merged runner itself runs after them.

```go
ok, failed := kitsune.TryEffect(messages, publish, SNSPolicy)

okRunner := ok.ForEach(recordSuccess).
    WithFinalizer(func(_ context.Context, _ kitsune.RunSummary) error {
        log.Print("ok branch flushed")
        return nil
    })
failedRunner := failed.ForEach(recordFailure).
    WithFinalizer(func(_ context.Context, _ kitsune.RunSummary) error {
        log.Print("failed branch flushed")
        return nil
    })

merged, _ := kitsune.MergeRunners(okRunner, failedRunner)
merged.WithFinalizer(func(_ context.Context, s kitsune.RunSummary) error {
    return alerts.Notify(ctx, s) // runs after both per-branch finalizers
})

summary, _ := merged.Run(ctx)
// summary.FinalizerErrs has 3 entries: [okRunner's, failedRunner's, merged's]
```

There is one shared `RunSummary` for the whole merged run. Per-branch finalizers do not see per-branch outcomes; they see the merged result.

---

## Common patterns

### Persist last-run timestamp

```go
runner := pipeline.ForEach(handle).
    WithFinalizer(func(ctx context.Context, s kitsune.RunSummary) error {
        if s.Outcome == kitsune.RunSuccess {
            return store.SetLastSuccess(ctx, s.CompletedAt)
        }
        return nil // do not advance the watermark on partial/failed runs
    })
```

### Outcome-driven alerting

```go
runner.WithFinalizer(func(ctx context.Context, s kitsune.RunSummary) error {
    switch s.Outcome {
    case kitsune.RunFailure:
        return pager.Page(ctx, "pipeline run failed", s.Err)
    case kitsune.RunPartialSuccess:
        return pager.Notify(ctx, "best-effort effect failures")
    }
    return nil // RunSuccess: nothing to do
})
```

### Metrics emission

```go
runner.WithFinalizer(func(_ context.Context, s kitsune.RunSummary) error {
    metrics.Histogram("pipeline.duration_ms").Record(s.Duration.Milliseconds())
    metrics.Counter("pipeline.outcome", "outcome", s.Outcome.String()).Inc()
    return nil
})
```

### Run audit log

```go
runner.WithFinalizer(func(ctx context.Context, s kitsune.RunSummary) error {
    return audit.Log(ctx, audit.Entry{
        At:       s.CompletedAt,
        Duration: s.Duration,
        Outcome:  s.Outcome.String(),
        Err:      errString(s.Err),
        Stages:   summarizeStages(s.Metrics),
    })
})
```

### CI smoke test

```go
func TestPipelineDoesNotRegress(t *testing.T) {
    summary, err := buildPipeline().Run(context.Background())
    if err != nil {
        t.Fatalf("err=%v", err)
    }
    if summary.Outcome != kitsune.RunSuccess {
        t.Errorf("Outcome=%v, want RunSuccess", summary.Outcome)
    }
    if summary.Duration > 5*time.Second {
        t.Errorf("Duration=%v, slower than budget", summary.Duration)
    }
}
```

---

## Interaction with `Effect`

`RunOutcome` derivation is driven entirely by per-`Effect`-stage success/failure counters. A pipeline without `Effect` stages always has `RunSuccess` on a clean exit and `RunFailure` only on a fatal pipeline error.

So if you want fine-grained outcome classification, the way to opt in is to wrap externally-visible side effects in [`Effect`](side-effects.md) rather than plain `Map`. `Effect` automatically registers itself in `runCtx.effectStats` and increments the appropriate counter on each emitted outcome.

A `Map` that publishes to SNS and ignores transient errors is invisible to outcome derivation. The same logic wrapped in `Effect(BestEffort())` registers a counter and contributes `RunPartialSuccess` on terminal failures.

For more, see the [Side effects guide](side-effects.md).

---

## Interaction with `DevStore`

A replayed `Segment` does not register success or failure counts because no `Effect` actually ran inside it. So `RunSummary.Outcome` reflects only the segments that ran live during this run.

Concretely:
- Run 1 with no snapshots: every segment runs live; `Effect` counters are populated; `Outcome` reflects actual effect failures.
- Run 2 with snapshots present: the segment is bypassed; its effect counters stay at zero; `Outcome` is `RunSuccess` (assuming no other live failures).

This is consistent with `RunSummary` describing *this* run, not the cumulative history of runs that produced the snapshots. If you want to verify outcome derivation after a code change, delete all snapshots first to force a fully-live run.

For more, see the [Dev iteration guide](dev-iteration.md#interaction-with-runsummary).

---

## Interaction with `MetricsHook`

When a [`MetricsHook`](operators.md#metricshook) is attached via `WithHook`, `summary.Metrics` is the hook's snapshot at the end of the run:

```go
m := kitsune.NewMetricsHook()
runner.Run(ctx, kitsune.WithHook(m))
// summary.Metrics == m.Snapshot() at end-of-run
```

When no hook is attached, `summary.Metrics` is a minimal snapshot:

```go
MetricsSnapshot{
    Timestamp: time.Now(),
    Elapsed:   summary.Duration,
    Stages:    nil,
    Graph:     nil,
    Buffers:   nil,
}
```

The minimal snapshot lets callers read `.Timestamp` and `.Elapsed` without a nil check.

### Known limitation: `Effect` outcomes not reflected in `MetricsHook`

`Effect` does not currently call `hook.OnItem` per outcome. As a consequence, `summary.Metrics.Stages["my-effect"]` is empty even when an `Effect` named `"my-effect"` is in the pipeline. The per-effect counters live in `runCtx.effectStats` and feed `Outcome` derivation, but they do not surface as `StageMetrics` entries.

If you need per-stage effect counts for dashboards, use `summary.Outcome` plus `OnRetry` callbacks on the `RetryStrategy`:

```go
SNSPolicy := kitsune.EffectPolicy{
    Required: true,
    Retry: kitsune.RetryUpTo(3, kitsune.FixedBackoff(50*time.Millisecond)).
        WithOnRetry(func(attempt int, err error, _ time.Duration) {
            metrics.Counter("publish.retries", "stage", "publish").Inc()
        }),
}
```

Wiring `Effect → hook.OnItem` so MetricsHook reflects effect counts is a tracked follow-up; not in v1.

---

## Async runs via `RunAsync`

`Runner.RunAsync(ctx)` returns a `*RunHandle` that exposes the same `(RunSummary, error)` shape as synchronous `Run`:

```go
handle := runner.RunAsync(ctx)

// Block until done:
summary, err := handle.Wait()

// Non-blocking accessors:
if h := handle.Done(); h != nil { /* select on h */ }
err := handle.Err()           // nil before completion
s   := handle.Summary()       // zero RunSummary before completion

// Pause/resume sources via the gate that RunAsync attached:
handle.Pause()
handle.Resume()
```

`Wait` blocks until the pipeline completes; `Done` returns a channel closed at completion; `Err` and `Summary` are non-blocking accessors safe to call from multiple goroutines.

Finalizers attached to the runner run inside the `RunAsync` goroutine before `Done` is closed. So `handle.Wait()` returns *after* finalizers have completed; `handle.Summary().FinalizerErrs` is fully populated.

---

## What's not included

These were considered and explicitly excluded from v1:

- **`Effect → hook.OnItem` integration.** As above; tracked as a follow-up.
- **Per-stage `EffectStats` in `RunSummary`.** The internal `runCtx.effectStats` is not exported. If users want per-effect counts, they currently rely on `OnRetry` callbacks or wrapping `Effect` in a stage that bumps a counter. A future version may surface a structured `summary.EffectStats` map.
- **Finalizer-driven `Outcome` mutation.** Finalizers cannot change `Outcome`. The intent: finalizers observe; they do not decide. If you want to re-classify a run based on a side check, do it in the calling code after `Run` returns.
- **Cancellation of finalizers.** All registered finalizers run unconditionally after the pipeline completes. There is no "halt on first finalizer error" mode. If you want short-circuit behaviour, build it into the finalizer functions themselves.

---

## Where to next

- [Operator catalog → Run Summary](operators.md#run-summary) for the type and method signatures.
- [Side effects guide](side-effects.md) for how `Effect` populates the per-effect counters that feed `Outcome`.
- [Dev iteration guide](dev-iteration.md) for how `WithDevStore` interacts with replayed effect counters.
