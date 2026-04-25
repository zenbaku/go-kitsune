# Features

Kitsune is an in-process pipeline engine. It handles the plumbing: channels, goroutines, backpressure, error routing, and observability. Stage functions stay focused on business logic.

---

## :material-valve: Automatic backpressure

Every stage is connected by a bounded channel. When a downstream stage is slow, its input channel fills up and the upstream stage blocks; backpressure propagates all the way to the source automatically. Nothing is dropped silently and nothing queues unboundedly.

Buffer size is configurable per stage with [`Buffer(n)`](options.md#buffern-int). The overflow behaviour ([`Block`, `DropNewest`, `DropOldest`](options.md#overflows-overflowstrategy)) is also configurable when blocking is not appropriate.

---

## :material-shield-check-outline: Compile-time type safety

`Pipeline[T]` carries the element type `T` through the graph. Free functions like [`Map`](operators.md#map) and [`FlatMap`](operators.md#flatmap) can change the type; methods like [`Filter`](operators.md#filter) and [`Take`](operators.md#take) preserve it. Every stage transition is checked at compile time: no type assertions, no `interface{}`, no runtime surprises.

```go
orders   := kitsune.Map(raw, parseOrder)      // Pipeline[Order]
enriched := kitsune.Map(orders, enrich)        // Pipeline[EnrichedOrder]
batched  := kitsune.Batch(enriched, kitsune.BatchCount(500))       // Pipeline[[]EnrichedOrder]
```

---

## :material-lightning-bolt-outline: Per-stage concurrency

Add [`Concurrency(n)`](options.md#concurrencyn-int) to any stage to spin up `n` parallel workers reading from the same input channel. Arrival order is not preserved by default (workers finish in I/O completion order); add [`Ordered()`](options.md#ordered) to re-sequence output without sacrificing throughput.

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
| [`Partition(p)`](operators.md#partition) | Route each item to one of two typed branches based on a predicate |
| [`Broadcast(n)`](operators.md#broadcast) | Copy every item to N independent consumer branches |
| [`Share(src)`](operators.md#share) | Register consumers one at a time, each with independent options |
| [`Balance(n)`](operators.md#balance) | Round-robin distribute across N branches |
| [`KeyedBalance(n, keyFn)`](operators.md#keyedbalance) | Route by `hash(key) % n` for stable per-key assignment |
| [`Merge(...)`](operators.md#merge) | Fan N same-type pipelines back into one |
| [`Zip / ZipWith`](operators.md#zip--zipwith) | Pairwise combine two streams into one |
| [`LatestFrom`](operators.md#latestfrom--latestfromwith) | Combine a primary stream with the latest value from a secondary |

All fan-out operators integrate with `MergeRunners` so every branch shares the same source and runs within a single `Run` call. [See the operator catalog →](operators.md#fan-out--fan-in)

---

## :material-layers-triple-outline: Batching & windowing

Group items before passing them downstream.

| Operator | Trigger |
|---|---|
| [`Batch(n)`](operators.md#batch) | Every N items (or `BatchTimeout` deadline) |
| [`MapBatch(n, fn)`](operators.md#mapbatch) | Batch → call fn → flatten; ideal for bulk API calls |
| [`Window(n)`](operators.md#window) | Count-based tumbling window |
| [`SlidingWindow(n, step)`](operators.md#slidingwindow) | Overlapping windows |
| [`SessionWindow(gap)`](operators.md#sessionwindow) | Gap-based session grouping |
| [`ChunkBy(keyFn)`](operators.md#chunkby) | Consecutive same-key grouping |
| [`ChunkWhile(predFn)`](operators.md#chunkwhile) | Consecutive predicate grouping |

```go
batched := kitsune.Batch(enriched, 500, kitsune.BatchTimeout(2*time.Second))
_, err  := batched.ForEach(bulkInsert, kitsune.Concurrency(4)).Run(ctx)
```

---

## :material-memory: Stateful processing

`MapWith` and `MapWithKey` give stage functions access to typed `Ref` state that lives for the lifetime of one `Run`. No global variables, no external stores for in-process accumulation.

**[`MapWith`](operators.md#mapwith)**: one shared `Ref` for the entire stream. Suitable for running totals, sequence numbers, or any aggregate that spans all items.

**[`MapWithKey`](operators.md#mapwithkey)**: one `Ref` per unique key, sharded across workers by `hash(key) % n`. Items for the same key always land on the same worker: per-entity state never crosses goroutine boundaries. This is the **in-process actor model**, lock-free by design.

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
| [`Halt`](operators.md#halt) (default) | Stop the pipeline and return the error from `Run` |
| [`Skip`](operators.md#skip) | Drop the failed item and continue |
| [`Return(v)`](operators.md#return) | Emit a default value in place of the failed item |
| [`RetryMax(n, backoff)`](operators.md#retrymax--retrythen) | Retry up to N times with configurable backoff |
| [`RetryThen(n, backoff, h)`](operators.md#retrymax--retrythen) | Retry, then apply handler `h` if all attempts fail |
| [`MapResult(fn)`](operators.md#mapresult) | Route successes to one pipeline, errored items (with the original input) to another |

Backoff helpers: [`FixedBackoff`, `ExponentialBackoff`, `JitteredBackoff`](operators.md#backoff-helpers).

---

## :material-electric-switch: Circuit breaker

[`CircuitBreaker`](operators.md#circuitbreaker) wraps a stage function and tracks consecutive failures. After `FailureThreshold` failures the circuit opens: subsequent items receive `ErrCircuitOpen` immediately without calling the function. After `CooldownDuration` it enters half-open state and allows `HalfOpenProbes` test calls through before deciding to close or re-open.

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

[`RateLimit`](operators.md#ratelimit) applies a token-bucket limiter to a pipeline stage.

- `RateLimitWait` (default): block until a token is available. Backpressure propagates upstream.
- `RateLimitDrop`: silently discard excess items. Useful for metrics sampling.
- `Burst(n)`: allow short bursts above the steady-state rate.

For **per-entity rate limiting** (each user gets an independent budget), use [`MapWithKey`](operators.md#mapwithkey). Key-sharded routing means per-user state never crosses goroutine boundaries; no mutex, no contention.

---

## :material-restart: Supervision & restart

`Supervise` wraps any stage with automatic restart semantics. Use it for long-lived consumer stages that should recover from transient errors without bringing down the whole pipeline.

| Policy | Behaviour |
|---|---|
| [`RestartOnError`](options.md#supervise) | Restart the stage goroutine when it returns a non-nil error |
| [`RestartOnPanic`](options.md#supervise) | Recover panics and restart |
| [`RestartAlways`](options.md#supervise) | Restart on both errors and panics |

Configurable backoff between restart attempts prevents tight retry loops on persistent failures.

---

## :material-refresh: Pipeline-level retry

[`Retry`](operators.md#retry) re-runs an entire upstream pipeline from scratch when it errors: the right primitive for sources that must reconnect on failure (websocket tails, CDC streams, long-poll HTTP).

```go
kitsune.Retry(
    kitsune.Generate(websocketTail),
    kitsune.RetryForever(kitsune.ExponentialBackoff(100*time.Millisecond, 30*time.Second)),
)
```

Unlike `OnError(RetryMax(...))` which retries individual item transformations within a running stage, `Retry` tears down and re-subscribes to the source pipeline on each attempt. Items emitted during failed attempts are forwarded downstream immediately and not replayed.

| Constructor | Behaviour |
|---|---|
| [`RetryUpTo(n, backoff)`](operators.md#retry) | At most `n` total attempts including the first |
| [`RetryForever(backoff)`](operators.md#retry) | Retry indefinitely until context cancellation |

The `RetryStrategy` type exposes `WithRetryable(fn)` to restrict which errors trigger a retry and `WithOnRetry(fn)` for logging or metrics hooks.

---

## :material-puzzle-outline: Stage composition

[`Stage[I, O]`](operators.md#stagei-o-then-through-or) is a typed function `func(*Pipeline[I]) *Pipeline[O]`. It is a first-class value: store it in a variable, pass it to a function, compose it with [`Then`](operators.md#stagei-o-then-through-or). [`Composable[I, O]`](operators.md#stage-composition) is the interface both `Stage` and [`Segment`](operators.md#segment) satisfy, so they compose interchangeably.

```go
var ParseInt  kitsune.Stage[string, int]   = ...
var Double    kitsune.Stage[int, int]      = ...
var Stringify kitsune.Stage[int, string]   = ...

pipeline := kitsune.Then(kitsune.Then(ParseInt, Double), Stringify)
```

[`Segment[I, O]`](operators.md#segment) wraps a `Stage` with a business name. Constituent stages carry that name in `GraphNode.SegmentName`, making the group visible in `Describe()` and the inspector dashboard.

```go
fetch   := kitsune.NewSegment("fetch",  fetchStage)
enrich  := kitsune.NewSegment("enrich", enrichStage)
publish := kitsune.NewSegment("publish", publishStage)

pipeline := kitsune.Then(kitsune.Then(fetch, enrich), publish)
```

`Stage.Or(fallback)` wraps a primary stage with a typed fallback: if the primary fails, the same item is passed to the fallback. Stages are independently testable with [`FromSlice`](operators.md#fromslice) + [`Collect`](operators.md#collect-first-last-count-any-all-find-contains); no mocks, no infrastructure.

---

## :material-play-circle-outline: Side effects { #side-effects }

[`Effect[I, R]`](operators.md#effect) models externally-visible side effects (publish to a queue, write to a database, call an external API) with retry, per-attempt timeout, and required-vs-best-effort outcomes. Every input produces exactly one [`EffectOutcome[I, R]`](operators.md#effect): success carries the result; terminal failure carries the last error. The pipeline does not error out; downstream decides how to route success vs failure.

```go
out := kitsune.Effect(messages, publish,
    kitsune.EffectPolicy{
        Required:       true,
        Retry:          kitsune.RetryUpTo(3, kitsune.ExponentialBackoff(100*time.Millisecond, 2*time.Second)),
        AttemptTimeout: 5 * time.Second,
    },
)
```

[`TryEffect`](operators.md#tryeffect) is a two-output convenience that splits outcomes by `Err`: ok branch + failed branch. [`DryRun()`](options.md#dryrun) skips every `Effect` call so you can validate pipeline wiring without producing side effects.

---

## :material-clipboard-text-clock-outline: Run summary { #run-summary }

Every `Runner.Run`, `ForEachRunner.Run`, `DrainRunner.Run`, and `RunHandle.Wait` returns a [`RunSummary`](operators.md#run-summary) alongside the fatal error. `RunSummary.Outcome` classifies the run as `RunSuccess`, `RunPartialSuccess` (best-effort `Effect`s failed), or `RunFailure` (required `Effect`s failed or the pipeline returned a fatal error). `Duration`, `CompletedAt`, and a `MetricsSnapshot` are populated on every run.

```go
summary, err := runner.Run(ctx)
log.Printf("run %v in %v: outcome=%v", summary.CompletedAt, summary.Duration, summary.Outcome)
```

[`(*Runner).WithFinalizer(fn)`](operators.md#withfinalizer) and [`(*ForEachRunner[T]).WithFinalizer(fn)`](operators.md#withfinalizer) register post-run callbacks that observe the summary. Multiple finalizers run in registration order; their errors are recorded in `RunSummary.FinalizerErrs` and do not change `Outcome`.

---

## :material-floppy-variant: Dev iteration { #higher-level-authoring }

[`WithDevStore(store)`](operators.md#devstore) is a development-time RunOption that captures each named [`Segment`](operators.md#segment)'s output on first run and replays from snapshot on subsequent runs. Iterate on a downstream segment without re-running expensive upstream work; delete a snapshot file to force its segment to re-run.

```go
store := kitsune.NewFileDevStore("/tmp/kitsune-snapshots")
runner.Run(ctx, kitsune.WithDevStore(store))
```

[`FromCheckpoint[T](store, name)`](operators.md#fromcheckpoint) loads a stored snapshot directly as a pipeline source for unit testing downstream stages from frozen upstream output. Strictly dev-only: no schema versioning, no production safety.

---

## :material-clock-fast: Time-based operators

| Operator | What it does |
|---|---|
| [`Ticker(d)`](operators.md#ticker) | Emit `time.Time` at interval `d` |
| [`Timer(d, fn)`](operators.md#timer) | Emit one value after delay `d` |
| [`Throttle(d)`](operators.md#throttle) | Emit at most one item per `d`, leading edge |
| [`Debounce(d)`](operators.md#debounce) | Emit only after `d` of silence |
| [`Sample(d)`](operators.md#sample) | Emit the latest item seen in each `d` window |
| [`Timeout(d)`](options.md#timeoutd-timeduration) | Cancel a stage function's context after `d`; combine with `OnError` |

All time operators accept a [`WithClock`](options.md#withclockc-clock) option for deterministic testing without `time.Sleep`. [See the operator catalog →](operators.md#time-based-operators)

---

## :material-chart-timeline-variant: Observability

**`Hook` interface** is called on every stage lifecycle event. Implement it to send telemetry anywhere.

**Built-in hooks:**

- `MetricsHook`: in-memory per-stage counters and latency histograms; JSON-serialisable snapshot.
- `LogHook`: structured `slog` output for every item and error.
- `MultiHook`: fan events to multiple hooks simultaneously.

**Tail hooks** (separate modules, zero-dependency on the core):

- [`kotel`](tails.md#kotel): OpenTelemetry spans and metrics
- [`kprometheus`](tails.md#kprometheus): Prometheus counters and duration histograms
- [`kdatadog`](tails.md#kdatadog): Datadog DogStatsD counts and distributions

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
