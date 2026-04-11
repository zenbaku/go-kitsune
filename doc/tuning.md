# Performance Tuning Guide

Kitsune pipelines run as a DAG of goroutines connected by bounded channels. The defaults work well for most pipelines: buffer size 16, single-goroutine stages, no batching. This guide explains when and how to deviate from those defaults.

---

## Overview

!!! tip "Measure first"
    The default `Buffer(16)` + `Concurrency(1)` handles most I/O pipelines at >1 M items/sec. Profile with `MetricsHook` or the Inspector before tuning; most "obvious" improvements don't move the needle.

Every stage in a pipeline has an input channel. When a stage finishes processing an item, it writes to the next stage's channel. If that channel is full, the writer blocks, which is backpressure. When the channel has room, the writer proceeds without waiting.

This model means:
- Slow downstream stages naturally slow down fast upstream stages (backpressure propagates).
- No goroutines are started until `Run()` is called (lazy execution).
- Context cancellation propagates to all stages cleanly.

The default buffer size of 16 (`engine.DefaultBuffer`) is intentionally modest. Most pipelines are I/O-bound and spend most of their time waiting, not queuing.

---

## Fast path and stage fusion

Kitsune has two internal execution shortcuts that can dramatically increase throughput for serial, hook-free pipelines: the **fast path** and **stage fusion**. Both are applied automatically when the pipeline meets certain conditions. Understanding them helps you avoid accidentally disabling them — and helps you diagnose throughput drops when you do.

### What the fast path is

By default, each item passes through a processing loop that:

- calls `time.Now()` before and after user code to measure latency
- wraps user code in `ProcessItem`, which handles error policies (retry, skip, return)
- calls `hook.OnItem(...)` for every item
- guards against context cancellation with a `select` on `ctx.Done()`

For serial, hook-free pipelines that use default error handling, all of this overhead is unnecessary. The **fast path** is a simplified loop that:

- reads items in micro-batches of up to 16 (one blocking receive, then up to 15 non-blocking non-blocking receives)
- passes items directly to user code with no hook calls, no `time.Now`, and no `ProcessItem` wrapper
- returns the first error immediately

Throughput improvements of 2–5x are common in CPU-bound pipelines. I/O-bound pipelines (HTTP, database) benefit less because I/O wait dominates.

### What stage fusion is

When a `Map → Filter → ForEach` chain is serial and hook-free, Kitsune can go further: it composes all three stages into **one goroutine** with no inter-stage channel hops. Items flow from source through Map and Filter directly into ForEach without ever being written to an intermediate `chan T`. This eliminates two channel sends and two goroutine handoffs per item.

Fusion is only possible for single-consumer chains. If two operators both consume the output of a `Map` stage, fusion cannot be used for that stage.

Operators that support fusion: `Map`, `Filter`. `FlatMap` has a fast path but does not fuse. `Batch`, windowing operators, and multi-input operators never fuse.

### Exact eligibility conditions

Both fast path and fusion require **all** of the following conditions to hold simultaneously:

| Condition | How to disable it (unintentionally) |
|---|---|
| Serial execution (`Concurrency(1)`) | `Concurrency(n)` with `n >= 2` |
| No supervision | `Supervise(...)` on the stage |
| Default error handler | `OnError(...)` on the stage |
| Block overflow | `Overflow(DropNewest)` or `Overflow(DropOldest)` |
| No per-item timeout | `Timeout(d)` on the stage |
| No hook at run time | `WithHook(...)` on `Runner.Run` (any non-`NoopHook`) |
| No pipeline-level error strategy | `WithErrorStrategy(...)` on `Runner.Run` |
| No cache (Map only) | `CacheBy(keyFn)` on the Map stage |
| Single consumer (fusion only) | Passing the same pipeline to two operators, or using `MergeRunners` with two ForEach on the same upstream |

### Inspecting optimisation status with `IsOptimized`

`Pipeline[T].IsOptimized(opts ...RunOption)` returns an `[]OptimizationReport` showing, for each stage, whether it would use the fast path and whether it would be fused. Call it after the full DAG (including the terminal `ForEach`) is constructed.

```go
src    := kitsune.FromSlice(records)
mapped := kitsune.Map(src, enrich)
runner := mapped.Filter(isValid).ForEach(store)

// Assert the chain stays on the fast path in your test suite:
for _, r := range mapped.IsOptimized() {
    if r.SupportsFastPath && !r.FastPath {
        t.Errorf("stage %s left fast path: %v", r.Name, r.Reasons)
    }
}
```

`Pipeline[T].IsFastPath(opts ...RunOption) bool` is a convenience wrapper that returns `false` if any fast-path-capable stage would leave the fast path:

```go
if !mapped.IsFastPath() {
    t.Error("expected pipeline to stay on the fast path")
}
```

Both methods accept the same `RunOption`s as `Runner.Run`, so you can test the run-time hook condition too:

```go
// Verify the pipeline stays fast-path without a hook,
// but correctly reports the drop when LogHook is added.
assert.True(t, pipeline.IsFastPath())
assert.False(t, pipeline.IsFastPath(kitsune.WithHook(kitsune.LogHook(slog.Default()))))
```

`IsOptimized` is non-destructive: it allocates a temporary channel graph (like `Describe`) and returns before any goroutines are started.

### Common pitfalls

**Adding `WithHook(LogHook(...))` for debugging** silently disables the fast path across every Map and Filter stage in the run. If you need per-item logging without losing throughput, install the hook only in non-production builds, or use `WithSampleRate(-1)` to disable the sampling hooks while keeping stage lifecycle events.

**Setting `Concurrency(2)` on a cheap CPU-bound Map** is almost always slower than staying on the fast path. The goroutine scheduling overhead, channel synchronisation, and cache-line contention typically exceed the gains. Measure with `pprof` before reaching for concurrency on fast operations.

**`CacheBy` on Map always disables the Map fast path.** If you need caching on one stage but want the rest of the chain to be fast, put the cached Map first in the chain and let the downstream Maps stay cache-free.

---

## Buffer Sizing (`Buffer(n)`)

The channel buffer between two stages holds up to `n` items in memory. Each stage sees at most `Buffer` pending items at any time.

**Increase the buffer when:**
- Your source is bursty, emitting many items in rapid succession before pausing.
- Adjacent stages have variable latency (a slow stage occasionally falls behind, but catches up quickly).
- You see goroutines blocking frequently under profiling and want to reduce that overhead.

**Decrease the buffer when:**
- Items are large (structs with big fields, file contents, etc.) and memory is constrained.
- You want strict backpressure so a slow consumer immediately slows the producer.

**`Buffer(0)`: synchronous channel:**
```go
pipe.Buffer(0)
```
A zero-size buffer makes every send block until the receiver is ready. Useful for testing backpressure behavior or enforcing strict sequential hand-off between stages. Not generally recommended for production throughput.

!!! warning "`Buffer(0)` is for testing"
    A synchronous channel forces every hand-off to block. Use it to exercise backpressure in tests, not for production throughput.

**Rule of thumb:** set buffer ≈ expected burst size, capped by what you can afford in memory. If your source emits 100 items in a burst every few seconds, `Buffer(100)` lets the source drain quickly while downstream processes at its own pace.

---

## Concurrency (`Concurrency(n)`)

By default each stage runs on a single goroutine. `Concurrency(n)` starts `n` goroutines, all reading from the same input channel.

**Use for I/O-bound stages:**
```go
pipe.Concurrency(20) // HTTP enrichment, DB lookups, file reads
```
I/O-bound stages spend most of their time waiting, for a network response, a disk read, a lock release. Running 20 goroutines means 20 outstanding requests in flight simultaneously with no extra CPU cost.

**CPU-bound stages** rarely benefit beyond `runtime.NumCPU()`. Beyond that point you add goroutine scheduling overhead and GC pressure without gaining real parallelism.

**Order is not preserved.** With `Concurrency(n) > 1`, items are processed in whatever order goroutines happen to finish. If your pipeline requires deterministic ordering, keep the stage at `Concurrency(1)`.

!!! note "`Ordered()` concurrency trade-off"
    `Ordered()` with `Concurrency(n)` uses a slot-based resequencer. Peak throughput is ~10–15% lower than unordered, and a single slow item head-of-lines the downstream channel until it completes.

**Buffer interaction:** the `n` goroutines all draw from the same input channel. If items arrive in bursts, consider increasing `Buffer` alongside `Concurrency` so workers stay busy between bursts:
```go
pipe.Concurrency(20), pipe.Buffer(64)
```

**Starting point:** 10–20 for HTTP enrichment, then profile. CPU-bound: start at `runtime.NumCPU()`.

---

## Batch Sizing

`Batch(p, size, opts...)` collects up to `size` items before passing them downstream as a slice. This amortizes per-call overhead: a single bulk database insert of 100 rows is typically much cheaper than 100 individual inserts.

**Larger batches:**
- Reduce per-call overhead (fewer round trips, better bulk API efficiency).
- Increase memory usage (the batch is held in memory until it flushes).
- Increase latency to first result.

**Smaller batches:**
- Lower memory pressure and latency.
- Higher per-call overhead.

**`BatchTimeout`: preventing stalls:**
```go
pipe.BatchTimeout(500 * time.Millisecond)
```
Without a timeout, a partial batch sits in memory until it fills up. For near-real-time pipelines with variable or low volume, this can cause items to stall indefinitely. `BatchTimeout` flushes the partial batch after the specified duration, bounding worst-case latency.

**`Window(p, d)`: time-bucketed aggregation:**
```go
pipe.Window(p, 10*time.Second)
```
`Window` is `Batch(p, MaxInt, BatchTimeout(d))`: it collects all items that arrive within the window and flushes them together. Use it when you want time-bucketed aggregation, rather than size-bounded batching.

---

## Memory

**`MemoryCache(maxSize)`**

Grows until it holds `maxSize` entries, then evicts the least-recently-used entry on each new insert. Size it to your working set of unique keys:
- Too small: excessive eviction means cache misses, defeating the purpose.
- Too large: memory grows unnecessarily.

If you don't know your working set size, start with an estimate and watch eviction rate via your `Hook` metrics.

**`MemoryDedupSet`**

Grows unboundedly with every unique key seen. Fine for batch jobs with a bounded input. For long-running pipelines that see many distinct keys over time, this will eventually exhaust memory. Switch to a Redis-backed dedup set (`kredis.NewDedupSet`) to bound memory:
```go
dedup := kredis.NewDedupSet(redisClient, "pipeline:seen-keys")
```

**Buffering operators**

Several operators materialise the *entire stream* in memory before emitting any output. Use them only on bounded (finite) pipelines and size your heap accordingly:

| Operator | Memory held |
|---|---|
| `GroupBy` | All items, grouped by key |
| `ChunkBy`, `ChunkWhile` | All items, then split into chunks |
| `Sort`, `SortBy` | All items, then sorted |

If you are sorting or grouping a large dataset, consider pre-sorting upstream (e.g., a sorted database query or a pre-bucketed Kafka topic) so the pipeline can process records without buffering them all.

**Large items**

For pipelines that process large items (reading big files line by line, large API responses), prefer streaming with small buffers over materializing everything. A `Buffer(4)` with large items uses far less memory than `Buffer(64)`.

---

## Observability and Profiling

**Name every stage:**
```go
pipe.WithName("enrich-user")
```
Stage names appear in `Hook` events. Without names, profiling output is hard to correlate back to your pipeline definition.

**Use the `Hook` interface for metrics:**
```go
type Hook interface {
    OnStageStart(ctx context.Context, stage string)
    OnItem(ctx context.Context, stage string, dur time.Duration, err error)
    OnStageDone(ctx context.Context, stage string, processed int64, errors int64)
}
```
Implement `Hook` to collect per-stage timing, throughput, and error counts. See `examples/metrics` for a working example that writes to a Prometheus registry.

**Quick debugging with `LogHook`:**
```go
err := runner.Run(ctx, kitsune.WithHook(kitsune.LogHook(slog.Default())))
```
`LogHook` logs stage start and done events with item counts to the provided `slog.Logger`. Useful for tracing where items are being lost or where a stage is slow.

**Control per-item sampling rate:**

`SampleHook.OnItemSample` fires once every 10 items by default. Adjust with `WithSampleRate`:
```go
runner.Run(ctx,
    kitsune.WithHook(myHook),
    kitsune.WithSampleRate(100), // sample every 100th item
    // kitsune.WithSampleRate(-1), // disable sampling entirely
)
```
Lower rates reduce hook overhead in high-throughput pipelines.

**CPU and memory profiling:**
```bash
go test -bench=. -cpuprofile cpu.out -memprofile mem.out
go tool pprof cpu.out
go tool pprof mem.out
```
Run benchmarks with profiling enabled, then inspect with `pprof`. Look for stages with unexpectedly high CPU time or heap allocations.

---

## Benchmarks

See [`doc/benchmarks.md`](benchmarks.md) for baseline throughput numbers measured on reference hardware.

Note that all benchmarks measure pipeline-construction cost plus execution cost together: each benchmark creates and runs a fresh pipeline. For a long-running production pipeline, the construction overhead is negligible; what matters is per-item throughput, which you can isolate by profiling a running process rather than relying solely on micro-benchmarks.
