---
name: MemoryStore codec bypass + ContextCarrier/WithContextMapper guide
description: Eliminate codec overhead for in-process stores; add tracing context propagation decision guide
type: project
---

# Design: MemoryStore Codec Bypass + Tracing Context Guide

## Scope

Two independent items bundled together:

1. **Bypass codec serialization for MemoryStore Ref operations** — performance improvement: eliminate `json.Marshal`/`json.Unmarshal` when the backing Store is in-process.
2. **ContextCarrier vs WithContextMapper decision guide** — new `doc/tracing.md` documenting the two per-item tracing approaches with a comparison table and worked examples.

---

## Part 1: MemoryStore Codec Bypass

### Problem

`Ref[T]` has two paths:

- `r.store == nil` (default, no `WithStore` configured): stores `T` directly in `r.value`; zero serialization overhead.
- `r.store != nil` (explicit `WithStore`): calls `codec.Marshal`/`codec.Unmarshal` on every `Get`/`Set`/`UpdateAndGet`/`Update`.

When users pass `WithStore(MemoryStore())`, they get the slow path even though the store is purely in-process. Every `ref.Get()` allocates a `[]byte` and JSON-decodes it; every `ref.Set()` JSON-encodes and allocates again. For hot `MapWith`/`MapWithKey` loops, this is wasted allocation with no benefit.

### Design

**Step 1 — Add `InProcessStore` interface to `internal/hooks.go`**

```go
// InProcessStore is implemented by stores that live in the same process and
// support direct any-typed access, bypassing codec serialization.
// MemoryStore implements this interface.
type InProcessStore interface {
    GetAny(ctx context.Context, key string) (any, bool)
    SetAny(ctx context.Context, key string, value any)
    DeleteAny(ctx context.Context, key string)
}
```

No error returns on `GetAny`/`SetAny`/`DeleteAny`: in-process maps cannot fail.

**Step 2 — Update `memoryStore` to implement `InProcessStore`**

Change `values map[string][]byte` to `values map[string]any`. Implement `GetAny`/`SetAny`/`DeleteAny` using direct map access under the existing `sync.RWMutex`.

Keep the `Store` interface methods (`Get`/`Set`/`Delete`) working for external callers. `Store.Get` marshals the stored `any` value to `[]byte`; `Store.Set` unmarshals bytes to `any`. These callers are rare (nobody reads raw bytes from MemoryStore in production) and the paths are not on the critical path.

**Step 3 — Update `Ref` methods in `state_ref.go`**

Add a helper:

```go
func storeIsInProcess(s internal.Store) bool {
    _, ok := s.(internal.InProcessStore)
    return ok
}
```

In each public Ref method, add a check before the existing `r.storeXxx()` dispatch:

```go
func (r *Ref[T]) Get(ctx context.Context) (T, error) {
    if r.store == nil {
        // existing fast path
    }
    if ips, ok := r.store.(internal.InProcessStore); ok {
        return refIPSGet(ips, r.key, r.initialVal, r.ttl, &r.lastWrite, &r.mu)
    }
    return r.storeGet(ctx)
}
```

For each operation, the `InProcessStore` path mirrors the `r.store == nil` logic but reads/writes through `ips.GetAny`/`ips.SetAny` instead of `r.value`. This means TTL, UpdateAndGet atomicity, and GetOrSet semantics are all preserved correctly.

Concretely, for `UpdateAndGet`:

```go
// InProcessStore path — no codec, lock covers read+write atomically
r.mu.Lock()
defer r.mu.Unlock()
var current T
if v, ok := ips.GetAny(ctx, r.key); ok {
    current = v.(T)
} else {
    current = r.initialVal
}
newVal, err := fn(current)
if err != nil { return zero, err }
ips.SetAny(ctx, r.key, newVal)
return newVal, nil
```

The type assertion `v.(T)` is safe because only `ips.SetAny(..., T-value)` ever writes to the store via this path.

**TTL handling**

The `r.store == nil` path tracks TTL via `r.lastWrite time.Time` on the Ref itself. The InProcessStore path can do the same — `r.lastWrite` is still a Ref field and is not coupled to the store type.

**Atomicity**

`storeUpdateAndGet` and `storeUpdate` currently hold `r.mu` across the Get+Set pair. The InProcessStore path does the same, ensuring atomicity.

### Affected files

| File | Change |
|---|---|
| `internal/hooks.go` | Add `InProcessStore` interface; update `memoryStore` |
| `state_ref.go` | Add InProcessStore dispatch in all 5 public Ref methods |
| `state_test.go` | Existing tests pass unchanged (behavior preserved) |
| `bench_state_test.go` | Add `BenchmarkMapWith_WithMemoryStore` to measure before/after |
| `doc/operators.md` | Update `Ref` section to mention the bypass |

### Non-goals

- No change to the external `Store` interface.
- No change to behavior when `WithStore` is not passed at all.
- No change to external store implementations (Redis, DynamoDB, etc.) — they do not implement `InProcessStore`.

---

## Part 2: ContextCarrier vs WithContextMapper Guide

### Problem

Two mechanisms exist for per-item trace context propagation:

1. **`ContextCarrier` interface** — the item type implements `Carrier() context.Context`. Kitsune calls this on every item and merges the item context with the stage context.
2. **`WithContextMapper[T]` StageOption** — a function `func(T) context.Context` is passed as an option; no interface change to the item type required.

The tradeoffs are meaningfully different but undocumented:

- `ContextCarrier` requires modifying the item type — impossible for third-party types (Kafka messages, protobuf structs).
- `WithContextMapper` is a stage option requiring no type change, but must be configured per-stage.

Neither godoc mentions the other. Users discover them by searching.

### Design

Create `doc/tracing.md` with:

1. **Introduction**: purpose of per-item context propagation.
2. **Comparison table**: `ContextCarrier` vs `WithContextMapper` across key dimensions (type modification required, works with third-party types, granularity, ergonomics).
3. **When to use each**: clear decision prose.
4. **Worked example — `ContextCarrier`**: custom `Order` type implementing the interface; stage function creates a child span from `ctx`.
5. **Worked example — `WithContextMapper`**: Kafka message type (third-party) with `WithContextMapper` extracting the trace context from a message header.
6. **Combining both**: if some stages use `ContextCarrier` items and later stages use third-party types, both mechanisms can coexist.

Cross-reference added to:
- `kitsune.go` godoc for `ContextCarrier` — link to `doc/tracing.md`
- `config.go` godoc for `WithContextMapper` — link to `doc/tracing.md`

### Affected files

| File | Change |
|---|---|
| `doc/tracing.md` | New file |
| `kitsune.go` | Add `See doc/tracing.md` to `ContextCarrier` godoc |
| `config.go` | Add `See doc/tracing.md` to `WithContextMapper` godoc |

---

## Testing

### MemoryStore bypass

- All existing `state_test.go` tests must continue to pass without modification — they use `WithStore(MemoryStore())` and must get identical results.
- Add `BenchmarkMapWith_WithMemoryStore` and `BenchmarkMapWithKey_WithMemoryStore` benchmarks to confirm allocation reduction.
- Run `task test:race` — the bypass introduces no new concurrency (same `r.mu` locking pattern).

### Tracing guide

- No runtime tests needed — it's documentation only.
- Verify the two worked examples compile (or note they are pseudo-code referencing kotel/external types).

---

## Spec self-review

- No TBDs or placeholders.
- Both parts are independent; either can be implemented first.
- InProcessStore path preserves all existing Ref semantics: TTL, atomicity, GetOrSet fn-on-absent.
- The type assertion `v.(T)` in the InProcessStore path is safe by construction: only the InProcessStore path writes typed values; external `Store.Set` callers (if any) write to the same map but as `any` after JSON-unmarshal — this is an edge case not encountered in normal usage.
- Scope is tight: two focused changes, no new public API surface (InProcessStore is internal-only).
