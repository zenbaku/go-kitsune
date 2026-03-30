# Roadmap

Items are loosely grouped by theme and ordered within each group by impact.
Checked items are complete.

---

## Error handling & reliability

- [x] **Structured errors** — errors returned by `Runner.Run` should carry the
  originating stage name, attempt number, and underlying cause in a typed
  `StageError` struct. Today the raw error gives no context about which stage
  failed or how many retries were exhausted.

- [x] **Dead-letter routing** — items that fail permanently (retries exhausted or
  `OnError(Skip())`) silently disappear. Added `DeadLetter` and `DeadLetterSink`
  which embed retry via `ProcessItem` and route exhausted-failure items to a
  second `ErrItem` pipeline using the existing `MapResult` two-port node.

---

## API correctness & completeness

- [x] **`RestartOnPanic` must not restart on regular errors** — `RestartOnPanic`
  and `RestartAlways` were identical: both restarted on errors and panics.
  Fixed by adding a `PanicOnly` guard to the supervision loop.

- [x] **`MapBatch` opts double-application** — `opts` were forwarded to both the
  internal `Batch` and `FlatMap` stages. Fixed by extracting only `BatchTimeout`
  for the collection stage and passing all opts to the processing stage.

- [x] **`Dedupe` context and error propagation** — `Dedupe` used
  `context.Background()` for store calls (cancellation not propagated) and
  silently discarded `Add` errors. Rewritten as a `Map` node so the pipeline
  context flows through and `Add` errors halt the pipeline.

- [x] **Mutable-state closures made explicit** — `SlidingWindow` and `Scan`
  closed over mutable state with no synchronization, relying silently on the
  default `Concurrency(1)`. Both now pass `Concurrency(1)` explicitly to their
  inner operator. `ConsecutiveDedup`/`ConsecutiveDedupBy` are safe by engine
  design (Filter is always single-goroutine); comments updated accordingly.

- [x] **`Min` / `Max` / `MinMax` should be O(1) memory** — all three call
  `p.Collect()` and buffer the entire stream before scanning for the extremum.
  Rewritten using `Reduce` + an `optional[T]` accumulator to keep only the
  current best candidate. `MinBy`/`MaxBy` use a `byAcc[T,K]` variant.

- [x] **`Scan` and `Dedupe` should accept `StageOption`** — both now accept
  `opts ...StageOption`. `Scan` appends `Concurrency(1)` last to enforce
  sequential execution. `Dedupe` uses `WithDedupSet(s DedupSet)` to accept a
  custom backend; the old positional `DedupSet` argument is removed.

- [x] **`Frequencies` concurrency guard** — `Frequencies` and `FrequenciesBy`
  accumulate into a plain map inside a `ForEach` closure. The closure always
  runs at `Concurrency(1)` today because no opts are forwarded to `ForEach`,
  but this is implicit. Enforce `Concurrency(1)` explicitly, matching the
  pattern used in `Scan`.

---

## Observability

- [x] **Configurable `SampleHook` rate** — `OnItemSample` fires every 10th item,
  hard-coded across all instrumented stage runners. Added `WithSampleRate(n int)`
  `RunOption`. Pass a negative value to disable sampling entirely.

- [x] **Structured stage metadata in `GraphNode`** — `GraphNode` now exposes
  `BatchSize`, `Timeout`, `HasRetry`, and `HasSupervision` in addition to the
  existing `Kind`, `Name`, `Concurrency`, `Buffer`, and `Overflow` fields.

---

## Performance

- [x] **`[]any` allocation pressure in FlatMap** — the old `FlatMap` signature
  (`func(ctx, I) ([]O, error)`) allocated an intermediate `[]any` slice on every
  call. `FlatMap` now takes a yield callback (`func(ctx, I, func(O) error) error`)
  that emits items one at a time, eliminating the intermediate allocation entirely.
  The engine fast-path (default error handler) yields directly to the outbox with
  zero buffering. All internal operators (`Pairwise`, `Intersperse`, `SlidingWindow`,
  `ChunkBy`, `ChunkWhile`, `Sort`, `SortBy`, `ConcatMap`, `FlatMapWith`) were
  migrated to the new signature.

- [x] **JSON-only cache and store serialization** — `CacheBy`, `MapWith`, and all
  `Store`-backed `Ref` operations serialize through `encoding/json`. There is no
  binary-safe alternative. Added `Codec` interface and `WithCodec` `RunOption`;
  a custom codec replaces `encoding/json` for both cache and store-backed `Ref`
  serialization. `JSONCodec` remains the default for backward compatibility.

---

## Multi-graph and composition

- [x] **Independent pipeline merge** — `Merge` and `MergeRunners` both panic if
  their inputs don't share the same graph. Added `MergeIndependent[T]` which
  runs each input pipeline concurrently and fans items into a single stream.
  Automatically delegates to `Merge` when all inputs share a graph. Errors from
  any input cancel the others and propagate to the caller.

---

## Testing infrastructure

- [x] **`kitsune/testkit` package** — added `testkit` with `MustCollect`,
  `CollectAndExpect`, `CollectAndExpectUnordered`, and `RecordingHook`.
  `RecordingHook` implements all six hook interfaces and provides typed
  accessors (`Items`, `Errors`, `Drops`, `Restarts`, `Graph`, `Dones`).

---

## Benchmarks & performance evidence

- [ ] **Benchmark vs raw goroutines** — measure the overhead of Kitsune's
  engine against an equivalent hand-rolled goroutine/channel pipeline for a
  3-stage linear graph (Map → Filter → ForEach) at 1M items. Include comparisons
  against `sourcegraph/conc` and `reugn/go-streams`. The goal is a documented,
  reproducible number (e.g., "X% overhead at 1M items/s") that engineers can
  cite before adopting. Publish results in `doc/benchmarks.md` and keep the
  benchmark programs in `bench/`.

- [ ] **Allocation benchmark suite** — extend `bench_test.go` with
  `AllocsPerOp` tracking for the highest-traffic paths: `Map`, `FlatMap`,
  `Batch`, `RateLimit`, and `CircuitBreaker`. Zero-alloc hot paths should be
  verified and regressions caught in CI.

---

## Ecosystem: tails

- [ ] **Azure Service Bus tail (`kazsb`)** — source and sink for Azure Service
  Bus queues and topics. Covers the most common Azure messaging pattern and
  fills the only major cloud provider gap in the tails ecosystem.

- [ ] **Azure Event Hubs tail (`kazeh`)** — source for Azure Event Hubs
  (consumer group–based). Complements `kkafka` for teams on Azure.

- [ ] **Elasticsearch / OpenSearch tail (`kes`)** — bulk-index sink and
  scroll/search source. A natural destination for enrichment pipelines and one
  of the most-requested sinks in data engineering workflows.

- [ ] **Google Cloud Storage tail (`kgcs`)** — object source (list + read) and
  sink (upload). Fills the gap left by the existing `ks3` tail for GCP users.

---

## Backlog / needs exploring

Items that may be worth doing but require more design work or a concrete use case before committing.

- [ ] **Per-item span propagation** — the `kotel` tail creates one span per
  stage. True per-item tracing would require span contexts to travel with items
  across stage boundaries, which the engine does not currently support. Three
  approaches were considered: (a) an internal item envelope carrying a
  `trace.SpanContext` — transparent to users but adds an allocation per hop and
  breaks down semantically at `Batch`/`FlatMap`/`Merge`; (b) user-wrapped
  `Traced[T]` items — zero engine cost but pollutes every stage signature;
  (c) an optional `ContextCarrier` interface on items — opt-in with no cost for
  non-carrier items, but requires propagation across type-changing stages.
  The most common motivation (propagating an incoming `traceparent` from Kafka
  or HTTP) is already achievable today by passing a trace-enriched context to
  `runner.Run`. Revisit if a concrete use case emerges that current stage-level
  spans cannot address.

---

## API: Go 1.23 iterator protocol

- [x] **`Pipeline[T]` as `iter.Seq[T]`** — pipelines can already be _created_
  from `iter.Seq[T]` via `FromIter`. Added the reverse: `.Iter(ctx, opts…)`
  terminal that exposes a `Pipeline[T]` as an `iter.Seq[T]` for range-over-func.
  Returns `(iter.Seq[T], func() error)` — iterator for the loop, error function
  to call after. Breaking out of the loop early cancels the pipeline and suppresses
  the resulting `context.Canceled`; the caller's own context cancellation is
  propagated normally.

---

## API: dynamic flow control

- [x] **`RunHandle.Pause()` / `RunHandle.Resume()`** — a source-only gate
  blocks sources from emitting while downstream stages continue draining
  in-flight items naturally. Implemented via a channel-based `Gate` type in
  the engine (`engine/gate.go`). `RunAsync` creates a gate automatically and
  exposes it on `RunHandle`. `WithPauseGate(g)` provides external gate control
  for synchronous `Run`. `Gate.Wait` is lock + nil-check only when open —
  zero channel ops on the fast path. The live inspector exposes `PauseCh()`
  and `ResumeCh()` so the UI Pause/Resume button can be wired to a `Gate`.

---

## Community & discoverability

- [ ] **`CONTRIBUTING.md`** — document dev setup, how to run tests and
  examples, the PR workflow, and the commit message convention. Lowers the
  barrier for first-time contributors.

- [ ] **GitHub issue templates** — separate templates for bug reports (repro
  steps, expected vs actual, Go version) and feature requests (use case,
  proposed API sketch). Reduces triage time and sets expectations for reporters.

- [ ] **GitHub Discussions** — enable the Discussions tab as the canonical
  place for "how do I…?" questions and design proposals. Keeps issues focused
  on confirmed bugs and actionable tasks.

- [ ] **Fuzz testing for the pipeline engine** — add `go test -fuzz` targets
  for the scheduler, overflow logic, and context cancellation paths. Focus on
  concurrent interleavings that unit tests cannot cover deterministically. Run
  as a nightly CI job rather than in every PR.
