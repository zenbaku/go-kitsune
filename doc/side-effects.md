# Side effects with `Effect` and `TryEffect`

`Effect` is the operator for actions that change state outside the pipeline: publishing to a queue, writing to a database, calling an external API, anything where "did it actually happen?" matters. It models retry, per-attempt timeout, required-vs-best-effort outcomes, and idempotency intent in one place.

This page is the hands-on guide. For the type-by-type reference, see [Effects in the operator catalog](operators.md#effects).

---

## Why `Effect` instead of `Map`?

A plain `Map` that publishes to SNS works, mostly. But it has structural problems:

```go
acks := kitsune.Map(messages, func(ctx context.Context, m Message) (string, error) {
    return sns.Publish(ctx, m.Body)
})
```

- An error from `sns.Publish` halts the pipeline by default. To recover, every caller has to add `OnError(...)` per item.
- There is no first-class notion of "this stage might fail half its items, but the run is still successful overall."
- The graph reads as a generic transformation; an inspector or run summary cannot distinguish "this stage published 1000 messages with 3 retries" from "this stage doubled some integers."
- Retries on a transient SNS hiccup require manual code inside the function.
- There is no way to validate the wiring without actually publishing.

`Effect` packages all of that:

```go
out := kitsune.Effect(messages, publish,
    kitsune.EffectPolicy{
        Required:       true,
        Retry:          kitsune.RetryUpTo(3, kitsune.ExponentialBackoff(100*time.Millisecond, 2*time.Second)),
        AttemptTimeout: 5 * time.Second,
    },
)
```

Every input produces exactly one `EffectOutcome[I, R]`. The outcome's `Applied` field tells you whether the function returned successfully on this run; `Result` carries the value; `Err` carries the last error after retries. The pipeline does not error out: downstream decides what to do with successes and failures.

---

## Anatomy of an `EffectOutcome`

```go
type EffectOutcome[I, R any] struct {
    Input   I
    Result  R
    Err     error
    Applied bool
}
```

- **`Input`** is the original item. Always set.
- **`Result`** is the function's return value on success; the zero value of `R` on terminal failure or in dry-run mode.
- **`Err`** is `nil` on success; the last error after exhausting retries on terminal failure; `nil` in dry-run mode.
- **`Applied`** is `true` only when the function returned successfully. Treat it as a hint, not a guarantee: on a per-attempt timeout the underlying side effect may have happened anyway, but `Applied` will be `false`.

The combinations:

| Scenario | `Applied` | `Err` | `Result` |
|---|---|---|---|
| Success on first attempt | `true` | `nil` | the return value |
| Success after retries | `true` | `nil` | the return value |
| Retries exhausted | `false` | last error | zero `R` |
| Non-retryable error | `false` | the error | zero `R` |
| Per-attempt timeout exhausted retries | `false` | timeout error | zero `R` |
| Dry run | `false` | `nil` | zero `R` |

The dry-run row is the interesting one: `Applied: false` and `Err: nil` together is unique to dry-run.

---

## Designing an `EffectPolicy`

`EffectPolicy` is a reusable bundle of `Effect` settings. Define one as a package-level value and share it across call sites:

```go
var SNSPolicy = kitsune.EffectPolicy{
    Required:       true,
    Retry:          kitsune.RetryUpTo(3, kitsune.ExponentialBackoff(100*time.Millisecond, 2*time.Second)),
    AttemptTimeout: 5 * time.Second,
}

var AuditPolicy = kitsune.EffectPolicy{
    Required:       false, // best-effort: an audit miss does not fail the run
    Retry:          kitsune.RetryUpTo(2, kitsune.FixedBackoff(50*time.Millisecond)),
    AttemptTimeout: 1 * time.Second,
}
```

`EffectPolicy` itself satisfies `EffectOption`, so passing one to `Effect` applies all of its non-zero fields at once. Per-call-site overrides come after:

```go
// Default SNSPolicy timeout is 5s; this call site needs 30s.
out := kitsune.Effect(messages, publish, SNSPolicy, kitsune.AttemptTimeout(30*time.Second))
```

Last-write-wins. Anything you can put in `EffectPolicy`, you can also override with a call-site option helper.

### Field-by-field

**`Required bool`**: if any required `Effect` has terminal failures, [`RunSummary.Outcome`](operators.md#run-summary) is `RunFailure`. The default when neither `Required()` nor `BestEffort()` is supplied is `Required: true`. Explicitly:

```go
kitsune.Effect(p, fn, kitsune.Required())   // failures → RunFailure
kitsune.Effect(p, fn, kitsune.BestEffort()) // failures → RunPartialSuccess (if no required failures)
```

When in doubt: required for things that must succeed (the queue publish that triggers downstream business logic), best-effort for things that are nice to have (audit logs, metrics emission, cache warming).

**`Retry RetryStrategy`**: reuses the same `RetryStrategy` value type as the standalone [`Retry[T]`](operators.md#retry) operator. The zero value performs a single attempt:

```go
RetryUpTo(n, backoff)        // at most n total attempts
RetryForever(backoff)        // unlimited (use sparingly inside Effect)
RetryStrategy{}.WithRetryable(predicate)  // restrict which errors retry
RetryStrategy{}.WithOnRetry(callback)     // log/metric per retry
```

Inside `Effect`, retries are synchronous and serial: a slow retry blocks downstream emission for the next item. To parallelize, place the `Effect` downstream of a fan-out operator, or set `Concurrency` on a wrapper stage.

**`AttemptTimeout time.Duration`**: applied per attempt via `context.WithTimeout`. Distinct from the stage-level `Timeout(d) StageOption` which applies to the whole `Effect` stage's per-item processing. When both are set, the earlier deadline wins.

**`Idempotent bool` and `IdempotencyKey func(any) string`**: informational in v1. The fields are stored on the policy and propagated into stage metadata for future use by external idempotent backends. v1 does NOT de-duplicate retries against a store; the keys are a hint.

---

## Required vs best-effort outcomes

The `Required()` / `BestEffort()` choice decides how `RunSummary.Outcome` is computed:

```
no fatal pipeline error
  AND every required Effect succeeded
  AND no best-effort Effect failed
    → RunSuccess

no fatal pipeline error
  AND every required Effect succeeded
  AND at least one best-effort Effect failed
    → RunPartialSuccess

(any) required Effect failed
  OR fatal pipeline error
    → RunFailure
```

Concrete worked example: a pipeline that processes orders with two effects:

```go
publishOrder := kitsune.Effect(orders, sendToFulfillment,
    kitsune.Required(),    // fulfillment must happen, or the run failed
    kitsune.EffectPolicy{Retry: kitsune.RetryUpTo(5, kitsune.ExponentialBackoff(100*time.Millisecond, 5*time.Second))},
)

audited := kitsune.Effect(publishOrder, writeAuditLog,
    kitsune.BestEffort(),  // missing audit is recorded but does not fail the run
)
```

If the audit log silently fails for 5% of items but every fulfillment publish succeeds, `Outcome` is `RunPartialSuccess`. If even one fulfillment publish fails after retries, `Outcome` is `RunFailure` regardless of audit success.

You can inspect outcomes after a run:

```go
summary, err := runner.Run(ctx)
switch summary.Outcome {
case kitsune.RunSuccess:
    metrics.Counter("runs.success").Inc()
case kitsune.RunPartialSuccess:
    metrics.Counter("runs.partial").Inc()
    log.Warn("some best-effort effects failed: see RunSummary.Metrics")
case kitsune.RunFailure:
    metrics.Counter("runs.failure").Inc()
    return fmt.Errorf("run failed: %w", err)
}
```

---

## Routing successes and failures with `TryEffect`

`Effect` returns one pipeline. To split successes from failures cleanly, use `TryEffect`:

```go
ok, failed := kitsune.TryEffect(messages, publish,
    kitsune.EffectPolicy{Required: true, Retry: kitsune.RetryUpTo(3, kitsune.FixedBackoff(50*time.Millisecond))},
)

okRunner := ok.ForEach(func(_ context.Context, o kitsune.EffectOutcome[Message, string]) error {
    return acks.Record(o.Input.ID, o.Result)
})
failedRunner := failed.ForEach(func(_ context.Context, o kitsune.EffectOutcome[Message, string]) error {
    return deadLetter.Send(ctx, DeadLetter{
        ID:    o.Input.ID,
        Body:  o.Input.Body,
        Error: o.Err.Error(),
    })
})

runner, _ := kitsune.MergeRunners(okRunner, failedRunner)
runner.Run(ctx)
```

The split predicate is `outcome.Err != nil`: anything with a non-nil terminal error goes to `failed`. On the `ok` branch `Err` is always nil; on `failed` `Err` is always non-nil. **`Applied` follows `Err`** in normal runs but not under `DryRun` (see below).

Both branches must be consumed (same rule as `Partition` / `MapResult`); merging them via `MergeRunners` is the typical pattern.

---

## Dry runs

[`DryRun()`](options.md#dryrun) is a RunOption that turns every `Effect` into a no-op:

```go
runner.Run(ctx, kitsune.DryRun())
```

Under `DryRun`:
- The function passed to `Effect` is **never called**.
- Every outcome is `EffectOutcome{Input: item, Applied: false, Err: nil, Result: zero}`.
- Pure stages (`Map`, `Filter`, `Batch`) and stateful stages (`MapWith`) run normally.
- `RunSummary.Outcome` is `RunSuccess` (no failures recorded; no dry-run outcomes count toward the per-effect success/failure counters).

What this is for: validating wiring. You want to run the full pipeline through the source, transformations, and routing logic, without actually sending anything to SNS. Especially useful in CI smoke tests:

```go
func TestPipelineWiring(t *testing.T) {
    ctx := context.Background()
    runner := buildPipeline()
    summary, err := runner.Run(ctx, kitsune.DryRun())
    if err != nil {
        t.Fatalf("dry run failed: %v", err)
    }
    if summary.Outcome != kitsune.RunSuccess {
        t.Errorf("dry run outcome = %v, want RunSuccess", summary.Outcome)
    }
}
```

**Sharp edge with `TryEffect`**: under `DryRun`, every outcome has `Err: nil` (effects are skipped, not failed). So every outcome routes to the **`ok` branch**. If your `ok` consumer asserts `outcome.Applied == true`, that assertion fires under `DryRun` but breaks. Reach for `outcome.Err == nil` as the predicate when writing consumers, not `Applied`.

---

## Pairing with `RunSummary`

`RunSummary.Outcome` is derived from per-`Effect`-stage success/failure counters. Each `Effect` increments either `success` or `failure` per emitted outcome. Counts are kept in `runCtx.effectStats` keyed by stage ID.

A single failed required `Effect` propagates to `Outcome: RunFailure` even if every other stage succeeded. A single failed best-effort `Effect` propagates to `Outcome: RunPartialSuccess` if no required effects failed.

This means you can build alerting on the run:

```go
runner.WithFinalizer(func(ctx context.Context, s kitsune.RunSummary) error {
    switch s.Outcome {
    case kitsune.RunFailure:
        return alerts.Page(ctx, "pipeline run failed", s.Err)
    case kitsune.RunPartialSuccess:
        return alerts.Notify(ctx, "best-effort effect failures", s.Metrics)
    }
    return nil
})
```

See the [Run Summary section](features.md#run-summary) for finalizer ordering and `FinalizerErrs` semantics.

---

## Pairing with `DevStore`

`Effect` calls inside a `Segment` are bypassed when [`WithDevStore`](dev-iteration.md) is attached and a snapshot exists for that segment. This is usually what you want during dev iteration: you don't want each iteration to re-publish to SNS.

But if you want to genuinely fire the effect on every run, do one of:

1. Don't put `Effect` inside a `Segment`. The replay logic only triggers at segment boundaries.
2. Run without `WithDevStore` (production runs always do this).
3. Delete the segment's snapshot file before each run.

For reasoning about effect counters under replay: replayed segments register zero successes and zero failures because no `Effect` actually ran. So `RunSummary.Outcome` for a replay-only run is always `RunSuccess` (assuming no live segments fail).

For deeper coverage, see the [Dev iteration guide](dev-iteration.md#interaction-with-effect).

---

## Pairing with `Segment`

`Effect` and `TryEffect` are regular operators; you can wrap them in a `Segment` like any other transformation:

```go
publish := kitsune.Stage[Message, kitsune.EffectOutcome[Message, string]](
    func(p *kitsune.Pipeline[Message]) *kitsune.Pipeline[kitsune.EffectOutcome[Message, string]] {
        return kitsune.Effect(p, sendToSNS, SNSPolicy)
    },
)
publishSeg := kitsune.NewSegment("publish", publish)
```

Once wrapped:
- The graph shows `publish` as a named segment in `Describe()` and the inspector dashboard.
- Failures are still routed via `EffectOutcome` and counted in the per-effect counters.
- `WithDevStore` will capture/replay the segment's outcome stream (the `EffectOutcome` values themselves, not the underlying side effect).

If you want to capture only the *successful* part of a `TryEffect`, route the `ok` branch through a downstream `Segment` and let `WithDevStore` snapshot that.

---

## Parallelizing effects

`Effect` itself processes items serially per goroutine: a slow retry blocks the next item. To run effects in parallel, fan out upstream:

```go
// Round-robin distribute across 4 worker pipelines, run Effect on each.
branches := kitsune.Balance(messages, 4)
runners := make([]kitsune.Runnable, len(branches))
for i, branch := range branches {
    out := kitsune.Effect(branch, publish, SNSPolicy)
    runners[i] = out.ForEach(recordAck)
}
runner, _ := kitsune.MergeRunners(runners...)
```

Or use a hash-keyed balance for ordering-by-key:

```go
branches := kitsune.KeyedBalance(messages, 4, func(m Message) string { return m.PartitionKey })
// every message with the same key always lands on the same Effect goroutine
```

Be careful with `Concurrency(n)` on a `Map` stage that wraps an `Effect`: the wrapping `Map` parallelizes, but the inner `Effect` still does its retries serially per item. If you want full parallelism with retry, fan out at the pipeline level (above) and put one `Effect` per branch.

---

## Idempotency

`EffectPolicy.Idempotent` and `EffectPolicy.IdempotencyKey func(any) string` are informational in v1. The fields are stored on the policy and propagated into stage metadata. The operator does NOT use them to de-duplicate retries; that requires an external idempotent backend (or a future kitsune-level dedupe layer).

The recommended pattern today: use the key function to derive a deterministic key, and pass it to your effect function as part of the input or via context:

```go
type Job struct {
    ID   string
    Body string
}

func publish(ctx context.Context, j Job) (string, error) {
    return queue.SendIdempotent(ctx, queue.Message{
        DedupeKey: j.ID, // queue-level idempotency, not kitsune-level
        Body:      j.Body,
    })
}

kitsune.Effect(jobs, publish,
    kitsune.EffectPolicy{
        Required:   true,
        Idempotent: true,
        IdempotencyKey: func(item any) string {
            return item.(Job).ID
        },
        Retry: kitsune.RetryUpTo(3, kitsune.FixedBackoff(100*time.Millisecond)),
    },
)
```

Future versions may use these fields to skip redundant retries against a configured store.

---

## Common patterns

### Dead-letter queue

```go
ok, failed := kitsune.TryEffect(messages, publish, SNSPolicy)
runners := []kitsune.Runnable{
    ok.ForEach(recordSuccess),
    failed.ForEach(func(ctx context.Context, o kitsune.EffectOutcome[Message, string]) error {
        return dlq.Send(ctx, dlq.Letter{Input: o.Input, Error: o.Err.Error()})
    }),
}
runner, _ := kitsune.MergeRunners(runners...)
```

### Observability per effect

```go
publishOutcomes := kitsune.Effect(messages, publish,
    kitsune.EffectPolicy{
        Required: true,
        Retry: kitsune.RetryUpTo(3, kitsune.FixedBackoff(50*time.Millisecond)).
            WithOnRetry(func(attempt int, err error, wait time.Duration) {
                metrics.Counter("publish.retries").Inc()
                log.Warn("retrying publish", "attempt", attempt, "err", err, "wait", wait)
            }),
    },
)
```

### Wiring validation in CI

```go
//go:build dryrun
// (build tag so production builds never accidentally enable it)

func main() {
    runner := buildPipeline()
    summary, err := runner.Run(context.Background(), kitsune.DryRun())
    if err != nil || summary.Outcome != kitsune.RunSuccess {
        log.Fatalf("wiring check failed: outcome=%v err=%v", summary.Outcome, err)
    }
    log.Info("pipeline graph validates")
}
```

### Required + best-effort, separated

```go
// Required: must succeed; failures fail the run.
publish := kitsune.Effect(messages, sendToFulfillment, kitsune.Required())

// Best-effort downstream: failures are tolerated.
audited := kitsune.Effect(publish, writeAuditLog, kitsune.BestEffort())

runner := audited.ForEach(handleFinalAck)
```

---

## What `Effect` is NOT for

- **In-pipeline transformations.** Use `Map`. `Effect` is for state changes outside the pipeline.
- **Per-item retries on a `Map`.** Use `OnError(RetryMax(n, backoff))` on the `Map` stage. `Effect` re-runs the *attempt* with retry; per-item `OnError(RetryMax)` re-runs the *function* on the same item.
- **Pipeline-source reconnection.** Use the standalone [`Retry[T]`](operators.md#retry) operator. `Retry[T]` resubscribes to the upstream pipeline; `Effect` retries individual function calls.
- **Composing two effectful stages serially.** Just chain them with regular `Map` or another `Effect`. `Effect` does not require that effects be the only stages between sources and sinks.

---

## What's not yet implemented

These are documented in the spec but deferred to a future release:

- **Idempotency-key-driven deduplication.** Stored on `EffectPolicy` but not yet wired to a backend.
- **`DryRunNoop()` per-effect override.** Dropped from v1 because it adds no observable behaviour over the default `DryRun` skip. Can be added if a use case for opting *out* of the default skip emerges.

---

## Where to next

- [Operator catalog → Effects](operators.md#effects) for the type and option signatures.
- [Operator catalog → Run Summary](operators.md#run-summary) for the `Outcome` derivation rules.
- [Dev iteration guide](dev-iteration.md) for `Effect`'s interaction with `WithDevStore`.
- [examples/effect](https://github.com/zenbaku/go-kitsune/tree/main/examples/effect) for a runnable end-to-end demo of `Effect` + `TryEffect` with retry, attempt timeout, and dead-letter routing.
