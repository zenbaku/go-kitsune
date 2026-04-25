# Roadmap

Completed milestones are preserved in [roadmap-archive.md](roadmap-archive.md).

---

## Upcoming

### Correctness & safety

- [x] **`runWithDrain` timer goroutine leak**: `runWithDrain` creates a `time.After` inside a goroutine to enforce the drain timeout. If the pipeline finishes before the timeout fires, `drainCtx.Done()` exits the select — but the allocated `time.Timer` goroutine continues running until the duration elapses, creating a temporary goroutine leak per `Run` call that uses `WithDrainTimeout`. Replace `time.After(drainTimeout)` with `time.NewTimer(drainTimeout)` and call `timer.Stop()` when `drainCtx` is cancelled first.

- [x] **`RunHandle.errCh` single-consumer footgun**: `Wait()` consumes from the buffered(1) `errCh`; `Err()` returns the same channel. Concurrent callers of `Wait()` and `<-handle.Err()` will have one block forever after the other consumes the single value. Redesign `RunHandle` to store the result in an atomic field written once and signal completion by closing the existing `done` channel, so both `Wait()` and channel-select callers are safe to use concurrently.

- [x] **`kkafka.Consume` uncommitted last message on early exit**: When `yield(v)` returns false (downstream closed via `Take` or `TakeWhile`), the function exits without calling `CommitMessages` for the last fetched message. On reconnect, that message redelivers. This is correct at-least-once behaviour but is undocumented. Add an explicit note to the `Consume` godoc stating that the last message before a pipeline boundary will redeliver on reconnect.

- [x] **`Supervise` + `MapWithKey` silent state loss on restart**: When a supervised `MapWithKey` stage restarts after a panic or error, the in-memory key map is re-initialized and all accumulated `Ref` state is silently discarded. With `MemoryStore`, state is unrecoverable; with an external Store, state survives only if the Ref was flushed before the failure. Document this interaction prominently in the `Supervise` godoc and in `doc/operators.md`: supervised stateful stages require an external Store to survive restarts.

- [x] **`ExpandMap` has no depth or fanout bound**: `ExpandMap` performs BFS on an arbitrary graph with no limit on expansion depth or total items. A graph with high branching factor produces `fan^depth` items, exhausting memory silently. Add `MaxDepth(n int)` and `MaxItems(n int)` options that stop expansion when the limit is reached. At minimum add a prominent godoc warning and recommend pairing with `Take(n)` downstream.

---

### API and ergonomics

- [x] **`MergeRunners` should accept a `Runnable` interface instead of `*Runner`**: `Build()` currently leaks into user-facing code solely because `MergeRunners` requires `*Runner` arguments. Introduce a `Runnable` interface implemented by both `*ForEachRunner[T]` and `*Runner`, change `MergeRunners` to accept `...Runnable`, and add `RunAsync` directly to `ForEachRunner[T]`. `Build()` is retained for backwards compatibility but nothing in the new design requires it and the docs should stop recommending it. Added `Runnable` interface satisfied by `*Runner` and `*ForEachRunner[T]`; added `RunAsync` to `*ForEachRunner[T]` and `*DrainRunner[T]`; `Build()` retained as an identity on `*Runner` and unchanged on `*ForEachRunner[T]` for backwards compatibility.

- [x] **Operator cohesiveness — cleanup pass**: remove dead/duplicated symbols and standardise naming across the surface. Removed `ConsecutiveDedup`/`ConsecutiveDedupBy`, `DeadLetter`/`DeadLetterSink`, `WindowByTime`, `ElementAt`, `BroadcastN`, `Skip()` alias, `Lift` alias. Renamed `SkipLast`→`DropLast`, `SkipUntil`→`DropUntil`, `WithLatestFrom`→`LatestFrom`, `FrequenciesStream`→`RunningFrequencies` (and converted to true running semantics). Replaced string-keyed `CountBy`/`SumBy` with generic `RunningCountBy`/`RunningSumBy`. Relocated `MapBatch`, `MapIntersperse`, `EndWith`, `Stage.Or` from `compat.go` to their natural homes; deleted `compat.go` entirely. See [cleanup plan](../docs/superpowers/plans/2026-04-17-cohesiveness-cleanup.md).

- [x] **Operator cohesiveness — API changes**: redesign the operator surface for consistent flush triggers, dedup semantics, and grouping shape. Replace positional `Batch(p, size)` with option-driven flush triggers (`BatchCount`, `BatchMeasure`, `BatchTimeout`). Make `Dedupe`/`DedupeBy` global by default with a `DedupeWindow(n)` option (n=1 = consecutive, n=k = sliding). Drop `WithDedupSet` from `Distinct`/`DistinctBy`. Replace terminal `GroupBy` and `GroupByStream` with a single buffering pipeline operator returning `*Pipeline[map[K][]T]`. Reclassify `TakeRandom` from terminal to buffering pipeline operator. Add `Single` terminal with `OrDefault`/`OrZero`. Add `Within` for applying pipeline stages to slice contents. Add `RandomSample` streaming probabilistic filter. See [design](../docs/superpowers/specs/2026-04-17-operator-cohesiveness-design.md) and [API plan](../docs/superpowers/plans/2026-04-17-cohesiveness-api.md).

- [x] **`LookupBy` / `Enrich` batch timeout**: Under low throughput, items accumulate in the internal `MapBatch` buffer until the full batch size is reached, introducing unbounded latency before `Fetch` is called. Add a `BatchTimeout time.Duration` field to `LookupConfig` and `EnrichConfig`, using the same semantics as `Batch`'s `BatchTimeout` option: flush a partial batch when the duration elapses with no new item.

- [x] **Named result types to replace `Pair` proliferation**: `LookupBy` returns `Pair[T,V]`, `Zip` returns `Pair[A,B]`, `Timestamp` wraps items in `Pair[T,time.Time]`, `WithIndex` wraps in `Pair[T,int]`. Users read `.First` / `.Second` everywhere without type context. Replace with named, self-documenting types: `Timestamped[T]{Value T; Time time.Time}`, `Indexed[T]{Value T; Index int}`, `Enriched[T,V]{Item T; Value V}`. `Pair` can remain as a utility but named types should be the canonical output of their respective operators.

- [x] **`ConcatMap` should reject incompatible options at construction time**: `ConcatMap` appends `Concurrency(1)` last, silently overriding any user-supplied `Concurrency(n)`. A user who passes `Concurrency(4)` gets Concurrency(1) with no warning. Validate at construction time and panic (with a clear message) or return an error when a concurrency incompatible option is detected.

- [x] **Document `StageOption` last-write-wins semantics**: Passing `Buffer(16), Buffer(512)` produces buffer 512 — last option wins. This applies to every option and is not stated anywhere in `doc/options.md` or the `StageOption` type godoc. Add one sentence to both locations.

- [x] **`Generate` vs `Channel[T]` selection guide**: Both bridge external code into a pipeline — `Generate` is pull-based (callback yields values), `Channel[T]` is push-based (send from any goroutine). Neither godoc mentions the other, leaving users to discover the difference by trial and error. Add a "When to use each" note to both godocs and include a comparison in `doc/operators.md`.

---

### Performance

- [x] **Bypass codec serialization for `MemoryStore` Ref operations**: Every `ref.Get()` and `ref.Set()` in `MapWith` / `MapWithKey` marshals to/from `[]byte` via the Codec even when the backing Store is `MemoryStore` (in-process). For latency-sensitive inner loops this is wasted allocation. Add a fast path: if the Store implements an `InProcessStore` marker interface (or is type-asserted to be `*memoryStore`), store the value as `any` directly, bypassing codec.

- [x] **`DrainChan` goroutine burst on mass teardown**: Every transform stage does `defer func() { go internal.DrainChan(inCh) }()`. A `Take(1)` on a 20-stage pipeline launches 20 drain goroutines simultaneously at teardown. For pipelines that cycle frequently (via `Retry`), this creates sustained goroutine pressure. Investigate cooperative drain: pass a drain-ready signal down the topology so upstream stages can self-drain without spawning goroutines.

- [x] **Cooperative drain: full operator rollout** — complete conversion of all operators deferred from the prototype: `Map` concurrent/ordered paths and fused fast path; `Filter`; `Batch`, `BufferWith`, `ChunkBy`, `ChunkWhile`, `SlidingWindow`, `SessionWindow`; `FlatMap`, `ConcatMap`; `Merge`, `Amb`, `Zip`, `CombineLatest`, `LatestFrom`; `TakeUntil`, `DropUntil`; `GroupByStream`, `Balance`, `Partition`, `Broadcast`; `MapWith`, `MapWithKey`, `FlatMapWith`, `FlatMapWithKey`; `Scan`, `Reduce`, `Aggregate`; `LookupBy`, `Enrich`; all sources. Multi-consumer fan-out stages require explicit ref-count initialization using the actual downstream count. See [plan](../docs/superpowers/plans/2026-04-14-cooperative-drain-full-rollout.md).

- [x] **Sharded `DropOldest` outbox**: `dropOldestOutbox` holds a single mutex protecting drain-and-resend when the buffer is full. Under sustained backpressure with `Concurrency(n)`, all n workers serialize on this mutex; exactly the scenario `DropOldest` is designed for becomes its hot path. Implemented a sharded outbox (worker i uses shard i % n) to eliminate cross-worker contention when both `Overflow(DropOldest)` and `Concurrency(n > 1)` are active.

---

### Testing

- [x] **Property tests for windowing operators**: `Batch`, `BufferWith`, `SlidingWindow`, `SessionWindow`, `ChunkBy`, and `ChunkWhile` have no property-based tests. Laws to verify: `Batch(n)` completeness — every input item appears in exactly one batch; all batches except the last have exactly n items. `SlidingWindow(size, step)` — adjacent windows share exactly `size - step` elements. `SessionWindow(gap)` — items separated by more than gap appear in different sessions. `ChunkBy(keyFn)` — consecutive same-key items always cogroup; key boundaries produce new chunks. Use `testkit.NewTestClock()` for deterministic timing.

- [x] **Property tests for `GroupByStream`**: `GroupByStream` routes items to per-key sub-pipelines and is structurally one of the most complex operators in the library, with no property tests. Key law: for any input stream, items with key K must appear in arrival order in exactly the sub-pipeline rooted at K, with no cross-key contamination.

- [x] **Test `Supervise` + `MapWithKey` state contract on restart**: Add a test that panics a supervised `MapWithKey` stage mid-stream and verifies the exact post-restart state of per-key `Ref` values — zeroed with `MemoryStore`, preserved with an external Store. This test codifies the contract that is currently only implied by the documentation item above.

- [x] **Verify `benchstat` regression baseline** *(re-open: marked done in roadmap but not confirmed)*: The roadmap marks the `benchstat` performance regression baseline as complete, but `testdata/bench/baseline.txt` and CI integration were not confirmed present. Verify the baseline file is committed and the CI diff step is active; if not, implement from scratch.

---

### Developer experience

- [x] **Source selection guide (`doc/sources.md`)**: Fourteen source operators exist with overlapping use cases and no unified decision guide. Cover: `FromSlice` for in-memory data; `From` to wrap an existing channel; `Generate` for pull-based external sources; `Channel[T]` for push-based multi-sender bridging; `Ticker`/`Timer` for time-driven emission; `Unfold`/`Iterate` for mathematical sequences; `Concat` for sequential chaining; `Amb` for racing sources. Include the `Generate` vs `Channel[T]` comparison from the ergonomics item above.

- [x] **`ContextCarrier` vs `WithContextMapper` decision guide**: The two approaches for per-item trace propagation have meaningfully different trade-offs: `ContextCarrier` requires modifying the item type (impossible for third-party types); `WithContextMapper` is a stage option requiring no type changes. The comparison exists only as a one-line godoc mention. Add a section to `doc/operators.md` or a new `doc/tracing.md` with a comparison table and worked examples for both.

- [x] **Tail godoc quality baseline and template**: Audited all 27 tail packages against a standard template. Added missing caller-owns statements, worked examples, and delivery semantics declarations to each package godoc. Replaced all em dashes in godoc with colons or semicolons. Template codified in `doc/tails.md`.

---

### Ecosystem

- [x] **Shared tail interface contract (`doc/tails.md`)**: Wrote `doc/tails.md` covering: naming conventions (primary verbs plus accepted domain-idiomatic alternatives), parameter order, connection lifecycle ownership, error propagation, delivery semantics, the package godoc template, and a tail matrix with all 27 tails. Existing tails were audited and brought to conform.

- [x] **`kkafka` batched commit variant**: `kkafka.Consume` commits each Kafka message individually after `yield`, which serializes commit latency with processing latency. For high-throughput consumers, batched commits (flush N offsets at once, or on a timer) reduce broker round-trips significantly. Implemented as `BatchSize(n)` and `BatchTimeout(d)` options on `Consume` (variadic, backward-compatible) rather than a standalone function.

---

## Long-term

- [ ] **Event-time / watermark support**: All time-based operators (`Debounce`, `SessionWindow`, `Throttle`, etc.) use processing time — the wall clock when the item arrives. For Kafka, event sourcing, and log-replay workloads, items carry their own timestamps and windowing correctness requires event time. Add a `WithEventTime[T](fn func(T) time.Time)` option to windowing operators and a watermark mechanism (advancing a per-pipeline event-time frontier) that drives window closure based on event timestamps rather than `time.Now()`. This is the most significant remaining gap relative to production streaming systems (Flink, Kafka Streams).

- [ ] **Checkpointing and fault-tolerant restart**: Process restarts lose all `MapWithKey` state (unless an external Store is configured), all window accumulators, and all in-progress batch buffers. Add a `Checkpoint` mechanism: periodic serialization of operator state snapshots to the configured Store, with deterministic recovery on restart from the last checkpoint. Even a basic implementation for `MapWithKey` (flush key map on a signal or interval) would make fault-tolerant pipelines possible without external state management overhead.

- [x] **First-class composable segment type**: There is no way to define and name a reusable pipeline segment — a `*Pipeline[A] → *Pipeline[B]` transform that can be introspected, documented, and composed as a named unit. Users work around this with Go functions, which works but does not integrate with `Describe()`, `IsOptimized()`, or the inspector dashboard. A `Segment[A,B]` type wrapping a transform function with its own name and metadata would enable building reusable pipeline libraries on top of kitsune that remain observable. Implemented as `Segment[I,O]` in `segment.go` with a `Composable[I,O]` interface widening for `Then` and `Pipeline.Through` (M1 of the higher-level authoring layer; spec at `docs/superpowers/specs/2026-04-15-higher-level-authoring-design.md`).

- [ ] **Higher-level authoring layer M2-M4**: M1 (`Segment`) shipped 2026-04-24. The remaining three milestones from `docs/superpowers/specs/2026-04-15-higher-level-authoring-design.md` are still open:

    - **M2 — `Effect[I,R]` + outcome routing.** *(shipped 2026-04-25.)* Adds `Effect(p, fn, opts...)` returning `*Pipeline[EffectOutcome[I,R]]`; `TryEffect` two-output convenience; `EffectPolicy{Retry RetryStrategy, Required bool, AttemptTimeout time.Duration, Idempotent bool, IdempotencyKey func(any) string}`; options (`Required()`, `BestEffort()`, `AttemptTimeout(d)`, `WithIdempotencyKey(fn)`, `EffectStageOption(opt)`); and a `DryRun()` RunOption. `RetryPolicy` was renamed to `RetryStrategy` as part of the same change. M3 (`RunSummary`) and M4 (`DevStore`) are still open.
    - **M3 — `RunSummary` + `WithFinalizer`.** Upgrade `Runner.Run(ctx)` to return `(RunSummary, error)`. Source-breaking; project is pre-1.0 with no users so a hard change is acceptable. `RunSummary` carries `Outcome` (RunSuccess / RunPartialSuccess / RunFailure derived from `stageMeta.isEffect` + `effectRequired` + per-stage error counts), `Err`, `MetricsSnapshot`, `Duration`, `CompletedAt`. `WithFinalizer(fn)` attaches to `*Runner` and `*ForEachRunner[T]`; multiple finalizers run in registration order. M3 also needs to decide what `RunAsync` (returns `*RunHandle`) does with the summary.
    - **M4 — `DevStore` + `FromCheckpoint`.** Per-segment snapshot/replay for dev iteration. `WithDevStore(store)` RunOption captures each segment's output on first run and replays on subsequent runs; `ReplayThrough(name)` replays up to a named segment then runs live. `FromCheckpoint[T](store, name)` is a test-time source. Documentation must make clear this is dev-only (no schema versioning, no production safety).

    Pick milestones by impact: M2 (Effect) is the most user-facing; M3 (RunSummary) is the most invasive; M4 (DevStore) is self-contained dev tooling. Nothing in M2-M4 blocks any of the other Long-term items.

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

- [x] **`ContextCarrier` non-interface alternative**: tracing context propagation via `ContextCarrier` requires every item type to implement the interface. Third-party structs (Kafka messages, Protobuf-generated types, stdlib types) cannot be retrofitted without a wrapper. Add a `WithContextMapper[T](fn func(T) context.Context) RunOption` (or `StageOption`) that extracts a context from items by value rather than by interface. This makes per-item tracing a configuration choice rather than a type constraint, and removes the need for wrapper types in the common case where only one field carries the trace context.

---

### Developer experience

- [x] **`IsOptimized()` should surface ineligibility reasons**: `IsOptimized()` returns a per-stage boolean but does not say why a stage lost eligibility. A user who adds `OnError(Skip())` to a hot `Map` stage silently falls off the fast path with no feedback. `stageMeta` already carries `isFastPathCfg` and `supportsFastPath`; extend the return type to `[]OptimizationReport` (or equivalent) where each report names the failing condition: `"OnError handler disables fast path"`, `"Timeout set"`, `"Hook active"`, `"consumerCount > 1 disables fusion"`. This makes `IsOptimized` genuinely actionable rather than a binary indicator.

- [x] **Document fusion boundaries in the tuning guide**: stage fusion applies to `Map → Filter` chains ending at `ForEach`, but any operator that does not set `fusionEntry` (sources, `FlatMap`, `Batch`, all fan-out/fan-in operators) is a silent fusion boundary. After a `FlatMap`, even a long `Map → Filter → Map` chain will not fuse. The tuning guide explains fast-path conditions but does not list fusion boundaries. Add a table of operators that break fusion so users tuning a hot path know which operators to avoid or isolate.

- [x] **Document `MapPooled` mutex contention under high concurrency**: `Pool.Get()` acquires a `sync.Mutex`. With `Concurrency(8)`, eight goroutines call `pool.Get()` in the hot loop and all serialize on that lock. `sync.Pool` avoids this via per-P caches but does not offer LIFO semantics or no-eviction guarantees. Add a note to the `MapPooled` and `Pool` godoc explaining the contention behaviour at high worker counts, and consider providing a sharded pool variant (`ShardedPool[T]`) for use cases where allocation avoidance and high concurrency are both required.

- [x] **Fix `Pool.Warmup` godoc**: the current comment reads "Warmup is best-effort: sync.Pool may evict objects at any time (e.g. on GC)." This is copied from `sync.Pool` documentation and does not apply to the custom `Pool[T]` implementation, which never evicts. Rewrite the comment to accurately describe actual behaviour: objects pre-populated by `Warmup` remain in the pool until retrieved by `Get()` and not yet returned by `Release()`.

- [x] **Document `DropOldest` behaviour under sustained load**: `dropOldestOutbox` uses a fast lock-free send when the buffer has space but falls back to a mutex-protected drain-and-resend when full. In a pipeline where downstream is consistently slower than upstream — exactly the scenario `DropOldest` is designed for — the slow path becomes the hot path and all `Concurrency(n)` workers serialize on the mutex. Add a note to the `Overflow(DropOldest)` godoc and the tuning guide explaining this, so users can make an informed choice between `DropOldest`, `DropNewest`, and back-pressure.

---

### Long-term

- [x] **Typed `ErrorHandler[T]`**: `TypedReturn[O](val O) StageOption` added as a compile-time-safe alternative for the standalone case. A mismatched type in `Return` is documented and has a regression test. Full parameterization of `ErrorHandler[T]` across all handler combinators is deferred to v2.

- [x] **Pull-based (iterator) execution path**: deferred. `Iter(ctx, p)` exposes the pull interface externally. Internal engine adoption is a future concern.
