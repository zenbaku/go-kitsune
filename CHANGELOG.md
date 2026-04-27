# Changelog

All notable changes to go-kitsune are documented here. Format roughly follows [Keep a Changelog](https://keepachangelog.com/); versioning follows [SemVer](https://semver.org/), with the caveat that the project is pre-1.0 so minor releases may include source-breaking changes (called out below).

## [v0.2.0] - 2026-04-26

A large release that adds a higher-level authoring layer (Segment, Effect, RunSummary, DevStore), expands the operator surface, hardens concurrency and observability, and introduces a live inspector dashboard.

### Highlights

- **Higher-level authoring (M1-M5).** New abstractions on top of the operator core: `Segment` for named, graph-visible business units; `Effect` / `TryEffect` for externally-visible side effects with retry, per-attempt timeout, and required-vs-best-effort outcomes; `RunSummary` with structured run results returned from every `Run`; `DevStore` for snapshot-and-replay dev iteration. The inspector dashboard renders all four.
- **Live inspector dashboard.** `inspector.New()` plus `WithHook(insp)` opens a real-time web UI: pipeline graph, per-stage metrics, throughput, latency, event log, "Last Run" card, segment hulls, effect badges, REPLAY-badge for cached segments. Persistent state across restarts via `WithStore`.
- **Source-breaking signature change.** `Run` now returns `(RunSummary, error)`. `RetryPolicy` renamed to `RetryStrategy`. Operator cohesiveness pass renamed and removed several symbols. See "Breaking changes" below.

### Breaking changes

- **`Run` signature.** `Runner.Run`, `ForEachRunner.Run`, `DrainRunner.Run`, and `RunHandle.Wait` now return `(RunSummary, error)` instead of just `error`. The summary carries `Outcome`, `Err`, `Metrics`, `Duration`, `CompletedAt`, `FinalizerErrs`, and `EffectStats`. Existing call sites that ignored the return value compile unchanged with `_` for the summary; sites that captured the error need a leading `_, ` (or to use the summary).
- **`RetryPolicy` → `RetryStrategy`.** The retry configuration type was renamed for clarity. `RetryUpTo`, `FixedBackoff`, `ExponentialBackoff`, etc. continue to return the renamed type.
- **`Batch` API.** Positional `Batch(p, size)` replaced with option-driven flush triggers: `BatchCount(n)`, `BatchMeasure(fn, threshold)`, `BatchTimeout(d)`. Callers updating from v0.1 should pass the size via `BatchCount`.
- **`Dedupe` / `DedupeBy` semantics.** Now global by default with an explicit `DedupeWindow(n)` option (n=1 = consecutive, n=k = sliding). `WithDedupSet` removed from `Distinct` / `DistinctBy`.
- **Operator cohesiveness cleanup.** Removed: `ConsecutiveDedup`/`ConsecutiveDedupBy`, `DeadLetter`/`DeadLetterSink`, `WindowByTime`, `ElementAt`, `BroadcastN`, `Skip()` alias, `Lift` alias. Renamed: `SkipLast`→`DropLast`, `SkipUntil`→`DropUntil`, `WithLatestFrom`→`LatestFrom`, `FrequenciesStream`→`RunningFrequencies` (now true running semantics). `CountBy`/`SumBy` replaced by generic `RunningCountBy`/`RunningSumBy`.
- **`GroupBy` shape.** Terminal `GroupBy` and `GroupByStream` replaced by a single buffering pipeline operator returning `*Pipeline[map[K][]T]`.
- **Stage IDs migrated to `int64`** to prevent silent truncation on 32-bit platforms. `GraphNode.ID` and the internal `stageMeta.id` type changed from `int` to `int64`.
- **`Runnable` interface.** `MergeRunners` now accepts `...Runnable` instead of `...*Runner`. `*Runner`, `*ForEachRunner[T]`, and `*DrainRunner[T]` all satisfy `Runnable`. `Build()` is retained but no longer required at most call sites.
- **`Pair[T,V]` shifts to named result types.** `LookupBy`, `Zip`, `Timestamp`, and `WithIndex` now emit `Enriched[T,V]`, `Pair[A,B]` (kept), `Timestamped[T]`, and `Indexed[T]` respectively. Field access is now `.Item` / `.Value` / `.Time` / `.Index` instead of `.First` / `.Second`.

### Higher-level authoring layer

- **M1 - `Segment[I,O]`.** Named, graph-visible business units. `NewSegment(name, stage)` wraps a `Stage[I,O]` with a name that flows into `Pipeline.Describe()`, `GraphHook`, and the inspector dashboard's segment hulls. `Composable[I,O]` interface widening lets `Then` and `Pipeline.Through` accept both `Stage` and `Segment` interchangeably.
- **M2 - `Effect[I,R]` / `TryEffect[I,R]`.** Models externally-visible side effects. Returns `*Pipeline[EffectOutcome[I,R]]` (one outcome per input). `EffectPolicy` bundles `Retry`, `Required`, `AttemptTimeout`, `Idempotent`, `IdempotencyKey`, `IdempotencyStore`. Per-call options: `Required()`, `BestEffort()`, `AttemptTimeout(d)`, `WithIdempotencyKey(fn)`, `EffectStageOption(opt)`. New `DryRun()` RunOption skips every effect call so wiring can be validated without side effects.
- **M3 - `RunSummary` and `WithFinalizer`.** Every `Run` returns a structured summary: `Outcome` (RunSuccess / RunPartialSuccess / RunFailure derived from per-Effect-stage success/failure counters), `Err`, `Metrics`, `Duration`, `CompletedAt`, `FinalizerErrs`, `EffectStats`. `(*Runner).WithFinalizer` and `(*ForEachRunner[T]).WithFinalizer` register post-run callbacks; finalizers run in registration order and their errors are recorded in `FinalizerErrs` without changing `Outcome`. `MergeRunners` collects finalizers from input runners. `RunHandle.Summary()` is a non-blocking accessor.
- **M4 - `DevStore` and `FromCheckpoint`.** Per-`Segment` capture/replay for dev iteration. `WithDevStore(store) RunOption` enables: snapshot present → segment is bypassed and the cached items become its output; snapshot missing → segment runs live and output is captured. `FileDevStore` writes one JSON file per segment. `FromCheckpoint[T](store, name)` is a test-time source loading a stored snapshot directly.
- **M5 - Inspector RunSummary surfacing.** New optional `RunSummaryHook` interface (`OnRunComplete(ctx, RunSummary)`) fired by `Runner.Run` after finalizers complete. Inspector implements it, broadcasts a `summary` SSE event, includes the snapshot in `/state`, and renders a "Last Run" card with outcome badge, duration, completion time, optional fatal error, and collapsible finalizer-errors list.
- **Effect outcomes through `Hook.OnItem`.** Effect stages now fire `OnStageStart` / per-item `OnItem(dur, err)` / `OnStageDone`, so `MetricsHook.Stages["my-effect"]` populates with success / error counts and a latency histogram. `DryRun` skips hook calls.
- **Per-stage `EffectStats` in `RunSummary`.** `summary.EffectStats map[string]EffectStats` is keyed by stage name and carries `Required bool`, `Success int64`, `Failure int64`, `Deduped int64`. Map is always allocated; empty when no Effect stages exist.
- **Idempotency-key dedupe for `Effect`.** New `IdempotencyStore` interface with atomic `Add(ctx, key) (firstTime, err)`. When `EffectPolicy.Idempotent` is true with a non-nil `IdempotencyKey`, items whose key matches a previously-recorded invocation are skipped: the effect function is not called and the outcome is emitted with `Deduped: true`. Default per-Run in-memory store auto-attached; user-supplied `EffectPolicy.IdempotencyStore` enables cross-Run dedupe (Redis SETNX, etc.).
- **Inspector visibility for replayed Segments.** `tryReplaySegment` registers the replay loop as a synthetic stage with `Kind="segment-replay"`, fires `OnStageStart` / per-item `OnItem` / `OnStageDone`, and renders on the dashboard with a blue **REPLAY** badge inside the segment hull. Avg-latency cell shows an em dash for replay nodes.

### Inspector dashboard

- **`inspector.New(opts...)`** opens a live web dashboard on a random port. `WithStore(store)` attaches an `InspectorStore` for persistent state across restarts (graph topology, per-stage counters, log ring buffer, last `RunSummary`). `inspector.NewMemoryInspectorStore(logTTL)` is the built-in in-process implementation.
- **Pipeline Graph panel.** Auto-laid-out SVG with edges, segment hulls (tinted dashed-border groups), Effect corner badges (`REQ` for required, `BE` for best-effort, `REPLAY` for replayed segments), and a "Last Run" card.
- **Stage Metrics table.** Per-stage live counters: items, errors, drops, restarts, throughput sparkline, avg latency, buffer fill bar.
- **Detail sidebar.** Click any node for status, items, throughput, avg latency, errors / drops / restarts, buffer state, sparkline, and configuration (segment, effect kind, concurrency, buffer, overflow, batch size, timeout, retry / supervision indicators).
- **Stop / Restart / Pause / Resume controls.** SSE-driven; the running pipeline gets cancellation or restart signals through `CancelCh` / `RestartCh` / `PauseCh` / `ResumeCh`.
- **Theme and persistence.** Light/dark theme; state survives browser reload when `WithStore` is attached.

### New operators

- **Composition.** `Segment[I,O]`, `Composable[I,O]`, `Then`, `Pipeline.Through`.
- **Side effects.** `Effect[I,R]`, `TryEffect[I,R]`, `EffectOutcome[I,R]`, `EffectPolicy`, `EffectOption`, `Required`, `BestEffort`, `AttemptTimeout`, `WithIdempotencyKey`, `EffectStageOption`, `DryRun`, `IdempotencyStore`.
- **Routing and fan-out.** `KeyedBalance[T]` (consistent-hash fan-out), `Share` (hot multicast for late subscribers), `SwitchMap[I,O]` (cancels active inner on new input), `ExhaustMap[I,O]` (ignores new while inner active), `ConcatMap[I,O]` (sequential inner pipelines).
- **Error handling.** `MapResult[I,O]` (returns ok / errored branches), `MapRecover[I,O]` (per-item fallback), `Return[T]` error handler, `RetryIf` / `RetryIfThen` (per-error-type retry control), `TapError`, `Finally`, `Retry[T]` (re-subscribes the upstream on failure), `Materialize[T]` / `Dematerialize[T]`.
- **Buffering and windowing.** `BufferWith(p, closingSelector)`, batch flush triggers (`BatchCount`, `BatchMeasure`, `BatchTimeout`).
- **State.** `MapWith` / `MapWithKey` / `FlatMapWith` / `FlatMapWithKey` with `WithKeyTTL(d)` for lazy eviction; `Ref[T]` typed state cells; `TTLDedupSet(ttl)`.
- **Sources.** `Empty[T]()`, `Never[T]()`, `Iter`, `Unfold`, `Channel[T]`, `Ticker`, `Timer`, `Concat`, `Amb`.
- **Resources.** `Using[T,R](acquire, build, release)` with guaranteed cleanup.
- **Recursion.** `ExpandMap[T]` recursive BFS expansion with `MaxDepth`, `MaxItems`, `VisitedBy(keyFn)`.
- **Misc.** `IgnoreElements`, `SampleWith`, `Single` terminal with `OrDefault` / `OrZero`, `Within` (apply pipeline stages to slice contents), `RandomSample` (streaming probabilistic filter), `LookupBy` / `Enrich` (with `BatchTimeout`).

### Performance

- **Cooperative drain rollout.** All operators converted from `go internal.DrainChan(inCh)` per-stage drain goroutines to a cooperative drain protocol that signals upstream stages to self-drain. Eliminates the goroutine burst on `Take(n)` / `TakeWhile` teardown of long pipelines.
- **Sharded `DropOldest` outbox.** `Overflow(DropOldest)` + `Concurrency(n>1)` no longer serializes on a single mutex; per-worker shards remove cross-worker contention.
- **MemoryStore codec bypass.** `MapWith` / `MapWithKey` now skip codec serialization when the backing Store implements the `InProcessStore` marker interface, directly storing typed values via `GetAny`/`SetAny`.
- **Fast-path and fusion.** `Pipeline.IsOptimized()` returns `[]OptimizationReport` listing per-stage eligibility and the specific reason for ineligibility (e.g. `"OnError handler disables fast path"`).

### Observability

- **`MetricsHook`** with structured `MetricsSnapshot` (per-stage `Processed`, `Errors`, `Skipped`, latency histogram with `Min`/`Max`/percentiles, `BucketCounts`).
- **`LogHook`** built on `slog` with structured per-event records.
- **Optional hook interfaces.** `GraphHook.OnGraph(nodes)`, `BufferHook.OnBuffers(query)`, `OverflowHook.OnDrop`, `SupervisionHook.OnStageRestart`, `SampleHook.OnItemSample`, `RunSummaryHook.OnRunComplete`. Detected via type assertion; existing `Hook` implementations need not implement them.
- **Per-item tracing.** `WithContextMapper[T](fn)` extracts a `context.Context` from items by value, alternative to the type-constraint `ContextCarrier`.

### Correctness and safety

- `runWithDrain` no longer leaks a goroutine when the pipeline finishes before the drain timeout fires.
- `RunHandle.errCh` redesigned: `Wait()` and channel-select access are both safe to use concurrently.
- `Supervise` + `MapWithKey` documented and tested: in-memory state is lost on restart; external `Store`-backed Refs survive.
- `ExpandMap` bounds added: `MaxDepth(n)`, `MaxItems(n)`.
- `Pooled.Value` use-after-release protection (opt-in panic guard).
- `globalIDSeq` migrated to `int64` end-to-end (32-bit platform fix).
- `ConcatMap` rejects incompatible `Concurrency(n>1)` at construction.

### Testing and CI

- **Property-based tests in the default suite.** `pgregory.net/rapid` property tests previously gated behind a build tag now run as part of `task test`. Coverage includes `Batch`, `BufferWith`, `SlidingWindow`, `SessionWindow`, `ChunkBy`, `ChunkWhile`, `GroupByStream`, `Merge`, `Sort`, `Take`, `Broadcast`, `Balance`, `Empty`, `Never`/`Amb`, `IgnoreElements`, `Map`, `FlatMap`, `Scan`/`Reduce`, `Take`/`Drop`, `Filter`. The `Amb` suite caught a real bug missed by 27 example tests.
- **Fuzz targets** for `FromSlice → Map(panicRecover) → Collect` and `BloomDedupSet`.
- **Unified tail integration matrix.** All 27 tail packages report combined pass/fail in a single table.
- **`benchstat` regression baseline.** `testdata/bench/baseline.txt` snapshot plus a CI step that fails PRs with regressions beyond a threshold.
- **`testkit`** package: `NewTestClock`, deterministic `Step`/`Advance`, virtual time for windowing tests.

### Tails (separate modules)

27 tail integrations, each in its own module with its own go.mod and tag policy. Audited against a unified template (`doc/tails.md`): naming conventions, parameter order, lifecycle ownership, error propagation, delivery semantics, package godoc structure. Includes Kafka (`kkafka` with `BatchSize(n)` / `BatchTimeout(d)`), NATS, RabbitMQ, Postgres, Redis, S3, MongoDB, ClickHouse, SQS, Kinesis, Pub/Sub, and 16 others.

### Documentation

New guides:

- `doc/operators.md` - full operator reference.
- `doc/api-matrix.md` - operator × `StageOption` compatibility table.
- `doc/options.md` - `StageOption` and `RunOption` reference.
- `doc/sources.md` - source selection guide.
- `doc/tails.md` - tail-package conventions and matrix.
- `doc/inspector.md` - dashboard guide.
- `doc/run-summary.md` - `RunSummary` and `WithFinalizer`.
- `doc/side-effects.md` - `Effect` workflow.
- `doc/dev-iteration.md` - `DevStore` mental model.
- `doc/concurrency-guide.md` - choosing a concurrency model.
- `doc/error-handling.md` - `OnError` and `Supervise` interaction.
- `doc/tracing.md` - `ContextCarrier` vs `WithContextMapper`.
- `doc/tuning.md` - fast-path eligibility and fusion boundaries.

### Examples

50+ runnable examples under `examples/` covering each operator. New for v0.2:
- `examples/inspector-segment-replay/` - interactive demo of capture-then-replay with the dashboard.
- `examples/segment/`, `examples/inspector/`, `examples/within/`, plus dozens for individual operators.

### Module dependencies

- Root module now requires `github.com/zenbaku/go-kitsune/hooks v0.2.0` (was v0.1.0). The hooks submodule was retagged with the same set of changes (`SegmentName`, `IsEffect`, `EffectRequired` on `GraphNode`; stage IDs migrated to int64).

---

## [v0.1.0] - 2026-04-10

Initial release. Core pipeline operators, `Channel[T]`, `RunAsync`, `Stage`, `CacheBy`, `Dedupe`, plus v2 operators: `Timeout`, `Ticker`, `Interval`, `Pairwise`, `ConcatMap`, `SlidingWindow`, `MapResult`, `WithLatestFrom`. `hooks/v0.1.0` tagged in tandem.

[v0.2.0]: https://github.com/zenbaku/go-kitsune/compare/v0.1.0...v0.2.0
[v0.1.0]: https://github.com/zenbaku/go-kitsune/releases/tag/v0.1.0
