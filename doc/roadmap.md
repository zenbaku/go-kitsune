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

## Operators: missing transforms

- [x] **`SwitchMap[I, O]`** — like `FlatMap` but cancels the active inner pipeline
  when a new upstream item arrives, then starts a fresh one. The defining pattern
  for search-as-you-type, live queries, and any scenario where a new request
  supersedes the previous one. Without it, users reach for `ConcatMap` (wrong
  ordering semantics) or wire up their own cancellation. Engine implementation
  needs a per-outer-item context that is cancelled on the next upstream receive.

- [x] **`ExhaustMap[I, O]`** — like `FlatMap` but ignores new upstream items while
  an inner pipeline is still active. The defining pattern for "submit once, wait
  for completion" flows: form submissions, idempotent API calls, debounced writes.
  Complements `SwitchMap` — together they cover the three meaningful concurrency
  modes for inner pipelines (`FlatMap` = all concurrent, `ConcatMap` = all
  sequential, `SwitchMap` = latest wins, `ExhaustMap` = first wins).

- [x] **`LiftPure`** — ergonomic wrapper for context-free, error-free functions,
  complementing the existing `Lift` (which wraps `func(I) (O, error)`):
  `kitsune.LiftPure(func(n int) int { return n * 2 })`. Removes the most common
  friction point when onboarding users who just want a simple transform.

- [x] **`OnErrorReturn`** — `StageOption` that replaces a failed item with a
  caller-supplied default value instead of dropping it (`Skip`) or halting the
  pipeline (`Halt`). Essential for enrichment pipelines where a failed lookup
  should produce a sentinel value rather than a gap in the output:
  `kitsune.OnError(kitsune.Return(User{Name: "unknown"}))`.

- [ ] **Session windows** — gap-based window that closes after a configurable
  period of inactivity rather than on a fixed wall-clock boundary:
  `kitsune.SessionWindow(p, gap time.Duration)`. Produces `[]T` slices like
  `Window` and `SlidingWindow`. Requires a per-item timer that resets on each
  new item and fires when the gap elapses with no new arrivals.

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

- [x] **`Concurrency(1)` fast paths** — for `Map`, `FlatMap`, `Filter`, and `Sink`
  at `Concurrency(1)` with no custom hook or error handler, a stripped-down loop
  bypasses `time.Now()` / `time.Since()`, `ProcessItem()` retry dispatch,
  `hook.OnItem()` interface dispatch, and `outbox.Send()` interface dispatch.
  Also added `sync.Pool` reuse for ordered-concurrent `mapSlot` / `flatMapSlot`
  structs (including their `done` channels), and `clear`+reslice batch-buffer
  reuse in `runBatch`. Fast paths use `for range` on receive (no per-item
  ctx.Done select) plus a single send-side `select`; cancellation is detected
  via `ctx.Err()` after the loop exits.

- [x] **Receive-side micro-batching** — fast-path dispatchers drain up to 15
  additional items non-blocking after each blocking channel receive, processing
  them as a local `[16]any` buffer. This trades 16 expensive goroutine-handoff
  receives for 1 expensive + 15 cheap non-blocking checks, cutting per-item
  channel overhead by ~14×. Gated on `!n.Supervision.HasSupervision()` so that
  pre-fetched items are never silently lost on supervision restart.
  Net result: MapLinear improved from 5,066 µs → 2,855 µs; 3-stage comparison
  improved from ~1.69 M/s to ~2.96 M/s (overhead vs raw goroutines: 3.5x → 2.1x).

- [x] **Stage fusion** — consecutive fusible stages (Map, Filter, Sink at
  `Concurrency(1)` with default config and `NoopHook`) are detected at run time
  and collapsed into a single goroutine with direct function calls, eliminating
  all inter-stage channel hops between them. A fusible chain of length ≥ 2 reads
  from the head's real input channel, calls each stage function inline with the
  same receiveBatchSize drain, writes to the tail's output channel (or calls the
  sink directly), and closes all bypassed channels on exit. FlatMap is excluded
  from fusion (its 1:N expansion already has a zero-alloc fast path via yield
  closure; fusing it would require per-item closure allocation). Net result:
  3-stage comparison improved from ~2.96 M/s to ~5.31 M/s (overhead vs raw
  goroutines: 2.1x → +17%); MapLinear improved from 2,855 µs → 1,845 µs.

- [x] **Concurrent fast paths** — extended the fast-path pattern to
  `runMapConcurrent` and `runFlatMapConcurrent` (unordered). When
  `DefaultHandler + NoopHook` hold, worker goroutines skip per-item timing,
  hook dispatch, `ProcessItem`/`wrapStageErr`, and atomic counter updates.
  The inner-context `Done` arm is retained (needed for prompt worker exit when
  a sibling worker errors). Result: `MapConcurrent` 6,129 → 4,534 µs (−26%),
  `MapOrdered/unordered` 7,563 → 5,909 µs (−22%).

- [x] **Chunked inter-stage transport** — achieved via two complementary
  approaches rather than `chan []any`. (1) Receive-side micro-batching: each
  fast-path dispatcher drains up to 15 non-blocking items after one blocking
  receive, amortising goroutine-handoff cost without changing the channel type or
  public API. (2) Stage fusion: consecutive fusible stages collapse into one
  goroutine, eliminating channel hops entirely rather than batching across them.
  Together these brought 3-stage overhead from ~3.1× to +17% vs raw goroutines.
  The `chan []any` approach would have been sufficient for goal (1) but fusion
  subsumes it for goal (2) at lower latency cost.

- [x] **Drain protocol (send-side select elimination)** — replaced
  `select { case ch <- v: case <-ctx.Done(): }` with plain `ch <- v` in all
  fast-path stage runners and the fused chain runner. Safety is maintained by a
  drain goroutine (`go func() { for range inCh {} }()`) deferred at stage exit —
  which unblocks any upstream blocked on a plain send. `nodeRunner` also defers
  drain goroutines for all non-Source stages, covering slow-path exits. A new
  `runSourceFastPath` skips the per-item 3-case early-exit select, dispatched
  when `NoopHook + blockingOutbox + no Gate`. Net result: 3-stage comparison
  improved from 189 ms → 75.7 ms (~2.16× faster than raw goroutines, overhead
  +17% → -54%); MapLinear improved from 1,845 µs → 768 µs.

- [ ] **Generics at the engine layer (v2)** — the remaining 2 allocs/item are from
  boxing values into `any` at stage boundaries. After the drain protocol, Kitsune
  (75.7 ms, 2M allocs) is 4.2× faster than `engine/typed` (317 ms, 43 allocs) at
  1M items. The benefit of generics is GC pressure reduction (2M allocs/run →
  ~setup only) rather than raw throughput. Requires a new module path; revisit
  once the API surface is stable.

---

## Multi-graph and composition

- [x] **Universal multi-stream operators** — `Merge`, `Zip`, `ZipWith`, and
  `WithLatestFrom` all accept pipelines from independent graphs. Same-graph
  inputs use the engine-native node (fast path); independent inputs fall back to
  a `Generate`-based implementation that runs each pipeline concurrently.
  `MergeIndependent` was removed — `Merge` subsumes it.

- [x] **`CombineLatest[A, B]`** — symmetric counterpart to `WithLatestFrom`:
  either side emitting triggers an output, always paired with the latest value
  from the other side. `WithLatestFrom` is asymmetric (only primary triggers);
  `CombineLatest` is needed whenever both streams are equally authoritative —
  price feeds, sensor fusion, derived UI state. Implementation mirrors
  `WithLatestFrom` but with two background goroutines feeding into a shared
  output channel, each reading the current latest from the other side before
  emitting.

- [x] **`Balance[T]`** — round-robin fan-out: each item goes to exactly one of N
  output pipelines, distributing load evenly rather than copying it. `Broadcast`
  replicates every item to all outputs; `Balance` is for parallelising work across
  independent downstream branches where each item only needs one path. Completes
  the fan-out vocabulary alongside `Broadcast` and `Partition`.

---

## State management

Kitsune's `Key[T]` + `Ref[T]` + `Store` model is unique in the Go streaming
ecosystem. The items below complete it into a first-class stateful stream
processing system competitive with Apache Flink for single-process workloads.

- [ ] **Keyed state (`MapWithKey`, `FlatMapWithKey`)** — the most impactful missing
  feature. Currently `MapWith` binds to a single global state slot shared by all
  items. Keyed state partitions the `Ref` by a key extracted from each item,
  backed by `Store.Get("key:"+entityID)` under the hood:

  ```go
  sessionKey := kitsune.NewKey("session", SessionState{})
  kitsune.MapWithKey(events,
      func(e Event) string { return e.UserID },
      sessionKey,
      func(ctx context.Context, ref *kitsune.Ref[SessionState], e Event) (Result, error) {
          s, _ := ref.Get(ctx)  // state for this user only
          s.EventCount++
          ref.Set(ctx, s)
          return Result{Count: s.EventCount}, nil
      },
  )
  ```

  Enables per-user session tracking, per-device state machines, per-entity rate
  limiting, and any pattern where state must be scoped to a key derived from the
  item itself — without users managing maps manually. The `Store` interface already
  supports arbitrary string keys, so the implementation delta is entirely in the
  public API and `Ref` lookup path.

- [ ] **State TTL / expiry** — without TTL, keyed state grows unboundedly as new
  entity keys appear. Add an optional TTL parameter to `NewKey`:
  `kitsune.NewKey("session", SessionState{}, kitsune.StateTTL(30*time.Minute))`.
  `Ref.Get` returns the zero value and resets the slot when the TTL has elapsed.
  `MemoryStore` needs a background reaper or lazy expiry on read; distributed
  backends (Redis, DynamoDB) get this via their native TTL mechanisms.

- [ ] **Aggregating state operators (`CountBy`, `SumBy`)** — thin ergonomic wrappers
  over `MapWithKey` for the most common accumulation patterns. Built on top of
  keyed state rather than buffering the entire stream, so they work on unbounded
  streams:

  ```go
  kitsune.CountBy(events, func(e Event) string { return e.Type })
  // → *Pipeline[map[string]int64], emitting a snapshot on each flush trigger

  kitsune.SumBy(transactions,
      func(t Transaction) string { return t.AccountID },
      func(t Transaction) float64 { return t.Amount },
  )
  // → *Pipeline[map[string]float64]
  ```

  Flush trigger could be a time window, a count, or an explicit `Flush()` call.
  Design should align with the session window and `Window` operators.

---

## Testing infrastructure

- [x] **`kitsune/testkit` package** — added `testkit` with `MustCollect`,
  `CollectAndExpect`, `CollectAndExpectUnordered`, and `RecordingHook`.
  `RecordingHook` implements all six hook interfaces and provides typed
  accessors (`Items`, `Errors`, `Drops`, `Restarts`, `Graph`, `Dones`).

- [x] **Virtual time / `TestClock`** — testing `Window`, `Debounce`, `Throttle`,
  `Ticker`, `Interval`, and `SessionWindow` currently requires real sleeps, making
  time-based tests slow and flaky. Add a `Clock` interface threaded through
  time-sensitive operators, with a `testkit.TestClock` implementation that
  advances on demand:

  ```go
  clock := testkit.NewTestClock()
  w := kitsune.Window(source, 5*time.Second, kitsune.WithClock(clock))
  // ... send items ...
  clock.Advance(5 * time.Second)  // triggers flush immediately, no sleep
  ```

  Requires a non-trivial pass through the engine and time-based operators to
  inject the clock interface, but is a prerequisite for Kitsune to be taken
  seriously as a production library — teams cannot ship pipelines whose tests
  sleep for real durations.

---

## Benchmarks & performance evidence

- [x] **Benchmark vs raw goroutines** — measure the overhead of Kitsune's
  engine against an equivalent hand-rolled goroutine/channel pipeline for a
  3-stage linear graph (Map → Filter → ForEach) at 1M items. Include comparisons
  against `sourcegraph/conc` and `reugn/go-streams`. The goal is a documented,
  reproducible number (e.g., "X% overhead at 1M items/s") that engineers can
  cite before adopting. Publish results in `doc/benchmarks.md` and keep the
  benchmark programs in `bench/`.

- [x] **Allocation benchmark suite** — extend `bench_test.go` with
  `AllocsPerOp` tracking for the highest-traffic paths: `Map`, `FlatMap`,
  `Batch`, `RateLimit`, and `CircuitBreaker`. Zero-alloc hot paths should be
  verified and regressions caught in CI.

---

## Ecosystem: tails

- [x] **Azure Service Bus tail (`kazsb`)** — source and sink for Azure Service
  Bus queues and topics. Covers the most common Azure messaging pattern and
  fills the only major cloud provider gap in the tails ecosystem.

- [x] **Azure Event Hubs tail (`kazeh`)** — source for Azure Event Hubs
  (consumer group–based). Complements `kkafka` for teams on Azure.

- [x] **Elasticsearch / OpenSearch tail (`kes`)** — bulk-index sink and
  scroll/search source. A natural destination for enrichment pipelines and one
  of the most-requested sinks in data engineering workflows.

- [x] **Google Cloud Storage tail (`kgcs`)** — object source (list + read) and
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

- [x] **`CONTRIBUTING.md`** — document dev setup, how to run tests and
  examples, the PR workflow, and the commit message convention. Lowers the
  barrier for first-time contributors.

- [ ] **GitHub issue templates** — separate templates for bug reports (repro
  steps, expected vs actual, Go version) and feature requests (use case,
  proposed API sketch). Reduces triage time and sets expectations for reporters.

- [ ] **GitHub Discussions** — enable the Discussions tab as the canonical
  place for "how do I…?" questions and design proposals. Keeps issues focused
  on confirmed bugs and actionable tasks.

- [x] **Fuzz testing for the pipeline engine** — added `go test -fuzz` targets
  for the scheduler, overflow logic, and context cancellation paths. Focus on
  concurrent interleavings that unit tests cannot cover deterministically. Run
  as a nightly CI job rather than in every PR.
