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

## API at a glance

| Category | Functions |
|---|---|
| **Sources** | `FromSlice`, `From` (channel), `Generate`, `FromIter` (iter.Seq) |
| **Transforms** | `Map`, `FlatMap`, `Batch`, `Unbatch`, `CachedMap`, `Window` |
| **Filters** | `.Filter`, `.Tap`, `.Take`, `.Dedupe`, `.Through` |
| **Fan-out/in** | `Partition`, `Broadcast`, `Merge` |
| **Terminals** | `.ForEach`, `.Drain`, `.Collect` |
| **State** | `NewKey`, `Ref` (Get/Set/Update), `MapWith`, `FlatMapWith` |
| **Config** | `Concurrency`, `Buffer`, `WithName`, `OnError`, `BatchTimeout` |
| **Errors** | `Halt`, `Skip`, `Retry`, `RetryThen`, `FixedBackoff`, `ExponentialBackoff` |
| **Observe** | `Hook` interface, `LogHook`, `WithHook` |

Free functions (`Map`, `FlatMap`, `Batch`) change the flowing type. Methods (`.Filter`, `.Tap`, `.Take`) preserve it. This is driven by Go's generics — methods can't introduce new type parameters.

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

### Deduplication and caching

```go
deduped := kitsune.Dedupe(input, func(e Event) string { return e.ID }, kitsune.MemoryDedupSet())

cached := kitsune.CachedMap(items, expensiveLookup,
    func(i Item) string { return i.Key },
    kitsune.MemoryCache(10000),
    5*time.Minute,
)
```

## Extensions

Extensions are separate Go modules — you only pull in the dependencies you use.

| Extension | Import | What |
|---|---|---|
| **kredis** | `github.com/jonathan/go-kitsune/tails/kredis` | Redis Store, Cache, DedupSet, list source/sink |
| **ksqlite** | `github.com/jonathan/go-kitsune/tails/ksqlite` | SQLite query source, single/batch insert sinks |
| **kfile** | `github.com/jonathan/go-kitsune/tails/kfile` | File, CSV, JSONL sources and sinks |

All extensions follow the **user-managed connections** principle: you create, configure, and close clients yourself. Kitsune never opens or closes connections.

## Runnable examples

The [`examples/`](examples/) directory contains 10 standalone programs:

```
examples/basic       — FromSlice, Map, Lift, ForEach
examples/filter      — Filter, Tap, Take
examples/batch       — Batch, Unbatch, BatchTimeout
examples/flatmap     — 1:N expansion patterns
examples/concurrent  — parallel workers, LogHook
examples/fanout      — Partition, MergeRunners
examples/errors      — Skip, Retry, RetryThen
examples/state       — MapWith, FlatMapWith, Ref
examples/compose     — Through for reusable middleware
examples/generate    — paginated APIs, tickers, infinite streams
```

Run any example: `go run ./examples/basic`

## Design

See [`doc/design.md`](doc/design.md) for the full design specification, including the progressive complexity model, internal architecture, and design rationale.

## License

MIT
