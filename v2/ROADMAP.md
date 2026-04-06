# go-kitsune v2 Roadmap

v2 is a clean rewrite that keeps v1's semantics while eliminating the `chan any` boxing overhead — every pipeline stage carries a fully typed `chan T`. The architecture uses a **blueprint model**: `Pipeline[T]` is a lazy description; channels are allocated fresh on each `Run()`, making pipelines reusable.

---

## Completed

### Foundation (blueprints + execution model)
- [x] `Pipeline[T]` blueprint — `build func(*runCtx) chan T`, channels allocated at run time
- [x] `runCtx` memoisation — shared upstream stages built only once per `Run()` (diamond-safe)
- [x] `Runner` / `Runner.Run` / `Runner.RunAsync` / `RunHandle` (pause, resume, wait, done, err)
- [x] `MergeRunners` — combine forked terminals into a single run
- [x] `ForEach` / `Drain` terminals
- [x] Stage options: `Concurrency`, `Ordered`, `Buffer`, `Overflow`, `WithName`, `Timeout`, `OnError`, `Supervise`, `WithSampleRate`, `WithDedupSet`
- [x] Run options: `WithHook`, `WithStore`, `WithCodec`, `WithCache`, `WithDrain`, `WithPauseGate`
- [x] Error handlers: `Retry`, `RetryThen`, `Return`, `Skip`
- [x] Backoff: `FixedBackoff`, `ExponentialBackoff`
- [x] Supervision: `RestartAlways`, `RestartOnError`, `RestartOnPanic`
- [x] Hooks: `GraphHook`, `BufferHook`, `SampleHook`, `SupervisionHook`, `OverflowHook`
- [x] `MultiHook`, `LogHook`
- [x] `Gate` / `NewGate` — external pause/resume of sources

### Sources
- [x] `FromSlice`, `From`, `Generate`, `FromIter`
- [x] `Channel[T]` — push-based source with `Send`, `TrySend`, `Close`
- [x] `Ticker`, `Interval`, `Timer`
- [x] `Unfold`, `Iterate`, `Repeatedly`, `Cycle`
- [x] `Concat` (factory-based, strictly ordered)
- [x] `Amb` — first-to-emit wins

### Core operators
- [x] `Map` (serial, concurrent, ordered-concurrent)
- [x] `FlatMap` (serial, concurrent, ordered-concurrent)
- [x] `Filter`, `Reject`
- [x] `Tap`
- [x] `Take`, `Drop`, `TakeWhile`, `DropWhile`
- [x] `TakeEvery`, `DropEvery`, `MapEvery`
- [x] `WithIndex`
- [x] `Pairwise`
- [x] `Intersperse`
- [x] `StartWith`, `DefaultIfEmpty`

### Batching & windowing
- [x] `Batch` (count-based, with optional timeout flush)
- [x] `Unbatch`
- [x] `Window` (non-overlapping count windows)
- [x] `SlidingWindow` (overlapping count windows)
- [x] `SessionWindow` (gap-based time windows)
- [x] `ChunkBy` (consecutive same-key grouping)
- [x] `ChunkWhile` (consecutive predicate grouping)

### Aggregation
- [x] `Scan`, `Reduce`
- [x] `Distinct`, `DistinctBy`
- [x] `Dedupe`, `DedupeBy`
- [x] `GroupBy`
- [x] `Frequencies`, `FrequenciesBy`
- [x] `Sort`, `SortBy`

### Time operators
- [x] `Throttle` (leading-edge rate limit)
- [x] `Debounce`
- [x] `Timestamp`, `TimeInterval`

### Advanced concurrency
- [x] `SwitchMap`, `ExhaustMap`, `ConcatMap`
- [x] `MapResult`, `MapRecover`

### Fan-out / fan-in
- [x] `Merge`
- [x] `Partition`
- [x] `Broadcast` / `BroadcastN`
- [x] `Balance`
- [x] `Zip`, `ZipWith`
- [x] `CombineLatest`, `CombineLatestWith`
- [x] `WithLatestFrom`, `WithLatestFromWith`
- [x] `Unzip`

### Terminals & collectors
- [x] `Collect`, `First`, `Last`, `Count`
- [x] `Any`, `All`, `Find`, `Contains`, `ElementAt`
- [x] `Sum`, `Min`, `Max`, `MinMax`, `MinBy`, `MaxBy`
- [x] `ToMap`, `SequenceEqual`
- [x] `ReduceWhile`
- [x] `TakeRandom` (reservoir sampling)
- [x] `Iter` (range-over-func, Go 1.23+)

### Composition helpers
- [x] `Stage[I, O]` — named function type, zero-cost, `Map`-compatible
- [x] `Then` — chain two stages
- [x] `Pipeline.Through` — method form of `Map(p, s)`
- [x] `LiftPure`, `LiftFallible`

### Backends
- [x] `MemoryStore`, `MemoryCache`, `MemoryDedupSet`
- [x] `JSONCodec` (default)

### Config (defined, partially wired)
- [x] `CacheBy` / `CacheTTL` / `CacheBackend` — option types exist in `config.go`
- [x] `WithCache` run option — runner-level cache default exists

### Regression tests (all passing)
- [x] `SequenceEqual` length-mismatch returns false
- [x] `StartWith` prefix always strictly before main stream
- [x] `ExhaustMap` inner errors propagate
- [x] `Merge` does not cross-contaminate stage lists

---

## Remaining

### P0 — API gaps (missing wiring)

- [x] **`CacheBy` wired into `Map`** — `rc.cache`/`rc.cacheTTL`/`rc.codec` threaded through `runCtx`; `Map`'s build closure wraps `fn` with cache lookup/write when `CacheBy` is set; stage-level `CacheBackend`/`CacheTTL` override runner defaults
- [x] **`WithDedupSet` wired into operators** — `DistinctBy` and `DedupeBy` now check `cfg.dedupSet`; when set, uses the external backend (Redis SETNX, Bloom filter) with string keys (`fmt.Sprintf`); for `DedupeBy`, presence of a set upgrades consecutive dedup to global dedup

### P1 — Middleware operators

- [x] **`RateLimit`** — token-bucket via `golang.org/x/time/rate`; `RateLimitWait` (backpressure) and `RateLimitDrop` (skip excess) modes; `Burst(n)`, `RateMode(m)` options
- [x] **`CircuitBreaker`** — Closed → Open → Half-Open state machine built on `Map`; `FailureThreshold(n)`, `CooldownDuration(d)`, `HalfOpenProbes(n)` options; all `StageOption`s (including `OnError`) apply; emits `ErrCircuitOpen` when open
- [x] **`Pool[T]` / `MapPooled`** — `NewPool[T](newFn)`, `Pooled[T]` with `Release()`, `MapPooled[I,O]` acquires slot → calls fn → emits `*Pooled[O]`, `ReleaseAll` bulk helper

### P2 — Observability

- [x] **`MetricsHook`** — lock-free atomic counters per stage (processed, errors, dropped, restarts, total processing time); `Stage(name)` / `Snapshot()` / `Reset()`; `Snapshot().JSON()` serialization; `AvgLatency()` on `StageMetrics`; implements `Hook + OverflowHook + SupervisionHook`
- [x] **Hook engine wiring** — `OnStageStart`, `OnItem` (with timing), `OnStageDone` now called by `mapSerial`, `mapConcurrent`, `mapOrdered`, `flatMap*`, and `ForEach`; `LogHook` and all other hooks now fire correctly

### P3 — Shared hooks module (`go-kitsune/hooks`)

**Goal:** tails work against both v1 and v2 engines; swapping the engine is a one-line `go.mod` change in user code.

```
github.com/zenbaku/go-kitsune/hooks   ← shared interface contract
        ↑                   ↑
github.com/zenbaku/go-kitsune (v1)    github.com/zenbaku/go-kitsune/v2
        ↑                                       ↑
tails/kotel, tails/files, …       (same tails, no changes needed when engine swaps)
```

- [x] Created `hooks/` as a standalone `go.mod` module — `Hook`, `OverflowHook`, `SupervisionHook`, `SampleHook`, `GraphHook`, `BufferHook`, `GraphNode`, `BufferStatus`, `NoopHook`
- [x] v2 `internal/hooks.go` replaced all definitions with type aliases from `hooks`; v2 `go.mod` adds `require + replace`
- [x] v1 `engine/compile.go` same migration; v1 `go.mod` adds `require + replace`
- [x] Verified: `MyTail` implementing `hooks.Hook` satisfies both `kitsunev2.Hook` and `engine.Hook` without any conversion — the alias chain is identical

### P3.5 — Tails

~~The tails (`kotel`, `files`, `sqlite`, `http`) are v1-owned modules. The engine-swap architecture is already complete: once a tail is migrated to import `github.com/zenbaku/go-kitsune/hooks` instead of the v1 engine, it works with v2 for free — no v2-side work required. This was proven by the compile-time type check in P3.~~

**Remaining v1-side work** (tracked in v1, not here):
- Migrate `kotel`, `kdatadog`, `kprometheus` from `engine.Hook` to `hooks.Hook`

**The other 22 tails are engine-coupled by nature** — they build pipelines using operators (`FromSlice`, `Map`, etc.) whose signatures differ between v1 (`chan any`) and v2 (`Pipeline[T]`). Full engine-independence is not possible. The recommended dual-engine pattern is shared logic in an `internal/` package with thin `v1/` and `v2/` wrappers (see v1 ROADMAP for details). No v2-side work planned at this time.

### P4 — Performance parity with v1

v1 accumulated three major throughput optimizations that v2 did not initially have. After implementation, v2 exceeds v1:

- **Trivial workload (Map → Filter → Drain, 1M items)**: v2 = ~21 M/s vs v1 = ~13 M/s vs raw goroutines = ~5.9 M/s. v2 is 1.6× faster than v1.
- **Zero per-item allocations**: v2 = 47 allocs/run (pipeline setup only) vs v1 = ~2M allocs/run from chan any boxing. The typed fusion eliminates all boxing.
- **The gap disappears for I/O-bound work** (1 µs sleep: v2 ≈ v1 ≈ raw goroutines). Framework overhead is negligible when stages block on I/O.

The three optimizations:

- [x] **Stage fusion (typed build-time composition)** — `Pipeline[T].fusionEntry func(*runCtx, func(ctx, T) error) stageFunc` set at construction time on fast-path Map/Filter stages. When `ForEach` detects `p.fusionEntry != nil && p.consumerCount == 1 && NoopHook`, it calls `p.fusionEntry(rc, fn)` instead of `p.build(rc)`, composing the entire chain into one goroutine with zero inter-stage channel hops and **zero boxing**. `consumerCount atomic.Int32` is incremented by every operator that consumes a pipeline; fusion is skipped for shared pipelines.
- [x] **Receive-side micro-batching** — after each blocking receive on an input channel, drain up to 15 additional items non-blocking into a `[16]T` buffer before processing. Amortises goroutine-handoff cost. Applied to all fast-path variants: `mapSerialFastPath`, `filterFastPath`, `flatMapSerialFastPath`, `forEachFastPath`, and the fused chain hot loop.
- [x] **Drain protocol (send-side select elimination)** — replaced `select { case ch <- v: case <-ctx.Done(): }` with plain `ch <- v` in all fast-path stage runners and source fast paths. Drain goroutines (`for range inCh {}`) deferred at stage exit provide deadlock safety. Sources use `ch <- item` with post-send `ctx.Err()` check.

### P5 — Examples + CI

A runnable example program per major feature, smoke-tested in CI:

- [x] `examples/basic` — FromSlice → Map → Filter → ForEach
- [x] `examples/channel` — `Channel[T]` push-based source
- [x] `examples/concurrent` — `Map` with `Concurrency` + `Ordered`
- [x] `examples/fanout` — `Partition` + `MergeRunners`
- [x] `examples/broadcast` — `BroadcastN` + `MergeRunners`
- [x] `examples/stages` — `Stage[I,O]`, `Then`, `Through`
- [x] `examples/caching` — `CacheBy` + `MemoryCache`
- [x] `examples/ratelimit` — `RateLimit` with backpressure
- [x] `examples/circuitbreaker` — `CircuitBreaker` with cooldown
- [x] `examples/switchmap` — `SwitchMap` cancellation semantics
- [x] `examples/ticker` — `Ticker` + `Take`; `done`-channel fix so `Take` terminates infinite sources cleanly
- [x] `examples/timeout` — `Timeout` per-item deadline
- [x] `examples/hooks` — `LogHook` + `MetricsHook` wiring
- [x] `examples/inspector` — live `BufferHook` dashboard (excluded from CI smoke tests)
- [x] `Taskfile.yml` — `task v2:test`, `task v2:test:examples`, `task v2:test:all`
- [x] `.github/workflows/ci.yml` — v2 race tests + example smoke-test steps
- [x] `examples_test.go` — `TestExamples` with `t.Parallel()` + `exec.Command("go run ...")` per example

---

## Backlog

### P7 — API compatibility with v1

v2's goal is a drop-in engine swap: same public API, typed channels underneath. These are the remaining divergences to fix. Each item notes what v1 has, what v2 currently has, and what the fix is.

#### `Stage` type and `Through`

- [x] **`Stage[I,O]`** — changed from `func(ctx, I) (O, error)` to `func(*Pipeline[I]) *Pipeline[O]` (pipeline transformer matching v1). `Through` updated to `func(s Stage[T,T]) *Pipeline[T]` (no opts). `Then` composes two transformers. `stages/main.go` example updated.

#### Predicate / side-effect signatures

- [x] **`Filter`/`Reject` adapters** — added `FilterFunc[T](fn func(T) bool)` and `RejectFunc[T]` in `misc.go`; v2's richer `func(ctx, T) (bool, error)` form is kept as the canonical signature.
- [x] **`Tap` adapter** — added `TapFunc[T](fn func(T))` in `misc.go`.

#### Terminals as methods

- [x] `(p *Pipeline[T]) Collect`, `First`, `Last`, `Count`, `Any`, `All`, `Find`, `ReduceWhile` — added in `methods.go`. `ForEach` already existed as a method. `Sum`, `Min`, `Max`, `MinMax`, `Contains`, `Frequencies`, `SequenceEqual` cannot be added as methods (require extra type params or constrained `T`).

#### `First` / `Last` / `Min` / `Max` / `MinMax` empty-stream semantics

- [x] Changed all to return `(T, bool, error)` — `bool=false, err=nil` for empty stream. `ErrEmpty` kept for `ElementAt`.

#### `MinBy` / `MaxBy` — restore `less` parameter

- [x] Restored `less func(a, b K) bool`; dropped the `cmp.Ordered` constraint on `K`.

#### `MinMax` — restore `Pair[T,T]` return type

- [x] Now returns `(Pair[T,T], bool, error)`. `Pair` fields renamed `First`/`Second` (matching v1) from `Left`/`Right`.

#### `Frequencies` / `FrequenciesBy` — restore as terminals

- [x] Now `Frequencies(ctx, p) (map[T]int, error)` and `FrequenciesBy(ctx, p, keyFn) (map[K]int, error)`. Streaming operator variants kept as `FrequenciesStream` / `FrequenciesByStream`.

#### `SortBy` — restore `less` parameter

- [x] Restored `less func(a, b K) bool`; dropped the `cmp.Ordered` constraint.

#### `MapResult` — restore branching return type

- [x] Now returns `(*Pipeline[O], *Pipeline[ErrItem[I]])`. Added `ErrItem[I any]` type. Both branches share one stage and must be consumed together (same rule as `Partition`).

#### `MapRecover` — restore `recover` parameter

- [x] Now `MapRecover(p, fn, recover func(ctx, I, error) O) *Pipeline[O]`. `Result[T]` type retained for other uses.

#### `GroupBy` — restore `map[K][]V` return type

- [x] Now `GroupBy(ctx, p, keyFn) (map[K][]T, error)` (terminal). Ordered-slice variant kept as `GroupByOrdered`.

---

### P6 — v1 feature parity

Gaps identified by deep comparison with v1. These are v1 features absent from v2, not intentional removals.

#### Deduplication variants
- [ ] **`ConsecutiveDedup[T comparable](p) *Pipeline[T]`** — emit only when value changes (stateless per-item comparison, not set-based); v2's `Dedupe` uses an in-memory set (global)
- [ ] **`ConsecutiveDedupBy[T any, K comparable](p, keyFn func(T) K) *Pipeline[T]`** — emit only when the extracted key changes; distinct from `DedupeBy` which is set-based

#### Aggregation shorthands
- [ ] **`CountBy[T any](p, keyFn func(T) string, opts...) *Pipeline[map[string]int64]`** — streaming operator: emits a full snapshot of the count map after each item; key is always `string`; runs at `Concurrency(1)`
- [ ] **`SumBy[T any, V Numeric](p, keyFn func(T) string, valueFn func(T) V, opts...) *Pipeline[map[string]V]`** — like `CountBy` but sums a numeric value per key; `V` must satisfy the `Numeric` constraint; compose with `Throttle` for periodic snapshots

#### Intersperse variant
- [ ] **`MapIntersperse[T, O any](p, sep O, fn func(ctx, T) (O, error)) *Pipeline[O]`** — apply `fn` to each item then insert `sep` between consecutive mapped outputs (not before the first or after the last)

#### Time-based windowing
- [ ] **`Window[T](p, duration time.Duration) *Pipeline[[]T]`** — tumbling time window; emits a slice per elapsed interval; v2's current `Window(n)` is count-based only; note: `Batch` with `BatchTimeout` is NOT equivalent — it produces batches of up to N items flushed by time, not fixed-duration time buckets

#### Dead letter / error routing

- [x] **`ErrItem[I any]`** — done in P7; `struct { Item I; Err error }` in `advanced.go`
- [x] **`MapResult[I, O any]`** — done in P7; returns `(*Pipeline[O], *Pipeline[ErrItem[I]])`
- [ ] **`DeadLetter[I, O any](p, fn func(ctx, I) (O, error), opts...) (*Pipeline[O], *Pipeline[ErrItem[I]])`** — like `MapResult` but respects `OnError(Retry(...))` in opts; items that exhaust all retries go to the `ErrItem` branch; items that never error go directly to the success branch without touching the error branch
- [ ] **`DeadLetterSink[I any](p, fn func(ctx, I) error, opts...) (*Pipeline[ErrItem[I]], *Runner)`** — wraps a sink (ForEach-like fn) with dead-letter routing; returns the `ErrItem` pipeline and a `*Runner`; the caller must consume the `ErrItem` pipeline before calling `runner.Run`

#### Stage fallback composition
- [ ] **`Stage[I,O].Or(fallback Stage[I,O]) Stage[I,O]`** — returns a stage that calls the primary and, on error, calls `fallback` with the same input; composes with `Then`

#### State system
- [ ] **`Key[T any]`** — declares a named piece of typed run-scoped state; created with `NewKey[T](name string, initial T, opts ...KeyOption) Key[T]`; declare as package-level vars; supports `StateTTL(d)` option for lazy expiry
- [ ] **`Ref[T any]`** — concurrent-safe handle to a `Key`'s current value; injected by `MapWith`/`FlatMapWith`; API: `Get(ctx) (T, error)`, `Set(ctx, T) error`, `Update(ctx, func(T) T) error`, `UpdateAndGet(ctx, func(T) T) (T, error)`, `GetOrSet(ctx, T) (T, error)`; in-memory by default (mutex), Store-backed when `WithStore` is configured
- [ ] **`MapWith[I, O, S any](p, key Key[S], fn func(ctx, *Ref[S], I) (O, error), opts...) *Pipeline[O]`** — like `Map` but `fn` receives a `*Ref[S]` for mutable per-run state; state persists across items within a single `Run()`
- [ ] **`FlatMapWith[I, O, S any](p, key Key[S], fn func(ctx, *Ref[S], I, yield func(O) error) error, opts...) *Pipeline[O]`** — like `FlatMap` but stateful
- [ ] **`MapWithKey[I, O, S any](p, itemKeyFn func(I) string, key Key[S], fn func(ctx, *Ref[S], string, I) (O, error), opts...) *Pipeline[O]`** — variant where each item is routed to a per-item-key `Ref` shard; `itemKeyFn` partitions items into independent state cells
- [ ] **`FlatMapWithKey[I, O, S any]`** — same as `MapWithKey` but flat-maps

#### Enrich / bulk lookup
- [ ] **`MapBatch[I, O any](p, size int, fn func(ctx, []I) ([]O, error), opts...) *Pipeline[O]`** — collects up to `size` items, passes the slice to `fn`, flattens the returned slice; internally `Batch` + `FlatMap`; supports `BatchTimeout`, `Concurrency`, `OnError`; `fn` must return exactly `len(input)` items or an error
- [ ] **`LookupBy[T any, K comparable, V any](p, cfg LookupConfig[T, K, V], opts...) *Pipeline[Pair[T, V]]`** — batches items, deduplicates keys, calls `cfg.Fetch(ctx, []K) (map[K]V, error)`, emits `Pair{Item: t, Value: v}`; items whose key is absent in the fetch result receive the zero value for `V`; `LookupConfig` has `Key func(T) K`, `Fetch func(ctx, []K) (map[K]V, error)`, `BatchSize int` (default 100)
- [ ] **`Enrich[T any, K comparable, V, O any](p, cfg EnrichConfig[T, K, V, O], opts...) *Pipeline[O]`** — like `LookupBy` but lets the caller control the output type via `cfg.Combine func(T, V) O`; `EnrichConfig` adds `Combine` to the `LookupConfig` fields
