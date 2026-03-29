# Engine Tasks

Development backlog for the `engine` package. Items are independent of the
public kitsune API unless marked **[breaking]** — those require coordinated
changes in both packages.

---

## Test coverage gaps

- [ ] `runMapConcurrentOrdered` — ordered output under concurrent workers
- [ ] `runBatch` with `BatchTimeout` — partial flush on timer
- [ ] `runThrottle` and `runDebounce` — time-based emission logic
- [ ] `runReduce` — seed propagation, single-emit guarantee
- [ ] `runZip` — paired emission and early-exit when either input closes
- [ ] `runWithLatestFrom` — secondary-channel tracking and primary-only emission
- [ ] `runMapResult` — dual-port routing (success / error)
- [ ] `supervise` — restart counter, window reset, backoff delay, PanicOnly flag
- [ ] `runProtected` — panic recovery under each `PanicAction`
- [ ] `CreateOutboxes` with `OverflowHook` — drop events delivered correctly
- [ ] `runBroadcast` — all N outputs receive every item
- [ ] `runMerge` — fan-in from closed inputs (already one test; add ordering edge cases)
- [ ] Graceful drain — items in-flight at cancel time reach the sink
- [ ] `InitRefs` — called twice does not double-initialize (idempotency)
- [ ] Race detector pass — run `go test -race ./engine/...` in CI

---

## Correctness improvements

- [ ] **`Validate` port exhaustiveness** — check that every declared fan-out
      port (Partition port 1, BroadcastNode ports 0..N-1) is consumed. Currently
      only the single-consumer rule is enforced; unconsumed ports are silently
      wired but never drained, causing the stage goroutine to hang.

- [ ] **Sink concurrency** — `runSink` currently ignores `Node.Concurrency`.
      Decide: should concurrent sinks be supported (useful for parallel writes),
      or should the engine reject `Concurrency > 1` on Sink nodes at Validate time?

- [ ] **Source error vs drain** — a source returning a non-nil error after the
      drain signal fires is currently swallowed (`return nil`). Consider
      distinguishing between "stopped by drain" and "stopped by error" so hooks
      can report it accurately.

---

## Performance

- [ ] **Channel pre-allocation** — `CreateChannels` allocates channels one at a
      time. For large graphs (50+ nodes), a single-pass allocation with a
      pre-sized map would reduce GC pressure at startup.

- [ ] **Zero-copy Outbox** — `blockingOutbox.Send` boxes every `any` into a
      channel send. Explore whether a ring-buffer or unsafe-cast path can reduce
      allocations on hot paths (Map with Concurrency 1).

- [ ] **ProcessItem inline** — for stages with `DefaultHandler` (halt, no retry),
      `ProcessItem` adds an unnecessary function call per item. Consider a fast
      path that skips the loop when `HasRetry` is false.

---

## New node kinds (engine-level design only; kitsune wiring is separate)

- [ ] **`ScanNode`** — running accumulator that emits after every item (like
      Reduce but streams output). Fn: `func(acc, item any) any`. Needs `ReduceSeed`.
      Currently implemented in kitsune via MapWith; a dedicated kind would be
      simpler and avoid state management overhead.

- [ ] **`WindowNode`** — time-based batching. Similar to Batch but flushes on a
      timer tick rather than a count. Shares `BatchTimeout` field; needs a
      distinct kind to get correct `kindName` in hooks.

- [ ] **`PairwiseNode`** — emit `(prev, curr)` pairs. Fn-less; uses a one-item
      buffer. Currently implemented in kitsune via MapWith.

---

## Observability

- [ ] **`kindName` completeness** — audit that every `NodeKind` constant has a
      case in `kindName`. A missing case silently returns `"unknown"`, making
      hook events hard to read.

- [ ] **Per-node metrics in `GraphNode`** — `GraphNode` (passed to `GraphHook`)
      omits `Ordered`, `HasRetry`, and `Supervision` details. Expose them so
      topology snapshots are self-describing.

- [ ] **Structured run errors** — `Run` currently returns the first error from
      `errgroup.Wait`. If multiple stages fail concurrently, the other errors are
      lost. Consider accumulating all errors (e.g. via `errors.Join`) or exposing
      a multi-error type.

---

## API evolution (requires kitsune coordination)

- [ ] **[breaking] `Node.Fn` typed variants** — today `Fn any` holds different
      function types depending on `Kind`. A sum type (sealed interface or
      `FnSource | FnMap | …`) would make invalid combinations unrepresentable and
      remove the per-runner type assertion. Deferred until Go adds sealed
      interfaces or sum types.

- [ ] **[breaking] `RunConfig` context propagation** — `RunConfig.Store` and
      `RunConfig.Cache` are per-run. Some callers want per-item context values
      (e.g. request-ID propagation). Explore whether a `ContextFn` hook on
      `RunConfig` is a better model than carrying values through the shared store.
