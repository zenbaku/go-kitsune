# Roadmap

Completed milestones are preserved in [roadmap-archive.md](roadmap-archive.md).

---

## Active / Near-term

### Operators

- [x] **`Retry[T]` standalone operator**: first-class `Retry(p, policy)` stage that re-subscribes to the upstream on failure, independent of the `OnError(RetryMax(...))` handler path. The handler-based retry is per-item; standalone `Retry` re-runs the entire upstream pipeline, making it the right primitive for sources that should reconnect on drop (e.g. a websocket tail that disconnects). Policy controls max attempts, backoff, and which errors are retryable.

- [x] **`SampleWith(p, sampler)`**: emit the most recent item from `p` whenever the `sampler` pipeline fires. Distinct from `Throttle` (which limits emission rate) and `Debounce` (which waits for a gap): `SampleWith` is driven by an external pipeline signal. Useful for "poll latest value every N seconds" patterns without holding a reference to the latest value manually.

- [x] **`IgnoreElements(p)`**: drain `p` for side effects and emit nothing downstream. Currently requires `Filter(p, func(_ T) bool { return false })` which reads as intent-obscuring. A named combinator is clearer and optimizable (no outbox allocation needed).

- [x] **`Empty[T]()`** and **`Never[T]()`**: named source primitives. `Empty()` completes immediately with no items; `Never()` blocks forever until context cancellation. Both are implied by existing combinators (`FromSlice(nil)`, `Generate` that never yields) but unnamed, which makes pipeline algebra tests awkward. Used as identity elements in composition proofs.

- [x] **`Materialize[T]` / `Dematerialize[T]`**: `Materialize` wraps each item and the terminal error into a sum type `Notification[T]{Value T; Err error; Done bool}`; `Dematerialize` unwraps it. Enables passing error events through operators that only handle `T`, and makes error routing composable without needing `MapResult` at every stage.

- [x] **`BufferWith(p, closingSelector)`**: signal-driven buffering: collect items until the `closingSelector` pipeline fires, then emit the accumulated slice and reset. Generalizes `Batch(size)` and `BatchTimeout` to arbitrary boundary signals. The defining pattern for "accumulate until external trigger" (e.g. flush on heartbeat, flush on upstream signal). Named `BufferWith` to avoid collision with the `Buffer(n)` stage option.

---

### State

- [x] **`WithKeyTTL(d)` for `MapWith` / `FlatMapWith`**: evict per-key goroutines and their associated `Ref` state after `d` of inactivity. Without this, long-running pipelines keyed on high-cardinality fields (user IDs, session tokens) accumulate goroutines unboundedly. Eviction should be lazy (triggered on next access or a background sweeper, not a hard timer per key) to avoid thundering-herd on periodic activity bursts.

- [x] **`TTLDedupSet(ttl)`**: a time-bounded `DedupSet` implementation that forgets keys after `ttl`. `MemoryDedupSet` grows unbounded on infinite streams; `BloomDedupSet` is bounded but cannot expire. `TTLDedupSet` enables safe `Distinct`/`Dedupe` on never-ending streams where "seen in the last N minutes" is the correct semantic. Implement with a ring-buffer of `(key, expiry)` pairs and lazy eviction on `Contains`.

---

### API and ergonomics

- [x] **`WithDefaultBuffer(n)` RunOption**: set the channel buffer size for all stages in a run without annotating every operator individually. Currently every stage defaults to 16; users tuning for latency (smaller buffers, lower memory) or throughput (larger buffers, less scheduling) must annotate each stage. A run-level default would let a single option flip the entire pipeline's buffering posture, with per-stage `Buffer(n)` still taking precedence.

- [x] **Consolidate `Ticker` / `Interval`**: `Interval` removed. Use `Ticker` (which emits `time.Time`) and `Map` to derive a counter if needed.

- [x] **Consolidate `Drain` / `ForEach`**: `Drain` and `DrainRunner` are deprecated with a godoc note. `ForEach` is now the canonical terminal stage for all item processing, including the discard case.

- [x] **Numeric type constraint on `Sum`**: already implemented; `Sum[T Numeric]` uses the `Numeric` constraint defined in collect.go. `Min`, `Max`, and `MinMax` also use it.

- [x] **Error action naming audit**: `ActionDrop()` is now the canonical name. `Skip()` is kept as a deprecated alias pointing to `ActionDrop()`.

- [x] **Per-error-type retry control**: `RetryIf(predicate func(error) bool, backoff)` and `RetryIfThen(predicate, backoff, fallback)` added. `RetryIf` retries when the predicate returns true and halts otherwise; `RetryIfThen` delegates to a fallback handler on non-retryable errors.

---

### Developer experience

- [x] **"Choosing a concurrency model" guide**: document when to reach for each of the four concurrency primitives: `Concurrency(n)` (embarrassingly parallel, order optional), `Ordered()` (parallel but preserve input order), `MapWith` key-sharding (per-entity sequential, no locks), `Balance` + `Partition` (explicit fan-out). Include a decision flowchart and worked examples for the most common patterns: per-user rate limiting, parallel enrichment with ordering, stateful aggregation.

- [x] **Fast-path eligibility documentation**: the fast-path and stage-fusion optimizations are entirely opaque. Users who add a `WithHook` for debugging or set `Concurrency(2)` don't know they've disabled the fast path, and can't diagnose the resulting throughput drop. Add a section in `doc/tuning.md` listing the exact conditions for fast-path eligibility and stage fusion, and expose `Pipeline.IsOptimized() bool` (or similar) for use in tests.

- [x] **`WithInspectorStore(store)` for persistent inspector state**: the live inspector dashboard holds all pipeline metrics in-memory and loses them on restart. A `WithInspectorStore` option would let operators persist node snapshots and metric history to an external store (or even the existing `MemoryStore` equivalent with a longer TTL), enabling post-mortem analysis of pipeline behaviour after a crash or restart.

- [x] **`benchstat` performance regression baseline**: commit a `testdata/bench/baseline.txt` snapshot produced by `benchstat` from the main branch. Add a CI step that runs benchmarks on PRs and diffs against the baseline, failing if any benchmark regresses beyond a threshold (e.g. 10%). Prevents silent throughput regressions from landing unnoticed, especially around fast-path and fusion logic.

- [x] **Property-based tests in the default test run**: the `pgregory.net/rapid` property tests in `properties_test.go` are gated behind a `// +build property` tag and excluded from `task test`. They catch classes of bugs (operator algebra invariants, fan-out completeness, ordering guarantees) that example-based tests miss. Remove the build tag and include them in `task test`; if runtime is a concern, run them with a reduced number of iterations (`rapid.Settings{MaxRuns: 50}`) in the default run and the full count in `task test:all`.

- [x] **Unified tail integration test matrix**: the 27 tail packages each have their own test module, but there is no single CI step that reports their combined pass/fail status. `task test:ext` runs them sequentially but the output is scattered. Add a unified matrix report — a table of tail name, pass/fail, and skipped-reason (e.g. "no broker in CI") — so regressions across tails are visible at a glance rather than buried in individual log streams.

- [x] **Supervision + error handler interaction documentation**: it is not documented whether `OnError` and `Supervise` can be used together on the same stage, and if so, which takes precedence. Add a dedicated section to `doc/operators.md` (or a new `doc/error-handling.md`) that covers: the evaluation order of error handler → supervision policy, worked examples for common combinations (retry-then-restart, skip-unless-fatal-then-restart), and the distinction between per-item errors (`OnError`) and stage-level restart (`Supervise`).

---

### Correctness & safety

- [x] **`Pooled.Value` use-after-release protection**: `Pooled.Value` is a public field, and calling `Release()` followed by a read of `Value` is documented as undefined behaviour — but Go cannot enforce it. Under time pressure, a developer who releases and then reads `Value` in a logging call gets silent corruption. Add a `released atomic.Bool` guard and panic on `Value` access after release (opt-in via a build tag or constructor flag if the overhead is unacceptable in hot paths). At minimum, promote the warning to a prominent `// WARNING:` block in the type godoc rather than a single sentence in `Release`.

- [x] **`globalIDSeq` truncation on 32-bit platforms**: `nextPipelineID()` atomically increments an `int64` counter but casts the result to `int`. On 32-bit targets `int` is 32 bits; at 2^31 stage IDs the cast silently wraps and channel-memoisation in `runCtx.chans` starts colliding, producing incorrect DAG wiring. Either return `int64` throughout (stage IDs, `stageMeta.id`, `stageMeta.inputs`), or add a `//go:build !386 && !arm` constraint and document the limitation.

- [x] **`refRegistry.get` should use a read lock**: `refRegistry.get()` acquires a full `sync.Mutex` on every call. Because `init()` is called exactly once during the build phase — before any stage goroutine starts — all subsequent `get()` calls are read-only against a fully-initialised map. Switching to `sync.RWMutex` with `RLock()` in `get()` is more semantically correct and avoids unnecessary write-lock contention when many stage goroutines call `get()` at startup. Alternatively, replace the map with `sync.Map`, which is optimised for append-once, read-many workloads.

---

### Testing

- [x] **Property tests for windowing operators**: `Batch`, `BufferWith`, `SlidingWindow`, `SessionWindow`, `ChunkBy`, and `ChunkWhile` have no property-based tests. These are the most stateful, most timing-sensitive operators in the library and the most likely to have subtle partitioning bugs. Laws to verify: `Batch(n)` completeness (every input item appears in exactly one batch; all batches except possibly the last have exactly `n` items); `BufferWith` partition (concatenation of all emitted slices equals the input in order); `SlidingWindow` overlap invariant (adjacent windows share exactly `size - 1` elements for stride 1); `SessionWindow` closure (items separated by more than the timeout appear in different sessions). The property tests already caught a real `Amb` bug missed by 27 example tests — windowing operators deserve the same treatment.

- [x] **Fuzz targets**: No fuzz tests exist. Add fuzz targets for: (1) operators that accept structured input where a malformed item could panic inside user-supplied functions (e.g. a `Map` fn that calls `json.Unmarshal`); (2) `BloomDedupSet` to verify no panics or incorrect contains-results under adversarial key inputs. Even a minimal `FuzzFromSlice` that drives `FromSlice → Map(panicRecover) → Collect` provides a panic-safety baseline. Fuzz targets live alongside tests and run automatically with `go test -fuzz`.

---

### API and ergonomics

- [x] **`Or` error discard documentation**: when `primary` errors and `fallback` also errors, the primary error is silently discarded and only the fallback error is returned. This is the right default (most recent error wins), but it is not documented. Users who want both errors for logging or metrics will be surprised. Either document the discard explicitly in the godoc, wrap both errors with `errors.Join` (making them both visible), or accept an optional `onPrimaryError func(error)` callback so callers can observe the discarded error without changing the return value.

- [ ] **`ContextCarrier` non-interface alternative**: tracing context propagation via `ContextCarrier` requires every item type to implement the interface. Third-party structs (Kafka messages, Protobuf-generated types, stdlib types) cannot be retrofitted without a wrapper. Add a `WithContextMapper[T](fn func(T) context.Context) RunOption` (or `StageOption`) that extracts a context from items by value rather than by interface. This makes per-item tracing a configuration choice rather than a type constraint, and removes the need for wrapper types in the common case where only one field carries the trace context.

---

### Developer experience

- [ ] **`IsOptimized()` should surface ineligibility reasons**: `IsOptimized()` returns a per-stage boolean but does not say why a stage lost eligibility. A user who adds `OnError(Skip())` to a hot `Map` stage silently falls off the fast path with no feedback. `stageMeta` already carries `isFastPathCfg` and `supportsFastPath`; extend the return type to `[]OptimizationReport` (or equivalent) where each report names the failing condition: `"OnError handler disables fast path"`, `"Timeout set"`, `"Hook active"`, `"consumerCount > 1 disables fusion"`. This makes `IsOptimized` genuinely actionable rather than a binary indicator.

- [ ] **Document fusion boundaries in the tuning guide**: stage fusion applies to `Map → Filter` chains ending at `ForEach`, but any operator that does not set `fusionEntry` (sources, `FlatMap`, `Batch`, all fan-out/fan-in operators) is a silent fusion boundary. After a `FlatMap`, even a long `Map → Filter → Map` chain will not fuse. The tuning guide explains fast-path conditions but does not list fusion boundaries. Add a table of operators that break fusion so users tuning a hot path know which operators to avoid or isolate.

- [ ] **Document `MapPooled` mutex contention under high concurrency**: `Pool.Get()` acquires a `sync.Mutex`. With `Concurrency(8)`, eight goroutines call `pool.Get()` in the hot loop and all serialize on that lock. `sync.Pool` avoids this via per-P caches but does not offer LIFO semantics or no-eviction guarantees. Add a note to the `MapPooled` and `Pool` godoc explaining the contention behaviour at high worker counts, and consider providing a sharded pool variant (`ShardedPool[T]`) for use cases where allocation avoidance and high concurrency are both required.

- [x] **Fix `Pool.Warmup` godoc**: the current comment reads "Warmup is best-effort: sync.Pool may evict objects at any time (e.g. on GC)." This is copied from `sync.Pool` documentation and does not apply to the custom `Pool[T]` implementation, which never evicts. Rewrite the comment to accurately describe actual behaviour: objects pre-populated by `Warmup` remain in the pool until retrieved by `Get()` and not yet returned by `Release()`.

- [ ] **Document `DropOldest` behaviour under sustained load**: `dropOldestOutbox` uses a fast lock-free send when the buffer has space but falls back to a mutex-protected drain-and-resend when full. In a pipeline where downstream is consistently slower than upstream — exactly the scenario `DropOldest` is designed for — the slow path becomes the hot path and all `Concurrency(n)` workers serialize on the mutex. Add a note to the `Overflow(DropOldest)` godoc and the tuning guide explaining this, so users can make an informed choice between `DropOldest`, `DropNewest`, and back-pressure.

---

### Long-term

- [x] **Typed `ErrorHandler[T]`**: `TypedReturn[O](val O) StageOption` added as a compile-time-safe alternative for the standalone case. A mismatched type in `Return` is documented and has a regression test. Full parameterization of `ErrorHandler[T]` across all handler combinators is deferred to v2.

- [x] **Pull-based (iterator) execution path**: deferred. `Iter(ctx, p)` exposes the pull interface externally. Internal engine adoption is a future concern.
