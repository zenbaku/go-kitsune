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

- [ ] **JSON-only cache and store serialization** — `CacheBy`, `MapWith`, and all
  `Store`-backed `Ref` operations serialize through `encoding/json`. There is no
  binary-safe alternative. A pluggable `Codec` interface on `RunOption` would
  let users substitute `encoding/gob`, `protobuf`, or `msgpack`.

---

## Multi-graph and composition

- [ ] **Independent pipeline merge** — `Merge` and `MergeRunners` both panic if
  their inputs don't share the same graph. There is no way to merge two entirely
  independent pipelines. There is no way to merge two entirely
  independent pipelines (e.g., two separate `FromSlice` sources). A
  `MergeIndependent` (or similar) variant would run two graphs concurrently and
  fan their outputs into a single stream, covering the common "aggregate results
  from two sources" pattern.

---

## Testing infrastructure

- [x] **`kitsune/testkit` package** — added `testkit` with `MustCollect`,
  `CollectAndExpect`, `CollectAndExpectUnordered`, and `RecordingHook`.
  `RecordingHook` implements all six hook interfaces and provides typed
  accessors (`Items`, `Errors`, `Drops`, `Restarts`, `Graph`, `Dones`).
