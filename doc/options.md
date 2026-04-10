# Stage Options

`StageOption` values tune individual stages. Pass any number of them as trailing arguments to operators like `Map`, `FlatMap`, `ForEach`, `Batch`, and so on.

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

**Applies to:** `Map`, `FlatMap`, `MapWith`, `MapWithKey`, `ForEach`, `DeadLetter`, `CircuitBreaker`

Set the per-stage error handler. When a stage function returns a non-nil error, the handler decides what happens next.

Default: `Halt()` (stop the pipeline and return the error from `Run`).

| Handler | Behaviour |
|---|---|
| `Halt()` | Stop the pipeline immediately. **Default.** |
| `Skip()` | Drop the failed item and continue. |
| `Return(v)` | Emit `v` in place of the failed item and continue. |
| `Retry(n, backoff)` | Retry up to `n` times with the given backoff. |
| `RetryThen(n, backoff, h)` | Retry, then apply handler `h` if all attempts fail. |
| `DeadLetter(fn, ...)` | Route successes to one pipeline, exhausted failures to another. |

```go
out := kitsune.Map(items, callAPI,
    kitsune.OnError(kitsune.Retry(3, kitsune.ExponentialBackoff(
        100*time.Millisecond,
        5*time.Second,
    ))),
)
```

Backoff helpers: `FixedBackoff(d)`, `ExponentialBackoff(min, max)`, `JitteredBackoff(min, max)`.

---

## `Timeout(d time.Duration)`

**Applies to:** `Map`, `FlatMap`, `MapWith`, `FlatMapWith`

Cancel a stage function's context after `d`. If the function does not return within the deadline, its context is cancelled and the item fails with `context.DeadlineExceeded`.

Each retry attempt (when combined with `Retry`) gets a fresh timeout.

Panics at pipeline construction time if used on any other operator.

```go
out := kitsune.Map(items, callSlowAPI,
    kitsune.Timeout(500*time.Millisecond),
    kitsune.OnError(kitsune.Skip()),
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

Flush a partial batch after `d` even if it has not reached the size limit. Without this option, a partial batch at the end of the stream is flushed only when the input closes.

```go
batched := kitsune.Batch(enriched, 500,
    kitsune.BatchTimeout(2*time.Second),
)
```

Useful for latency-sensitive pipelines where a full batch might never arrive within an acceptable time window.

---

## `WithClock(c Clock)`

**Applies to:** `Ticker`, `Interval`, `Timer`, `Batch`, `Throttle`, `Debounce`, `Sample`, `SessionWindow`, `Timestamp`, `TimeInterval`

Substitute a deterministic clock for testing. Instead of calling `time.Now()` or `time.Sleep()`, the stage uses the provided `Clock` implementation.

```go
clock := testkit.NewTestClock()

batched := kitsune.Batch(in, 100,
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

**Applies to:** `Dedupe`, `DedupeBy`, `Distinct`, `DistinctBy`, `ExpandMap`

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
