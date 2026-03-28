# Performance Tuning Guide

Kitsune pipelines run as a DAG of goroutines connected by bounded channels. The defaults work well for most pipelines: buffer size 16, single-goroutine stages, no batching. This guide explains when and how to deviate from those defaults.

---

## Overview

Every stage in a pipeline has an input channel. When a stage finishes processing an item, it writes to the next stage's channel. If that channel is full, the writer blocks — this is backpressure. When the channel has room, the writer proceeds without waiting.

This model means:
- Slow downstream stages naturally slow down fast upstream stages (backpressure propagates).
- No goroutines are started until `Run()` is called (lazy execution).
- Context cancellation propagates to all stages cleanly.

The default buffer size of 16 (`engine.DefaultBuffer`) is intentionally modest. Most pipelines are I/O-bound and spend most of their time waiting, not queuing.

---

## Buffer Sizing (`Buffer(n)`)

The channel buffer between two stages holds up to `n` items in memory. Each stage sees at most `Buffer` pending items at any time.

**Increase the buffer when:**
- Your source is bursty — it emits many items in rapid succession before pausing.
- Adjacent stages have variable latency (a slow stage occasionally falls behind, but catches up quickly).
- You see goroutines blocking frequently under profiling and want to reduce that overhead.

**Decrease the buffer when:**
- Items are large (structs with big fields, file contents, etc.) and memory is constrained.
- You want strict backpressure so a slow consumer immediately slows the producer.

**`Buffer(0)` — synchronous channel:**
```go
pipe.Buffer(0)
```
A zero-size buffer makes every send block until the receiver is ready. Useful for testing backpressure behavior or enforcing strict sequential hand-off between stages. Not generally recommended for production throughput.

**Rule of thumb:** set buffer ≈ expected burst size, capped by what you can afford in memory. If your source emits 100 items in a burst every few seconds, `Buffer(100)` lets the source drain quickly while downstream processes at its own pace.

---

## Concurrency (`Concurrency(n)`)

By default each stage runs on a single goroutine. `Concurrency(n)` starts `n` goroutines, all reading from the same input channel.

**Use for I/O-bound stages:**
```go
pipe.Concurrency(20) // HTTP enrichment, DB lookups, file reads
```
I/O-bound stages spend most of their time waiting — for a network response, a disk read, a lock release. Running 20 goroutines means 20 outstanding requests in flight simultaneously with no extra CPU cost.

**CPU-bound stages** rarely benefit beyond `runtime.NumCPU()`. Beyond that point you add goroutine scheduling overhead and GC pressure without gaining real parallelism.

**Order is not preserved.** With `Concurrency(n) > 1`, items are processed in whatever order goroutines happen to finish. If your pipeline requires deterministic ordering, keep the stage at `Concurrency(1)`.

**Buffer interaction:** the `n` goroutines all draw from the same input channel. If items arrive in bursts, consider increasing `Buffer` alongside `Concurrency` so workers stay busy between bursts:
```go
pipe.Concurrency(20), pipe.Buffer(64)
```

**Starting point:** 10–20 for HTTP enrichment, then profile. CPU-bound: start at `runtime.NumCPU()`.

---

## Batch Sizing

`Batch(p, size, opts...)` collects up to `size` items before passing them downstream as a slice. This amortizes per-call overhead — a single bulk database insert of 100 rows is typically much cheaper than 100 individual inserts.

**Larger batches:**
- Reduce per-call overhead (fewer round trips, better bulk API efficiency).
- Increase memory usage (the batch is held in memory until it flushes).
- Increase latency to first result.

**Smaller batches:**
- Lower memory pressure and latency.
- Higher per-call overhead.

**`BatchTimeout` — preventing stalls:**
```go
pipe.BatchTimeout(500 * time.Millisecond)
```
Without a timeout, a partial batch sits in memory until it fills up. For near-real-time pipelines with variable or low volume, this can cause items to stall indefinitely. `BatchTimeout` flushes the partial batch after the specified duration, bounding worst-case latency.

**`Window(p, d)` — time-bucketed aggregation:**
```go
pipe.Window(p, 10*time.Second)
```
`Window` is `Batch(p, MaxInt, BatchTimeout(d))` — it collects all items that arrive within the window and flushes them together. Use it when you want time-bucketed aggregation rather than size-bounded batching.

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

Note that all benchmarks measure pipeline-construction cost plus execution cost together — each benchmark creates and runs a fresh pipeline. For a long-running production pipeline, the construction overhead is negligible; what matters is per-item throughput, which you can isolate by profiling a running process rather than relying solely on micro-benchmarks.
