# Error Handling in Kitsune

Kitsune provides two orthogonal error-handling primitives that operate at different scopes:

- **`OnError`** handles errors produced by a single item's invocation of the stage function.
- **`Supervise`** handles the crash of the entire stage goroutine.

They can be used independently or together. When both are present on the same stage, they form a two-layer filter with a well-defined evaluation order.

---

## Two error layers: per-item and per-stage

### What `OnError` handles

`OnError` is a `StageOption` that attaches an `ErrorHandler` to a stage. The handler is evaluated each time the stage's transformation function returns an error.

Handlers and their outcomes:

| Handler | Outcome |
|---|---|
| `Halt()` | Return the error; stop processing (default). |
| `ActionDrop()` / `Skip()` | Drop the item and continue with the next one. |
| `Return(val)` | Replace the failed item with `val` and continue. Composable; see type safety note below. |
| `TypedReturn[O](val)` | Same as `Return`, but `O` is checked against the stage output type at compile time. |
| `RetryMax(n, backoff)` | Retry the item up to `n` times; halt if all retries fail. |
| `RetryThen(n, backoff, fallback)` | Retry up to `n` times; if exhausted, delegate to `fallback`. |
| `RetryIf(pred, backoff)` | Retry while `pred(err)` is true; halt otherwise. |
| `RetryIfThen(pred, backoff, fallback)` | Retry while `pred(err)` is true; otherwise delegate to `fallback`. |

`OnError` consumes the item: whether it drops it, replaces it, or exhausts retries, the item is resolved before the next one is processed. The stage loop continues.

**`Return(val)` type safety:** `ErrorHandler` is not parameterized on the stage's output type. The type of `val` is inferred at the call site and is not checked against the stage's output type at compile time. If they do not match, the substitution silently fails at runtime: the original error is propagated as though `Halt` had been used. To avoid this, use `TypedReturn[O](val)` instead: it returns a `StageOption` directly and the type `O` is checked against the stage output type at compile time. `TypedReturn` cannot be composed inside `RetryThen`; for retry chains, use `Return` with a typed variable (`var fallback MyType`).

`OnError` is only triggered for **per-item errors** returned by the stage function. It does not fire for panics or context cancellations.

### What `Supervise` handles

`Supervise` is a `StageOption` that wraps the **entire stage loop**. When the loop returns a non-nil error (or panics, depending on the policy), `Supervise` decides whether to restart the stage goroutine.

Convenience constructors:

| Constructor | Trigger | Panics |
|---|---|---|
| `RestartOnError(n, backoff)` | Errors | Still propagate |
| `RestartOnPanic(n, backoff)` | Panics only | Restart on panic; errors halt |
| `RestartAlways(n, backoff)` | Errors and panics | Restart on both |

For finer control, construct `SupervisionPolicy` directly:

```go
policy := kitsune.SupervisionPolicy{
    MaxRestarts: 10,
    Window:      1 * time.Minute, // reset restart counter after 1 min without a crash
    Backoff:     kitsune.JitteredBackoff(50*time.Millisecond, 2*time.Second),
    OnPanic:     kitsune.PanicRestart,
}
```

`Supervise` is stage-level: it does not replay individual items. When the stage restarts, it resumes reading from wherever the input channel's read pointer is at that moment.

---

## Evaluation order when both are present

When a stage has both `OnError` and `Supervise`, the evaluation order is:

```
fn(item) returns error
    ↓
OnError handler evaluates
    ├── ActionDrop / Return / (retry succeeded) → item resolved; stage loop continues
    │                                             Supervise is NOT triggered
    └── Halt (explicit, or after retry exhaustion) → inner loop exits with error
                                                       ↓
                                                   Supervise evaluates
                                                       ├── budget remaining → restart stage loop
                                                       └── budget exhausted → error propagates; Run returns
```

**Key rule:** `Supervise` only sees an error when `OnError`'s final decision is `Halt`. If `OnError` drops, replaces, or successfully retries an item, the stage loop never exits, and `Supervise` is never triggered.

### What `Supervise` does NOT see

- Items dropped by `ActionDrop()`.
- Items replaced by `Return(val)`.
- Items that succeed on a retry.

`Supervise` only triggers a restart when the stage loop has actually crashed.

---

## Common combination patterns

### Pattern 1: Retry-then-restart

**Goal:** retry transient errors quickly per item; if the stage keeps failing (e.g., a downstream service is down), restart the whole stage to re-establish connections.

```go
out := kitsune.Map(events, callService,
    kitsune.OnError(kitsune.RetryThen(3,
        kitsune.ExponentialBackoff(100*time.Millisecond, 2*time.Second),
        kitsune.Halt(), // after 3 retries, let Supervise decide
    )),
    kitsune.Supervise(kitsune.RestartOnError(
        5,
        kitsune.ExponentialBackoff(1*time.Second, 30*time.Second),
    )),
)
```

**Walkthrough:**

1. An item fails. `OnError` retries it up to 3 times with exponential backoff.
2. After 3 failed attempts, `RetryThen` falls back to `Halt()`. The stage loop exits with an error.
3. `Supervise` catches the exit and restarts the stage loop (up to 5 times).
4. The restarted loop reads remaining items from the same input channel.
5. After 5 restarts, the error propagates and `Run` returns it.

**When to use:** stage functions that call external services over persistent connections. A single item failing is retried; repeated failures indicate the connection itself is broken and the stage should restart.

### Pattern 2: Skip transient errors, restart on fatal errors

**Goal:** silently skip items that cause known-transient errors (e.g., timeouts); restart the whole stage on unexpected errors that may have corrupted the stage's internal state.

```go
out := kitsune.Map(events, callService,
    kitsune.OnError(kitsune.RetryIfThen(
        func(err error) bool { return errors.Is(err, context.DeadlineExceeded) },
        kitsune.FixedBackoff(500*time.Millisecond),
        kitsune.Halt(), // non-timeout errors: let Supervise restart the stage
    )),
    kitsune.Supervise(kitsune.RestartOnError(5, kitsune.FixedBackoff(5*time.Second))),
)
```

**Walkthrough:**

1. An item times out. `RetryIfThen` retries it (predicate is true).
2. An item fails with a non-timeout error. `RetryIfThen` falls back to `Halt()` (predicate is false). `Supervise` restarts the stage.

**Variant: drop non-transient errors instead of restarting:**

Replace `Halt()` with `ActionDrop()` if you want non-timeout errors to drop the item silently. In that case, `Supervise` would never trigger for item-level errors. Combine `Supervise` with `RestartOnPanic` to still handle unexpected panics.

### Pattern 3: Panic-only supervision with per-item retries

**Goal:** retry per-item API errors locally; restart the stage only if its goroutine panics.

```go
out := kitsune.Map(events, callService,
    kitsune.OnError(kitsune.RetryMax(2, kitsune.FixedBackoff(time.Second))),
    kitsune.Supervise(kitsune.RestartOnPanic(3, kitsune.FixedBackoff(time.Second))),
)
```

**Walkthrough:**

1. `RetryMax(2)` retries each failing item up to 2 times. After 2 failed attempts, `Halt()` is the implicit fallback. The stage loop exits with an error.
2. `RestartOnPanic` has `PanicOnly: true`. Regular errors (including after retry exhaustion) propagate immediately without triggering a restart.
3. If the stage goroutine panics (e.g., a nil pointer dereference), `RestartOnPanic` restarts it.

**When to use:** stages where you want aggressive error handling per item but do not want to mask programming bugs behind supervisor restarts.

---

## The distinction: per-item vs. per-stage

| Dimension | `OnError` | `Supervise` |
|---|---|---|
| Granularity | Single item | Entire stage run |
| Trigger | `fn` returns an error | Stage loop exits with error or panics |
| Consumes the item? | Yes: drops, replaces, or exhausts retries | No: item is lost; restart resumes from the next queued item |
| Affects the channel graph? | No | No: same input/output channels |
| Restart budget | None (attempt counter resets per item) | `MaxRestarts` (optionally reset by `Window`) |
| Context cancellation | Bypasses handler; loop exits | Bypasses restart; `Supervise` checks `ctx.Err()` before each restart |

---

## What happens to in-flight items on restart

When `Supervise` restarts a stage, the input channel is not rewound. Items that were already dequeued from the input channel during the crashed run are not re-processed. The restarted loop begins reading from wherever the channel's read pointer is at the moment of restart.

Stages are not transactions. If idempotency matters, implement it in the stage function or use the `CacheBy` option.

---

## Observability

Both layers produce observable events:

- `Hook.OnItem` fires for each item, including items that fail, are dropped, or are replaced. Use this to count per-item error rates.
- `SupervisionHook.OnStageRestart` fires each time `Supervise` triggers a restart. Use this to alert on stage instability.
- `MultiHook` combines multiple hooks:

```go
h := kitsune.MultiHook(metricsHook, kitsune.LogHook(slog.Default()))
_, err := p.Run(ctx, kitsune.WithHook(h))
```

`StageError` wraps the terminal error with the stage name and attempt count:

```go
var se *kitsune.StageError
if errors.As(err, &se) {
    log.Printf("stage %q failed after %d attempt(s): %v", se.Stage, se.Attempt+1, se.Cause)
}
```

---

## FAQ

**Q: Does `OnError(Halt())` + `Supervise` restart on every item failure?**

Yes. Each time an item fails and the error handler returns `Halt`, the stage loop exits with an error, and `Supervise` triggers a restart (if the budget allows). For high-failure-rate stages, combine with a backoff to avoid a tight restart loop.

**Q: Can I use `OnError(ActionDrop())` and `Supervise` together?**

Yes, but `Supervise` will never trigger for dropped items. Use this combination when you want to drop individual bad items but still restart the stage on unexpected errors that break the loop entirely (e.g., a panic or a nil-pointer dereference). Set `RestartOnPanic` instead of `RestartOnError` to express this precisely.

**Q: What if both `OnError` and `Supervise` are set but the error is `context.Canceled`?**

Context cancellation bypasses both layers. The stage loop checks `ctx.Err()` and exits cleanly; `OnError` is not invoked, and `Supervise` checks `ctx.Err()` before each restart attempt and stops immediately. The pipeline exits cleanly.

**Q: Does `Supervise` restart the source pipeline too?**

No. The input channel is shared and is not reset. Only the stage goroutine itself is restarted. The upstream source continues producing items into the channel. To reconnect a source that disconnects, use the [`Retry`](operators.md#retry) operator on the source pipeline.

**Q: Can `Supervise` be used on all operators?**

`Supervise` applies to `Map`, `FlatMap`, `MapWith`, and `ForEach`. It is silently ignored on other operators. Check the [Stage Options Reference](operators.md#options) for the full applicability table.
