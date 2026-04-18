# MemoryStore Codec Bypass + Tracing Context Guide Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate codec overhead for in-process MemoryStore Ref operations and add a `doc/tracing.md` guide for per-item context propagation.

**Architecture:** Add an `InProcessStore` interface to `internal/hooks.go`; `memoryStore` implements it by storing `any` values. `Ref` detects `InProcessStore` via type assertion and bypasses marshal/unmarshal, storing `T` directly. The tracing guide is a pure documentation addition.

**Tech Stack:** Go generics, `sync.RWMutex`, `internal/hooks.go`, `state_ref.go`

---

## File Map

| File | Change |
|---|---|
| `internal/hooks.go` | Add `InProcessStore` interface; update `memoryStore` to store `any` and implement `InProcessStore` |
| `state_ref.go` | Add InProcessStore fast-path in all 5 public Ref methods (`Get`, `Set`, `GetOrSet`, `UpdateAndGet`, `Update`) |
| `misc_test.go` | Update `TestCodecCustom` to use a plain `testStore` (not MemoryStore) so the codec path is still exercised |
| `bench_state_test.go` | Add `BenchmarkMapWith_MemoryStore` and `BenchmarkMapWithKey_Serial_MemoryStore` |
| `doc/tracing.md` | New file: ContextCarrier vs WithContextMapper comparison guide |
| `kitsune.go` | Add `See also: doc/tracing.md` to `ContextCarrier` godoc |
| `config.go` | Add `See also: doc/tracing.md` to `WithContextMapper` godoc |
| `doc/roadmap.md` | Mark two items `[x]` |

---

## Task 1: Add `InProcessStore` interface to `internal/hooks.go`

**Files:**
- Modify: `internal/hooks.go` (after the `Store` interface block, around line 155)

- [ ] **Step 1: Write a unit test for the new interface**

Add to `misc_test.go` (or create `internal_test.go` in `internal/` if preferred — but simpler to test via public `MemoryStore()`). Add a new function at the bottom of `misc_test.go`:

```go
func TestMemoryStore_InProcessBypass(t *testing.T) {
	// MemoryStore should store and retrieve typed values without codec
	// when accessed via kitsune.MapWith — codec must NOT be called.
	panicCodec := &panicOnCallCodec{}
	key := kitsune.NewKey[int]("ips-bypass", 0)
	p := kitsune.MapWith(
		kitsune.FromSlice([]int{1, 2, 3}),
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], n int) (int, error) {
			return ref.UpdateAndGet(ctx, func(acc int) (int, error) { return acc + n, nil })
		},
	)
	got, err := p.Collect(context.Background(),
		kitsune.WithStore(kitsune.MemoryStore()),
		kitsune.WithCodec(panicCodec),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 3, 6}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("item %d: got %d, want %d", i, got[i], w)
		}
	}
}

// panicOnCallCodec panics immediately if Marshal or Unmarshal is called.
// Used to verify InProcessStore paths bypass codec entirely.
type panicOnCallCodec struct{}

func (panicOnCallCodec) Marshal(v any) ([]byte, error) {
	panic("codec.Marshal called on InProcessStore path — bypass is broken")
}

func (panicOnCallCodec) Unmarshal(data []byte, v any) error {
	panic("codec.Unmarshal called on InProcessStore path — bypass is broken")
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/jonathan/projects/go-kitsune && go test -run TestMemoryStore_InProcessBypass -v
```

Expected: FAIL — test panics because codec IS called (before the bypass is implemented).

- [ ] **Step 3: Add `InProcessStore` interface to `internal/hooks.go`**

Insert after the closing brace of `func (s *memoryStore) Delete(...)` (around line 186), before the Cache block:

```go
// InProcessStore is implemented by stores that live in the same process and
// support direct any-typed access, bypassing codec serialization.
// [MemoryStore] implements this interface.
type InProcessStore interface {
	GetAny(key string) (any, bool)
	SetAny(key string, value any)
	DeleteAny(key string)
}
```

- [ ] **Step 4: Update `memoryStore` to store `any` and implement `InProcessStore`**

Replace the entire `memoryStore` block (lines 162-186 of `internal/hooks.go`):

```go
type memoryStore struct {
	mu     sync.RWMutex
	values map[string]any
}

func MemoryStore() Store {
	return &memoryStore{values: make(map[string]any)}
}

func (s *memoryStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[key]
	if !ok {
		return nil, false, nil
	}
	// Values written via Store.Set are stored as []byte; return directly.
	if b, isByteSlice := v.([]byte); isByteSlice {
		return b, true, nil
	}
	// Values written via SetAny are typed — marshal for external callers.
	data, err := json.Marshal(v)
	return data, true, err
}

func (s *memoryStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func (s *memoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

// InProcessStore implementation — no error returns; in-process maps cannot fail.

func (s *memoryStore) GetAny(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[key]
	return v, ok
}

func (s *memoryStore) SetAny(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
}

func (s *memoryStore) DeleteAny(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
}
```

Note: `MemoryStore()` constructor is moved here (remove the duplicate at line 158 of the original file).

- [ ] **Step 5: Verify the package compiles**

```bash
cd /Users/jonathan/projects/go-kitsune && go build ./internal/...
```

Expected: no errors.

- [ ] **Step 6: Commit**

```bash
cd /Users/jonathan/projects/go-kitsune
git add internal/hooks.go
git commit -m "feat(internal): add InProcessStore interface; update memoryStore to store any"
```

---

## Task 2: Add InProcessStore fast-path to all Ref methods

**Files:**
- Modify: `state_ref.go` (all 5 public methods: `Get`, `Set`, `GetOrSet`, `UpdateAndGet`, `Update`)

- [ ] **Step 1: Add InProcessStore dispatch to `Get`**

Replace `state_ref.go:79-96` (`Get` method body):

```go
func (r *Ref[T]) Get(ctx context.Context) (T, error) {
	if r.store == nil {
		r.mu.RLock()
		if r.ttl > 0 && !r.lastWrite.IsZero() && time.Now().After(r.lastWrite.Add(r.ttl)) {
			r.mu.RUnlock()
			r.mu.Lock()
			defer r.mu.Unlock()
			if !r.lastWrite.IsZero() && time.Now().After(r.lastWrite.Add(r.ttl)) {
				r.value = r.initialVal
				r.lastWrite = time.Time{}
			}
			return r.value, nil
		}
		v := r.value
		r.mu.RUnlock()
		return v, nil
	}
	if ips, ok := r.store.(internal.InProcessStore); ok {
		r.mu.RLock()
		defer r.mu.RUnlock()
		if v, found := ips.GetAny(r.key); found {
			return v.(T), nil
		}
		return r.initialVal, nil
	}
	return r.storeGet(ctx)
}
```

- [ ] **Step 2: Add InProcessStore dispatch to `Set`**

Replace `state_ref.go:100-111` (`Set` method body):

```go
func (r *Ref[T]) Set(ctx context.Context, value T) error {
	if r.store == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.value = value
		if r.ttl > 0 {
			r.lastWrite = time.Now()
		}
		return nil
	}
	if ips, ok := r.store.(internal.InProcessStore); ok {
		r.mu.Lock()
		defer r.mu.Unlock()
		ips.SetAny(r.key, value)
		return nil
	}
	return r.storeSet(ctx, value)
}
```

- [ ] **Step 3: Add InProcessStore dispatch to `GetOrSet`**

Replace `state_ref.go:116-123` (`GetOrSet` method body):

```go
func (r *Ref[T]) GetOrSet(ctx context.Context, fn func() (T, error)) (T, error) {
	if r.store == nil {
		r.mu.RLock()
		defer r.mu.RUnlock()
		return r.value, nil
	}
	if ips, ok := r.store.(internal.InProcessStore); ok {
		r.mu.Lock()
		defer r.mu.Unlock()
		if v, found := ips.GetAny(r.key); found {
			return v.(T), nil
		}
		val, err := fn()
		if err != nil {
			var zero T
			return zero, err
		}
		ips.SetAny(r.key, val)
		return val, nil
	}
	return r.storeGetOrSet(ctx, fn)
}
```

- [ ] **Step 4: Add InProcessStore dispatch to `UpdateAndGet`**

Replace `state_ref.go:126-142` (`UpdateAndGet` method body):

```go
func (r *Ref[T]) UpdateAndGet(ctx context.Context, fn func(T) (T, error)) (T, error) {
	if r.store == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		v, err := fn(r.value)
		if err != nil {
			var zero T
			return zero, err
		}
		r.value = v
		if r.ttl > 0 {
			r.lastWrite = time.Now()
		}
		return v, nil
	}
	if ips, ok := r.store.(internal.InProcessStore); ok {
		r.mu.Lock()
		defer r.mu.Unlock()
		var current T
		if v, found := ips.GetAny(r.key); found {
			current = v.(T)
		} else {
			current = r.initialVal
		}
		newVal, err := fn(current)
		if err != nil {
			var zero T
			return zero, err
		}
		ips.SetAny(r.key, newVal)
		return newVal, nil
	}
	return r.storeUpdateAndGet(ctx, fn)
}
```

- [ ] **Step 5: Add InProcessStore dispatch to `Update`**

Replace `state_ref.go:145-160` (`Update` method body):

```go
func (r *Ref[T]) Update(ctx context.Context, fn func(T) (T, error)) error {
	if r.store == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		v, err := fn(r.value)
		if err != nil {
			return err
		}
		r.value = v
		if r.ttl > 0 {
			r.lastWrite = time.Now()
		}
		return nil
	}
	if ips, ok := r.store.(internal.InProcessStore); ok {
		r.mu.Lock()
		defer r.mu.Unlock()
		var current T
		if v, found := ips.GetAny(r.key); found {
			current = v.(T)
		} else {
			current = r.initialVal
		}
		newVal, err := fn(current)
		if err != nil {
			return err
		}
		ips.SetAny(r.key, newVal)
		return nil
	}
	return r.storeUpdate(ctx, fn)
}
```

- [ ] **Step 6: Run `TestMemoryStore_InProcessBypass` to verify it now passes**

```bash
cd /Users/jonathan/projects/go-kitsune && go test -run TestMemoryStore_InProcessBypass -v
```

Expected: PASS — no panic, correct values `[1, 3, 6]`.

- [ ] **Step 7: Run full test suite**

```bash
cd /Users/jonathan/projects/go-kitsune && go test ./...
```

Expected: all pass except `TestCodecCustom` which will now FAIL because codec is no longer called for MemoryStore. This is expected — the next task fixes it.

- [ ] **Step 8: Commit the Ref changes**

```bash
cd /Users/jonathan/projects/go-kitsune
git add state_ref.go misc_test.go
git commit -m "feat(state): bypass codec for InProcessStore (MemoryStore) Ref operations"
```

---

## Task 3: Fix `TestCodecCustom` to use a non-InProcessStore backend

**Files:**
- Modify: `misc_test.go`

The test `TestCodecCustom` currently verifies that a custom codec is invoked when using `WithStore(MemoryStore())`. After the bypass, codec is never called for MemoryStore. The test needs to use a plain store that implements `Store` but not `InProcessStore`, so it still verifies the codec integration works for external stores.

- [ ] **Step 1: Add a `plainStore` helper in `misc_test.go`**

Add this after the `panicOnCallCodec` type added in Task 1:

```go
// plainStore implements Store but not InProcessStore.
// Used to test that the codec path is exercised for non-InProcess stores.
type plainStore struct {
	mu     sync.RWMutex
	values map[string][]byte
}

func newPlainStore() *plainStore {
	return &plainStore{values: make(map[string][]byte)}
}

func (s *plainStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[key]
	return v, ok, nil
}

func (s *plainStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func (s *plainStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}
```

(Add `"sync"` to the import block in `misc_test.go` if not already present — check with `go build ./...`.)

- [ ] **Step 2: Update `TestCodecCustom` to use `plainStore`**

Replace `TestCodecCustom` in `misc_test.go`:

```go
func TestCodecCustom(t *testing.T) {
	// Custom codec must be called when the store does NOT implement InProcessStore.
	codec := &countingCodec{}
	key := kitsune.NewKey("codec-custom", 0)
	p := kitsune.MapWith(
		kitsune.FromSlice([]int{1, 2, 3}),
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], n int) (int, error) {
			return ref.UpdateAndGet(ctx, func(acc int) (int, error) { return acc + n, nil })
		},
	)
	_, err := p.Collect(context.Background(),
		kitsune.WithStore(newPlainStore()),
		kitsune.WithCodec(codec),
	)
	if err != nil {
		t.Fatal(err)
	}
	if codec.marshals.Load() == 0 {
		t.Error("custom codec Marshal was never called")
	}
}
```

- [ ] **Step 3: Run tests to verify they all pass**

```bash
cd /Users/jonathan/projects/go-kitsune && go test ./...
```

Expected: all pass.

- [ ] **Step 4: Run race detector**

```bash
cd /Users/jonathan/projects/go-kitsune && go test -race ./...
```

Expected: no races.

- [ ] **Step 5: Commit**

```bash
cd /Users/jonathan/projects/go-kitsune
git add misc_test.go
git commit -m "test: update TestCodecCustom to use plainStore; MemoryStore bypasses codec"
```

---

## Task 4: Add MemoryStore benchmarks to `bench_state_test.go`

**Files:**
- Modify: `bench_state_test.go`

- [ ] **Step 1: Add benchmarks with explicit MemoryStore**

Add at the end of `bench_state_test.go` (after `benchForEach`):

```go
// ---------------------------------------------------------------------------
// MemoryStore bypass benchmarks
// ---------------------------------------------------------------------------
//
// These benchmarks use WithStore(MemoryStore()) explicitly to exercise the
// InProcessStore fast path. Compare against BenchmarkMapWith_Serial and
// BenchmarkMapWithKey_Serial_FewKeys to confirm zero codec overhead.

func BenchmarkMapWith_MemoryStore(b *testing.B) {
	b.Helper()
	ctx := context.Background()
	items := make([]int, benchItemCount)
	for i := range items {
		items[i] = i
	}
	counterKey := kitsune.NewKey[int]("bench_ms_counter", 0)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		p := kitsune.MapWith(
			kitsune.FromSlice(items),
			counterKey,
			func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
				return ref.UpdateAndGet(ctx, func(acc int) (int, error) {
					return acc + v, nil
				})
			},
		)
		if err := p.Drain().Run(ctx, kitsune.WithStore(kitsune.MemoryStore())); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMapWithKey_Serial_FewKeys_MemoryStore(b *testing.B) {
	b.Helper()
	ctx := context.Background()
	items := make([]benchItem, benchItemCount)
	for i := range items {
		items[i] = benchItem{key: fmt.Sprintf("k%d", i%10), val: i}
	}
	totalKey := kitsune.NewKey[int]("bench_ms_total", 0)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		p := kitsune.MapWithKey(
			kitsune.FromSlice(items),
			func(it benchItem) string { return it.key },
			totalKey,
			func(ctx context.Context, ref *kitsune.Ref[int], it benchItem) (int, error) {
				return ref.UpdateAndGet(ctx, func(t int) (int, error) {
					return t + it.val, nil
				})
			},
		)
		if err := p.Drain().Run(ctx, kitsune.WithStore(kitsune.MemoryStore())); err != nil {
			b.Fatal(err)
		}
	}
}
```

- [ ] **Step 2: Run the new benchmarks**

```bash
cd /Users/jonathan/projects/go-kitsune && go test -bench='BenchmarkMapWith_MemoryStore|BenchmarkMapWith_Serial|BenchmarkMapWithKey_Serial_FewKeys' -benchmem -count=3
```

Expected: `BenchmarkMapWith_MemoryStore` and `BenchmarkMapWith_Serial` should show similar alloc counts (the former had higher allocs before the bypass due to JSON marshaling). No assertion failures.

- [ ] **Step 3: Commit**

```bash
cd /Users/jonathan/projects/go-kitsune
git add bench_state_test.go
git commit -m "bench: add MemoryStore bypass benchmarks for MapWith and MapWithKey"
```

---

## Task 5: Write `doc/tracing.md`

**Files:**
- Create: `doc/tracing.md`

- [ ] **Step 1: Create `doc/tracing.md`**

```markdown
# Per-item context propagation

Kitsune provides two mechanisms for propagating per-item trace context (or any
`context.Context` values) through a pipeline: `ContextCarrier` and
`WithContextMapper`. They solve the same problem with different trade-offs.

---

## Comparison

| | `ContextCarrier` | `WithContextMapper` |
|---|---|---|
| Requires modifying item type | Yes — must implement `Context() context.Context` | No |
| Works with third-party types | No (Kafka messages, protobuf structs, stdlib types cannot be retrofitted) | Yes |
| Granularity | Per-type: any item of that type carries context | Per-stage: configure on individual `Map`, `FlatMap`, `ForEach` stages |
| Opt-in/opt-out | Always active for items that implement the interface | Explicit — must configure each stage |
| Precedence | Lower | Higher — overrides `ContextCarrier` if both apply |

**Default rule:** use `ContextCarrier` when you own the item type. Use `WithContextMapper` when
you cannot modify the item type or when you want per-stage control.

---

## ContextCarrier

`ContextCarrier` is an interface implemented by item types that carry a `context.Context`:

```go
type ContextCarrier interface {
    Context() context.Context
}
```

When Kitsune processes an item that implements `ContextCarrier`, it merges the item's
context into the stage context. Cancellation and deadlines always come from the stage
context — the item context contributes values only (e.g. an active trace span).

### When to use

- You own the item type and can add a `Context()` method.
- Every instance of the type should carry a trace context — no per-stage configuration needed.
- You want stage functions to call `tracer.Start(ctx, "work")` without any extra plumbing.

### Example

```go
// Order carries a per-request trace span set by the HTTP handler.
type Order struct {
    ID       string
    Amount   int
    ctx      context.Context
}

func (o Order) Context() context.Context { return o.ctx }

// In a stage function, ctx already contains the span from the item.
processed := kitsune.Map(orders, func(ctx context.Context, o Order) (Invoice, error) {
    _, span := tracer.Start(ctx, "process-order")
    defer span.End()
    // ... work
    return invoice, nil
})
```

Use `kotel.NewWithTracing` to record stage-level spans that appear as parents of
the per-item spans:

```go
hook := kotel.NewWithTracing(otel.Meter("my-app"), otel.Tracer("my-app"))
runner.Run(ctx, kitsune.WithHook(hook))
```

---

## WithContextMapper

`WithContextMapper[T]` is a `StageOption` that extracts a context from each item
using a function, with no interface requirement on the item type:

```go
func WithContextMapper[T any](fn func(T) context.Context) StageOption
```

The returned context contributes values only; cancellation still comes from the stage context.

### When to use

- The item type is third-party (Kafka messages, protobuf-generated structs, stdlib types).
- You want per-stage control: some stages use context propagation, others do not.
- You need to extract context from a specific field or header (e.g. a W3C `traceparent` header in a Kafka message).

`WithContextMapper` is supported on `Map`, `FlatMap`, and `ForEach`.

### Example — Kafka messages with OpenTelemetry

```go
// kafkaHeaderCarrier adapts Kafka headers to the OTel TextMapCarrier interface.
type kafkaHeaderCarrier []kafka.Header

func (c kafkaHeaderCarrier) Get(key string) string {
    for _, h := range c {
        if strings.EqualFold(h.Key, key) {
            return string(h.Value)
        }
    }
    return ""
}

func (c kafkaHeaderCarrier) Set(key, val string) {}
func (c kafkaHeaderCarrier) Keys() []string {
    keys := make([]string, len(c))
    for i, h := range c {
        keys[i] = h.Key
    }
    return keys
}

// Extract the trace context from each Kafka message header.
processed := kitsune.Map(messages, processMessage,
    kitsune.WithContextMapper(func(m kafka.Message) context.Context {
        return otel.GetTextMapPropagator().Extract(
            context.Background(),
            kafkaHeaderCarrier(m.Headers),
        )
    }),
)
```

Inside `processMessage`, `ctx` contains the extracted span from the producer:

```go
func processMessage(ctx context.Context, m kafka.Message) (Result, error) {
    _, span := tracer.Start(ctx, "process-message")
    defer span.End()
    // ...
}
```

---

## Precedence

If a stage has `WithContextMapper` set AND the item type implements `ContextCarrier`,
the mapper function takes precedence: `ContextCarrier.Context()` is not called.

This lets you override context extraction on a per-stage basis even for types that
implement the interface.

---

## Mixing both in one pipeline

Items of different types can use different mechanisms in the same pipeline:

```go
// Stage 1: Order implements ContextCarrier — no option needed.
enriched := kitsune.Map(orders, enrich)

// Stage 2: result is a third-party proto type — use WithContextMapper.
sent := kitsune.Map(
    kitsune.Map(enriched, toProto),
    publish,
    kitsune.WithContextMapper(func(p *proto.Order) context.Context {
        return propagator.Extract(context.Background(), &protoCarrier{p})
    }),
)
```
```

- [ ] **Step 2: Verify the file exists**

```bash
ls /Users/jonathan/projects/go-kitsune/doc/tracing.md
```

Expected: file listed.

- [ ] **Step 3: Commit**

```bash
cd /Users/jonathan/projects/go-kitsune
git add doc/tracing.md
git commit -m "docs(tracing): add ContextCarrier vs WithContextMapper decision guide"
```

---

## Task 6: Update godocs and roadmap

**Files:**
- Modify: `kitsune.go` (ContextCarrier godoc)
- Modify: `config.go` (WithContextMapper godoc)
- Modify: `doc/roadmap.md` (mark two items done)

- [ ] **Step 1: Update `ContextCarrier` godoc in `kitsune.go`**

Find the `ContextCarrier` interface comment (around line 58). Append to the existing godoc (after the last existing comment line, before the type declaration):

```go
// For third-party types that cannot implement this interface (Kafka messages,
// protobuf-generated types, stdlib types), use [WithContextMapper] instead.
//
// See doc/tracing.md for a comparison table and worked examples.
```

The existing comment already has this partial sentence — the "See doc/tracing.md" line is the only addition needed after the existing `// For third-party types...` note.

Specifically, add `//\n// See doc/tracing.md for a comparison table and worked examples.` before the `type ContextCarrier interface {` line.

- [ ] **Step 2: Update `WithContextMapper` godoc in `config.go`**

Find the `WithContextMapper` function comment (around line 206). After the existing closing comment line (before `func WithContextMapper`), add:

```go
// See doc/tracing.md for a comparison table and worked examples for both approaches.
```

- [ ] **Step 3: Mark roadmap items done in `doc/roadmap.md`**

Find and replace:

```
- [ ] **Bypass codec serialization for `MemoryStore` Ref operations**:
```
→
```
- [x] **Bypass codec serialization for `MemoryStore` Ref operations**:
```

And:

```
- [ ] **`ContextCarrier` vs `WithContextMapper` decision guide**:
```
→
```
- [x] **`ContextCarrier` vs `WithContextMapper` decision guide**:
```

- [ ] **Step 4: Run full test suite one final time**

```bash
cd /Users/jonathan/projects/go-kitsune && go test ./...
```

Expected: all pass.

- [ ] **Step 5: Run race detector**

```bash
cd /Users/jonathan/projects/go-kitsune && go test -race ./...
```

Expected: no races.

- [ ] **Step 6: Final commit**

```bash
cd /Users/jonathan/projects/go-kitsune
git add kitsune.go config.go doc/roadmap.md
git commit -m "docs: link tracing guide from ContextCarrier and WithContextMapper godocs; mark roadmap items done"
```

---

## Self-review

**Spec coverage check:**
- [x] InProcessStore interface in `internal/hooks.go` — Task 1
- [x] `memoryStore` implements InProcessStore with `any` storage — Task 1
- [x] Ref methods bypass codec for InProcessStore — Task 2
- [x] Existing tests unbroken; TestCodecCustom updated to use plainStore — Task 3
- [x] Benchmarks added — Task 4
- [x] `doc/tracing.md` with comparison table, two worked examples, precedence, mixing — Task 5
- [x] Godoc cross-references added — Task 6
- [x] Roadmap items marked done — Task 6

**Placeholder scan:** No TBDs, no "similar to" references, all code blocks are complete.

**Type consistency check:**
- `InProcessStore.GetAny(key string) (any, bool)` — used consistently in Tasks 1 and 2
- `InProcessStore.SetAny(key string, value any)` — used consistently in Tasks 1 and 2
- `InProcessStore.DeleteAny(key string)` — defined in Task 1, not directly called from Ref (DeleteAny is available for future use such as Delete-on-TTL-expiry)
- `v.(T)` type assertion — safe by construction: only `ips.SetAny(key, T-value)` writes to the store via the Ref InProcessStore path
- `plainStore` — defined and used only in Task 3 (`misc_test.go`)
- `panicOnCallCodec` — defined in Task 1 step 1, used in `TestMemoryStore_InProcessBypass`
