# Kitsune

A type-safe, concurrent data pipeline library for Go. Compose ordinary functions into pipelines; the runtime handles channels, goroutines, backpressure, and error routing transparently.

```go
lines  := kitsune.FromSlice(rawLines)
parsed := kitsune.Map(lines, parse)
err    := parsed.Filter(isCritical).ForEach(notify).Run(ctx)
```

## Install

```
go get github.com/jonathan/go-kitsune
```

## When to use Kitsune

Kitsune is a good fit for **in-process data pipelines** where you want typed composition, backpressure, and concurrency control without boilerplate:

- **ETL and data processing** — read from files, APIs, or databases; transform; write to sinks
- **Fan-out workflows** — partition, broadcast, or duplicate streams with compile-time type safety
- **Concurrent enrichment** — call external services with bounded parallelism and automatic retry
- **Streaming aggregation** — batch, window, deduplicate, or cache in-flight data

Kitsune is **not** a distributed stream processor — there is no Kafka consumer group management, no checkpointing, and no cluster coordination. For distributed processing, look at dedicated frameworks. Kitsune complements them: use it for the in-process pipeline logic and connect to external systems through the `tails/` or your own `Generate`/`ForEach` stages.

## API at a glance

| Category | Functions |
|---|---|
| **Sources** | `FromSlice`, `From` (channel), `Generate`, `FromIter` (iter.Seq) |
| **Transforms** | `Map`, `FlatMap`, `Batch`, `Unbatch`, `CachedMap`, `Window` |
| **Filters** | `.Filter`, `.Tap`, `.Take`, `.Dedupe`, `.Through` |
| **Fan-out/in** | `Partition`, `Broadcast`, `Merge` |
| **Terminals** | `.ForEach`, `.Drain`, `.Collect` |
| **State** | `NewKey`, `Ref` (Get/Set/Update), `MapWith`, `FlatMapWith` |
| **Config** | `Concurrency`, `Ordered`, `Buffer`, `Overflow`, `WithName`, `OnError`, `BatchTimeout` |
| **Errors** | `Halt`, `Skip`, `Retry`, `RetryThen`, `FixedBackoff`, `ExponentialBackoff` |
| **Overflow** | `Overflow`, `Block`, `DropNewest`, `DropOldest`, `OverflowHook` |
| **Supervision** | `Supervise`, `RestartOnError`, `RestartOnPanic`, `RestartAlways`, `SupervisionPolicy`, `PanicAction` |
| **Observe** | `Hook` interface, `LogHook`, `WithHook` |

**Free functions** (`Map`, `FlatMap`, `Batch`) change the element type as items flow through. **Methods** (`.Filter`, `.Tap`, `.Take`) preserve it. This split is a Go language constraint: methods cannot introduce new type parameters, so any operation that changes `Pipeline[A]` to `Pipeline[B]` must be a free function like `Map[A, B]`. The upside is that each intermediate variable documents what is flowing, and the compiler checks every type transition. See [design.md](doc/design.md) for the full rationale.

## Quick start

```go
package main

import (
    "context"
    "fmt"
    "strconv"

    kitsune "github.com/jonathan/go-kitsune"
)

func main() {
    input := kitsune.FromSlice([]string{"1", "2", "3", "4", "5"})
    parsed := kitsune.Map(input, kitsune.Lift(strconv.Atoi))
    doubled := kitsune.Map(parsed, func(_ context.Context, n int) (int, error) {
        return n * 2, nil
    })

    results, err := doubled.Filter(func(n int) bool { return n > 4 }).
        Collect(context.Background())
    if err != nil {
        panic(err)
    }
    fmt.Println(results) // [6 8 10]
}
```

## Examples

### Concurrent batched processing

```go
raw      := kitsune.From(kafkaStream)
orders   := kitsune.Map(raw, parseOrder)
enriched := kitsune.Map(orders, enrichWithCustomer, kitsune.Concurrency(20))
batched  := kitsune.Batch(enriched, 500, kitsune.BatchTimeout(2*time.Second))
err      := batched.ForEach(bulkInsert, kitsune.Concurrency(4)).Run(ctx)
```

### Fan-out with Partition

```go
orders := kitsune.FromSlice(allOrders)
high, regular := kitsune.Partition(orders, func(o Order) bool { return o.Amount >= 100 })

vip := kitsune.Map(high, priorityProcess).ForEach(notifyVIP)
std := kitsune.Map(regular, standardProcess).ForEach(store)

err := kitsune.MergeRunners(vip, std).Run(ctx)
```

### Shared state across stages

```go
var queryOrigin = kitsune.NewKey[map[string]string]("origins", make(map[string]string))

items   := kitsune.FromSlice(records)
queries := kitsune.FlatMapWith(items, queryOrigin, buildQueries)   // writes to map
results := kitsune.FlatMap(queries, runSearch, kitsune.Concurrency(20))
final   := kitsune.MapWith(results, queryOrigin, correlate)        // reads from map

err := final.ForEach(sendToSQS).Run(ctx, kitsune.WithStore(kredis.NewStore(rdb, "app:")))
```

### Error handling with retry

```go
results := kitsune.Map(queries, callExternalAPI,
    kitsune.Concurrency(20),
    kitsune.OnError(kitsune.RetryThen(3,
        kitsune.ExponentialBackoff(time.Second, 30*time.Second),
        kitsune.Skip(),
    )),
    kitsune.WithName("external-api"),
)
```

### Mailbox overflow for real-time streams

```go
// Drop newest arrivals when the buffer is full — never block the producer.
kitsune.Map(sensorStream, process,
    kitsune.Buffer(64),
    kitsune.Overflow(kitsune.DropNewest),
    kitsune.WithName("sensor-stage"),
)

// Drop oldest buffered items — always keep the freshest data.
kitsune.Map(sensorStream, process,
    kitsune.Buffer(64),
    kitsune.Overflow(kitsune.DropOldest),
)
```

### Ordered concurrent output

```go
// Process in parallel but emit in input order.
results := kitsune.Map(records, enrichFromAPI,
    kitsune.Concurrency(20),
    kitsune.Ordered(),
)
```

### Supervision and panic recovery

```go
// Restart the stage up to 5 times on transient errors.
processed := kitsune.Map(records, callExternalAPI,
    kitsune.Supervise(kitsune.RestartOnError(5, kitsune.ExponentialBackoff(time.Second, 30*time.Second))),
)

// Recover from panics; treat them as restartable errors.
resilient := kitsune.Map(records, riskyTransform,
    kitsune.Supervise(kitsune.RestartOnPanic(3, kitsune.FixedBackoff(time.Second))),
)
```

### Deduplication and caching

```go
deduped := kitsune.Dedupe(input, func(e Event) string { return e.ID }, kitsune.MemoryDedupSet())

cached := kitsune.CachedMap(items, expensiveLookup,
    func(i Item) string { return i.Key },
    kitsune.MemoryCache(10000),
    5*time.Minute,
)
```

## Live inspector

The `inspector` sub-package serves a real-time web dashboard that shows your pipeline graph, per-stage throughput, latency, error and drop counts, and a live sample trail.

```
go get github.com/jonathan/go-kitsune/inspector
```

### Minimal usage

```go
import (
    kitsune  "github.com/jonathan/go-kitsune"
    "github.com/jonathan/go-kitsune/inspector"
)

func main() {
    insp := inspector.New()
    defer insp.Close()
    fmt.Println("Inspector:", insp.URL()) // open in browser

    // Build your pipeline as usual.
    records := kitsune.FromSlice(rawRecords)
    parsed  := kitsune.Map(records, parse,   kitsune.WithName("parse"))
    valid   := kitsune.Map(parsed,  validate, kitsune.WithName("validate"), kitsune.Concurrency(4))

    // Pass the inspector as a hook — no other changes needed.
    err := valid.ForEach(store, kitsune.WithName("store")).Run(ctx, kitsune.WithHook(insp))
}
```

Open the printed URL in a browser. The dashboard updates in real time; click any node to open the detail sidebar.

### Stop and Restart controls

The inspector exposes UI buttons for stopping and restarting the pipeline. Wire them to your context in a run loop:

```go
insp := inspector.New()
defer insp.Close()
fmt.Println("Inspector:", insp.URL())

// Build pipeline once — Run can be called multiple times.
// ...
sink := merged.ForEach(store, kitsune.WithName("store"))

for {
    ctx, cancel := context.WithCancel(context.Background())
    cancelCh, restartCh := insp.CancelCh(), insp.RestartCh()

    // Both Stop and Restart cancel the current run.
    go func() {
        select {
        case <-cancelCh:  cancel()
        case <-restartCh: cancel()
        case <-ctx.Done():
        }
    }()

    sink.Run(ctx, kitsune.WithHook(insp))
    cancel()

    // Loop on Restart; break on Stop or any other exit.
    select {
    case <-restartCh:
        continue
    default:
    }
    break
}
```

See [`examples/inspector`](examples/inspector) for a complete branching pipeline (Partition, Broadcast, Merge, supervision, overflow) with all inspector features enabled.

## Tails

Tails are Kitsune's extension modules — optional packages that connect pipelines to external systems. Each tail is a separate Go module, so you only pull in the dependencies you use.

| Tail | Import | What |
|---|---|---|
| **kredis** | `github.com/jonathan/go-kitsune/tails/kredis` | Redis Store, Cache, DedupSet, list source/sink |
| **ksqlite** | `github.com/jonathan/go-kitsune/tails/ksqlite` | SQLite query source, single/batch insert sinks |
| **kfile** | `github.com/jonathan/go-kitsune/tails/kfile` | File, CSV, JSONL sources and sinks |
| **khttp** | `github.com/jonathan/go-kitsune/tails/khttp` | Paginated HTTP GET source, POST/webhook sink |

All tails follow the **user-managed connections** principle: you create, configure, and close clients yourself. Kitsune never opens or closes connections.

## Runnable examples

The [`examples/`](examples/) directory contains 17 standalone programs (root module, no extra deps):

```
examples/basic       — FromSlice, Map, Lift, ForEach
examples/filter      — Filter, Tap, Take, Drain
examples/batch       — Batch, Unbatch, BatchTimeout
examples/flatmap     — 1:N expansion patterns
examples/concurrent  — parallel workers, Ordered output, LogHook
examples/fanout      — Partition, MergeRunners
examples/errors      — Skip, Retry, RetryThen
examples/state       — MapWith, FlatMapWith, Ref
examples/compose     — Through for reusable middleware
examples/generate    — paginated APIs, tickers, infinite streams
examples/window      — sliding and tumbling windows
examples/broadcast   — Broadcast to multiple downstream runners
examples/dedupe      — Dedupe with MemoryDedupSet
examples/iter        — FromIter with iter.Seq sources
examples/metrics     — custom Hook for per-stage metrics collection
examples/supervise   — Supervise, RestartOnError, RestartOnPanic, PanicSkip
examples/overflow    — Overflow, DropNewest, DropOldest, OverflowHook
examples/inspector   — live web dashboard: branching topology, supervision, Stop/Restart
```

Run any example: `go run ./examples/basic`

Four additional examples live in their own modules (they import tail packages):

```
examples/files   — kfile CSV/JSONL sources and sinks
examples/redis   — kredis list source, Redis-backed Store and Cache
examples/sqlite  — ksqlite query source, batch insert sink
examples/http    — HTTP pagination source with retry
```

## Design

See [`doc/design.md`](doc/design.md) for the full design specification, including the progressive complexity model, internal architecture, and design rationale.

See [`doc/tuning.md`](doc/tuning.md) for performance tuning guidance: buffer sizing, concurrency, batching, and memory trade-offs.

See [`doc/benchmarks.md`](doc/benchmarks.md) for baseline throughput numbers (items/sec, allocs/op) on Apple M1.

## License

MIT
