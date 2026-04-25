# Stage Options

`StageOption` values tune individual stages. Pass any number of them as trailing arguments to operators like `Map`, `FlatMap`, `ForEach`, `Batch`, and so on.

When the same option is passed more than once, the last value wins: options are applied in order and each call overwrites any previous setting for the same field. For example, `Buffer(8), Buffer(64)` is equivalent to `Buffer(64)`.

```go
out := kitsune.Map(in, fn,
    kitsune.Concurrency(20),
    kitsune.Ordered(),
    kitsune.Buffer(64),
    kitsune.WithName("enrich"),
)
```

---

## `Concurrency(n int)`

**Applies to:** `Map`, `FlatMap`, `MapWith`, `FlatMapWith`, `MapWithKey`, `FlatMapWithKey`, `ForEach`

Spin up `n` goroutines reading from the same input channel. Each goroutine runs the stage function independently, so throughput scales linearly for I/O-bound work.

Default: `1` (sequential).

```go
enriched := kitsune.Map(orders, callEnrichAPI, kitsune.Concurrency(20))
```

Output order is not preserved by default: workers emit results as they finish. Add `Ordered()` to re-sequence output without sacrificing throughput.

Starting points: 10-20 for I/O-bound stages; `runtime.NumCPU()` for CPU-bound work. See the [Tuning guide](tuning.md) for trade-offs.

---

## `Ordered()`

**Applies to:** `Map`, `FlatMap`

Emit results in the same order items arrived, even when `Concurrency > 1`. Workers still run in parallel; Kitsune resequences output before passing it downstream.

No effect when `Concurrency` is 1 (or not set).

```go
enriched := kitsune.Map(orders, callEnrichAPI,
    kitsune.Concurrency(20),
    kitsune.Ordered(),
)
```

---

## `Buffer(n int)`

**Applies to:** All operators

Set the channel buffer size between this stage and the next. Larger buffers smooth over burst throughput differences between adjacent stages at the cost of memory and latency.

Default: `16`.

```go
// Large buffer absorbs bursts from a bursty upstream
out := kitsune.Map(in, fn, kitsune.Buffer(512))

// Unbuffered: maximum backpressure sensitivity
out := kitsune.Map(in, fn, kitsune.Buffer(0))
```

---

## `Overflow(s OverflowStrategy)`

**Applies to:** `Map`, `FlatMap`, `Filter`, and most transforms

Control what happens when the output buffer is full.

| Strategy | Behaviour |
|---|---|
| `Block` | Wait until space is available. Backpressure propagates upstream. **Default.** |
| `DropNewest` | Discard the incoming item. Pipeline continues without blocking. |
| `DropOldest` | Evict the oldest buffered item to make room for the new one. |

```go
// Metrics sampling: drop excess, never block
sampled := kitsune.Map(events, recordMetric,
    kitsune.Buffer(128),
    kitsune.Overflow(kitsune.DropNewest),
)
```

`DropNewest` and `DropOldest` are appropriate when losing items is acceptable: telemetry sampling, live dashboards, audio/video frames. Never use them for data that must not be lost.

---

## `OnError(h ErrorHandler)`

**Applies to:** `Map`, `FlatMap`, `MapWith`, `MapWithKey`, `ForEach`, `CircuitBreaker`

Set the per-stage error handler. When a stage function returns a non-nil error, the handler decides what happens next.

Default: `Halt()` (stop the pipeline and return the error from `Run`).

| Handler | Behaviour |
|---|---|
| `Halt()` | Stop the pipeline immediately. **Default.** |
| `ActionDrop()` | Drop the failed item and continue. |
| `Return(v)` | Emit `v` in place of the failed item and continue. Composable; see type safety note below. |
| `TypedReturn[O](v)` | Same as `Return`, but `O` is checked against the stage output type at compile time. Not composable in `RetryThen`. |
| `RetryMax(n, backoff)` | Retry up to `n` times with the given backoff. |
| `RetryThen(n, backoff, h)` | Retry, then apply handler `h` if all attempts fail. |

```go
out := kitsune.Map(items, callAPI,
    kitsune.OnError(kitsune.RetryMax(3, kitsune.ExponentialBackoff(
        100*time.Millisecond,
        5*time.Second,
    ))),
)
```

Backoff helpers: `FixedBackoff(d)`, `ExponentialBackoff(min, max)`, `JitteredBackoff(min, max)`.

**`Return(v)` type safety:** `ErrorHandler` is not parameterized on the stage's output type. If the type of `v` does not match the stage's output type, the substitution silently fails at runtime and the original error is propagated as though `Halt` had been used. For a compile-time guarantee, use `TypedReturn[O](v)` as a `StageOption` directly; the type `O` is checked against the stage output at the call site. `TypedReturn` cannot be composed inside `RetryThen`; for retry chains use `Return` with a typed variable. See the [TypedReturn section in operators.md](operators.md#typedreturn) for details.

---

## `Timeout(d time.Duration)`

**Applies to:** `Map`, `FlatMap`, `MapWith`, `FlatMapWith`

Cancel a stage function's context after `d`. If the function does not return within the deadline, its context is cancelled and the item fails with `context.DeadlineExceeded`.

Each retry attempt (when combined with `RetryMax`) gets a fresh timeout.

Panics at pipeline construction time if used on any other operator.

```go
out := kitsune.Map(items, callSlowAPI,
    kitsune.Timeout(500*time.Millisecond),
    kitsune.OnError(kitsune.ActionDrop()),
)
```

---

## `WithName(s string)`

**Applies to:** All operators

Label the stage. The name appears in:

- `MetricsHook` counters and latency histograms
- `LogHook` structured log output
- OpenTelemetry span names (via `kotel`)
- `Pipeline.Describe()` graph snapshots
- The live inspector dashboard

```go
out := kitsune.Map(orders, enrich, kitsune.WithName("enrich-customer"))
```

If not set, Kitsune generates a name from the function name via reflection.

---

## `BatchTimeout(d time.Duration)`

**Applies to:** `Batch`, `MapBatch`

Flush a partial batch after `d` even if the count or measure threshold has not been reached. Without this option, a partial batch at the end of the stream is flushed only when the input closes.

```go
batched := kitsune.Batch(enriched,
    kitsune.BatchCount(500),
    kitsune.BatchTimeout(2*time.Second),
)
```

Useful for latency-sensitive pipelines where a full batch might never arrive within an acceptable time window.

---

## `WithClock(c Clock)`

**Applies to:** `Ticker`, `Timer`, `Batch`, `Throttle`, `Debounce`, `Sample`, `SessionWindow`, `Timestamp`, `TimeInterval`

Substitute a deterministic clock for testing. Instead of calling `time.Now()` or `time.Sleep()`, the stage uses the provided `Clock` implementation.

```go
clock := testkit.NewTestClock()

batched := kitsune.Batch(in,
    kitsune.BatchCount(100),
    kitsune.BatchTimeout(5*time.Second),
    kitsune.WithClock(clock),
)

// Advance time in the test without sleeping
clock.Advance(6 * time.Second)
```

Use `testkit.NewTestClock()` for deterministic, sleep-free tests of any time-sensitive stage. See the [Testing guide](testing.md) for full examples.

---

## `Supervise(policy SupervisionPolicy)`

**Applies to:** `Map`, `FlatMap`, `MapWith`, `ForEach`

Restart the stage goroutine automatically after a failure, instead of propagating the error.

Build a policy with one of these convenience constructors:

| Constructor | Behaviour |
|---|---|
| `RestartOnError(n, backoff)` | Restart up to `n` times on error. Panics still propagate. |
| `RestartOnPanic(n, backoff)` | Restart up to `n` times on panic. Errors still halt. |
| `RestartAlways(n, backoff)` | Restart up to `n` times on either error or panic. |

```go
out := kitsune.Map(events, processEvent,
    kitsune.Supervise(kitsune.RestartOnError(
        5,
        kitsune.ExponentialBackoff(100*time.Millisecond, 10*time.Second),
    )),
)
```

`SupervisionPolicy` can also be constructed directly for finer control:

```go
policy := kitsune.SupervisionPolicy{
    MaxRestarts: 10,
    Window:      1 * time.Minute, // reset counter after 1 min without crash
    Backoff:     kitsune.JitteredBackoff(50*time.Millisecond, 2*time.Second),
    OnPanic:     kitsune.PanicRestart,
}
```

**Using `OnError` and `Supervise` together:** `OnError` is evaluated first, per item. Only when the error handler's final decision is `Halt` (including after retry exhaustion) does `Supervise` see the error and decide whether to restart the stage. Items that are dropped or replaced by `OnError` never reach `Supervise`. See the [Error Handling guide](error-handling.md) for worked examples.

---

## `CacheBy(keyFn func(T) string, opts ...CacheOpt)`

**Applies to:** `Map` only

Enable TTL-based result caching keyed by `keyFn(item)`. On a cache hit, the stage function is skipped entirely and the cached result is emitted directly.

Requires a cache backend: either pass `CacheBackend(c)` as a `CacheOpt` or provide a runner-level default via `WithCache` at run time.

```go
enriched := kitsune.Map(orders, fetchCustomer,
    kitsune.CacheBy(func(o Order) string { return o.CustomerID },
        kitsune.CacheTTL(5*time.Minute),
    ),
)
```

`CacheOpt` values:

| Option | Description |
|---|---|
| `CacheTTL(d)` | Override the TTL for this stage. Falls back to the runner's default. |
| `CacheBackend(c)` | Override the cache backend for this stage. Falls back to the runner's default. |

---

## `WithDedupSet(s DedupSet)`

**Applies to:** `Dedupe`, `DedupeBy`, `ExpandMap`

Provide an external deduplication backend. The default is an in-process `MemoryDedupSet` that does not survive restarts and is not shared across pipeline instances.

Use an external backend (Redis, Bloom filter) when:

- The pipeline restarts and must not reprocess already-seen items.
- Multiple pipeline instances run in parallel and must share seen state.

```go
redisSet := kredis.NewDedupSet(rdb, "pipeline:seen", 24*time.Hour)

deduped := kitsune.Dedupe(events,
    kitsune.WithDedupSet(redisSet),
)
```

---

## `VisitedBy(keyFn func(T) string)`

**Applies to:** `ExpandMap`

Enable cycle detection during graph walks. `keyFn` extracts a string key from each item; any item whose key has already been seen is dropped, along with its entire subtree.

Uses `MemoryDedupSet` by default. Combine with `WithDedupSet` for a persistent or shared visited set.

```go
nodes := kitsune.ExpandMap(roots, fetchChildren,
    kitsune.VisitedBy(func(n Node) string { return n.ID }),
)
```

Silently ignored on all operators other than `ExpandMap`.

---

## `MaxDepth(n int)`

**Applies to:** `ExpandMap`

Limit BFS expansion to at most `n` levels below the root items.

- `MaxDepth(0)` emits only the root items and performs no expansion.
- `MaxDepth(1)` emits roots plus their immediate children.
- Default is unlimited.

When the depth cap is reached the stage stops enqueueing children but continues to drain items already in the BFS queue. The output channel closes normally; no error is returned.

```go
// Walk at most 3 levels below each root.
nodes := kitsune.ExpandMap(roots, fetchChildren,
    kitsune.MaxDepth(3),
)
```

Combine with [`MaxItems`](#maxitemsn-int) to cap both depth and total output. Whichever limit fires first wins.

Silently ignored on all operators other than `ExpandMap`.

---

## `MaxItems(n int)`

**Applies to:** `ExpandMap`

Limit total items emitted by an `ExpandMap` stage to `n`. When the stage has emitted `n` items it stops enqueueing children, cancels its currently-running inner pipeline, and closes its output channel normally; no error is returned, matching the semantics of `Take(n)` downstream.

Unlike a downstream `Take(n)`, `MaxItems` stops the BFS queue from growing in memory after the cap is reached. Prefer `MaxItems` over `Take` for graphs with high fan-out.

```go
// Emit at most 1 000 items, regardless of tree shape.
nodes := kitsune.ExpandMap(roots, fetchChildren,
    kitsune.MaxItems(1_000),
)
```

`MaxItems(0)` (or any non-positive value) is ignored; use `MaxDepth(0)` if you want roots-only behaviour.

Combine with [`MaxDepth`](#maxdepthn-int) to cap both depth and total output. Whichever limit fires first wins.

Silently ignored on all operators other than `ExpandMap`.

---

## `WithKeyTTL(d time.Duration)`

**Applies to:** `MapWithKey`, `FlatMapWithKey`

Evict a per-entity `Ref` from the internal key map after `d` of inactivity. When no item has arrived for a given key for longer than `d`, the entry is removed lazily on the next access; the next item for that key starts from the initial value.

Without this option, the key map grows without bound on high-cardinality streams (unique user IDs, session tokens, device IDs). Use `WithKeyTTL` to cap memory use when "seen in the last N minutes" is the right semantic.

`WithKeyTTL` is independent of `StateTTL` set on the `Key`:

| Option | What expires | When |
|---|---|---|
| `StateTTL(d)` (on `Key`) | The value stored inside a `Ref` | On the next `Get` after `d` since the last `Set` |
| `WithKeyTTL(d)` (StageOption) | The entire map entry holding the `Ref` | On the next item for any key, after `d` of inactivity for the expired key |

Default: `0` (disabled; entries persist for the lifetime of the run).

```go
var sessionKey = kitsune.NewKey[SessionState]("session", SessionState{})

kitsune.MapWithKey(events,
    func(e Event) string { return e.UserID },
    sessionKey,
    func(ctx context.Context, ref *kitsune.Ref[SessionState], e Event) (Result, error) {
        s, _ := ref.Get(ctx)
        // ... update s ...
        ref.Set(ctx, s)
        return Result{UserID: e.UserID}, nil
    },
    kitsune.WithKeyTTL(15*time.Minute), // evict idle users after 15 min
)
```

To apply the same TTL to all `MapWithKey` and `FlatMapWithKey` stages in a run without annotating each one, use the run-level option `WithDefaultKeyTTL(d)`. Per-stage `WithKeyTTL` takes precedence; `WithKeyTTL(0)` explicitly disables eviction for a stage even when a run-level default is set.

---

## `WithContextMapper[T](fn func(T) context.Context)`

**Applies to:** `Map`, `FlatMap`, `ForEach`

Extracts a per-item context from each item using `fn`, enabling per-item tracing or baggage propagation without requiring the item type to implement [`ContextCarrier`](../kitsune.go).

The returned context contributes values only (e.g. the active trace span). Cancellation and deadlines still come from the stage context, so pipeline shutdown and per-item `Timeout` behaviour are unaffected.

**When to use:** when your item type is a third-party struct (Kafka messages, protobuf types, stdlib types) that cannot implement `ContextCarrier`. For item types you own, implementing `ContextCarrier` is often simpler.

**Precedence:** when `WithContextMapper` is set, it takes precedence over `ContextCarrier`. If the item type implements both, only the mapper function is called.

**Fast-path note:** setting `WithContextMapper` disqualifies a stage from the micro-batching fast path. For tracing-intensive workloads this is negligible (span creation costs far exceed the routing overhead), but it is worth knowing if you are tuning throughput on a hot non-tracing stage.

```go
// QueueMessage is a third-party type; cannot implement ContextCarrier.
type QueueMessage struct {
    Body    string
    Headers map[string]string // carries W3C traceparent, etc.
}

msgs := kitsune.FromChan(queueCh)
processed := kitsune.Map(msgs, processMsg,
    kitsune.WithContextMapper(func(m QueueMessage) context.Context {
        // Extract the trace context from the message headers.
        return otel.GetTextMapPropagator().Extract(
            context.Background(), propagation.MapCarrier(m.Headers),
        )
    }),
)
```

If `fn` returns `nil`, the stage context is used unchanged; same behaviour as when `WithContextMapper` is not set.

---

## `BatchCount(n int)`

**Applies to:** `Batch`

Flush the current batch when it contains exactly `n` items. At least one of `BatchCount`, `BatchMeasure`, or `BatchTimeout` must be provided to `Batch`; the stage panics at construction time if none are set.

```go
// Flush every 200 items.
batched := kitsune.Batch(events, kitsune.BatchCount(200))

// Flush at 200 items OR after 1 second, whichever comes first.
batched := kitsune.Batch(events,
    kitsune.BatchCount(200),
    kitsune.BatchTimeout(time.Second),
)
```

---

## `BatchMeasure[T any](fn func(T) int, n int)`

**Applies to:** `Batch`

Flush the current batch when the cumulative value of `fn` across all buffered items reaches or exceeds `n`. `fn` is called for each item as it enters the buffer; the batch flushes as soon as the running total meets the threshold.

At least one of `BatchCount`, `BatchMeasure`, or `BatchTimeout` must be provided to `Batch`.

```go
// Flush when accumulated payload bytes reach 64 KiB.
batched := kitsune.Batch(messages,
    kitsune.BatchMeasure(func(m Message) int { return len(m.Payload) }, 64*1024),
)

// Combine with a timeout for low-throughput streams.
batched := kitsune.Batch(messages,
    kitsune.BatchMeasure(func(m Message) int { return len(m.Payload) }, 64*1024),
    kitsune.BatchTimeout(5*time.Second),
)
```

---

## `DedupeWindow(n int)`

**Applies to:** `Dedupe`, `DedupeBy`

Controls the scope of deduplication:

| Value | Behaviour |
|---|---|
| `0` | Global (default): all previously-seen keys are remembered for the lifetime of the pipeline run. |
| `1` | Consecutive only: an item is dropped only if it is identical to the immediately preceding item. |
| `n > 1` | Sliding window: an item is dropped if it appeared in the last `n` items. |

Without `DedupeWindow`, `Dedupe` and `DedupeBy` use global deduplication backed by an in-process `MemoryDedupSet`.

```go
// Global dedup (default): drop any value seen previously.
unique := kitsune.Dedupe(events)

// Consecutive-only dedup: suppress adjacent duplicates.
changes := kitsune.DedupeBy(statuses, func(s Status) string { return s.State },
    kitsune.DedupeWindow(1),
)

// Sliding window: drop any value seen in the last 100 items.
recent := kitsune.Dedupe(values, kitsune.DedupeWindow(100))
```

---

## `DryRun()`

**Applies to:** `Runner.Run`, `Runner.RunAsync`, and all terminal functions (as a `RunOption`)

When the runner is started with `DryRun()`, every [`Effect`](operators.md#effect) and [`TryEffect`](operators.md#tryeffect) skips its function and emits an `EffectOutcome` with `Applied: false` and no error. All pure stages (`Map`, `Filter`, `Batch`, etc.) and stateful stages (`MapWith`, `MapWithKey`) run normally. Use for validating pipeline graph wiring without producing externally-visible side effects.

```go
err := runner.Run(ctx, kitsune.DryRun())
```
