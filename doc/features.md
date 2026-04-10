# Features

Kitsune is an in-process pipeline engine. It handles the plumbing — channels, goroutines, backpressure, error routing, observability — so stage functions stay focused on business logic.

---

## :material-valve: Automatic backpressure

Every stage is connected by a bounded channel. When a downstream stage is slow, its input channel fills up and the upstream stage blocks — backpressure propagates all the way to the source automatically. Nothing is dropped silently and nothing queues unboundedly.

Buffer size is configurable per stage with `Buffer(n)`. The overflow behaviour (`Block`, `DropNewest`, `DropOldest`) is also configurable when blocking is not appropriate.

---

## :material-shield-check-outline: Compile-time type safety

`Pipeline[T]` carries the element type `T` through the graph. Free functions like `Map` and `FlatMap` can change the type; methods like `Filter` and `Take` preserve it. Every stage transition is checked at compile time — no type assertions, no `interface{}`, no runtime surprises.

```go
orders   := kitsune.Map(raw, parseOrder)      // Pipeline[Order]
enriched := kitsune.Map(orders, enrich)        // Pipeline[EnrichedOrder]
batched  := kitsune.Batch(enriched, 500)       // Pipeline[[]EnrichedOrder]
```

---

## :material-lightning-bolt-outline: Per-stage concurrency

Add `Concurrency(n)` to any stage to spin up `n` parallel workers reading from the same input channel. Arrival order is not preserved by default (workers finish in I/O completion order); add `Ordered()` to re-sequence output without sacrificing throughput.

```go
enriched := kitsune.Map(orders, callEnrichAPI,
    kitsune.Concurrency(20),
    kitsune.Ordered(),
)
```

**Starting points:** 10–20 for I/O-bound stages; `runtime.NumCPU()` for CPU-bound work. See the [Tuning guide](tuning.md) for buffer-sizing and concurrency trade-offs.

**Throughput:** linear `Map` runs at ~13 M items/sec on Apple M1 via stage fusion, receive-side micro-batching, and a zero-alloc fast path.

---

## :material-source-branch: Fan-out & fan-in

Split a stream into multiple branches or merge multiple streams into one.

| Operator | What it does |
|---|---|
| `Partition(p)` | Route each item to one of two typed branches based on a predicate |
| `BroadcastN(n)` | Copy every item to N independent consumer branches |
| `Share(src)` | Register consumers one at a time, each with independent options |
| `Balance(n)` | Round-robin distribute across N branches |
| `KeyedBalance(n, keyFn)` | Route by `hash(key) % n` for stable per-key assignment |
| `Merge(...)` | Fan N same-type pipelines back into one |
| `Zip / ZipWith` | Pairwise combine two streams into one |
| `WithLatestFrom` | Combine a primary stream with the latest value from a secondary |

All fan-out operators integrate with `MergeRunners` so every branch shares the same source and runs within a single `Run` call.

---

## :material-layers-triple-outline: Batching & windowing

Group items before passing them downstream.

| Operator | Trigger |
|---|---|
| `Batch(n)` | Every N items (or `BatchTimeout` deadline) |
| `MapBatch(n, fn)` | Batch → call fn → flatten; ideal for bulk API calls |
| `Window(n)` | Count-based tumbling window |
| `SlidingWindow(n, step)` | Overlapping windows |
| `SessionWindow(gap)` | Gap-based session grouping |
| `ChunkBy(keyFn)` | Consecutive same-key grouping |
| `ChunkWhile(predFn)` | Consecutive predicate grouping |

```go
batched := kitsune.Batch(enriched, 500, kitsune.BatchTimeout(2*time.Second))
err     := batched.ForEach(bulkInsert, kitsune.Concurrency(4)).Run(ctx)
```

---

## :material-memory: Stateful processing

`MapWith` and `MapWithKey` give stage functions access to typed `Ref` state that lives for the lifetime of one `Run`. No global variables, no external stores for in-process accumulation.

**`MapWith`** — one shared `Ref` for the entire stream. Suitable for running totals, sequence numbers, or any aggregate that spans all items.

**`MapWithKey`** — one `Ref` per unique key, sharded across workers by `hash(key) % n`. Items for the same key always land on the same worker: per-entity state never crosses goroutine boundaries. This is the **in-process actor model** — lock-free by design.

```go
// Per-user rate limiter: no mutex, no contention
kitsune.MapWithKey(events, func(e Event) string { return e.UserID }, budgetKey,
    func(ctx context.Context, ref *kitsune.Ref[Budget], e Event) (Result, error) {
        b, _ := ref.UpdateAndGet(ctx, deductOrReject(e))
        return b, nil
    },
    kitsune.Concurrency(8),
)
```

---

## :material-shield-sync-outline: Error routing

Each stage has an independent `OnError` policy. Errors never silently swallow data or crash the pipeline.

| Handler | Behaviour |
|---|---|
| `Halt` (default) | Stop the pipeline and return the error from `Run` |
| `Skip` | Drop the failed item and continue |
| `Return(v)` | Emit a default value in place of the failed item |
| `Retry(n, backoff)` | Retry up to N times with configurable backoff |
| `RetryThen(n, backoff, h)` | Retry, then apply handler `h` if all attempts fail |
| `DeadLetter(fn, ...)` | Route successes to one pipeline, exhausted failures to another |

Backoff helpers: `FixedBackoff`, `ExponentialBackoff`, `JitteredBackoff`.

---

## :material-electric-switch: Circuit breaker

`CircuitBreaker` wraps a stage function and tracks consecutive failures. After `FailureThreshold` failures the circuit opens: subsequent items receive `ErrCircuitOpen` immediately without calling the function. After `CooldownDuration` it enters half-open state and allows `HalfOpenProbes` test calls through before deciding to close or re-open.

```go
out := kitsune.CircuitBreaker(items, callAPI,
    []kitsune.CircuitBreakerOpt{
        kitsune.FailureThreshold(5),
        kitsune.CooldownDuration(30 * time.Second),
        kitsune.HalfOpenProbes(2),
    },
    kitsune.OnError(kitsune.Skip()),
)
```

---

## :material-speedometer: Rate limiting

`RateLimit` applies a token-bucket limiter to a pipeline stage.

- `RateLimitWait` (default): block until a token is available. Backpressure propagates upstream.
- `RateLimitDrop`: silently discard excess items. Useful for metrics sampling.
- `Burst(n)`: allow short bursts above the steady-state rate.

For **per-entity rate limiting** (each user gets an independent budget), use `MapWithKey`. Key-sharded routing means per-user state never crosses goroutine boundaries — no mutex, no contention.

---

## :material-restart: Supervision & restart

`Supervise` wraps any stage with automatic restart semantics. Use it for long-lived consumer stages that should recover from transient errors without bringing down the whole pipeline.

| Policy | Behaviour |
|---|---|
| `RestartOnError` | Restart the stage goroutine when it returns a non-nil error |
| `RestartOnPanic` | Recover panics and restart |
| `RestartAlways` | Restart on both errors and panics |

Configurable backoff between restart attempts prevents tight retry loops on persistent failures.

---

## :material-puzzle-outline: Stage composition

`Stage[I, O]` is a typed function `func(*Pipeline[I]) *Pipeline[O]`. It is a first-class value: store it in a variable, pass it to a function, compose it with `Then`.

```go
var ParseInt  kitsune.Stage[string, int]   = ...
var Double    kitsune.Stage[int, int]      = ...
var Stringify kitsune.Stage[int, string]   = ...

pipeline := kitsune.Then(kitsune.Then(ParseInt, Double), Stringify)
```

`Stage.Or(fallback)` wraps a primary stage with a typed fallback: if the primary fails, the same item is passed to the fallback. Useful for DB-then-cache or primary-API-then-secondary-API patterns.

Stages are independently testable with `FromSlice` + `Collect` — no mocks, no infrastructure.

---

## :material-clock-fast: Time-based operators

| Operator | What it does |
|---|---|
| `Ticker(d)` | Emit `time.Time` at interval `d` |
| `Interval(d)` | Emit a monotonically increasing counter at interval `d` |
| `Timer(d, fn)` | Emit one value after delay `d` |
| `Throttle(d)` | Emit at most one item per `d`, leading edge |
| `Debounce(d)` | Emit only after `d` of silence |
| `Sample(d)` | Emit the latest item seen in each `d` window |
| `Timeout(d)` | Cancel a stage function's context after `d`; combine with `OnError` |

All time operators accept a `WithClock` option for deterministic testing without `time.Sleep`.

---

## :material-chart-timeline-variant: Observability

**`Hook` interface** is called on every stage lifecycle event. Implement it to send telemetry anywhere.

**Built-in hooks:**

- `MetricsHook` — in-memory per-stage counters and latency histograms; JSON-serialisable snapshot.
- `LogHook` — structured `slog` output for every item and error.
- `MultiHook` — fan events to multiple hooks simultaneously.

**Tail hooks** (separate modules, zero-dependency on the core):

- `kotel` — OpenTelemetry spans and metrics
- `kprometheus` — Prometheus counters and duration histograms
- `kdatadog` — Datadog DogStatsD counts and distributions

**Live inspector dashboard:** add one line to any pipeline to open a real-time web UI with a live DAG, per-stage throughput sparklines, buffer fill gauges, and stop/restart controls. [See the inspector guide →](inspector.md)

---

## :material-power-plug-outline: 27 integrations

Each tail is a separate Go module — import only what you use.

| Category | Tails |
|---|---|
| Messaging | `kkafka`, `knats`, `kjetstream`, `kamqp`, `kpulsar`, `kmqtt` |
| Cloud | `kpubsub`, `ksqs`, `kkinesis`, `kdynamo` |
| Databases | `kpostgres`, `kmongo`, `kclickhouse`, `ksqlite`, `kes`, `kredis` |
| Files & HTTP | `kfile`, `khttp`, `ks3`, `kwebsocket`, `kgrpc` |
| Observability | `kotel`, `kprometheus`, `kdatadog` |

[See the Tails guide for per-tail configuration and examples →](tails.md)
