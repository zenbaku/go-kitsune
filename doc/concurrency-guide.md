# Choosing a Concurrency Model

Kitsune offers four orthogonal primitives for adding parallelism. They solve different problems; picking the wrong one either limits throughput (over-serialising) or breaks correctness (sharing state across goroutines unsafely). This guide walks through how to choose.

!!! tip "TL;DR"
    - **`Concurrency(n)`**: parallel workers on one stage, no order guarantee, no shared state; for I/O-bound fan-in.
    - **`Concurrency(n)` + `Ordered()`**: same, but output is resequenced to match input order.
    - **`MapWithKey` + `Concurrency(n)`**: items hash-routed to stable workers by key; for lock-free per-entity state.
    - **`Partition` / `Balance`**: explicit fan-out to independent downstream subgraphs; for heterogeneous branches.

---

## The four primitives at a glance

| Primitive | Parallel unit | Order preserved | Per-key locality | State model | Typical use |
|---|---|---|---|---|---|
| `Concurrency(n)` | n workers on one stage | No | No | Stateless or externally synchronised | HTTP/DB enrichment per item |
| `Concurrency(n)` + `Ordered()` | n workers + resequencer | Yes (input order) | No | Same | Parallel enrichment feeding an ordered sink |
| `MapWithKey` + `Concurrency(n)` | n workers, hash-sharded by key | Per-key only | Yes (lock-free) | Per-entity `Ref[S]` state | Per-user aggregation, rate limiting |
| `Balance(n)` / `Partition(pred)` | n (or 2) independent downstream pipelines | Per-branch only | No (`Balance`); by predicate (`Partition`) | Each branch owns its own stage chain | Routing to heterogeneous subgraphs |

---

## Decision flowchart

```
Does the stage touch per-entity state
(running totals, sessions, per-user counters)?
│
├── Yes ──► MapWithKey + Concurrency(n)       [§ Key-sharding]
│          (if entities arrive on separate streams, Merge first)
│
└── No ──► Does the downstream need input order?
           │
           ├── Yes ──► Concurrency(n) + Ordered()    [§ Ordered parallelism]
           │
           └── No ──► Do branches need *different*
                       stage shapes or configurations?
                       │
                       ├── Yes, 2 branches by rule ──► Partition(pred)     [§ Fan-out]
                       ├── Yes, n branches by rule ──► Balance(n) + per-branch stages
                       └── No ──────────────────────► Concurrency(n)       [§ Unordered]
```

The primitives compose freely. A `Partition` branch can use `Concurrency(20)` on its enrichment stage; the other branch can use `MapWithKey` for stateful routing. Each primitive is a `StageOption` or a pipeline-shape operator; there is no global "concurrency mode".

---

## When to reach for each model

### `Concurrency(n)`: unordered parallel workers

```go
enriched := kitsune.Map(events, callAPI,
    kitsune.Concurrency(20),
    kitsune.Buffer(64),
    kitsune.WithName("enrich"),
)
```

**Reach for it when:**
- The stage is I/O-bound: HTTP calls, database lookups, file reads. Each goroutine blocks on I/O most of the time, so 20 goroutines means 20 outstanding requests with no extra CPU cost.
- Items are independent: processing item A does not affect item B.
- Downstream does not care about order: a bulk insert, a metrics sink, a dead-letter queue.

**Avoid when:**
- The stage mutates shared state without external synchronisation. Use `MapWithKey` instead to get per-key isolation for free.
- The stage is CPU-cheap. `Concurrency(n >= 2)` leaves the fast path, adding goroutine scheduling and channel overhead that often exceeds the gain. Measure before reaching for concurrency on fast operations. See [tuning.md: fast path](tuning.md#fast-path-and-stage-fusion).
- Downstream must see items in arrival order. Use `Ordered()`.

**Performance note:** start with 10-20 for HTTP enrichment, then profile. For CPU-bound stages, start at `runtime.NumCPU()`. See [tuning.md: Concurrency](tuning.md#concurrency-concurrencyn) for heuristics.

---

### `Ordered()`: parallel but resequenced

```go
enriched := kitsune.Map(events, callAPI,
    kitsune.Concurrency(20),
    kitsune.Ordered(),   // <-- slot-based resequencer added
    kitsune.Buffer(64),
)
```

**Reach for it when:**
- Downstream is order-sensitive: writing to an append-only log, driving UI updates in sequence, feeding a stateful reducer that expects monotonic input.
- The stage itself is parallelisable (items are independent) but the *consumer* of its output is not.

**Avoid when:**
- Throughput is critical and downstream is actually order-tolerant. `Ordered()` adds a resequencer that costs ~10-15% peak throughput compared to unordered `Concurrency(n)`.
- One item in the in-flight window might be significantly slower than its peers. A single slow item head-of-lines the output channel until it completes, stalling all faster items that have already finished. If latency outliers are common, `Ordered()` amplifies them.

**Key behaviour:** `Ordered()` does not change how items are *processed* (n goroutines still run in parallel). It only changes how results are *emitted*: a slot-based resequencer holds completed items until all earlier items have been released.

---

### `MapWith` / `MapWithKey`: key-sharded per-entity workers

```go
var totalKey = kitsune.NewKey[int]("running_total", 0)

results := kitsune.MapWithKey(
    events,
    func(e Event) string { return e.UserID },  // routing key
    totalKey,
    func(ctx context.Context, ref *kitsune.Ref[int], e Event) (Result, error) {
        total, _ := ref.UpdateAndGet(ctx, func(t int) (int, error) {
            return t + e.Amount, nil
        })
        return Result{UserID: e.UserID, Total: total}, nil
    },
    kitsune.Concurrency(4),
)
```

**Mechanism:** `hash(key) % n` routes each item to one of n stable workers. All items with the same key always reach the same worker, so per-key `Ref[S]` state is single-owner in the hot path. No mutex is required inside the handler.

**Reach for it when:**
- State is per-entity: running totals, session counters, per-user rate-limit buckets, per-device configuration.
- Order only needs to be preserved *within* a key, not across all keys.
- The key space is large (many distinct keys): `MapWithKey` with `Concurrency(4)` uses 4 shared workers rather than one goroutine per key, bounding goroutine count regardless of key cardinality.

**Avoid when:**
- State must be global across all keys. Use a single `MapWith` at `Concurrency(1)`, or an external store.
- The key space is heavily skewed: if one key generates 90% of traffic, one worker becomes a bottleneck. Diagnose with `WithHook` metrics; mitigate by using a subkey salt or reducing `n`.
- Per-item work is sub-microsecond. Hash routing overhead dominates at very fine granularity; prefer `Concurrency(1)` + a plain `map[string]T` in a single goroutine.

**Correctness guarantee:** you never need a mutex on `Ref[S]` state inside the handler. Kitsune's key-sharding ensures that concurrent workers never share a `Ref`; single-owner access is enforced by construction.

**`MapWith` vs `MapWithKey`:** `MapWith` carries a single shared `Ref[S]` across all items (global state). `MapWithKey` partitions state by key (per-entity state). Use `MapWith` when your accumulator aggregates across the entire stream; use `MapWithKey` when each key has its own independent state bucket.

---

### `Balance(n)` and `Partition(pred)`: explicit fan-out

**`Partition`** splits one pipeline into two based on a predicate. Each item goes to exactly one branch.

```go
valid, invalid := kitsune.Partition(src, func(o Order) bool { return o.Valid })

// Each branch can have its own stage chain, Concurrency, Buffer, and OnError.
enriched := kitsune.Map(valid, callAPI, kitsune.Concurrency(10))
merged, _ := kitsune.MergeRunners(
    enriched.ForEach(store),
    invalid.ForEach(deadLetter),
)
_, _ = merged.Run(ctx)
```

**`Balance`** distributes items across n output pipelines in **round-robin order** (not work-stealing). Each item goes to exactly one branch in sequence.

```go
branches := kitsune.Balance(src, 3)
// branches[0], branches[1], branches[2] each receive every third item.
```

**Reach for `Partition` when:**
- Two branches need *different* processing: different stage chains, different error handling, different sinks.
- Items are routed by their content (a field, a type, a validity check).

**Reach for `Balance` when:**
- You need n independent downstream subgraphs, each with its own configuration, and the assignment can be round-robin.
- You want to route to heterogeneous downstream systems without caring which item goes where.

**Avoid `Balance` for pure load splitting:** that is what `Concurrency(n)` is for. `Concurrency(n)` uses a single shared input channel and self-levels naturally; workers pull items as fast as they can. `Balance` is round-robin: a slow branch will stall the splitter via backpressure, holding back faster branches. Use `Concurrency(n)` when the stage is the same on all workers; use `Balance` only when you need n distinct downstream subgraphs.

!!! warning "All branches must be consumed"
    `Partition` and `Balance` produce n output pipelines. Every output must be connected to a downstream stage and consumed. An unconsumed branch's channel fills up, exerting backpressure that stalls all other branches. Use `MergeRunners` to start and await all branches in one call.

---

## Worked examples

### Parallel HTTP enrichment: unordered vs ordered

**Goal:** enrich a stream of event records with data from a slow API. Throughput should be limited by the API's concurrency budget, not by single-goroutine serialisation.

**Choice:** `Concurrency(n)`. Items are independent, and the stage is purely I/O-bound.

```go
enriched := kitsune.Map(events, callAPI,
    kitsune.Concurrency(10),
    kitsune.Buffer(32),
    kitsune.WithName("enrich-unordered"),
)
```

When the downstream (`ForEach(store)`) is an append-only log that requires deterministic ordering, add `Ordered()`:

```go
enriched := kitsune.Map(events, callAPI,
    kitsune.Concurrency(10),
    kitsune.Ordered(),
    kitsune.Buffer(32),
    kitsune.WithName("enrich-ordered"),
)
```

**What to notice:** both pipelines use the same `Concurrency(10)` and complete in roughly the same wall time. The difference is whether downstream sees items in arrival order. If the downstream doesn't need order, omit `Ordered()`; it is free throughput.

**Common mistake:** setting `Concurrency(2)` expecting a 2x speedup on a CPU-cheap `Map` stage. The goroutine scheduling overhead and fast-path loss typically exceed the gain. Profile first; `Concurrency(n)` helps I/O-bound stages, not CPU-cheap ones.

Full runnable example: [`examples/concurrency-guide/enrich/`](../examples/concurrency-guide/enrich/main.go). A simpler variant is in [`examples/concurrent/`](../examples/concurrent/main.go).

---

### Per-user stateful aggregation: running totals

**Goal:** maintain a per-user running total across a multi-user payment stream. Each payment must read the current total, add the amount, and emit the new total. No locks.

**Choice:** `MapWithKey` + `Concurrency(4)`. State is per-entity; `hash(userID) % 4` routes all events for the same user to the same worker.

```go
var totalKey = kitsune.NewKey[int]("running_total", 0)

updates := kitsune.MapWithKey(
    payments,
    func(p Payment) string { return p.UserID },
    totalKey,
    func(ctx context.Context, ref *kitsune.Ref[int], p Payment) (TotalUpdate, error) {
        total, _ := ref.UpdateAndGet(ctx, func(t int) (int, error) {
            return t + p.Amount, nil
        })
        return TotalUpdate{UserID: p.UserID, Total: total}, nil
    },
    kitsune.Concurrency(4),
)
```

**What to notice:** `ref.UpdateAndGet` is an atomic read-modify-write call. Because `hash("alice") % 4` is the same for every alice payment, all alice payments land on the same worker goroutine. No two goroutines ever access the same `Ref` concurrently; the lock-free guarantee is structural, not incidental.

**Common mistake:** replacing this with `Map(p, fn, kitsune.Concurrency(4))` and a shared `map[string]int` + `sync.Mutex`. This works for correctness, but serialises all key updates on the mutex and gains no real concurrency on the hot path. `MapWithKey` eliminates the mutex and shards the hot path.

Full runnable example: [`examples/concurrency-guide/useragg/`](../examples/concurrency-guide/useragg/main.go). A more elaborate version with final-total verification is in [`examples/keyedstate/`](../examples/keyedstate/main.go).

---

### Per-user rate limiting

**Goal:** allow at most 3 requests per user per time window. Accept requests within budget; reject excess requests. No global lock.

**Choice:** `MapWithKey` + `Concurrency(n)` with a `bucket` state struct tracking window and count.

This pattern is fully demonstrated in [`examples/perkeyratelimit/`](../examples/perkeyratelimit/main.go). The key insight: `Ref.UpdateAndGet` is the read-modify-write primitive, and key-sharding ensures the bucket for each user is only ever touched by one worker at a time.

**Why not `Concurrency(n)` + `Map` + mutex?** A global mutex on the rate-limit state makes all users contend for a single lock. Key-sharding makes each worker responsible for a disjoint partition of the user space; the lock disappears entirely.

---

### Fan-out routing: Partition to heterogeneous branches

**Goal:** route valid orders through a parallel enrichment pipeline; send invalid orders directly to a dead-letter sink. The two branches have different stage shapes.

**Choice:** `Partition(pred)`. Two branches with different stage chains: this is the signal that `Partition` is the right tool, not `Concurrency(n)` (which would replicate the same stage on n goroutines) or `Balance(n)` (which is round-robin, not content-based).

```go
valid, invalid := kitsune.Partition(src, func(o Order) bool { return o.Valid })

// Valid branch: parallel enrichment + store.
enriched := kitsune.Map(valid, callAPI, kitsune.Concurrency(4))

merged, _ := kitsune.MergeRunners(
    enriched.ForEach(store),
    invalid.ForEach(deadLetter),
)
if _, err := merged.Run(ctx); err != nil { ... }
```

**What to notice:** the valid branch runs `Concurrency(4)` on enrichment; the invalid branch does not. This asymmetry (different stage shapes per branch) is the defining signal for `Partition` over `Concurrency(n)`.

**Common mistakes:**
- Using `Balance(2)` when items should be routed by content, not round-robin. `Balance` does not inspect items; `Partition` does.
- Forgetting to consume one branch. If the dead-letter runner is not started, its channel fills up and stalls the valid branch via backpressure. `MergeRunners` prevents this by requiring all runners before starting.

Full runnable example: [`examples/concurrency-guide/routing/`](../examples/concurrency-guide/routing/main.go).

---

## Composing models

The four primitives are not mutually exclusive. A realistic pipeline might use three of them together:

```
FromSource → Map(parse)
           → Partition(isPriority)
             ├── true  → Map(enrich, Concurrency(20), Ordered())
             │          → MapWithKey(aggregate, Concurrency(4))
             │          → ForEach(write)
             └── false → Map(enrich, Concurrency(10))
                        → ForEach(logLow)
```

Each primitive is a local decision at one stage. There is no global "concurrency mode" to configure; the pipeline DAG itself expresses the concurrency model.

---

## Anti-patterns

- **`Concurrency(n)` on a CPU-cheap `Map`**: goroutine scheduling and fast-path loss can exceed the gain. Measure first. See [tuning.md](tuning.md#fast-path-and-stage-fusion).
- **`Ordered()` by default**: adds a resequencer and enables head-of-line blocking. Only add it when downstream actually requires input order.
- **Shared `map[string]T` + mutex inside `Concurrency(n) + Map`**: this serialises all updates on the mutex. Use `MapWithKey` to eliminate the lock and shard the hot path.
- **`Balance` for load balancing**: `Balance` is round-robin. A slow branch stalls faster branches via backpressure. Use `Concurrency(n)` on a single stage for load balancing; its shared input channel self-levels naturally.
- **Unconsumed `Partition` / `Balance` branches**: every output branch must be consumed. An unconsumed branch stalls all others. Use `MergeRunners` to enforce this.
- **`MapWithKey` with small `n` and many keys**: `Concurrency(2)` with 100 users means 50 users per worker. That is fine. `Concurrency(2)` with 2 heavily skewed users means one worker gets 90% of the traffic. Profile per-worker load before concluding that key-sharding is the bottleneck.

---

## FAQ

**Q: Can I combine `Concurrency(n)` with `MapWithKey`?**

Yes: that *is* the sharded form. `MapWithKey(..., kitsune.Concurrency(n))` starts n workers and routes items by `hash(key) % n`. Without `Concurrency(n)`, all keys share one worker (serial).

**Q: Does `Ordered()` work with `MapWithKey`?**

Items for the same key are already processed in arrival order within that key. Adding `Ordered()` to `MapWithKey` would restore global input order across all keys at resequencer cost. This is rarely needed; per-key ordering is usually sufficient.

**Q: Is `Balance` work-stealing?**

No. `Balance` distributes items in round-robin order. If one downstream branch is slower, its input channel fills up and the splitter stalls, which eventually stalls the other branches too. For pure load balancing (same stage on n goroutines), use `Concurrency(n)`; it uses a single shared channel and self-levels naturally.

**Q: How do I pick `n` for `Concurrency(n)`?**

Start with 10-20 for I/O-bound stages (HTTP, database). Start with `runtime.NumCPU()` for CPU-bound stages. Then profile. See [tuning.md](tuning.md#concurrency-concurrencyn).

**Q: Does adding `Concurrency(n >= 2)` affect the fast path?**

Yes. Any `Concurrency(n >= 2)` leaves the fast path on that stage. See [tuning.md: exact eligibility conditions](tuning.md#exact-eligibility-conditions) for the full list of conditions.

**Q: Can I use `Partition` + `Concurrency(n)` on the same stage?**

Yes. `Partition` is a pipeline-shape operator that produces two `*Pipeline[T]` values. Each of those pipelines can independently use any `StageOption`, including `Concurrency(n)`. The branching and the concurrency are orthogonal.

---

## Further reading

- [tuning.md](tuning.md): buffer sizing, fast path, concurrency heuristics, and GC trade-offs.
- [operators.md](operators.md): full operator reference with exact signatures.
- [error-handling.md](error-handling.md): combining concurrency with per-stage supervision and error routing.
- [`examples/concurrency-guide/`](../examples/concurrency-guide/): runnable versions of every pattern in this guide.
- [`examples/perkeyratelimit/`](../examples/perkeyratelimit/main.go): per-user rate limiting with `MapWithKey`.
- [`examples/keyedstate/`](../examples/keyedstate/main.go): per-user stateful aggregation, serial vs concurrent comparison.
