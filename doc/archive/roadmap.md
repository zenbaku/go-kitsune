# Kitsune Roadmap

## Polish for v1 release

- [x] **README.md** — quick-start, API overview, examples
- [x] **tails/kfile** — file/CSV/JSON sources and sinks (zero external deps)
- [x] **Benchmarks** — baseline throughput (items/sec, allocations per op)
- [x] **GitHub Actions CI** — automate `task ci` + tail tests on push

## v1.x additions

- [x] **FromIter** — `iter.Seq[T]` integration (Go 1.23+)
- [x] **Broadcast** — fan-out to all N consumers (complement to Partition)
- [x] **Window** — time-based batching alongside size-based Batch
- [x] **tails/khttp** — HTTP source (paginated GET) and sink (POST/webhook)

## Pre-v2 polish

- [x] **Panic contracts documented** — godocs + tests for `Broadcast`, `Merge`, `MergeRunners`
- [x] **Benchmark results** — `doc/benchmarks.md` with real numbers (Apple M1, items/sec, allocs/op)
- [x] **"Why free functions?"** — expanded explanation in README of Go type parameter constraint
- [x] **"When to use Kitsune"** — positioning section in README (good fits vs non-fits)
- [x] **Metrics example** — `examples/metrics`: custom `Hook` with per-stage counters and latency
- [x] **Performance tuning guide** — `doc/tuning.md`: buffer, concurrency, batch, memory trade-offs

## v2

- [x] **Ordered output** — `Ordered()` StageOption + slot-based resequencer for `Concurrency(n)` stages (Map, FlatMap)
- [x] **Actor-like supervision** — `Supervise()` + `RestartOnError` / `RestartOnPanic` / `RestartAlways`; `PanicSkip` / `PanicRestart`; optional `SupervisionHook` for restart events
- [x] **Mailbox overflow strategies** — `Overflow()` + `Block`/`DropNewest`/`DropOldest`; `OverflowHook` for observability; safe with concurrent senders
- [x] **Live inspector web UI** — runtime event stream visualization

## v2.x / post-v2

- [x] **Context propagation audit** — routing nodes (`Merge`, `Broadcast`, etc.) silently swallow some `outbox.Send` errors; ensure error cause is preserved and propagated faithfully across the full graph
- [x] **Inspector: buffer fill-level** — expose per-stage channel occupancy (how full each inter-stage buffer is) so back-pressure bottlenecks are immediately visible in the dashboard
- [x] **Test coverage pass** — edge cases for supervision exhaustion, concurrent restarts, ordered output under cancellation, overflow under load, and routing node error propagation
- [x] **PrometheusHook / OTelHook** — built-in hook that exposes per-stage counters and latency histograms; covers the most common production observability need without users writing their own hook
- [x] **Graceful drain on shutdown** — a drain mode that lets in-flight items finish and flushes buffered batches before exiting, instead of dropping everything on context cancellation
- [x] **More tails** — Kafka (source + sink), PostgreSQL (LISTEN/NOTIFY source, COPY sink), S3/GCS (batch file ingestion source)
