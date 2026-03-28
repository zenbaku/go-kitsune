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

---

## Notes

**MapConcurrent is slower than MapLinear** for this synthetic workload because the transformation (`n * 2`) is CPU-trivial — goroutine coordination overhead dominates. `Concurrency(n)` pays off for I/O-bound stages (HTTP calls, database queries) where goroutines block waiting for external systems rather than competing for CPU.

**FlatMap items/sec** is measured as output items (10,000) per ns/op, since the expansion ratio (1:10) is the primary cost driver.

**Dedupe allocs** are higher because each item requires a map lookup and potential insert into the `MemoryDedupSet`, which allocates map entries.

**MapWithCache allocs** are highest because every cache miss requires JSON marshalling and unmarshalling the result, and every item requires a map lookup. With a real workload of larger, less-frequent misses, the amortised cost will be lower.

**Memory (B/op)** reflects allocations over the full pipeline lifetime — channels, goroutine stacks, item boxing, and any data structure overhead. Not peak RSS.

---

## Running benchmarks yourself

```
go test -bench=. -benchmem ./...
```

For stable results across multiple runs:

```
go test -bench=. -benchmem -count=5 ./...
```

Or via Task:

```
task bench
```
