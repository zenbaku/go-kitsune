# Roadmap Archive

Completed milestones, preserved for reference. Active work is in [roadmap.md](roadmap.md).

---

### Code organization

- [x] **Split large implementation files by concern**: `operator.go` (2118 lines), `state.go` (1924 lines), and `fan.go` (979 lines) split into 11 focused files. `operator.go` → `operator_map.go`, `operator_flatmap.go`, `operator_filter.go`, `operator_take.go`, `operator_transform.go`. `state.go` → `state_ref.go`, `state_with.go`, `state_withkey.go`. `fan.go` → `fan_out.go`, `fan_combine.go`. Shared helpers (`orDefault`, `itemContext`, `reportErr`) extracted to `helpers.go`. Empty `fusion.go` deleted. No public API changes; all symbols remain in the root package.

---

### Operators

- [x] **`TapError`**: side-effect on the error path without consuming or rerouting the error. Natural complement to `Tap`: observe errors for logging, metrics, or alerting while letting them propagate unchanged. Fires a callback `func(ctx, error)` and re-returns the original error. Context cancellation does not trigger the callback. Implemented via `Generate`/`ForEach` (same pattern as `Catch`) to observe the terminal error from the upstream run.

- [x] **`Finally`**: guaranteed cleanup hook that fires when a stage exits (completion, cancellation, or error). Useful for resource tracking, test assertions, and teardown logic that must run regardless of how the pipeline terminates. Implemented via `Generate`/`ForEach` (same pattern as `TapError` and `Catch`). On early consumer stop (e.g. downstream `Take`), fn receives nil.

- [x] **`Using[T, R]`**: acquire a resource, build a pipeline from it, release the resource on exit. Signature: `Using(acquire func(ctx) (R, error), build func(R) *Pipeline[T], release func(R))`. Eliminates boilerplate `defer` patterns around `Run` calls for resource-bound stages (DB connections, file handles, etc.). If acquire fails, release is not called. Otherwise release is guaranteed exactly once via `defer`.

- [x] **`ExpandMap[T]`**: recursive BFS expansion. `fn(item)` returns a new `*Pipeline[T]`; all emitted items are fed back through `fn` again until the inner pipeline is empty. The defining primitive for tree traversal, recursive API pagination, and graph walks. Emits items at depth N before depth N+1. fn may return nil for leaf nodes. `ExpandMapFunc` provided as a context-free convenience wrapper. Accepts `...StageOption`: `WithName` (stage label for hooks/metrics), `Buffer`, and `VisitedBy(keyFn)` (cycle detection: skips items whose key was already seen, along with their subtrees; defaults to `MemoryDedupSet`, overridable with `WithDedupSet`).

- [x] **`KeyedBalance[T]`**: content-based fan-out by consistent hash. Routes all items with the same key to the same downstream branch, enabling per-entity parallelism without lock contention. Complements `MapWithKey` for stateful workloads. Counterpart to the round-robin `Balance`.

- [x] **`Share`**: hot multicast without a fixed subscriber count. Unlike `Broadcast(n)`, `Share` lets downstream stages subscribe at any point; late subscribers receive items from the moment they attach. Returns a factory (`func(...StageOption) *Pipeline[T]`); each call creates a new branch. All branches share one fan-out stage with synchronised blocking delivery (same semantics as `Broadcast`). Late-subscriber semantics: every registered branch receives items from the start of execution; no replay, no runtime subscription. Per-branch `Buffer` and `WithName` are supported; factory opts act as defaults. Calling the factory after `Run()` has started panics. A single subscriber is allowed (unlike `Broadcast`'s n≥2 requirement).

- [x] **`SwitchMap[I, O]`**: cancels the active inner pipeline when a new upstream item arrives, then starts a fresh one. The defining pattern for search-as-you-type and any scenario where a new request supersedes the previous one.

- [x] **`ExhaustMap[I, O]`**: ignores new upstream items while an inner pipeline is still active. The defining pattern for "submit once, wait for completion" flows: form submissions, idempotent API calls, debounced writes.

- [x] **`ConcatMap[I, O]`**: sequential inner pipelines; each inner pipeline completes before the next starts.

- [x] **`MapResult[I, O]`**: returns `(*Pipeline[O], *Pipeline[ErrItem[I]])`; both branches share one stage and must be consumed together (same rule as `Partition`).

- [x] **`MapRecover[I, O]`**: `fn` is called for each item; `recover func(ctx, I, error) O` is called on error, producing a fallback value instead of propagating the error.

- [x] **`Return[T]` error handler**: `OnError(Return[T](val))` replaces a failed item with a caller-supplied default value instead of dropping it (`Skip`) or halting the pipeline (`Halt`). Essential for enrichment pipelines where a failed lookup should produce a sentinel rather than a gap. (Planned as a standalone `OnErrorReturn` StageOption; landed as the `Return[T]` ErrorHandler used via `OnError`.)

- [x] **`MapBatch[I, O]`**: collects up to `size` items, passes the slice to `fn`, flattens results; supports `BatchTimeout`, `Concurrency`, `OnError`. Built on `Batch` + `FlatMap`.

- [x] **`LookupBy[T, K, V]`**: batches items, deduplicates keys, calls `Fetch(ctx, []K) (map[K]V, error)`, emits `Pair{Item, Value}`; items whose key is absent receive the zero value for `V`. `BatchSize` defaults to 100.

- [x] **`Enrich[T, K, V, O]`**: like `LookupBy` but calls `Join func(T, V) O` to produce the output directly.

- [x] **`SessionWindow`**: gap-based window that closes after a configurable period of inactivity: `kitsune.SessionWindow(p, gap time.Duration)`. Produces `[]T` slices like `Window` and `SlidingWindow`.

- [x] **`ChunkBy` / `ChunkWhile`**: consecutive same-key grouping and consecutive predicate grouping, respectively.

- [x] **`WindowByTime`**: tumbling time window; emits a `[]T` slice per elapsed interval. Named `WindowByTime` to avoid collision with the count-based `Window(p, n)`.

- [x] **`ConsecutiveDedup` / `ConsecutiveDedupBy`**: emit only when the value (or extracted key) changes; stateless per-item comparison, not set-based. Safe by engine design at `Concurrency(1)`.

- [x] **`CountBy` / `SumBy`**: streaming operators that emit a full snapshot of the count/sum map after each item; run at `Concurrency(1)`. Compose with `Throttle` or `Debounce` for periodic snapshots.

- [x] **`MapIntersperse[T, O]`**: applies `fn` to each item then inserts `sep` between consecutive mapped outputs.

- [x] **`RateLimit`**: token-bucket via `golang.org/x/time/rate`; `RateLimitWait` (backpressure) and `RateLimitDrop` (skip excess) modes; `Burst(n)`, `RateMode(m)` options.

- [x] **`CircuitBreaker`**: Closed → Open → Half-Open state machine built on `Map`; `FailureThreshold(n)`, `CooldownDuration(d)`, `HalfOpenProbes(n)` options; emits `ErrCircuitOpen` when open.

- [x] **`Pool[T]` / `MapPooled`**: `NewPool[T](newFn)`, `Pooled[T]` with `Release()`, `MapPooled[I,O]` acquires a slot → calls fn → emits `*Pooled[O]`; `ReleaseAll` bulk helper.

- [x] **`TakeEvery` / `DropEvery` / `MapEvery`**: operate on every Nth item.

- [x] **`WithIndex`**: tags each item with its 0-based position, emitting `Indexed[T]{Index, Value}`.

- [x] **`Intersperse`**: inserts a separator value between consecutive items.

- [x] **`StartWith[T]`**: prepend one or more items before a pipeline.

- [x] **`DefaultIfEmpty[T]`**: emit a default value when the upstream produces no items.

- [x] **`Contains[T comparable]`**: terminal: returns `true` if any item equals the given value; stops early on first match.

- [x] **`ElementAt`**: terminal: return the item at a 0-based index; `(zero, false, nil)` if the stream is shorter than the index. `ErrEmpty` kept for this operator.

- [x] **`ToMap[T, K, V]`**: terminal: collect the stream into `map[K]V`; duplicate keys: last value wins.

- [x] **`SequenceEqual[T comparable]`**: terminal: compare two finite pipelines item-by-item.

- [x] **`Timestamp[T]`**: tag each item with wall-clock time; emits `Timestamped[T]{Value, Time}`. Respects `WithClock`.

- [x] **`TimeInterval[T]`**: tag each item with elapsed duration since the previous item; emits `TimedInterval[T]{Value, Elapsed}`; first item has `Elapsed == 0`. Respects `WithClock`.

---

### Sources & composition helpers

- [x] **Push-based source**: `Channel[T]` with `Send`, `TrySend`, `Close`; the canonical pattern for bridging external event streams into a pipeline.

- [x] **Time sources**: `Ticker`, `Timer`; respect `WithClock` for deterministic tests.

- [x] **Generative sources**: `Unfold`, `Iterate`, `Repeatedly`, `Cycle`; `Concat` (factory-based, strictly ordered).

- [x] **`Amb[T]`**: race multiple pipeline factories; forward items exclusively from whichever factory emits first, cancelling all others. Useful for redundant sources, failover, and latency hedging.

- [x] **`Stage[I, O]`**: named function type (`func(*Pipeline[I]) *Pipeline[O]`); zero-cost pipeline transformer compatible with `Map`. `Then` composes two stages; `Pipeline.Through` is the method form. `Stage.Or(fallback)` tries the primary and, on no output, falls back with the same input.

- [x] **`LiftPure` / `LiftFallible`**: ergonomic wrappers for context-free, error-free (`func(I) O`) and fallible (`func(I) (O, error)`) functions; removes the most common friction point when writing simple transforms.

---

### Multi-graph and composition

- [x] **Universal multi-stream operators**: `Merge`, `Zip`, `ZipWith`, and `WithLatestFrom` all accept pipelines from independent graphs. Same-graph inputs use the engine-native node (fast path); independent inputs fall back to a `Generate`-based implementation that runs each pipeline concurrently.

- [x] **`CombineLatest[A, B]`**: symmetric counterpart to `WithLatestFrom`: either side emitting triggers an output, always paired with the latest value from the other side.

- [x] **`Balance[T]`**: round-robin fan-out: each item goes to exactly one of N output pipelines. Completes the fan-out vocabulary alongside `Broadcast` and `Partition`.

---

### State management

- [x] **`Key[T]` + `Ref[T]` + `Store` model**: `NewKey[T](name, initial, opts...)` declares a named piece of typed run-scoped state. `Ref[T]` is the concurrent-safe handle injected into stage functions; API: `Get`, `Set`, `Update`, `UpdateAndGet`, `GetOrSet`. In-memory by default (mutex); Store-backed when `WithStore` is configured.

- [x] **`MapWith` / `FlatMapWith`**: like `Map`/`FlatMap` but the function receives a `*Ref[S]` for mutable per-run state; runs at `Concurrency(1)`.

- [x] **`MapWithKey` / `FlatMapWithKey`**: variant where each item is routed to a per-item-key `Ref` shard via `itemKeyFn func(I) string`; enables per-user session tracking, per-device state machines, per-entity rate limiting without users managing maps manually. The `Store` interface supports arbitrary string keys, so the implementation delta is entirely in the public API and `Ref` lookup path.

- [x] **State TTL / expiry**: optional TTL parameter to `NewKey`: `kitsune.NewKey("session", SessionState{}, kitsune.StateTTL(30*time.Minute))`. `Ref.Get` returns the zero value and resets the slot when the TTL has elapsed. `MemoryStore` uses lazy expiry on read; distributed backends (Redis, DynamoDB) get this via their native TTL mechanisms.

- [x] **`CacheBy` wired into `Map`**: `rc.cache`/`rc.cacheTTL`/`rc.codec` threaded through `runCtx`; `Map`'s build closure wraps `fn` with cache lookup/write when `CacheBy` is set; stage-level `CacheBackend`/`CacheTTL` override runner defaults.

- [x] **`WithDedupSet` wired into operators**: `DistinctBy` and `DedupeBy` check `cfg.dedupSet`; when set, uses the external backend with string keys; for `DedupeBy`, presence of a set upgrades consecutive dedup to global dedup.

- [x] **`BloomDedupSet`**: a probabilistic Bloom filter backend for `DedupSet`; bounded memory with approximate correctness, useful when exact dedup is not required and the key space is huge. Implemented in `internal/bloom.go` using `hash/maphash` double-hashing (Kirsch-Mitzenmacker) with a `[]uint64` bit array. Constructor: `BloomDedupSet(expectedItems int, falsePositiveRate float64)`; panics on invalid input. Thread-safe via `sync.RWMutex`. Zero false-negative rate guaranteed; false-positive rate bounded by the configured probability.

---

### Foundation & typed engine

- [x] **`Pipeline[T]` blueprint model**: `Pipeline[T]` is a lazy description carrying a `build func(*runCtx) chan T` closure; channels are allocated fresh on every `Run()` via a memoising `runCtx`, making pipelines reusable and diamond-graph-safe. Replaced the prior `chan any` engine entirely, eliminating all per-item boxing. Result: ~21 M/s, 47 allocs/run (0/item) for the trivial Map→Filter→Drain benchmark.

- [x] **`Runner` / `RunAsync` / `RunHandle`**: `Runner.Run` blocks until the pipeline completes; `Runner.RunAsync` returns a `RunHandle` with `Pause()`, `Resume()`, `Wait()`, `Done()`, and `Err()`. `MergeRunners` combines forked terminals into a single run so all branches drain together.

- [x] **Stage options**: `Concurrency`, `Ordered`, `Buffer`, `Overflow`, `WithName`, `Timeout`, `OnError`, `Supervise`, `WithSampleRate`, `WithDedupSet`.

- [x] **Run options**: `WithHook`, `WithStore`, `WithCodec`, `WithCache`, `WithDrain`, `WithPauseGate`.

- [x] **Error handlers & backoff**: `Retry`, `RetryThen`, `Return`, `Skip`; `FixedBackoff`, `ExponentialBackoff`.

- [x] **Supervision**: `RestartAlways`, `RestartOnError`, `RestartOnPanic`.

- [x] **Hooks**: `GraphHook`, `BufferHook`, `SampleHook`, `SupervisionHook`, `OverflowHook`; `MultiHook`, `LogHook`; shared `github.com/zenbaku/go-kitsune/hooks` module so tails implement the interface once and satisfy both the engine and any tail-level consumer without conversion.

---

### Error handling & reliability

- [x] **Structured errors**: errors returned by `Runner.Run` carry the originating stage name, attempt number, and underlying cause in a typed `StageError` struct.

- [x] **Dead-letter routing**: `DeadLetter[I, O]` wraps a function with retry; items that exhaust all retries route to a second `*Pipeline[ErrItem[I]]` branch. `DeadLetterSink` does the same for sink functions, returning the dead-letter pipeline and the runner separately so the caller can wire them before calling `Run`.

- [x] **`ErrItem[I any]`**: `struct { Item I; Err error }` used by `DeadLetter`, `DeadLetterSink`, and `MapResult`.

---

### API correctness & completeness

- [x] **`RestartOnPanic` must not restart on regular errors**: `RestartOnPanic` and `RestartAlways` were identical: both restarted on errors and panics. Fixed by adding a `PanicOnly` guard to the supervision loop.

- [x] **`MapBatch` opts double-application**: `opts` were forwarded to both the internal `Batch` and `FlatMap` stages. Fixed by extracting only `BatchTimeout` for the collection stage and passing all opts to the processing stage.

- [x] **`Dedupe` context and error propagation**: `Dedupe` used `context.Background()` for store calls and silently discarded `Add` errors. Rewritten as a `Map` node so the pipeline context flows through and `Add` errors halt the pipeline.

- [x] **Mutable-state closures made explicit**: `SlidingWindow` and `Scan` closed over mutable state with no synchronization. Both now pass `Concurrency(1)` explicitly. `ConsecutiveDedup`/`ConsecutiveDedupBy` are safe by engine design.

- [x] **`Min` / `Max` / `MinMax` are O(1) memory**: rewritten using `Reduce` + an `optional[T]` accumulator. `MinBy`/`MaxBy` use a `byAcc[T,K]` variant. All return `(T, bool, error)`; `bool=false, err=nil` for empty stream.

- [x] **`MinMax` returns `Pair[T,T]`**: fields named `First`/`Second`.

- [x] **`MinBy` / `MaxBy` / `SortBy` restore `less` parameter**: `less func(a, b K) bool` restored; dropped the `cmp.Ordered` constraint on `K`.

- [x] **`Scan` and `Dedupe` accept `StageOption`**: `Scan` appends `Concurrency(1)` last to enforce sequential execution; `Dedupe` uses `WithDedupSet(s DedupSet)` to accept a custom backend.

- [x] **`Frequencies` / `FrequenciesBy` concurrency guard**: enforce `Concurrency(1)` explicitly. Terminal forms: `Frequencies(ctx, p) (map[T]int, error)` and `FrequenciesBy(ctx, p, keyFn) (map[K]int, error)`. Streaming variants kept as `FrequenciesStream` / `FrequenciesByStream`.

- [x] **`GroupBy` terminal**: `GroupBy(ctx, p, keyFn) (map[K][]T, error)`. Streaming ordered-by-first-seen variant available as `GroupByStream` (emits one `Group[K,T]` per distinct key when the source closes; was named `GroupByOrdered` during development).

- [x] **Terminals as methods**: `Collect`, `First`, `Last`, `Count`, `Any`, `All`, `Find`, `ReduceWhile` added as methods on `*Pipeline[T]` alongside the existing `ForEach`.

- [x] **`MapResult` branching return**: returns `(*Pipeline[O], *Pipeline[ErrItem[I]])`.

---

### Observability

- [x] **Per-item span propagation (`ContextCarrier`)**: items implement `ContextCarrier` (`Context() context.Context`) to carry a trace span from their origin (HTTP request, queue message, etc.). The engine threads the item's context into stage function calls; cancellation still comes from the pipeline stage context, item context contributes values only; so stage functions call `tracer.Start(ctx, "my-work")` normally with no signature changes. Zero overhead for items that don't implement the interface. `kotel` documents the pattern; per-item child spans appear automatically in any OTel-compatible backend. **Not yet covered: fan-in link propagation for `Batch`**: when N items with individual span contexts are collected into a batch, OTel recommends creating the batch span with `trace.WithLinks(...)` referencing each item's span. This requires either a new `BatchHook` interface that `kotel` implements, called by the `Batch` stage with the collected items before forwarding the slice, or a convention where the `[]T` batch type itself implements `ContextCarrier` with a pre-merged context. Deferred until a concrete use case drives the design.

- [x] **Configurable `SampleHook` rate**: `OnItemSample` fired every 10th item, hard-coded. Added `WithSampleRate(n int)` `RunOption`; pass a negative value to disable sampling entirely.

- [x] **Structured stage metadata in `GraphNode`**: exposes `BatchSize`, `Timeout`, `HasRetry`, and `HasSupervision` in addition to `Kind`, `Name`, `Concurrency`, `Buffer`, and `Overflow`.

- [x] **`MetricsHook`**: lock-free atomic counters per stage (processed, errors, dropped, restarts, total processing time); `Stage(name)` / `Snapshot()` / `Reset()`; `Snapshot().JSON()`; `AvgLatency()` on `StageMetrics`; implements `Hook + OverflowHook + SupervisionHook`.

- [x] **Hook engine wiring**: `OnStageStart`, `OnItem` (with timing), `OnStageDone` called by all stage runners; `LogHook` and all other hooks fire correctly.

- [x] **`Pipeline.Describe()`**: return a `GraphNode` tree representing the wired pipeline without executing it. Enables static validation that a pipeline is correctly assembled, and unit-testing of graph structure, without needing a full `Run`. Complements the live `inspector` dashboard.

---

### Performance

- [x] **`[]any` allocation pressure in FlatMap**: `FlatMap` now takes a yield callback (`func(ctx, I, func(O) error) error`) that emits items one at a time, eliminating the intermediate slice allocation entirely. All internal operators were migrated to the new signature.

- [x] **JSON-only cache and store serialization**: added `Codec` interface and `WithCodec` `RunOption`; a custom codec replaces `encoding/json` for both cache and store-backed `Ref` serialization. `JSONCodec` remains the default.

- [x] **`Concurrency(1)` fast paths**: for `Map`, `FlatMap`, `Filter`, and `Sink` at `Concurrency(1)` with no custom hook or error handler, a stripped-down loop bypasses `time.Now()`, `ProcessItem()` retry dispatch, hook dispatch, and outbox dispatch. Added `sync.Pool` reuse for ordered-concurrent slot structs, and `clear`+reslice batch-buffer reuse in `runBatch`.

- [x] **Receive-side micro-batching**: fast-path dispatchers drain up to 15 additional items non-blocking after each blocking channel receive, processing them as a local `[16]T` buffer. Trades 16 expensive goroutine-handoff receives for 1 expensive + 15 cheap non-blocking checks. Gated on `!n.Supervision.HasSupervision()` so pre-fetched items are never silently lost on supervision restart. Net result: MapLinear improved from 5,066 µs → 2,855 µs.

- [x] **Stage fusion**: consecutive fusible stages (Map, Filter, Sink at `Concurrency(1)` with default config and `NoopHook`) are detected at run time and collapsed into a single goroutine. At the typed engine layer, `fusionEntry func(*runCtx, func(ctx, T) error) stageFunc` is set at construction time; when `ForEach` detects a single-consumer fast-path chain it calls `fusionEntry` directly, composing the entire chain with zero channel hops and zero boxing. `consumerCount atomic.Int32` prevents fusion for shared (diamond) pipelines. Net result: 3-stage comparison improved from ~2.96 M/s to ~5.31 M/s.

- [x] **Concurrent fast paths**: extended the fast-path pattern to `runMapConcurrent` and `runFlatMapConcurrent` (unordered). Result: `MapConcurrent` −26%, `MapOrdered/unordered` −22%.

- [x] **Drain protocol (send-side select elimination)**: replaced `select { case ch <- v: case <-ctx.Done(): }` with plain `ch <- v` in all fast-path stage runners. Safety maintained by drain goroutines (`for range inCh {}`) deferred at stage exit. `nodeRunner` also defers drain goroutines for all non-Source stages. Net result: 3-stage comparison improved from 189 ms → 75.7 ms (~2.16× faster than raw goroutines).

---

### Test coverage & correctness

- [x] **Property-based testing**: `pgregory.net/rapid` property tests for operator algebra invariants: commutativity and multiset preservation of `Merge`, `Take(n) ∘ Sort` yields the n smallest elements, `Broadcast` fan-out completeness (every branch receives all items in order), `Balance` item-count preservation and round-robin fairness (per-branch counts differ by at most 1). Tests live in `properties_test.go` behind the `property` build tag; run with `task test:property`. Catches classes of bugs that example-based tests miss.

- [x] **`Pool.Put` and `Pool.Warmup`**: `Pool.Put(*Pooled[T])` exports the previously internal `put` method, allowing callers to return a pooled object without going through `Pooled.Release`. `Pool.Warmup(n int)` pre-populates the pool by calling the factory `n` times and immediately returning the objects; reduces first-request latency for latency-sensitive start-up paths. No-op for `n ≤ 0`. Best-effort per `sync.Pool` semantics (GC may evict objects at any time).

- [x] **`CircuitBreaker` `HalfOpenTimeout`**: new `HalfOpenTimeout(d time.Duration) CircuitBreakerOpt`; if the required number of successful probes has not been received within `d`, the circuit opens again (resetting the cooldown clock). Implemented in `circuitBreaker.allow()` by recording a `halfOpenDeadline` on each Closed→HalfOpen transition and checking it on every subsequent `allow()` call in the half-open state. Cleared when the circuit closes normally.

- [x] **`RateLimit` accepts `StageOption`**: signature changed from `opts ...RateLimitOpt` to `rlOpts []RateLimitOpt, stageOpts ...StageOption`, matching the `CircuitBreaker` API pattern. `WithName` and `Buffer` are applied to the stage metadata. All existing call sites updated.

- [x] **Test coverage parity with v1**: ported 51 missing test scenarios from the v1 archive across 10 test files: overflow (`DropNewest`, `DropOldest`, hook callbacks, broadcast, concurrent load), `WithDrain` (partial flush, hard stop, normal completion), `RunAsync`/pause-resume (7 scenarios), `WithPauseGate`, State TTL (4 scenarios), cache TTL/eviction, error handler combos (`ExponentialBackoff`, `RetryThenFallback`, `RestartAlways`), merge/broadcast edge cases, ordered concurrency edge cases, `Sort`/`SortBy`/`Unzip`, `Timeout` on `FlatMap`, CircuitBreaker (all gaps now closed), RateLimit (all gaps now closed). All skip stubs removed.

- [x] **Gate/Pause wired into sources**: `WithPauseGate` set `cfg.gate` but the gate was never propagated to `runCtx` or checked by any source stage, making `Pause()` / `Resume()` a no-op. Fixed by adding `gate *internal.Gate` to `runCtx`, threading it through `Runner.Run`, and checking it in `sourceStage`, `Generate` (both paths), and `FromSlice`.

- [x] **`WithDrain` two-phase shutdown**: `runWithDrain` cancelled `drainCtx` to stop sources, but this simultaneously cancelled the processing context that downstream stages (e.g. `Batch`) needed to flush buffered items. Fixed by calling `rc.signalDone()` in Phase 1 (sources watch `rc.done` via a goroutine in `Generate` that cancels `stageCtx`), so sources stop cleanly while downstream stages retain a live drain context. Context errors from the hard-stop timeout are suppressed on return.

- [x] **`RateLimit` swallowed preemptive context deadline errors**: `rate.Limiter.Wait` returns a custom `"rate: Wait(n=1) would exceed context deadline"` error before the context deadline actually fires. The stage was doing `return ctx.Err()` (which is nil at that point) instead of `return err`. Fixed.

- [x] **`kitsune/testkit` package**: `MustCollect`, `CollectAndExpect`, `CollectAndExpectUnordered`, and `RecordingHook`. `RecordingHook` implements all hook interfaces and provides typed accessors (`Items`, `Errors`, `Drops`, `Restarts`, `Graph`, `Dones`).

- [x] **Virtual time / `TestClock`**: `Clock` interface threaded through time-sensitive operators; `testkit.TestClock` advances on demand:

  ```go
  clock := testkit.NewTestClock()
  w := kitsune.Window(source, 5*time.Second, kitsune.WithClock(clock))
  // ... send items ...
  clock.Advance(5 * time.Second)  // triggers flush immediately, no sleep
  ```

---

### Benchmarks & performance evidence

- [x] **Benchmark vs raw goroutines**: 3-stage linear graph (Map → Filter → ForEach) at 1M items; comparisons against `sourcegraph/conc` and `reugn/go-streams`. Results published in `doc/benchmarks.md`.

- [x] **Allocation benchmark suite**: `AllocsPerOp` tracking for `Map`, `FlatMap`, `Batch`, `RateLimit`, and `CircuitBreaker`; zero-alloc hot paths verified and regressions caught in CI.

---

### Ecosystem: tails

- [x] **RabbitMQ / AMQP tail (`kamqp`)**: source (consume queue) and sink (publish to exchange) for RabbitMQ and any AMQP 0-9-1 broker.

- [x] **NATS JetStream tail (`kjetstream`)**: dedicated `tails/kjetstream` module covering patterns that `knats` cannot express: pull-batch consumers (`Fetch`, `FetchBytes`), ordered consumers with auto-recovery (`OrderedConsume`), key-value watch streams (`WatchKV`), and high-throughput async publish (`PublishAsync`, returns a sink + flush pair). KV upsert sink via `PutKV`. User owns all NATS objects; kitsune never creates or closes them.

- [x] **Azure Service Bus tail (`kazsb`)**: source and sink for Azure Service Bus queues and topics.

- [x] **Azure Event Hubs tail (`kazeh`)**: consumer group-based source for Azure Event Hubs.

- [x] **Elasticsearch / OpenSearch tail (`kes`)**: bulk-index sink and scroll/search source.

- [x] **Google Cloud Storage tail (`kgcs`)**: object source (list + read) and sink (upload).

---

### Developer experience

- [x] **Expanded examples**: worked examples for the patterns most often asked about: `MapWithKey` for per-user rate limiting, `MapWith` for running totals, `DeadLetter` for retry-with-fallback, and `Stage` composition via `Then` / `Or`.

---

### API: Go 1.23 iterator protocol

- [x] **`Pipeline[T]` as `iter.Seq[T]`**: `Iter(ctx, p, opts…)` exposes a pipeline as an `iter.Seq[T]` for range-over-func. Returns `(iter.Seq[T], func() error)`. Breaking out of the loop early cancels the pipeline and suppresses the resulting `context.Canceled`.

---

### API: dynamic flow control

- [x] **`RunHandle.Pause()` / `RunHandle.Resume()`**: a source-only `Gate` blocks sources from emitting while downstream stages continue draining in-flight items naturally. `RunAsync` creates a gate automatically; `WithPauseGate(g)` provides external gate control for synchronous `Run`. `Gate.Wait` is lock + nil-check only when open; zero channel ops on the fast path.

---

### Community & discoverability

- [x] **`CONTRIBUTING.md`**: dev setup, running tests and examples, PR workflow, commit message convention.
- [x] **GitHub issue templates**: separate templates for bug reports and feature requests.
- [x] **GitHub Discussions**: canonical place for "how do I…?" questions and design proposals.
- [x] **Fuzz testing**: `go test -fuzz` targets for the scheduler, overflow logic, and context cancellation paths; run as a nightly CI job.
