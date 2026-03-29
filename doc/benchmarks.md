# Kitsune Benchmarks

Baseline throughput measurements for the core operators.

**Machine**: Apple M1, darwin/arm64
**Go version**: 1.26.1
**Method**: `go test -bench=. -benchmem -count=5` (median of 5 runs reported)

Each benchmark constructs and runs a complete pipeline from scratch, so the numbers include both pipeline-setup cost and execution cost. Setup is negligible relative to execution for the dataset sizes used.

---

## Results

| Benchmark | Dataset | ns/op | items/sec | B/op | allocs/op |
|---|---|---:|---:|---:|---:|
| `MapLinear` | 10,000 items, `Concurrency(1)` | 5,066 µs | ~2.0 M/s | 156 KB | 19,659 |
| `MapConcurrent` | 10,000 items, `Concurrency(4)` | 6,129 µs | ~1.6 M/s | 157 KB | 19,672 |
| `FlatMap` | 1,000 items → 10,000 outputs | 2,837 µs | ~3.5 M out/s | 325 KB | 11,272 |
| `BatchUnbatch` | 10,000 items, `Batch(100)` + `Unbatch` | 4,228 µs | ~2.4 M/s | 599 KB | 20,041 |
| `Filter` | 10,000 items, 50% pass | 3,153 µs | ~3.2 M/s | 79 KB | 9,786 |
| `Dedupe` | 10,000 items, 50% unique | 4,232 µs | ~2.4 M/s | 653 KB | 29,048 |
| `MapWithCache` | 10,000 items, 1,000 unique keys (~90% hit) | 7,508 µs | ~1.3 M/s | 1.9 MB | 53,462 |
| `MapOrdered/ordered` | 10,000 items, `Concurrency(8)` + `Ordered()` | 8,061 µs | ~1.2 M/s | 1.9 MB | 39,696 |
| `MapOrdered/unordered` | 10,000 items, `Concurrency(8)` | 7,563 µs | ~1.3 M/s | 163 KB | 19,690 |

---

## Notes

**MapConcurrent is slower than MapLinear** for this synthetic workload because the transformation (`n * 2`) is CPU-trivial — goroutine coordination overhead dominates. `Concurrency(n)` pays off for I/O-bound stages (HTTP calls, database queries) where goroutines block waiting for external systems rather than competing for CPU.

**FlatMap items/sec** is measured as output items (10,000) per ns/op, since the expansion ratio (1:10) is the primary cost driver.

**Dedupe allocs** are higher because each item requires a map lookup and potential insert into the `MemoryDedupSet`, which allocates map entries.

**MapWithCache allocs** are highest because every cache miss requires JSON marshalling and unmarshalling the result, and every item requires a map lookup. With a real workload of larger, less-frequent misses, the amortised cost will be lower.

**MapOrdered allocs** are higher than unordered because the ordering buffer must hold in-flight results until earlier items complete, requiring extra per-result allocations.

**Memory (B/op)** reflects allocations over the full pipeline lifetime — channels, goroutine stacks, item boxing, and any data structure overhead. Not peak RSS.

---

## Backpressure

Measured with a fast producer (10,000 items) feeding into a Map with `Buffer(4)` and a `runtime.Gosched()` slow consumer. Shows the throughput cost of backpressure vs. the allocation parity when dropping.

| Strategy | ns/op | items/sec | B/op | allocs/op |
|---|---:|---:|---:|---:|
| `Block` (default) | 7,269 µs | ~1.4 M/s | 161 KB | 19,549 |
| `DropNewest` | 3,438 µs | ~2.9 M/s | 161 KB | 19,548 |
| `DropOldest` | 3,881 µs | ~2.6 M/s | 161 KB | 19,548 |

**Block** is the slowest — the producer stalls whenever the buffer is full, serialising producer and consumer. **DropNewest** and **DropOldest** are ~2× faster because the producer never stalls, at the cost of silently discarding some items. Allocations are nearly identical across strategies since the item pipeline is the same; the difference is purely in producer wait time.

---

## Concurrency scaling

Measured with a 200-item pipeline and `time.Sleep(50µs)` per item inside Map to simulate I/O. Shows near-linear speedup as workers increase for I/O-bound work.

| Workers | ns/op | items/sec | B/op | allocs/op |
|---|---:|---:|---:|---:|
| 1 | 13,835 µs | ~14.5 k/s | 5 KB | 132 |
| 2 | 6,871 µs | ~29.1 k/s | 6 KB | 142 |
| 4 | 3,447 µs | ~58.0 k/s | 6 KB | 146 |
| 8 | 1,691 µs | ~118.3 k/s | 7 KB | 154 |

Throughput roughly doubles with each doubling of workers because the bottleneck is the simulated I/O wait, not the CPU. Allocation overhead per worker is minimal (~10 additional allocs per extra worker per pipeline run). Compare with `BenchmarkMapConcurrent` where the same concurrency setting shows no gain — the workload must actually block for concurrency to help.

---

## Latency percentiles

Per-item processing latency is measured separately from throughput. The `TestLatencyPercentiles` test (in `bench_latency_test.go`) collects every item's duration via `RecordingHook` and reports p50/p95/p99.

Run it with:

```
go test -v -run TestLatencyPercentiles ./...
```

| Scenario | n | p50 | p95 | p99 | max |
|---|---:|---:|---:|---:|---:|
| `MapLinear` (CPU-trivial, 1 worker) | 10,000 | 42 ns | 83 ns | 125 ns | 73 µs |
| `MapConcurrent` (CPU-trivial, 4 workers) | 10,000 | 42 ns | 125 ns | 292 ns | 25 µs |
| `IOSimulated` (100µs sleep, 4 workers) | 200 | 131 µs | 136 µs | 141 µs | 149 µs |

**p50 latency for CPU-trivial work is ~42 ns** — the stage function executes in nanoseconds and the framework overhead is similarly low. The p99 tail (125–292 ns) reflects occasional goroutine scheduling contention under concurrency.

**IOSimulated p50 is ~131 µs** — the 100 µs sleep plus ~31 µs of framework and goroutine scheduling overhead. The tight p50–p99 spread (131–141 µs) shows that latency is dominated by the sleep, not by pipeline variance.

These measurements reflect stage processing time (the duration the stage function takes), not end-to-end pipeline latency from source emit to sink receive.

---

## Running benchmarks yourself

```
go test -bench=. -benchmem ./...
```

For stable results across multiple runs:

```
go test -bench=. -benchmem -count=5 ./...
```

For latency percentile output (uses `-run`, not `-bench`):

```
go test -v -run TestLatencyPercentiles ./...
```

Or via Task:

```
task bench
```
