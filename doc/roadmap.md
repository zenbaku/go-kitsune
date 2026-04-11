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

- [ ] **`Buffer(p, closingSelector)`**: signal-driven buffering: collect items until the `closingSelector` pipeline fires, then emit the accumulated slice and reset. Generalizes `Batch(size)` and `BatchTimeout` to arbitrary boundary signals. The defining pattern for "accumulate until external trigger" (e.g. flush on heartbeat, flush on upstream signal).

---

### State

- [ ] **`WithKeyTTL(d)` for `MapWith` / `FlatMapWith`**: evict per-key goroutines and their associated `Ref` state after `d` of inactivity. Without this, long-running pipelines keyed on high-cardinality fields (user IDs, session tokens) accumulate goroutines unboundedly. Eviction should be lazy (triggered on next access or a background sweeper, not a hard timer per key) to avoid thundering-herd on periodic activity bursts.

- [ ] **`TTLDedupSet(ttl)`**: a time-bounded `DedupSet` implementation that forgets keys after `ttl`. `MemoryDedupSet` grows unbounded on infinite streams; `BloomDedupSet` is bounded but cannot expire. `TTLDedupSet` enables safe `Distinct`/`Dedupe` on never-ending streams where "seen in the last N minutes" is the correct semantic. Implement with a ring-buffer of `(key, expiry)` pairs and lazy eviction on `Contains`.

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

- [ ] **"Choosing a concurrency model" guide**: document when to reach for each of the four concurrency primitives: `Concurrency(n)` (embarrassingly parallel, order optional), `Ordered()` (parallel but preserve input order), `MapWith` key-sharding (per-entity sequential, no locks), `Balance` + `Partition` (explicit fan-out). Include a decision flowchart and worked examples for the most common patterns: per-user rate limiting, parallel enrichment with ordering, stateful aggregation.

- [ ] **Fast-path eligibility documentation**: the fast-path and stage-fusion optimizations are entirely opaque. Users who add a `WithHook` for debugging or set `Concurrency(2)` don't know they've disabled the fast path, and can't diagnose the resulting throughput drop. Add a section in `doc/tuning.md` listing the exact conditions for fast-path eligibility and stage fusion, and expose `Pipeline.IsOptimized() bool` (or similar) for use in tests.

- [ ] **`WithInspectorStore(store)` for persistent inspector state**: the live inspector dashboard holds all pipeline metrics in-memory and loses them on restart. A `WithInspectorStore` option would let operators persist node snapshots and metric history to an external store (or even the existing `MemoryStore` equivalent with a longer TTL), enabling post-mortem analysis of pipeline behaviour after a crash or restart.

- [ ] **`benchstat` performance regression baseline**: commit a `testdata/bench/baseline.txt` snapshot produced by `benchstat` from the main branch. Add a CI step that runs benchmarks on PRs and diffs against the baseline, failing if any benchmark regresses beyond a threshold (e.g. 10%). Prevents silent throughput regressions from landing unnoticed, especially around fast-path and fusion logic.

- [x] **Property-based tests in the default test run**: the `pgregory.net/rapid` property tests in `properties_test.go` are gated behind a `// +build property` tag and excluded from `task test`. They catch classes of bugs (operator algebra invariants, fan-out completeness, ordering guarantees) that example-based tests miss. Remove the build tag and include them in `task test`; if runtime is a concern, run them with a reduced number of iterations (`rapid.Settings{MaxRuns: 50}`) in the default run and the full count in `task test:all`.

- [ ] **Unified tail integration test matrix**: the 27 tail packages each have their own test module, but there is no single CI step that reports their combined pass/fail status. `task test:ext` runs them sequentially but the output is scattered. Add a unified matrix report — a table of tail name, pass/fail, and skipped-reason (e.g. "no broker in CI") — so regressions across tails are visible at a glance rather than buried in individual log streams.

- [ ] **Supervision + error handler interaction documentation**: it is not documented whether `OnError` and `Supervise` can be used together on the same stage, and if so, which takes precedence. Add a dedicated section to `doc/operators.md` (or a new `doc/error-handling.md`) that covers: the evaluation order of error handler → supervision policy, worked examples for common combinations (retry-then-restart, skip-unless-fatal-then-restart), and the distinction between per-item errors (`OnError`) and stage-level restart (`Supervise`).

---


### Long-term

- [ ] **Typed `ErrorHandler[T]`**: `OnError(Return(value))` currently takes `any` for the fallback value because Go does not allow parameterizing `StageOption` on `T` without changing all call sites. In a v2 API, `ErrorHandler` should be parameterized: `ErrorHandler[T]` with `Return[T](value T)`, giving compile-time guarantees that the fallback type matches the stage's output type. Until then, a mismatched type silently produces a zero value at runtime; at minimum, document this limitation explicitly.

- [ ] **Pull-based (iterator) execution path alongside push-based channels**: the current channel-based model provides excellent throughput for high-volume pipelines but adds latency for request-response or low-volume scenarios (goroutine scheduling overhead even at 1 item). An optional pull-based path, where stages are composed as iterator chains rather than channel goroutines, would let the engine choose the right execution model based on pipeline structure and observed throughput. `Iter(ctx, p)` already exposes the pull interface; the question is whether the internal execution can use it natively.
