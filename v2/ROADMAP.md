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

- [ ] `examples/basic` — FromSlice → Map → Filter → ForEach
- [ ] `examples/channel` — `Channel[T]` push-based source
- [ ] `examples/concurrent` — `Map` with `Concurrency` + `Ordered`
- [ ] `examples/fanout` — `Partition` + `MergeRunners`
- [ ] `examples/broadcast` — `BroadcastN` + `MergeRunners`
- [ ] `examples/stages` — `Stage[I,O]`, `Then`, `Through`
- [ ] `examples/caching` — `CacheBy` + `MemoryCache`
- [ ] `examples/ratelimit` — `RateLimit` with backpressure
- [ ] `examples/circuitbreaker` — `CircuitBreaker` with cooldown
- [ ] `examples/switchmap` — `SwitchMap` cancellation semantics
- [ ] `examples/ticker` — `Ticker` + `Take`
- [ ] `examples/timeout` — `Timeout` per-item deadline
- [ ] `examples/hooks` — `LogHook` + `MetricsHook` wiring
- [ ] `examples/inspector` — live `BufferHook` dashboard (excluded from CI smoke tests)
- [ ] `Taskfile.yml` — `task test`, `task test:examples`, `task test:all`
- [ ] `.github/workflows/ci.yml` — fast race tests + examples smoke-test step
- [ ] `examples_test.go` — `TestExamples` with `t.Parallel()` + `exec.Command("go run ...")` per example
- [ ] Module version tag: `v2.0.0`
