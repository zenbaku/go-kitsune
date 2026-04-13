# Named Result Types Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Pair[T,T] / Pair[T,V] returns in LookupBy, Pairwise, and MinMax with named types Enriched, Consecutive, and MinMaxResult.

**Architecture:** Hard swap — three new named structs added beside the operators that use them; Pair remains for Zip/CombineLatest/WithLatestFrom/Unzip. Tests updated before implementation for each operator (TDD).

**Tech Stack:** Go generics, pgregory.net/rapid (property tests), task (task runner)

---

## Task 1: LookupBy — replace `Pair[T,V]` with `Enriched[T,V]`

### 1.1 Update `TestLookupBy` to use the new field names (red)

- [ ] Open `/Users/jonathan/projects/go-kitsune/state_test.go`. Replace lines 187–191 (the trailing loop body of `TestLookupBy`) so that the loop reads `pair.Item` / `pair.Value` instead of `pair.First` / `pair.Second`.

Replace this block:

```go
	for _, pair := range pairs {
		if pair.Second != db[pair.First] {
			t.Errorf("id %d: got %q, want %q", pair.First, pair.Second, db[pair.First])
		}
	}
}
```

with:

```go
	for _, pair := range pairs {
		if pair.Value != db[pair.Item] {
			t.Errorf("id %d: got %q, want %q", pair.Item, pair.Value, db[pair.Item])
		}
	}
}
```

### 1.2 Update `TestLookupByBatchTimeoutFlushesPartialBatch` to use the new field names (red)

- [ ] In the same file, replace line 279:

```go
	pairs := make(chan kitsune.Pair[int, string], 16)
```

with:

```go
	pairs := make(chan kitsune.Enriched[int, string], 16)
```

- [ ] Then replace the `ForEach` callback body at lines 282–287:

```go
		done <- kitsune.LookupBy(ch.Source(), cfg).
			ForEach(func(_ context.Context, p kitsune.Pair[int, string]) error {
				pairs <- p
				return nil
			}).Run(ctx)
```

with:

```go
		done <- kitsune.LookupBy(ch.Source(), cfg).
			ForEach(func(_ context.Context, p kitsune.Enriched[int, string]) error {
				pairs <- p
				return nil
			}).Run(ctx)
```

- [ ] Finally, replace line 307 inside the receive loop:

```go
			seen[p.First] = p.Second
```

with:

```go
			seen[p.Item] = p.Value
```

### 1.3 Verify the compile error

- [ ] Run `go build ./...` and confirm the failure mentions `kitsune.Enriched` undefined or `pair.Item` / `pair.Value` undefined. Expected output contains `undefined: kitsune.Enriched` (or similar).

```bash
cd /Users/jonathan/projects/go-kitsune && go build ./...
```

### 1.4 Add the `Enriched[T,V]` type and switch `LookupBy` to return it (green)

- [ ] Open `/Users/jonathan/projects/go-kitsune/enrich.go`. Insert the following block immediately after the `defaultLookupBatchSize` constant on line 8 (i.e. before the `LookupBy` divider on line 10):

```go
// Enriched is the output of [LookupBy]: an item paired with the value fetched
// for its key. Items whose key is absent from the [LookupConfig.Fetch] result
// carry the zero value for V.
type Enriched[T any, V any] struct {
	Item  T
	Value V
}

```

- [ ] In the same file, replace the `LookupBy` function body (lines 60–80). Replace:

```go
func LookupBy[T any, K comparable, V any](p *Pipeline[T], cfg LookupConfig[T, K, V], opts ...StageOption) *Pipeline[Pair[T, V]] {
	size := cfg.BatchSize
	if size <= 0 {
		size = defaultLookupBatchSize
	}
	if cfg.BatchTimeout > 0 {
		opts = append([]StageOption{BatchTimeout(cfg.BatchTimeout)}, opts...)
	}
	return MapBatch(p, size, func(ctx context.Context, batch []T) ([]Pair[T, V], error) {
		keys := uniqueKeys(batch, cfg.Key)
		m, err := cfg.Fetch(ctx, keys)
		if err != nil {
			return nil, err
		}
		result := make([]Pair[T, V], len(batch))
		for i, item := range batch {
			result[i] = Pair[T, V]{First: item, Second: m[cfg.Key(item)]}
		}
		return result, nil
	}, opts...)
}
```

with:

```go
func LookupBy[T any, K comparable, V any](p *Pipeline[T], cfg LookupConfig[T, K, V], opts ...StageOption) *Pipeline[Enriched[T, V]] {
	size := cfg.BatchSize
	if size <= 0 {
		size = defaultLookupBatchSize
	}
	if cfg.BatchTimeout > 0 {
		opts = append([]StageOption{BatchTimeout(cfg.BatchTimeout)}, opts...)
	}
	return MapBatch(p, size, func(ctx context.Context, batch []T) ([]Enriched[T, V], error) {
		keys := uniqueKeys(batch, cfg.Key)
		m, err := cfg.Fetch(ctx, keys)
		if err != nil {
			return nil, err
		}
		result := make([]Enriched[T, V], len(batch))
		for i, item := range batch {
			result[i] = Enriched[T, V]{Item: item, Value: m[cfg.Key(item)]}
		}
		return result, nil
	}, opts...)
}
```

- [ ] Update the `LookupBy` godoc on lines 46–52. Replace this block:

```go
// LookupBy enriches each item with a value fetched in bulk, emitting a [Pair]
// of the original item and its looked-up value.
//
// Items are batched internally; a single Fetch call is made per batch with
// deduplicated keys. Items whose key is absent from the Fetch result carry
// the zero value for V.
```

with:

```go
// LookupBy enriches each item with a value fetched in bulk, emitting an
// [Enriched] value carrying the original item and its looked-up value.
//
// Items are batched internally; a single Fetch call is made per batch with
// deduplicated keys. Items whose key is absent from the Fetch result carry
// the zero value for V.
```

### 1.5 Update the `lookupby` example

- [ ] Open `/Users/jonathan/projects/go-kitsune/examples/lookupby/main.go`. Replace line 13:

```go
//   - Pair[T,V] output attaching the fetched value to the original item
```

with:

```go
//   - Enriched[T,V] output attaching the fetched value to the original item
```

- [ ] Replace lines 64–68 (the `ForEach` callback):

```go
		done <- kitsune.LookupBy(ch.Source(), cfg).
			ForEach(func(_ context.Context, p kitsune.Pair[int, string]) error {
				fmt.Printf("  id=%d  name=%q\n", p.First, p.Second)
				return nil
			}).Run(ctx)
```

with:

```go
		done <- kitsune.LookupBy(ch.Source(), cfg).
			ForEach(func(_ context.Context, p kitsune.Enriched[int, string]) error {
				fmt.Printf("  id=%d  name=%q\n", p.Item, p.Value)
				return nil
			}).Run(ctx)
```

### 1.6 Verify build, tests, and example

- [ ] Run the build and tests. Both must pass.

```bash
cd /Users/jonathan/projects/go-kitsune && go build ./... && task test
```

Expected: build is clean and the test summary contains `ok  	github.com/zenbaku/go-kitsune` with no failures.

- [ ] Run the example to confirm it still prints all five users.

```bash
cd /Users/jonathan/projects/go-kitsune && go run ./examples/lookupby
```

Expected output (order may vary slightly under timing):

```
=== lookupby with BatchTimeout ===
  id=1  name="alice"
  id=2  name="bob"
  id=3  name="carol"
  id=4  name="dave"
  id=5  name="eve"
```

### 1.7 Commit

- [ ] Stage and commit only the files touched in this task.

```bash
cd /Users/jonathan/projects/go-kitsune && git add enrich.go state_test.go examples/lookupby/main.go && git commit -m "refactor(lookupby): return Enriched[T,V] instead of Pair[T,V]"
```

---

## Task 2: Pairwise — replace `Pair[T,T]` with `Consecutive[T]`

### 2.1 Update `TestPairwise` to use the new field names (red)

- [ ] Open `/Users/jonathan/projects/go-kitsune/operators2_test.go`. Replace lines 293–301 inside `TestPairwise`:

```go
	want := []kitsune.Pair[int, int]{{1, 2}, {2, 3}, {3, 4}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, p := range want {
		if got[i] != p {
			t.Fatalf("got[%d]=%v, want %v", i, got[i], p)
		}
	}
}
```

with:

```go
	want := []kitsune.Consecutive[int]{{Prev: 1, Curr: 2}, {Prev: 2, Curr: 3}, {Prev: 3, Curr: 4}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, p := range want {
		if got[i] != p {
			t.Fatalf("got[%d]=%v, want %v", i, got[i], p)
		}
	}
}
```

### 2.2 Verify the compile error

- [ ] Run the build and confirm `kitsune.Consecutive` is undefined.

```bash
cd /Users/jonathan/projects/go-kitsune && go build ./...
```

Expected: failure containing `undefined: kitsune.Consecutive`.

### 2.3 Add the `Consecutive[T]` type and switch `Pairwise` to return it (green)

- [ ] Open `/Users/jonathan/projects/go-kitsune/operator_transform.go`. Insert the following block immediately after the `Indexed` type declaration (around line 30, before the next divider). Place it just above the `// ---------------------------------------------------------------------------` divider that introduces `Pairwise` on line 82. Insert directly above line 82:

```go
// Consecutive is the output of [Pairwise]: two adjacent items from a stream,
// where Prev is the earlier item and Curr is the later one.
type Consecutive[T any] struct {
	Prev T
	Curr T
}

```

- [ ] Replace the `Pairwise` function (lines 88–140). Replace:

```go
func Pairwise[T any](p *Pipeline[T], opts ...StageOption) *Pipeline[Pair[T, T]] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "pairwise",
		name:   orDefault(cfg.name, "pairwise"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan Pair[T, T] {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan Pair[T, T])
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan Pair[T, T], buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			var prev T
			first := true
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if !first {
						if err := outbox.Send(ctx, Pair[T, T]{First: prev, Second: item}); err != nil {
							return err
						}
					}
					first = false
					prev = item
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
```

with:

```go
func Pairwise[T any](p *Pipeline[T], opts ...StageOption) *Pipeline[Consecutive[T]] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "pairwise",
		name:   orDefault(cfg.name, "pairwise"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan Consecutive[T] {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan Consecutive[T])
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan Consecutive[T], buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			var prev T
			first := true
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if !first {
						if err := outbox.Send(ctx, Consecutive[T]{Prev: prev, Curr: item}); err != nil {
							return err
						}
					}
					first = false
					prev = item
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
```

### 2.4 Verify build and tests

- [ ] Run build and tests.

```bash
cd /Users/jonathan/projects/go-kitsune && go build ./... && task test
```

Expected: clean build and `task test` reports `ok` with no failures. `TestPairwise`, `TestPairwiseSingleItem`, and `TestPairwiseEmpty` must all still pass (the latter two only inspect `len(got)`, so no edits are needed).

### 2.5 Run the race tests for the Pairwise stage

- [ ] Pairwise touches concurrency primitives. Run the race detector.

```bash
cd /Users/jonathan/projects/go-kitsune && task test:race
```

Expected: clean output, no race reports.

### 2.6 Commit

- [ ] Stage and commit.

```bash
cd /Users/jonathan/projects/go-kitsune && git add operator_transform.go operators2_test.go && git commit -m "refactor(pairwise): return Consecutive[T] instead of Pair[T,T]"
```

---

## Task 3: MinMax — replace `Pair[T,T]` with `MinMaxResult[T]`

### 3.1 Update `TestMinMax` to use the new field names (red)

- [ ] Open `/Users/jonathan/projects/go-kitsune/operators2_test.go`. Replace lines 487–496 inside `TestMinMax`:

```go
	pair, ok, err := kitsune.MinMax(ctx, p, less)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if pair.First != 1 || pair.Second != 9 {
		t.Fatalf("min=%d max=%d, want 1 9", pair.First, pair.Second)
	}
}
```

with:

```go
	result, ok, err := kitsune.MinMax(ctx, p, less)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if result.Min != 1 || result.Max != 9 {
		t.Fatalf("min=%d max=%d, want 1 9", result.Min, result.Max)
	}
}
```

### 3.2 Verify the compile error

- [ ] Run build and confirm `kitsune.MinMaxResult` is undefined or that `pair.Min` / `pair.Max` are undefined fields on `Pair`.

```bash
cd /Users/jonathan/projects/go-kitsune && go build ./...
```

Expected: build failure naming `MinMaxResult` or `Min` / `Max` on `Pair`.

### 3.3 Add the `MinMaxResult[T]` type and switch `MinMax` to return it (green)

- [ ] Open `/Users/jonathan/projects/go-kitsune/collect.go`. Insert the following block immediately above `// MinMax returns both the minimum and maximum...` on line 207 (i.e. between `Max` on line 205 and the `MinMax` doc comment):

```go
// MinMaxResult is the output of [MinMax]: the smallest and largest items
// observed in a single pass over a pipeline.
type MinMaxResult[T any] struct {
	Min T
	Max T
}

```

- [ ] Replace the `MinMax` function (lines 209–231):

```go
func MinMax[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (Pair[T, T], bool, error) {
	var result Pair[T, T]
	found := false
	err := p.ForEach(func(_ context.Context, v T) error {
		if !found {
			result.First = v
			result.Second = v
			found = true
			return nil
		}
		if less(v, result.First) {
			result.First = v
		}
		if less(result.Second, v) {
			result.Second = v
		}
		return nil
	}).Run(ctx, opts...)
	if err != nil {
		return Pair[T, T]{}, false, err
	}
	return result, found, nil
}
```

with:

```go
func MinMax[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (MinMaxResult[T], bool, error) {
	var result MinMaxResult[T]
	found := false
	err := p.ForEach(func(_ context.Context, v T) error {
		if !found {
			result.Min = v
			result.Max = v
			found = true
			return nil
		}
		if less(v, result.Min) {
			result.Min = v
		}
		if less(result.Max, v) {
			result.Max = v
		}
		return nil
	}).Run(ctx, opts...)
	if err != nil {
		return MinMaxResult[T]{}, false, err
	}
	return result, found, nil
}
```

### 3.4 Verify build and tests

- [ ] Run build and tests.

```bash
cd /Users/jonathan/projects/go-kitsune && go build ./... && task test
```

Expected: clean build and `task test` reports `ok`. `TestMinMax` and `TestMinMaxEmpty` must both still pass (`TestMinMaxEmpty` only inspects `ok`, no edits needed).

### 3.5 Commit

- [ ] Stage and commit.

```bash
cd /Users/jonathan/projects/go-kitsune && git add collect.go operators2_test.go && git commit -m "refactor(minmax): return MinMaxResult[T] instead of Pair[T,T]"
```

---

## Task 4: Update documentation (`doc/operators.md`, `doc/api-matrix.md`)

### 4.1 Update the `Pairwise` section in `doc/operators.md`

- [ ] Open `/Users/jonathan/projects/go-kitsune/doc/operators.md`. Replace the `Pairwise` signature on line 1156:

```go
func Pairwise[T any](p *Pipeline[T], opts ...StageOption) *Pipeline[Pair[T, T]]
```

with:

```go
func Pairwise[T any](p *Pipeline[T], opts ...StageOption) *Pipeline[Consecutive[T]]
```

- [ ] Replace the example on lines 1165–1172:

```go
deltas := kitsune.Map(
    kitsune.Pairwise(prices),
    kitsune.LiftPure(func(p kitsune.Pair[float64, float64]) float64 {
        return p.Second - p.First
    }),
)
```

with:

```go
deltas := kitsune.Map(
    kitsune.Pairwise(prices),
    kitsune.LiftPure(func(c kitsune.Consecutive[float64]) float64 {
        return c.Curr - c.Prev
    }),
)
```

### 4.2 Update the `LookupBy` section in `doc/operators.md`

- [ ] Replace the `LookupBy` signature on lines 2098–2104:

```go
func LookupBy[T any, K comparable, V any](
    p *Pipeline[T],
    cfg LookupConfig[T, K, V],
    opts ...StageOption,
) *Pipeline[Pair[T, V]]
```

with:

```go
func LookupBy[T any, K comparable, V any](
    p *Pipeline[T],
    cfg LookupConfig[T, K, V],
    opts ...StageOption,
) *Pipeline[Enriched[T, V]]
```

- [ ] Replace the description on line 2106:

```
Enriches each item with a value fetched in bulk, emitting `Pair[T, V]`. Items whose key is absent from the fetch result carry the zero value for `V`. `LookupConfig` carries:
```

with:

```
Enriches each item with a value fetched in bulk, emitting `Enriched[T, V]` (fields `Item` and `Value`). Items whose key is absent from the fetch result carry the zero value for `V`. `LookupConfig` carries:
```

- [ ] Replace the example trailing comment on line 2123:

```
// each item: Pair[Event, User]{First: event, Second: user}
```

with:

```
// each item: Enriched[Event, User]{Item: event, Value: user}
```

### 4.3 Update the `MinMax` section in `doc/operators.md`

- [ ] Replace the `MinMax` signature on line 2207:

```go
func MinMax[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (Pair[T, T], bool, error)
```

with:

```go
func MinMax[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (MinMaxResult[T], bool, error)
```

- [ ] Replace the description on line 2210:

```
Terminal aggregators. `Sum` works on any `Numeric` type. `Min` and `Max` take a `less` comparator and return `(zero, false, nil)` if the pipeline is empty. `MinMax` computes both in a single pass.
```

with:

```
Terminal aggregators. `Sum` works on any `Numeric` type. `Min` and `Max` take a `less` comparator and return `(zero, false, nil)` if the pipeline is empty. `MinMax` computes both in a single pass and returns a `MinMaxResult[T]` with fields `Min` and `Max`.
```

### 4.4 Update `doc/api-matrix.md`

- [ ] Open `/Users/jonathan/projects/go-kitsune/doc/api-matrix.md`. Replace the `LookupBy` row on line 178:

```
| `LookupBy` | `LookupBy[T,K,V](p, keyFn, fetch, opts...)` → `*Pipeline[Pair[T,V]]` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | ✓ | – |
```

with:

```
| `LookupBy` | `LookupBy[T,K,V](p, keyFn, fetch, opts...)` → `*Pipeline[Enriched[T,V]]` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | ✓ | – |
```

- [ ] Replace the `MinMax` row on line 239:

```
| `MinMax` | `MinMax[T](ctx, p, opts...)` → `(Pair[T,T], bool, error)` |
```

with:

```
| `MinMax` | `MinMax[T](ctx, p, opts...)` → `(MinMaxResult[T], bool, error)` |
```

- [ ] Replace the `Pairwise` row on line 283:

```
| `Pairwise` | `Pairwise[T](p, opts...)` → `*Pipeline[Pair[T,T]]` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
```

with:

```
| `Pairwise` | `Pairwise[T](p, opts...)` → `*Pipeline[Consecutive[T]]` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
```

### 4.5 Verify documentation is consistent

- [ ] Sanity-check that no stray `Pair[T, V]`, `Pair[T,V]`, `Pair[T, T]`, or `Pair[T,T]` references remain that should have been updated. The only remaining hits should be in `doc/roadmap-archive.md` (historical), `doc/roadmap.md` (historical), and the `Zip` / `CombineLatest` / `WithLatestFrom` / `Unzip` sections.

```bash
cd /Users/jonathan/projects/go-kitsune && rg -n 'Pair\[T,\s*[TV]\]' doc/operators.md doc/api-matrix.md
```

Expected: matches only in `Zip`, `ZipWith`, `CombineLatest`, `WithLatestFrom`, and `Unzip` rows / sections.

### 4.6 Commit

- [ ] Stage and commit doc updates.

```bash
cd /Users/jonathan/projects/go-kitsune && git add doc/operators.md doc/api-matrix.md && git commit -m "docs: reflect Enriched/Consecutive/MinMaxResult return types"
```

---

## Task 5: Final verification

### 5.1 Full test suite

- [ ] Run the broadest test target before declaring victory.

```bash
cd /Users/jonathan/projects/go-kitsune && task test:all
```

Expected: every line ends in `ok` or `PASS`. No failures, no race reports.

### 5.2 All examples build and run

- [ ] Run the example matrix to make sure no examples broke.

```bash
cd /Users/jonathan/projects/go-kitsune && task test:examples
```

Expected: each example completes with `PASS`.

### 5.3 Confirm the only remaining `Pair` users are the four sanctioned operators

- [ ] Sanity-check the source tree.

```bash
cd /Users/jonathan/projects/go-kitsune && rg -n 'Pair\[' --type go
```

Expected: every match falls under `Zip`, `ZipWith`, `CombineLatest`, `WithLatestFrom`, `Unzip`, the `Pair` type definition itself, or test files exercising those operators. No matches in `enrich.go`, `operator_transform.go` (Pairwise), or `collect.go` (MinMax).

---

## Self-review checklist (writing-plans skill)

- [x] **Goal is one sentence and concrete.** "Replace Pair[T,T] / Pair[T,V] returns in LookupBy, Pairwise, and MinMax with named types."
- [x] **Architecture explains the shape of the change.** Hard swap, three new structs, no aliases, Pair retained for the four named operators.
- [x] **Tasks are TDD-ordered.** Each operator task starts with a failing test, verifies the failure, then implements, then re-verifies.
- [x] **Each task is small.** No task should take more than ~5 minutes; each contains exact code.
- [x] **No placeholders / no TBDs.** Every code block is concrete; every command is exact.
- [x] **Commands are runnable as written.** Every `bash` block uses absolute paths; expected output is described.
- [x] **Commits are scoped per task.** One commit per operator plus one for docs.
- [x] **Final verification covers race tests, full suite, and examples.** Task 5 runs `task test:all` and `task test:examples`.
- [x] **Existing tests that don't depend on field names are explicitly noted as unchanged** (`TestPairwiseEmpty`, `TestPairwiseSingleItem`, `TestMinMaxEmpty`, `TestLookupByDeduplicatesKeys`).
- [x] **Cross-package collisions checked.** `Enriched` is also a type name in `examples/enrich/main.go` and `examples/concurrency-guide/enrich/main.go`, but those are in `package main`, so no collision with `kitsune.Enriched`.
- [x] **Pair retention is explicit.** Plan states up-front that `Zip`, `CombineLatest`, `WithLatestFrom`, and `Unzip` keep `Pair`.
