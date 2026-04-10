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
| `MapLinear` | 10,000 items, `Concurrency(1)` | 768 µs | ~13.0 M/s | 162 KB | 19,675 |
| `MapConcurrent` | 10,000 items, `Concurrency(4)` | 4,395 µs | ~2.3 M/s | 163 KB | 19,693 |
| `FlatMap` | 1,000 items → 10,000 outputs | 714 µs | ~14.0 M out/s | 111 KB | 10,297 |
| `BatchUnbatch` | 10,000 items, `Batch(100)` + `Unbatch` | 1,319 µs | ~7.6 M/s | 257 KB | 19,870 |
| `Filter` | 10,000 items, 50% pass | 659 µs | ~15.2 M/s | 82 KB | 9,801 |
| `Dedupe` | 10,000 items, 50% unique | 2,001 µs | ~5.0 M/s | 709 KB | 33,812 |
| `MapWithCache` | 10,000 items, 1,000 unique keys (~90% hit) | 4,060 µs | ~2.5 M/s | 1.9 MB | 53,491 |
| `MapOrdered/ordered` | 10,000 items, `Concurrency(8)` + `Ordered()` | 8,706 µs | ~1.1 M/s | 164 KB | 19,705 |
| `MapOrdered/unordered` | 10,000 items, `Concurrency(8)` | 4,870 µs | ~2.1 M/s | 163 KB | 19,697 |

---

## Notes

**MapConcurrent is slower than MapLinear** for this synthetic workload because the transformation (`n * 2`) is CPU-trivial, and goroutine coordination overhead dominates. `Concurrency(n)` pays off for I/O-bound stages (HTTP calls, database queries) where goroutines block waiting for external systems rather than competing for CPU. The concurrent fast path skips per-item timing, hook dispatch, and `ProcessItem` overhead in the worker loop (same conditions as the `Concurrency(1)` fast path: `DefaultHandler + NoopHook`).

**MapLinear throughput improved 2.4×** (1,845 µs → 768 µs). Stage fusion collapses Map→Drain into a single goroutine. Receive-side micro-batching amortises the Source→Map channel cost. The drain protocol removes the per-item 3-case `select` in the source fast path entirely: the source now does plain `outCh <- item`, with downstream drain goroutines unblocking it on pipeline exit.

**FlatMap items/sec** is measured as output items (10,000) per ns/op, since the expansion ratio (1:10) is the primary cost driver.

**Dedupe allocs** are higher because each item requires a map lookup and potential insert into the `MemoryDedupSet`, which allocates map entries.

**MapWithCache allocs** are highest because every cache miss requires JSON marshalling and unmarshalling the result, and every item requires a map lookup. With a real workload of larger, less-frequent misses, the amortised cost will be lower.

**MapOrdered allocs** are higher than unordered because the ordering buffer must hold in-flight results until earlier items complete, requiring extra per-result allocations.

**Memory (B/op)** reflects allocations over the full pipeline lifetime, including channels, goroutine stacks, and any data structure overhead. Not peak RSS. Fast-path pipelines allocate only setup (~4 KB); per-item memory is zero on the fast path.

---

## Backpressure

Measured with a fast producer (10,000 items) feeding into a Map with `Buffer(4)` and a `runtime.Gosched()` slow consumer. Shows the throughput cost of backpressure vs. the allocation parity when dropping.

| Strategy | ns/op | items/sec | B/op | allocs/op |
|---|---:|---:|---:|---:|
| `Block` (default) | 1,856 µs | ~5.4 M/s | 161 KB | 19,548 |
| `DropNewest` | 2,700 µs | ~3.7 M/s | 161 KB | 19,556 |
| `DropOldest` | 3,249 µs | ~3.1 M/s | 161 KB | 19,556 |

**Block** is now faster than Drop strategies for this workload. With the drain protocol, the source's plain send unblocks immediately when the downstream drain goroutine drains the buffer, with no producer stall on a full buffer. **DropNewest** and **DropOldest** involve additional logic to inspect the buffer before sending, making them slightly slower for CPU-trivial workloads where the buffer rarely fills. Allocations are nearly identical across strategies since the item pipeline is the same.

---

## Concurrency scaling

Measured with a 200-item pipeline and `time.Sleep(50µs)` per item inside Map to simulate I/O. Shows near-linear speedup as workers increase for I/O-bound work.

| Workers | ns/op | items/sec | B/op | allocs/op |
|---|---:|---:|---:|---:|
| 1 | 13,664 µs | ~14.6 k/s | 5 KB | 132 |
| 2 | 6,857 µs | ~29.2 k/s | 6 KB | 149 |
| 4 | 3,425 µs | ~58.4 k/s | 7 KB | 153 |
| 8 | 1,705 µs | ~117.3 k/s | 8 KB | 161 |

Throughput roughly doubles with each doubling of workers because the bottleneck is the simulated I/O wait, not the CPU. Allocation overhead per worker is minimal (~8 additional allocs per extra worker per pipeline run). Compare with `BenchmarkMapConcurrent` where the same concurrency setting shows no gain: the workload must actually block for concurrency to help.

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

**p50 latency for CPU-trivial work is ~42 ns**: the stage function executes in nanoseconds and the framework overhead is similarly low. The p99 tail (125–292 ns) reflects occasional goroutine scheduling contention under concurrency.

**IOSimulated p50 is ~131 µs**: the 100 µs sleep plus ~31 µs of framework and goroutine scheduling overhead. The tight p50–p99 spread (131–141 µs) shows that latency is dominated by the sleep, not by pipeline variance.

These measurements reflect stage processing time (the duration the stage function takes), not end-to-end pipeline latency from source emit to sink receive.

---

## Fast-path conditions

A stage enters the fast path (drain protocol + micro-batching, eligible for typed fusion) when ALL hold:
- `Concurrency(1)` (default)
- No `Supervise(...)` policy
- Default error handler (no explicit `OnError`, `Retry`, or `Skip`)
- Default overflow (`Block`)
- No per-item `Timeout`
- `NoopHook` (no `WithHook(...)` run option)

Additionally, typed fusion requires that the pipeline has exactly one consumer (`consumerCount == 1`). Fan-out pipelines (same `*Pipeline[T]` used by two operators) fall back to the channel path automatically.

Adding any real hook (`LogHook`, `MetricsHook`, custom) disables the fast path and fusion for instrumented stages. The slow path is fully correct and adequate for I/O-bound workloads where stage latency dwarfs framework overhead.

### Allocation budget

All fast-path pipelines, fused or not, allocate a fixed setup overhead independent of N:

| Pipeline | allocs/run (1M items) | allocs/item |
|---|---:|---:|
| `Map` → `Filter` → `ForEach` (fused) | 54 | **0** |
| Single `Map` + `Drain` (no fusion) | ~54 | **0** |
| `FlatMap` + `Drain` | ~54 | **0** |
| Any pipeline with `WithHook(...)` | scales with N | ~1–2 |

The 54 allocs/run are pure setup (goroutine stacks, channels, sync primitives). Per-item cost is zero for all fast-path cases, fused and non-fused alike.

---

## Comparison: Kitsune vs alternatives

Measures a 3-stage linear pipeline (Map → Filter → Drain) at 1M items across four implementations on the same machine.

**Pipeline**: Map `n*2` → Filter `n%3 != 0` (~67% pass-through) → Drain (discard)
**Machine**: Apple M1, darwin/arm64, Go 1.26.1
**Method**: `go test -bench=. -benchmem -count=5` in `archive/bench/`, median of 5 runs

| Implementation | ns/op | items/sec | B/op | allocs/op | vs raw |
|---|---:|---:|---:|---:|---:|
| Raw goroutines | 168 ms | ~5.95 M/s | 1 KB | 12 | N/A |
| `sourcegraph/conc` | 168 ms | ~5.96 M/s | 1 KB | 12 | +0% |
| **Kitsune** | **47 ms** | **~21.2 M/s** | **5 KB** | **54** | **-72%** |
| `reugn/go-streams` | 741 ms | ~1.35 M/s | 30.5 MB | 2,000,000 | +341% |

**Kitsune is 3.6× faster than raw goroutines** and 16× faster than `go-streams`. The gains stack:
1. **Typed build-time fusion** collapses Map→Filter→Drain into one goroutine with zero inter-stage channel hops and zero boxing. `Pipeline[T]` carries a `fusionEntry` closure; when `ForEach` detects a single-consumer fast-path chain with `NoopHook` it calls `fusionEntry` directly, composing the entire chain.
2. **Receive-side micro-batching** amortises the remaining Source→chain channel cost: one blocking receive + up to 15 non-blocking drains.
3. **Drain protocol** removes the per-item 3-case `select` from the source: plain `outCh <- item` with a downstream drain goroutine providing the deadlock safety net.

**Raw goroutines use 4 goroutines with 3 channel hops and plain `ch <- v` sends.** Kitsune uses 1 goroutine hop (Source → fused chain) with a plain send, then direct function calls within the fused chain. The drain protocol's safety net costs one `goroutine + for range` per stage exit, amortised to zero over a run.

**`reugn/go-streams`** is ~1.35M items/sec, slower than Kitsune for two reasons: (1) all stage channels are unbuffered (vs Kitsune's default 16-slot buffers), creating a handshake per item, and (2) go-streams has no built-in slice source, so all N items must be pre-boxed into a `chan any` before the pipeline starts (accounting for the 2× memory overhead vs Kitsune).

**When does Kitsune overhead matter?** Typed build-time fusion has zero per-item boxing: `chan T` stage boundaries mean the compiler can inline or at minimum avoid interface allocation at every hop. The only remaining cross-goroutine cost is the single channel receive from Source into the fused chain: hence ~21 M/s and a constant 54 allocs/run regardless of N.

To reproduce:

```
cd archive/bench && go test -bench=. -benchmem -count=5 -timeout 300s ./...
```

Or via Task:

```
task bench:compare
```

---

## Realistic workloads

The trivial benchmark above (Map `n*2`, Filter `n%3 != 0`) maximally amplifies
framework overhead because stage functions run in ~5 ns. Real pipelines do more
work per item. Two additional tiers show how overhead scales with stage cost.

**Machine**: Apple M1, darwin/arm64, Go 1.26.1
**Method**: `go test -bench='BenchmarkLightCPU|BenchmarkIOBound' -benchmem -count=5` in `archive/bench/`, median of 5 runs

### Light CPU: SHA-256 per item (~300 ns work)

Represents data transformation pipelines: hashing, encoding, schema validation.
Stage cost (~300 ns) is comparable to a single channel hop.

| Implementation | ns/op | items/sec | vs raw |
|---|---:|---:|---:|
| Raw goroutines | 276 ms | ~3.63 M/s | N/A |
| **Kitsune** | **122 ms** | **~8.17 M/s** | **-56%** |

Kitsune is 56% **faster** than raw goroutines: fusion eliminates 2 goroutine handoffs per item, and the drain protocol eliminates the per-item select in the source. Even with SHA-256 stage work, eliminating goroutine handoffs is worth more.

### I/O bound: 1 µs sleep per item (~3 µs effective on M1)

Represents stages that call external systems: HTTP, database queries, gRPC.
`time.Sleep(1µs)` sleeps ~3 µs on macOS due to OS scheduler granularity;
both implementations sleep equally so the overhead ratio is still accurate.

| Implementation | ns/op | items/sec | vs raw |
|---|---:|---:|---:|
| Raw goroutines | 31.6 ms | ~316 k/s | N/A |
| **Kitsune** | **28.5 ms** | **~350 k/s** | **-10%** |

Kitsune is 10% **faster** than raw goroutines for I/O-bound work.

### Overhead vs stage cost

| Stage cost | Kitsune overhead | Representative workload |
|---|---:|---|
| ~5 ns (trivial) | -72% (faster) | CPU-trivial transforms |
| ~300 ns (SHA-256) | -56% (faster) | Hashing, encoding, validation |
| ~3 µs (I/O sleep) | -10% (faster) | Database queries, HTTP calls |
| ~100 µs (network) | ~0% | Cross-datacenter RPC |

**Kitsune is faster than raw goroutines across all measured workloads.** The drain protocol, typed build-time fusion, and receive-side micro-batching together eliminate more overhead than the errgroup machinery adds. The typed engine uses `chan T` at every stage boundary, with zero per-item boxing anywhere in the fast path, leaving only the single channel hop from Source into the fused chain. Gains are most pronounced for CPU-bound work (−72% for trivial transforms, −56% for SHA-256 hashing) where eliminating goroutine handoffs compounds with stage cost.

---

## Allocation budgets

Per-pipeline allocation ceilings for the highest-traffic operators, measured on Apple M1, Go 1.26.1. Ceilings include ~5% headroom above the baseline measurement.

These bounds are enforced by `TestAllocBounds` in `bench_allocs_test.go` and run as part of the standard test suite.

| Operator | Config | allocs/run (10K items) | Ceiling | Allocs/item | Key sources |
|---|---|---:|---:|---:|---|
| `Map` | `Concurrency(1)`, `n*2` (fast path, fused) | ~54 | 65 | **0** | setup only: goroutine stacks, channels |
| `Map` | `Concurrency(1)`, `n*2` (with hook) | ~19,675 | 21,000 | ~2 | hook dispatch allocations per item |
| `FlatMap` | 1K items → 10K outputs | ~10,297 | 11,000 | ~1/output | yield closure per output item |
| `Batch` | `Batch(100)`, 100 flushes | ~10,003 | 10,500 | ~1 | `make([]T)` per flush; buffer reused via `clear`+reslice |
| `RateLimit` | wait mode, rate=1e9/s | ~54 | 65 | **0** | same as Map fast path (reservation amortised at high rate) |
| `CircuitBreaker` | closed state, all-success | ~54 | 65 | **0** | same as Map fast path; `ref.Update` closures amortised |

**Allocation count includes pipeline setup** (goroutine stacks, channels, sync primitives). For fast-path pipelines (`Concurrency(1)`, `NoopHook`, default error handler), setup is ~54 allocs regardless of item count: per-item cost is zero.

**`RateLimit` and `CircuitBreaker`** on the fast path match plain `Map`: the `rate.Reservation` and `ref.Update` closure costs are amortised and do not scale with N.

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
cd archive/bench && go test -bench=. -benchmem -count=5 -timeout 300s ./...
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
