# Operator Cohesiveness — API Changes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Redesign Batch, Dedupe/Distinct, TakeRandom, and GroupBy; add Single, Within, and RandomSample operators.

**Architecture:** Each task is self-contained and leaves tests green. Order: config changes first (new StageOption fields), then operator changes that use those fields, then new operators. Run `task test:race` after any concurrency change.

**Tech Stack:** Go 1.23+, `pgregory.net/rapid` for property tests, `task` CLI for test commands.

**Prerequisite:** The cleanup plan (`2026-04-17-cohesiveness-cleanup.md`) should be completed first, though tasks here are independent of the renames.

---

## File Map

| File | Changes |
|---|---|
| `config.go` | Add `batchCount`, `batchMeasureFn`, `batchMeasureMax`, `dedupeWindow` fields to `stageConfig`; add `BatchCount`, `BatchMeasure`, `DedupeWindow` option functions |
| `batch.go` | Rewrite `Batch` to use `BatchCount`/`BatchMeasure` options instead of positional `size`; add `Within` operator |
| `aggregate.go` | Remove `WithDedupSet` code path from `Distinct`/`DistinctBy`; change `Dedupe`/`DedupeBy` default to global in-memory; redesign `GroupBy` as buffering pipeline operator (remove old terminal + stream variants) |
| `collect.go` | Add `Single` terminal; reclassify `TakeRandom` from terminal to buffering pipeline operator |
| `misc.go` | Add `RandomSample` operator |
| `operator_test.go` | Tests for all changed/new operators |
| `clock_test.go` | Update `Batch` test calls to use `BatchCount` option |
| `properties_test.go` | Add property tests for `GroupBy`, `Within` |
| `doc/operators.md` | Document all changes |
| `doc/api-matrix.md` | Update rows for all changed operators; add new rows |
| `doc/options.md` | Add `BatchCount`, `BatchMeasure`, `DedupeWindow` sections |

---

## Task 1: Add new `stageConfig` fields and option functions

**Files:**
- Modify: `config.go`

All new operators in this plan read their configuration from `stageConfig`. Add all new fields here before touching operator logic.

- [ ] **Step 1: Add fields to `stageConfig`**

In `config.go`, add to the `stageConfig` struct after `dropPartial bool`:

```go
// Batch flush triggers (BatchCount / BatchMeasure options).
batchCount      int          // flush when len(buf) >= batchCount; 0 = no count trigger
batchMeasureFn  func(any) int // type-erased measure function; nil = no measure trigger
batchMeasureMax int          // flush when cumulative measure >= batchMeasureMax

// Dedupe sliding window (DedupeWindow option).
// 0 = global in-memory (default); 1 = consecutive; n > 1 = sliding window of n items.
dedupeWindow int
```

- [ ] **Step 2: Add `BatchCount` option function**

Add after the existing `BatchTimeout` function in `config.go`:

```go
// BatchCount flushes a [Batch] stage when the buffer accumulates n items.
// At least one of BatchCount or BatchMeasure must be set; otherwise Batch
// panics at construction.
func BatchCount(n int) StageOption {
	return func(cfg *stageConfig) {
		cfg.batchCount = n
	}
}
```

- [ ] **Step 3: Add `BatchMeasure` option function**

```go
// BatchMeasure flushes a [Batch] stage when the sum of measureFn across all
// buffered items reaches or exceeds max. measureFn is called once per item on
// arrival and must return a non-negative integer (e.g. byte length).
// At least one of BatchCount or BatchMeasure must be set; otherwise Batch
// panics at construction.
func BatchMeasure[T any](measureFn func(T) int, max int) StageOption {
	return func(cfg *stageConfig) {
		cfg.batchMeasureFn = func(v any) int { return measureFn(v.(T)) }
		cfg.batchMeasureMax = max
	}
}
```

- [ ] **Step 4: Add `DedupeWindow` option function**

```go
// DedupeWindow sets the sliding-window size for [Dedupe] and [DedupeBy].
// n=0 (default) means global in-memory dedup — items are never re-emitted.
// n=1 means consecutive dedup — only adjacent duplicates are suppressed.
// n>1 means the last n items are remembered; an item is re-emitted once it
// leaves the window.
func DedupeWindow(n int) StageOption {
	return func(cfg *stageConfig) {
		cfg.dedupeWindow = n
	}
}
```

- [ ] **Step 5: Run tests to confirm nothing broken**

```bash
task test
```
Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add config.go
git commit -m "feat: add BatchCount, BatchMeasure, DedupeWindow StageOption fields and constructors"
```

---

## Task 2: Rewrite `Batch` to use `BatchCount`/`BatchMeasure` options

**Files:**
- Modify: `batch.go`
- Modify: `operator_test.go`
- Modify: `clock_test.go`

The current signature is `Batch(p, size int, opts...)`. The new signature removes `size` and requires at least one of `BatchCount`, `BatchMeasure`, or `BatchTimeout`.

- [ ] **Step 1: Write new tests first (TDD)**

Add to `operator_test.go`:

```go
func TestBatch_BatchCount(t *testing.T) {
	ctx := context.Background()
	ch := kitsune.NewChannel[int](10)
	batches := make(chan []int, 10)
	done := make(chan error, 1)
	go func() {
		done <- kitsune.Batch(ch.Source(), kitsune.BatchCount(3)).
			ForEach(func(_ context.Context, b []int) error {
				batches <- b
				return nil
			}).Run(ctx)
	}()

	for _, v := range []int{1, 2, 3, 4, 5} {
		if err := ch.Send(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	ch.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	var got [][]int
	for len(batches) > 0 {
		got = append(got, <-batches)
	}
	if len(got) != 2 || len(got[0]) != 3 || len(got[1]) != 2 {
		t.Errorf("batches: got %v", got)
	}
}

func TestBatch_BatchMeasure(t *testing.T) {
	ctx := context.Background()
	ch := kitsune.NewChannel[string](10)
	batches := make(chan []string, 10)
	done := make(chan error, 1)
	go func() {
		// flush when total byte length >= 6
		done <- kitsune.Batch(ch.Source(),
			kitsune.BatchMeasure(func(s string) int { return len(s) }, 6),
		).ForEach(func(_ context.Context, b []string) error {
			batches <- b
			return nil
		}).Run(ctx)
	}()

	// "abc"=3, "def"=3 → flush at 6; "gh"=2 → partial on close
	for _, s := range []string{"abc", "def", "gh"} {
		if err := ch.Send(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	ch.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	var got [][]string
	for len(batches) > 0 {
		got = append(got, <-batches)
	}
	if len(got) != 2 || len(got[0]) != 2 || got[0][0] != "abc" {
		t.Errorf("batches: got %v", got)
	}
}

func TestBatch_PanicIfNoTrigger(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when no flush trigger provided")
		}
	}()
	ch := kitsune.NewChannel[int](1)
	kitsune.Batch(ch.Source()) // no BatchCount, BatchMeasure, or BatchTimeout
}
```

- [ ] **Step 2: Run new tests — expect compilation failure or panic test to fail**

```bash
task test -run "TestBatch_BatchCount|TestBatch_BatchMeasure|TestBatch_PanicIfNoTrigger"
```
Expected: compilation error (wrong number of arguments to Batch) — confirms old signature.

- [ ] **Step 3: Rewrite `Batch` in batch.go**

Replace the existing `Batch` function with:

```go
// Batch collects items into slices and emits each slice when a flush trigger
// fires. At least one of [BatchCount], [BatchMeasure], or [BatchTimeout] must
// be provided; Batch panics at construction if none are set.
//
// Flush triggers (any combination):
//   - [BatchCount](n)   — flush when n items have accumulated
//   - [BatchMeasure](fn, max) — flush when sum of fn across buffered items >= max
//   - [BatchTimeout](d) — flush when d elapses since last flush
//
// A partial batch remaining when the source closes is flushed unless
// [DropPartial] is set.
func Batch[T any](p *Pipeline[T], opts ...StageOption) *Pipeline[[]T] {
	track(p)
	cfg := buildStageConfig(opts)

	if cfg.batchCount == 0 && cfg.batchMeasureFn == nil && cfg.batchTimeout == 0 {
		panic("kitsune: Batch requires at least one flush trigger: BatchCount, BatchMeasure, or BatchTimeout")
	}

	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "batch",
		name:   orDefault(cfg.name, "batch"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan []T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan []T)
		}
		inCh := p.build(rc)
		bufSize := rc.effectiveBufSize(cfg)
		ch := make(chan []T, bufSize)
		m := meta
		m.buffer = bufSize
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			var buf []T
			var measure int // running measure total for current buffer

			flush := func() error {
				if len(buf) == 0 {
					return nil
				}
				batch := make([]T, len(buf))
				copy(batch, buf)
				buf = buf[:0]
				measure = 0
				return outbox.Send(ctx, batch)
			}

			flushOnClose := func() error {
				if cfg.dropPartial {
					return nil
				}
				return flush()
			}

			shouldFlush := func() bool {
				if cfg.batchCount > 0 && len(buf) >= cfg.batchCount {
					return true
				}
				if cfg.batchMeasureFn != nil && measure >= cfg.batchMeasureMax {
					return true
				}
				return false
			}

			if cfg.batchTimeout == 0 {
				for {
					select {
					case item, ok := <-inCh:
						if !ok {
							return flushOnClose()
						}
						if cfg.batchMeasureFn != nil {
							measure += cfg.batchMeasureFn(item)
						}
						buf = append(buf, item)
						if shouldFlush() {
							if err := flush(); err != nil {
								return err
							}
						}
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}

			clk := cfg.clock
			if clk == nil {
				clk = internal.RealClock{}
			}
			ticker := clk.NewTicker(cfg.batchTimeout)
			defer ticker.Stop()

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return flushOnClose()
					}
					if cfg.batchMeasureFn != nil {
						measure += cfg.batchMeasureFn(item)
					}
					buf = append(buf, item)
					if shouldFlush() {
						if err := flush(); err != nil {
							return err
						}
					}
				case <-ticker.C():
					if err := flush(); err != nil {
						return err
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}
```

- [ ] **Step 4: Update all existing `Batch` call sites**

```bash
grep -rn "kitsune\.Batch\|Batch(" . --include="*.go" | grep -v "_test.go"
```

Update any call that used the old positional `size` parameter. In `clock_test.go`, the `TestBatch_TestClock` test calls `kitsune.Batch(ch.Source(), 100, ...)` — change to `kitsune.Batch(ch.Source(), kitsune.BatchCount(100), ...)`.

Search test files too:
```bash
grep -rn "Batch(" . --include="*_test.go"
```

- [ ] **Step 5: Run tests**

```bash
task test:race
```
Expected: all tests pass with no races.

- [ ] **Step 6: Commit**

```bash
git add batch.go operator_test.go clock_test.go
git commit -m "feat: rewrite Batch to use BatchCount/BatchMeasure options, remove positional size"
```

---

## Task 3: Fix `Distinct`/`DistinctBy` — remove `WithDedupSet` code path

**Files:**
- Modify: `aggregate.go`
- Modify: `operator_test.go`

`DistinctBy` currently has two code paths: one using `cfg.dedupSet` (custom backend) and one using an in-memory map. Remove the `cfg.dedupSet` path entirely — `Distinct`/`DistinctBy` always use a plain `map[K]struct{}`.

- [ ] **Step 1: Read the current DistinctBy implementation**

```bash
grep -n "dedupSet\|WithDedupSet" aggregate.go
```

Note the line numbers of the `cfg.dedupSet != nil` branch.

- [ ] **Step 2: Remove the `cfg.dedupSet` code path from `DistinctBy`**

Replace the `build` closure's stage selection in `DistinctBy`. Delete the entire `if cfg.dedupSet != nil { ... }` branch. The stage function should always use the in-memory map path:

```go
stage = func(ctx context.Context) error {
    defer close(ch)
    defer func() { go internal.DrainChan(inCh) }()
    outbox := internal.NewBlockingOutbox(ch)
    seen := make(map[K]struct{})
    for {
        select {
        case item, ok := <-inCh:
            if !ok {
                return nil
            }
            k := keyFn(item)
            if _, dup := seen[k]; dup {
                continue
            }
            seen[k] = struct{}{}
            if err := outbox.Send(ctx, item); err != nil {
                return err
            }
        case <-ctx.Done():
            return ctx.Err()
        }
    }
}
```

- [ ] **Step 3: Remove any test that specifically tests WithDedupSet on Distinct/DistinctBy**

```bash
grep -n "WithDedupSet" operator_test.go
```

Delete any test that passes `WithDedupSet` to `Distinct` or `DistinctBy`.

- [ ] **Step 4: Run tests**

```bash
task test
```
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add aggregate.go operator_test.go
git commit -m "refactor: remove WithDedupSet code path from Distinct/DistinctBy"
```

---

## Task 4: Rewrite `Dedupe`/`DedupeBy` — global default, add `DedupeWindow`

**Files:**
- Modify: `aggregate.go`
- Modify: `operator_test.go`

Current behavior: consecutive dedup (drop only adjacent duplicates). New default: global in-memory dedup. `DedupeWindow(n)` option restores windowed behavior; `DedupeWindow(1)` = consecutive.

- [ ] **Step 1: Write new tests first**

Add to `operator_test.go`:

```go
func TestDedupe_GlobalDefault(t *testing.T) {
	// Default (no options) must suppress all duplicates globally, not just consecutive.
	ctx := context.Background()
	items, err := kitsune.Collect(ctx,
		kitsune.Dedupe(kitsune.FromSlice([]int{1, 2, 1, 3, 2, 1})),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3}
	if !slices.Equal(items, want) {
		t.Errorf("got %v, want %v", items, want)
	}
}

func TestDedupe_Consecutive(t *testing.T) {
	// DedupeWindow(1) = consecutive dedup only.
	ctx := context.Background()
	items, err := kitsune.Collect(ctx,
		kitsune.Dedupe(kitsune.FromSlice([]int{1, 1, 2, 1, 2}),
			kitsune.DedupeWindow(1)),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 1, 2} // only adjacent dups removed
	if !slices.Equal(items, want) {
		t.Errorf("got %v, want %v", items, want)
	}
}

func TestDedupe_Window(t *testing.T) {
	// DedupeWindow(3): remember last 3 items; re-emit once outside window.
	ctx := context.Background()
	items, err := kitsune.Collect(ctx,
		kitsune.Dedupe(kitsune.FromSlice([]int{1, 2, 3, 1, 4}),
			kitsune.DedupeWindow(3)),
	)
	if err != nil {
		t.Fatal(err)
	}
	// 1 seen; 2 seen; 3 seen; 1 within window [1,2,3] → drop; 4 seen → emit
	// window after 3: [2,3,1]; 1 is in [2,3,1] → drop; 4 not in window → emit
	want := []int{1, 2, 3, 4}
	if !slices.Equal(items, want) {
		t.Errorf("got %v, want %v", items, want)
	}
}
```

- [ ] **Step 2: Run new tests to confirm they fail**

```bash
task test -run "TestDedupe_GlobalDefault|TestDedupe_Consecutive|TestDedupe_Window"
```
Expected: `TestDedupe_GlobalDefault` fails (current behavior is consecutive), others may pass or fail.

- [ ] **Step 3: Rewrite `DedupeBy` in aggregate.go**

Find the `DedupeBy` implementation. Replace the stage function with one that respects `cfg.dedupeWindow`:

```go
stage := func(ctx context.Context) error {
    defer close(ch)
    defer func() { go internal.DrainChan(inCh) }()
    outbox := internal.NewBlockingOutbox(ch)

    switch {
    case cfg.dedupeWindow == 0:
        // Global in-memory: never re-emit a seen key.
        seen := make(map[K]struct{})
        for {
            select {
            case item, ok := <-inCh:
                if !ok {
                    return nil
                }
                k := keyFn(item)
                if _, dup := seen[k]; dup {
                    continue
                }
                seen[k] = struct{}{}
                if err := outbox.Send(ctx, item); err != nil {
                    return err
                }
            case <-ctx.Done():
                return ctx.Err()
            }
        }

    case cfg.dedupeWindow == 1:
        // Consecutive: suppress only adjacent duplicates.
        var last K
        var hasLast bool
        for {
            select {
            case item, ok := <-inCh:
                if !ok {
                    return nil
                }
                k := keyFn(item)
                if hasLast && k == last {
                    continue
                }
                last = k
                hasLast = true
                if err := outbox.Send(ctx, item); err != nil {
                    return err
                }
            case <-ctx.Done():
                return ctx.Err()
            }
        }

    default:
        // Sliding window of size cfg.dedupeWindow.
        window := make([]K, 0, cfg.dedupeWindow)
        inWindow := func(k K) bool {
            for _, w := range window {
                if w == k {
                    return true
                }
            }
            return false
        }
        for {
            select {
            case item, ok := <-inCh:
                if !ok {
                    return nil
                }
                k := keyFn(item)
                if inWindow(k) {
                    continue
                }
                if len(window) >= cfg.dedupeWindow {
                    window = window[1:]
                }
                window = append(window, k)
                if err := outbox.Send(ctx, item); err != nil {
                    return err
                }
            case <-ctx.Done():
                return ctx.Err()
            }
        }
    }
}
```

- [ ] **Step 4: Run tests**

```bash
task test:race
```
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add aggregate.go operator_test.go
git commit -m "feat: change Dedupe default to global in-memory, add DedupeWindow option"
```

---

## Task 5: Redesign `GroupBy` — remove terminal + old stream variants, add buffering pipeline operator

**Files:**
- Modify: `aggregate.go`
- Modify: `operator_test.go`
- Modify: `properties_test.go`

Remove the terminal `GroupBy(ctx, p, keyFn) (map[K][]T, error)` and `GroupByStream`. Add a new `GroupBy(p, keyFn, opts...) *Pipeline[map[K][]T]` that buffers all input and emits one `map[K][]T` on close.

- [ ] **Step 1: Write the new test first**

Add to `operator_test.go`:

```go
func TestGroupBy_Basic(t *testing.T) {
	ctx := context.Background()
	type event struct{ kind, val string }

	result, err := kitsune.Single(ctx,
		kitsune.GroupBy(
			kitsune.FromSlice([]event{
				{"a", "1"}, {"b", "2"}, {"a", "3"},
			}),
			func(e event) string { return e.kind },
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result["a"]) != 2 || len(result["b"]) != 1 {
		t.Errorf("got %v", result)
	}
}

func TestGroupBy_EmptySource(t *testing.T) {
	ctx := context.Background()
	result, err := kitsune.Single(ctx,
		kitsune.GroupBy(kitsune.Empty[int](), func(v int) int { return v }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}

func TestGroupBy_WithName(t *testing.T) {
	ctx := context.Background()
	_, err := kitsune.Single(ctx,
		kitsune.GroupBy(kitsune.FromSlice([]int{1, 2}),
			func(v int) int { return v % 2 },
			kitsune.WithName("my_groupby"),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run new tests — expect compilation failure**

```bash
task test -run "TestGroupBy_Basic|TestGroupBy_EmptySource|TestGroupBy_WithName"
```
Expected: compilation error — `Single` not yet defined, and `GroupBy` has wrong signature. That's fine; we'll fix it in the next tasks. For now confirm intent.

- [ ] **Step 3: Remove the terminal `GroupBy` and `GroupByStream` from aggregate.go**

```bash
grep -n "^func GroupBy\|^func GroupByStream" aggregate.go
```

Delete both functions and their godoc comments. Also remove the `Group[K,T]` type if it is only used by `GroupByStream` — check:

```bash
grep -rn "Group\[" . --include="*.go"
```

If `Group[K,T]` is only used by `GroupByStream`, delete it too.

- [ ] **Step 4: Add the new `GroupBy` to aggregate.go**

```go
// GroupBy buffers all items from p, groups them by the key returned by keyFn,
// and emits a single map[K][]T when the source closes. Use [Single] to collect
// the result, or pipe the map into further stages.
//
// Items within each group preserve arrival order.
func GroupBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[map[K][]T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "group_by",
		name:   orDefault(cfg.name, "group_by"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan map[K][]T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan map[K][]T)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan map[K][]T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()
			outbox := internal.NewBlockingOutbox(ch)
			groups := make(map[K][]T)
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return outbox.Send(ctx, groups)
					}
					k := keyFn(item)
					groups[k] = append(groups[k], item)
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}
```

- [ ] **Step 5: Update any tests that used the old GroupBy or GroupByStream**

```bash
grep -rn "GroupBy\|GroupByStream" operator_test.go properties_test.go
```

Update or delete old tests. The new `GroupBy` returns `*Pipeline[map[K][]T]` and requires `Single` to collect — but `Single` is added in Task 6. For now, use `Collect` which returns `[]map[K][]T` and index `[0]`:

```go
results, err := kitsune.Collect(ctx, kitsune.GroupBy(p, keyFn))
// results[0] is the map
```

- [ ] **Step 6: Run tests**

```bash
task test
```
Expected: all tests pass.

- [ ] **Step 7: Commit**

```bash
git add aggregate.go operator_test.go properties_test.go
git commit -m "feat: replace terminal GroupBy + GroupByStream with buffering pipeline GroupBy"
```

---

## Task 6: Add `Single` terminal

**Files:**
- Modify: `collect.go`
- Modify: `operator_test.go`

`Single` drains a pipeline that is expected to emit exactly one item. Errors if zero items (by default) or more than one item (always). Options: `OrDefault(v)`, `OrZero()`.

- [ ] **Step 1: Write the tests first**

Add to `operator_test.go`:

```go
func TestSingle_OneItem(t *testing.T) {
	ctx := context.Background()
	v, err := kitsune.Single(ctx, kitsune.FromSlice([]int{42}))
	if err != nil {
		t.Fatal(err)
	}
	if v != 42 {
		t.Errorf("got %d, want 42", v)
	}
}

func TestSingle_EmptyErrors(t *testing.T) {
	ctx := context.Background()
	_, err := kitsune.Single(ctx, kitsune.Empty[int]())
	if err == nil {
		t.Error("expected error for empty pipeline")
	}
}

func TestSingle_OrDefault(t *testing.T) {
	ctx := context.Background()
	v, err := kitsune.Single(ctx, kitsune.Empty[int](), kitsune.OrDefault(99))
	if err != nil {
		t.Fatal(err)
	}
	if v != 99 {
		t.Errorf("got %d, want 99", v)
	}
}

func TestSingle_OrZero(t *testing.T) {
	ctx := context.Background()
	v, err := kitsune.Single(ctx, kitsune.Empty[string](), kitsune.OrZero[string]())
	if err != nil {
		t.Fatal(err)
	}
	if v != "" {
		t.Errorf("got %q, want empty string", v)
	}
}

func TestSingle_TooManyErrors(t *testing.T) {
	ctx := context.Background()
	_, err := kitsune.Single(ctx, kitsune.FromSlice([]int{1, 2}))
	if err == nil {
		t.Error("expected error for pipeline emitting more than one item")
	}
}
```

- [ ] **Step 2: Run new tests — expect compilation failure**

```bash
task test -run "TestSingle"
```
Expected: compilation error — `Single`, `OrDefault`, `OrZero` not defined.

- [ ] **Step 3: Implement `Single`, `OrDefault`, `OrZero` in collect.go**

Add after the `Last` function:

```go
// ---------------------------------------------------------------------------
// Single
// ---------------------------------------------------------------------------

// SingleOption configures [Single] behaviour for empty pipelines.
type SingleOption func(*singleConfig)

type singleConfig struct {
	hasDefault bool
	defaultVal any
}

// OrDefault returns v when the pipeline emits no items, instead of an error.
func OrDefault[T any](v T) SingleOption {
	return func(cfg *singleConfig) {
		cfg.hasDefault = true
		cfg.defaultVal = v
	}
}

// OrZero returns the zero value of T when the pipeline emits no items,
// instead of an error. Equivalent to OrDefault(zero).
func OrZero[T any]() SingleOption {
	var zero T
	return OrDefault[T](zero)
}

// Single drains p and returns the single item it emits.
// It returns an error if the pipeline emits zero items (unless [OrDefault] or
// [OrZero] is provided) or more than one item (always an error).
func Single[T any](ctx context.Context, p *Pipeline[T], opts ...SingleOption) (T, error) {
	cfg := &singleConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	var result T
	count := 0
	err := p.ForEach(func(_ context.Context, v T) error {
		count++
		if count > 1 {
			return fmt.Errorf("kitsune: Single: pipeline emitted more than one item")
		}
		result = v
		return nil
	}).Run(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	if count == 0 {
		if cfg.hasDefault {
			return cfg.defaultVal.(T), nil
		}
		var zero T
		return zero, fmt.Errorf("kitsune: Single: pipeline emitted no items")
	}
	return result, nil
}
```

Ensure `fmt` is imported in `collect.go`.

- [ ] **Step 4: Update GroupBy tests from Task 5 to use Single**

```bash
grep -n "results\[0\]" operator_test.go
```

Replace any `Collect` + index `[0]` workarounds added in Task 5 with proper `Single` calls.

- [ ] **Step 5: Run tests**

```bash
task test:race
```
Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add collect.go operator_test.go
git commit -m "feat: add Single terminal with OrDefault/OrZero options"
```

---

## Task 7: Reclassify `TakeRandom` as a buffering pipeline operator

**Files:**
- Modify: `collect.go`
- Modify: `operator_test.go`

Current: `TakeRandom(ctx, p, n, ...RunOption) ([]T, error)` — terminal.
New: `TakeRandom(p, n, ...StageOption) *Pipeline[[]T]` — buffering pipeline operator that emits one `[]T` on close. Users call `Single` to collect.

- [ ] **Step 1: Write the new test first**

Add to `operator_test.go`:

```go
func TestTakeRandom_Pipeline(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	sample, err := kitsune.Single(ctx, kitsune.TakeRandom(src, 3))
	if err != nil {
		t.Fatal(err)
	}
	if len(sample) != 3 {
		t.Errorf("got %d items, want 3", len(sample))
	}
	// All items must come from the source.
	srcSet := map[int]struct{}{1:{}. 2:{}, 3:{}, 4:{}, 5:{}, 6:{}, 7:{}, 8:{}, 9:{}, 10:{}}
	for _, v := range sample {
		if _, ok := srcSet[v]; !ok {
			t.Errorf("item %d not in source", v)
		}
	}
}

func TestTakeRandom_NLargerThanSource(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1, 2, 3})
	sample, err := kitsune.Single(ctx, kitsune.TakeRandom(src, 10))
	if err != nil {
		t.Fatal(err)
	}
	if len(sample) != 3 {
		t.Errorf("got %d items, want 3 (capped at source size)", len(sample))
	}
}
```

- [ ] **Step 2: Replace the `TakeRandom` implementation in collect.go**

Delete the old terminal implementation. Add the new pipeline operator version (place it in the same section or move to `batch.go` if preferred — `collect.go` is fine since it is already there):

```go
// TakeRandom returns a pipeline that buffers all items from p and emits a
// single []T containing a random sample of up to n items, selected using
// reservoir sampling (Algorithm R). Each item has an equal probability of
// being selected. Use [Single] to collect the result.
//
// The emitted slice has min(n, sourceSize) items. Order is not guaranteed.
func TakeRandom[T any](p *Pipeline[T], n int, opts ...StageOption) *Pipeline[[]T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "take_random",
		name:   orDefault(cfg.name, "take_random"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan []T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan []T)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan []T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()
			outbox := internal.NewBlockingOutbox(ch)

			if n <= 0 {
				return outbox.Send(ctx, []T{})
			}

			reservoir := make([]T, 0, n)
			i := 0
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						result := make([]T, len(reservoir))
						copy(result, reservoir)
						return outbox.Send(ctx, result)
					}
					i++
					if len(reservoir) < n {
						reservoir = append(reservoir, item)
					} else {
						j := rand.Intn(i)
						if j < n {
							reservoir[j] = item
						}
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}
```

Ensure `math/rand` is imported (it already was in the old implementation).

- [ ] **Step 3: Update tests that used the old terminal TakeRandom**

```bash
grep -rn "TakeRandom" operator_test.go
```

Old calls: `kitsune.TakeRandom(ctx, p, n)` → new: `kitsune.Single(ctx, kitsune.TakeRandom(p, n))`.

- [ ] **Step 4: Run tests**

```bash
task test:race
```
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add collect.go operator_test.go
git commit -m "feat: reclassify TakeRandom as buffering pipeline operator, use Single to collect"
```

---

## Task 8: Add `Within` operator

**Files:**
- Modify: `batch.go`
- Modify: `operator_test.go`

`Within` applies a pipeline stage to the contents of each `[]T` slice element, emitting `[]O` for each input slice. Use `Unbatch` afterwards to flatten back to `*Pipeline[O]`.

- [ ] **Step 1: Write tests first**

Add to `operator_test.go`:

```go
func TestWithin_SortEachChunk(t *testing.T) {
	ctx := context.Background()
	// Batch into chunks of 3, sort each chunk, unbatch.
	src := kitsune.FromSlice([]int{3, 1, 2, 6, 4, 5})
	result, err := kitsune.Collect(ctx,
		kitsune.Unbatch(
			kitsune.Within(
				kitsune.Batch(src, kitsune.BatchCount(3)),
				func(w *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
					return kitsune.Sort(w, func(a, b int) bool { return a < b })
				},
			),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3, 4, 5, 6}
	if !slices.Equal(result, want) {
		t.Errorf("got %v, want %v", result, want)
	}
}

func TestWithin_FilterEachChunk(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	result, err := kitsune.Collect(ctx,
		kitsune.Within(
			kitsune.Batch(src, kitsune.BatchCount(3)),
			func(w *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
				return kitsune.Filter(w, func(_ context.Context, v int) (bool, error) {
					return v%2 == 0, nil
				})
			},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	// Each chunk [1,2,3] → [2], [4,5,6] → [4,6]
	want := [][]int{{2}, {4, 6}}
	if len(result) != len(want) {
		t.Errorf("got %v, want %v", result, want)
	}
}

func TestWithin_EmptyChunk(t *testing.T) {
	ctx := context.Background()
	// A chunk that filters to nothing should emit an empty slice.
	src := kitsune.FromSlice([]int{1, 3, 5})
	result, err := kitsune.Collect(ctx,
		kitsune.Within(
			kitsune.Batch(src, kitsune.BatchCount(3)),
			func(w *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
				return kitsune.Filter(w, func(_ context.Context, v int) (bool, error) {
					return v%2 == 0, nil // no evens → empty
				})
			},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || len(result[0]) != 0 {
		t.Errorf("expected one empty slice, got %v", result)
	}
}
```

- [ ] **Step 2: Run new tests — expect compilation failure**

```bash
task test -run "TestWithin"
```
Expected: compilation error — `Within` not defined.

- [ ] **Step 3: Implement `Within` in batch.go**

Add after `MapBatch`:

```go
// Within applies stage to the items within each slice emitted by p,
// collecting the stage's output into a new slice. The output pipeline emits
// one []O per input []T slice.
//
// All existing pipeline operators work unchanged inside the stage function.
// Use [Unbatch] to flatten the result back to *Pipeline[O] when the slice
// structure is no longer needed.
//
//	kitsune.Unbatch(kitsune.Within(chunks, func(w *kitsune.Pipeline[T]) *kitsune.Pipeline[T] {
//	    return kitsune.Sort(w, less)
//	}))
func Within[T, O any](p *Pipeline[[]T], stage func(*Pipeline[T]) *Pipeline[O], opts ...StageOption) *Pipeline[[]O] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "within",
		name:   orDefault(cfg.name, "within"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan []O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan []O)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan []O, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stageFunc := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()
			outbox := internal.NewBlockingOutbox(ch)
			for {
				select {
				case items, ok := <-inCh:
					if !ok {
						return nil
					}
					transformed, err := Collect(ctx, stage(FromSlice(items)))
					if err != nil {
						return err
					}
					if err := outbox.Send(ctx, transformed); err != nil {
						return err
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stageFunc, m)
		return ch
	}
	return newPipeline(id, meta, build)
}
```

- [ ] **Step 4: Run tests**

```bash
task test:race
```
Expected: all tests pass.

- [ ] **Step 5: Add a property test**

Add to `properties_test.go`:

```go
func TestWithin_SortProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		items := rapid.SliceOf(rapid.Int()).Draw(t, "items")
		chunkSize := rapid.IntRange(1, 10).Draw(t, "chunkSize")

		ctx := context.Background()
		got, err := kitsune.Collect(ctx,
			kitsune.Unbatch(
				kitsune.Within(
					kitsune.Batch(kitsune.FromSlice(items), kitsune.BatchCount(chunkSize)),
					func(w *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
						return kitsune.Sort(w, func(a, b int) bool { return a < b })
					},
				),
			),
		)
		if err != nil {
			t.Fatal(err)
		}
		// Total count must be preserved.
		if len(got) != len(items) {
			t.Errorf("len mismatch: got %d, want %d", len(got), len(items))
		}
		// Each chunk must be sorted.
		for i := 0; i+1 < len(got); i++ {
			// Within a chunk boundary, items should be non-decreasing.
			// (Across chunk boundaries they need not be.)
			chunkStart := (i / chunkSize) * chunkSize
			chunkPos := i - chunkStart
			if chunkPos > 0 && got[i-1] > got[i] {
				// Only flag if within same chunk.
				if i/chunkSize == (i-1)/chunkSize {
					t.Errorf("within chunk: got[%d]=%d > got[%d]=%d", i-1, got[i-1], i, got[i])
				}
			}
		}
	})
}
```

- [ ] **Step 6: Run property tests**

```bash
task test:property
```
Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add batch.go operator_test.go properties_test.go
git commit -m "feat: add Within operator for applying pipeline stages to slice contents"
```

---

## Task 9: Add `RandomSample` operator

**Files:**
- Modify: `misc.go`
- Modify: `operator_test.go`

`RandomSample` is a streaming (non-barrier) probabilistic filter. Each item passes independently with probability `rate`.

- [ ] **Step 1: Write tests first**

Add to `operator_test.go`:

```go
func TestRandomSample_RateZero(t *testing.T) {
	// rate=0.0: nothing passes through.
	ctx := context.Background()
	result, err := kitsune.Collect(ctx,
		kitsune.RandomSample(kitsune.FromSlice([]int{1, 2, 3, 4, 5}), 0.0),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Errorf("rate=0: expected empty, got %v", result)
	}
}

func TestRandomSample_RateOne(t *testing.T) {
	// rate=1.0: everything passes through.
	ctx := context.Background()
	src := []int{1, 2, 3, 4, 5}
	result, err := kitsune.Collect(ctx,
		kitsune.RandomSample(kitsune.FromSlice(src), 1.0),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result, src) {
		t.Errorf("rate=1: got %v, want %v", result, src)
	}
}

func TestRandomSample_WithName(t *testing.T) {
	ctx := context.Background()
	_, err := kitsune.Collect(ctx,
		kitsune.RandomSample(kitsune.FromSlice([]int{1}), 0.5, kitsune.WithName("sample")),
	)
	if err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run tests — expect compilation failure**

```bash
task test -run "TestRandomSample"
```
Expected: compilation error — `RandomSample` not defined.

- [ ] **Step 3: Implement `RandomSample` in misc.go**

Add after `DefaultIfEmpty`:

```go
// RandomSample passes each item with independent probability rate (0.0–1.0).
// Unlike [TakeRandom], which buffers the full stream for reservoir sampling,
// RandomSample makes a per-item decision as items arrive — it is a streaming
// (non-barrier) operator.
func RandomSample[T any](p *Pipeline[T], rate float64, opts ...StageOption) *Pipeline[T] {
	return Filter(p, func(_ context.Context, _ T) (bool, error) {
		return rand.Float64() < rate, nil
	}, opts...)
}
```

Ensure `math/rand` is imported in `misc.go`.

- [ ] **Step 4: Run tests**

```bash
task test:race
```
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add misc.go operator_test.go
git commit -m "feat: add RandomSample streaming probabilistic filter"
```

---

## Task 10: Documentation updates

**Files:**
- Modify: `doc/operators.md`
- Modify: `doc/api-matrix.md`
- Modify: `doc/options.md`

- [ ] **Step 1: Update `doc/operators.md`**

Make the following changes:

1. **Batch section**: update signature to `Batch(p, opts...)`, document `BatchCount`/`BatchMeasure` options, remove positional `size` from examples.
2. **Dedupe/DedupeBy section**: update to describe global default and `DedupeWindow` option.
3. **GroupBy section**: remove terminal `GroupBy(ctx,...)` entry; update to describe new buffering pipeline operator returning `*Pipeline[map[K][]T]`; add usage example with `Single`.
4. **TakeRandom section**: update to buffering pipeline operator returning `*Pipeline[[]T]`; add example with `Single`.
5. **Add Single section** (under Terminals): document `Single`, `OrDefault`, `OrZero`.
6. **Add Within section** (under Batch/Window): document `Within`, include the `Unbatch` pattern.
7. **Add RunningGroupBy section** — no, this was removed. Verify `GroupByStream` and old `GroupBy(ctx)` are fully removed from the doc.
8. **Add RandomSample section** (under Filtering): document `RandomSample`.
9. **Sort/SortBy**: add "buffers all input" callout.
10. **Aggregate section**: split into "Running aggregates" and "Buffering aggregates" subsections.

- [ ] **Step 2: Update `doc/api-matrix.md`**

1. Remove `GroupByStream` row.
2. Update `GroupBy` row: return type is now `*Pipeline[map[K][]T]`.
3. Update `Batch` row: note `BatchCount`/`BatchMeasure` required.
4. Update `TakeRandom` row: now a pipeline operator.
5. Add rows for `Single`, `Within`, `RandomSample`.
6. Add `BatchCount`, `BatchMeasure`, `DedupeWindow` to the options columns/notes.

- [ ] **Step 3: Update `doc/options.md`**

Add sections for:
- `BatchCount(n int)` — applies to: `Batch`
- `BatchMeasure(fn, max)` — applies to: `Batch`
- `DedupeWindow(n int)` — applies to: `Dedupe`, `DedupeBy`

- [ ] **Step 4: Run full suite**

```bash
task test:all
```
Expected: all tests pass including examples.

- [ ] **Step 5: Commit**

```bash
git add doc/operators.md doc/api-matrix.md doc/options.md
git commit -m "docs: update operators.md, api-matrix.md, options.md for API changes"
```
