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
| `MapConcurrent` | 10,000 items, `Concurrency(4)` | 4,604 µs | ~2.2 M/s | 157 KB | 19,685 |
| `FlatMap` | 1,000 items → 10,000 outputs | 2,837 µs | ~3.5 M out/s | 325 KB | 11,272 |
| `BatchUnbatch` | 10,000 items, `Batch(100)` + `Unbatch` | 4,228 µs | ~2.4 M/s | 599 KB | 20,041 |
| `Filter` | 10,000 items, 50% pass | 3,153 µs | ~3.2 M/s | 79 KB | 9,786 |
| `Dedupe` | 10,000 items, 50% unique | 4,232 µs | ~2.4 M/s | 653 KB | 29,048 |
| `MapWithCache` | 10,000 items, 1,000 unique keys (~90% hit) | 7,508 µs | ~1.3 M/s | 1.9 MB | 53,462 |
| `MapOrdered/ordered` | 10,000 items, `Concurrency(8)` + `Ordered()` | 8,764 µs | ~1.1 M/s | 1.9 MB | 39,697 |
| `MapOrdered/unordered` | 10,000 items, `Concurrency(8)` | 5,521 µs | ~1.8 M/s | 163 KB | 19,689 |

---

## Notes

**MapConcurrent is slower than MapLinear** for this synthetic workload because the transformation (`n * 2`) is CPU-trivial — goroutine coordination overhead dominates. `Concurrency(n)` pays off for I/O-bound stages (HTTP calls, database queries) where goroutines block waiting for external systems rather than competing for CPU. The concurrent fast path skips per-item timing, hook dispatch, and `ProcessItem` overhead in the worker loop (same conditions as the `Concurrency(1)` fast path: `DefaultHandler + NoopHook`).

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

## Experimental typed engine

The `engine/typed` package implements the same 3-stage pipeline (Map → Filter → Drain)
using `chan T` instead of `chan any`, eliminating all per-item interface boxing.

**Machine**: Apple M1, darwin/arm64, Go 1.26.1
**Method**: `go test -bench=BenchmarkTyped -benchmem -count=5` in `bench/`, median of 5 runs

Both engines now use `for range` on receive and a single send-side `select`, so the
comparison is apples-to-apples.

| Implementation | ns/op | items/sec | B/op | allocs/op | vs raw |
|---|---:|---:|---:|---:|---:|
| Raw goroutines | 167 ms | ~5.98 M/s | 1 KB | 13 | — |
| **Typed (experimental)** | **317 ms** | **~3.15 M/s** | **2 KB** | **29** | **+90%** |
| **Kitsune** | **350 ms** | **~2.85 M/s** | **15.3 MB** | **2,000,000** | **+110%** |

**Boxing is eliminated in typed** — 29 allocs is pipeline setup only (goroutine stacks,
channels, sync structures). Zero per-item allocations.

**The typed engine is ~10% faster than Kitsune** (317 vs 350 ms). With both engines on
equal footing (same `for range` receive pattern), the remaining gap is GC pressure from
Kitsune's 2M allocs/run visibly affecting throughput at 1M items. The earlier measurement
showing only a 7% gap was an artefact of typed still using a 2-case receive select.

**The remaining ~1.9x gap to raw goroutines** (317 vs 167 ms) is the send-side `select`
— both engines need it to prevent deadlock on early downstream exit. Raw goroutines use
plain `ch <- v` with no cancellation guard. Closing this gap requires either removing
the send-side `select` (unsafe without a different teardown protocol) or chunked transport
(amortising one select over N items).

---

## Comparison: Kitsune vs alternatives

Measures a 3-stage linear pipeline (Map → Filter → Drain) at 1M items across four implementations on the same machine.

**Pipeline**: Map `n*2` → Filter `n%3 != 0` (~67% pass-through) → Drain (discard)
**Machine**: Apple M1, darwin/arm64, Go 1.26.1
**Method**: `go test -bench=. -benchmem -count=5` in `bench/`, median of 5 runs

| Implementation | ns/op | items/sec | B/op | allocs/op | vs raw |
|---|---:|---:|---:|---:|---:|
| Raw goroutines | 167 ms | ~5.98 M/s | 1 KB | 13 | — |
| `sourcegraph/conc` | 167 ms | ~6.00 M/s | 1 KB | 12 | +0% |
| **Kitsune** | **350 ms** | **~2.85 M/s** | **15.3 MB** | **2,000,000** | **+110%** |
| `reugn/go-streams` | 733 ms | ~1.36 M/s | 30.5 MB | 2,000,000 | +341% |

**Raw goroutines and `sourcegraph/conc` are nearly identical** — conc is a structured-concurrency primitive (WaitGroup + panic propagation), not a pipeline library. Its overhead over raw goroutines is <1%.

**Kitsune processes 2.85M items/sec at 1M items**, carrying a ~2.1x overhead vs raw goroutines. Fast paths for `Map`, `FlatMap`, `Filter`, and `Sink` at `Concurrency(1)` use `for range` on receive (no per-item ctx.Done select) plus a single send-side `select`. The remaining overhead is `any`-boxing at stage boundaries (2 allocs/item) and the send-side `select` needed to prevent deadlock on early downstream exit.

**`reugn/go-streams`** is ~1.36M items/sec — slower than Kitsune for two reasons: (1) all stage channels are unbuffered (vs Kitsune's default 16-slot buffers), creating a handshake per item, and (2) go-streams has no built-in slice source, so all N items must be pre-boxed into a `chan any` before the pipeline starts (accounting for the 2× memory overhead vs Kitsune).

**When does Kitsune overhead matter?** At 2.85M items/sec, each item costs ~350 ns of total pipeline time, of which ~183 ns is Kitsune overhead. For stages doing real I/O (database queries, HTTP calls, gRPC), stage latency will be 10–100× higher, making Kitsune's overhead negligible. The overhead is only significant for CPU-trivial, high-throughput pipelines on a single core.

To reproduce:

```
cd bench && go test -bench=. -benchmem -count=5 -timeout 300s ./...
```

Or via Task:

```
task bench:compare
```

---

## Allocation budgets

Per-pipeline allocation ceilings for the highest-traffic operators, measured on Apple M1, Go 1.26.1. Ceilings include ~5% headroom above the baseline measurement.

These bounds are enforced by `TestAllocBounds` in `bench_allocs_test.go` and run as part of the standard test suite.

| Operator | Config | allocs/run (10K items) | Ceiling | Allocs/item | Key sources |
|---|---|---:|---:|---:|---|
| `Map` | `Concurrency(1)`, `n*2` | ~19,675 | 21,000 | ~2 | `any` boxing of input + output |
| `FlatMap` | 1K items → 10K outputs | ~10,288 | 11,000 | ~1/output | yield closure + `any` boxing per output |
| `Batch` | `Batch(100)`, 100 flushes | ~10,003 | 10,500 | ~1 | `make([]T)` per flush; `[]any` buffer reused via `clear`+reslice |
| `RateLimit` | wait mode, rate=1e9/s | ~19,556 | 21,000 | ~2 | Same as Map (reservation amortised at high rate) |
| `CircuitBreaker` | closed state, all-success | ~19,683 | 21,000 | ~2 | Map boxing + 2× `ref.Update` closures (amortised) |

**Allocation count includes pipeline setup** (goroutine stacks, channels, engine graph). Setup is roughly constant (~80 allocs) regardless of item count; per-item cost dominates for N ≥ 1,000.

**`RateLimit` and `CircuitBreaker`** show nearly the same alloc count as plain `Map` at high throughput — the `rate.Reservation` and `ref.Update` closure costs are real but small relative to the `any`-boxing that dominates at 2 allocs/item.

---

## Running benchmarks yourself

**Core operator benchmarks** (throughput + allocation benchmarks):

```
go test -bench=. -benchmem ./...
```

For stable results across multiple runs:

```
go test -bench=. -benchmem -count=5 ./...
```

**Comparison benchmarks** (Kitsune vs raw goroutines, conc, go-streams):

```
cd bench && go test -bench=. -benchmem -count=5 -timeout 300s ./...
```

**Allocation regression tests** (hard-fail on ceiling violation):

```
go test -run TestAllocBounds -count=1 ./...
```

**Latency percentile output** (uses `-run`, not `-bench`):

```
go test -v -run TestLatencyPercentiles ./...
```

Or via Task:

```
task bench           # core benchmarks
task bench:compare   # comparison benchmarks
```
