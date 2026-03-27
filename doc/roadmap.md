# Kitsune Roadmap

## Polish for v1 release

- [ ] **README.md** — quick-start, API overview, examples
- [ ] **tails/kfile** — file/CSV/JSON sources and sinks (zero external deps)
- [ ] **Benchmarks** — baseline throughput (items/sec, allocations per op)
- [ ] **GitHub Actions CI** — automate `task ci` + extension tests on push

## v1.x additions

- [ ] **FromIter** — `iter.Seq[T]` integration (Go 1.23+)
- [ ] **Broadcast** — fan-out to all N consumers (complement to Partition)
- [ ] **Window** — time-based batching alongside size-based Batch
- [ ] **tails/khttp** — HTTP source (paginated GET) and sink (POST/webhook)

## v2

- [ ] **Actor-like supervision** — per-stage restart, error isolation
- [ ] **Ordered output** — resequencer for `Concurrency(n)` stages
- [ ] **Mailbox overflow strategies** — drop-oldest, drop-newest for real-time
- [ ] **Live inspector web UI** — runtime event stream visualization
