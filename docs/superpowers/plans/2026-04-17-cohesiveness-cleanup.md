# Operator Cohesiveness — Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove dead code, dissolve compat.go, and rename operators to establish consistent naming conventions across the library.

**Architecture:** Pure refactoring — no new logic. Each task is a self-contained rename, removal, or file move that leaves all tests green. The order is: removals first (shrink surface), then renames (consistency), then compat.go dissolution (relocate survivors).

**Tech Stack:** Go 1.23+, `pgregory.net/rapid` for property tests, `task` CLI for test commands.

---

## File Map

| File | Changes |
|---|---|
| `fan_out.go` | Remove `BroadcastN`; inline its body into `Broadcast` |
| `misc.go` | Remove `ElementAt`, `Lift`; receive `MapIntersperse`, `EndWith` from compat |
| `config.go` | Remove deprecated `Skip()` error handler alias |
| `operator_take.go` | Rename `SkipLast`→`DropLast`, `SkipUntil`→`DropUntil`; remove `ElementAt` |
| `fan_combine.go` | Rename `WithLatestFrom`→`LatestFrom`, `WithLatestFromWith`→`LatestFromWith` |
| `aggregate.go` | Rename `FrequenciesStream`→`RunningFrequencies`, `FrequenciesByStream`→`RunningFrequenciesBy`; receive `RunningCountBy`/`RunningSumBy` from compat |
| `batch.go` | Receive `MapBatch` from compat |
| `pipeline.go` | Receive `Stage.Or` from compat |
| `compat.go` | Gutted task-by-task; deleted in final task |
| `operator_test.go` | Update all references to renamed/removed symbols |
| `clock_test.go` | Update any references to renamed symbols |
| `properties_test.go` | Update any references to renamed symbols |
| `doc/operators.md` | Rename all changed operators |
| `doc/api-matrix.md` | Rename all changed operators |

---

## Task 1: Remove `BroadcastN` — inline into `Broadcast`

**Files:**
- Modify: `fan_out.go`
- Modify: `operator_test.go`

`Broadcast` currently delegates to `BroadcastN`. Swap them: move the implementation body into `Broadcast` and delete `BroadcastN`.

- [ ] **Step 1: Verify existing tests pass**

```bash
task test
```
Expected: all tests pass.

- [ ] **Step 2: Replace `Broadcast` body with the inlined implementation**

In `fan_out.go`, replace:
```go
func Broadcast[T any](p *Pipeline[T], n int, opts ...StageOption) []*Pipeline[T] {
	return BroadcastN(p, n, opts...)
}
```
with the full body currently inside `BroadcastN` (the implementation that allocates IDs, builds the shared stage, and returns `[]*Pipeline[T]`). Then delete the `BroadcastN` function entirely.

The resulting `Broadcast` function should be:
```go
// Broadcast fans out each item to n identical output pipelines.
// All n pipelines must be consumed. The stage blocks until all consumers
// have accepted each item (synchronised fan-out).
func Broadcast[T any](p *Pipeline[T], n int, opts ...StageOption) []*Pipeline[T] {
	if n < 2 {
		panic("kitsune: Broadcast requires n >= 2")
	}
	track(p)
	cfg := buildStageConfig(opts)

	ids := make([]int64, n)
	for i := range ids {
		ids[i] = nextPipelineID()
	}

	metas := make([]stageMeta, n)
	for i := range metas {
		name := orDefault(cfg.name, "broadcast")
		if i > 0 {
			name = name + "_" + string(rune('0'+i))
		}
		metas[i] = stageMeta{
			id:     ids[i],
			kind:   "broadcast",
			name:   name,
			buffer: cfg.buffer,
			inputs: []int64{p.id},
		}
	}

	sharedBuild := func(rc *runCtx) []chan T {
		if existing := rc.getChan(ids[0]); existing != nil {
			chans := make([]chan T, n)
			for i, id := range ids {
				chans[i] = rc.getChan(id).(chan T)
			}
			return chans
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		chans := make([]chan T, n)
		for i := range chans {
			chans[i] = make(chan T, buf)
		}
		m := metas[0]
		m.buffer = buf
		m.getChanLen = func() int { return len(chans[0]) }
		m.getChanCap = func() int { return cap(chans[0]) }
		for i, id := range ids {
			rc.setChan(id, chans[i])
		}
		stage := func(ctx context.Context) error {
			defer func() {
				for _, c := range chans {
					close(c)
				}
			}()
			defer func() { go internal.DrainChan(inCh) }()

			outboxes := make([]internal.Outbox[T], len(chans))
			for i, c := range chans {
				outboxes[i] = internal.NewBlockingOutbox(c)
			}

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					for _, ob := range outboxes {
						if err := ob.Send(ctx, item); err != nil {
							return err
						}
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return chans
	}

	out := make([]*Pipeline[T], n)
	for i := range out {
		i := i
		out[i] = newPipeline(ids[i], metas[i], func(rc *runCtx) chan T {
			return sharedBuild(rc)[i]
		})
	}
	return out
}
```

- [ ] **Step 3: Search for any remaining `BroadcastN` references and remove them**

```bash
grep -r "BroadcastN" .
```
Expected: no matches (other than possibly docs — fix those too).

- [ ] **Step 4: Run tests**

```bash
task test
```
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add fan_out.go
git commit -m "refactor: inline BroadcastN into Broadcast, remove alias"
```

---

## Task 2: Remove `ElementAt`, `Skip()`, and `Lift`

**Files:**
- Modify: `misc.go` (remove `ElementAt`)
- Modify: `operator_take.go` (remove `ElementAt` — check which file it's actually in)
- Modify: `config.go` (remove `Skip()`)
- Modify: `misc.go` (remove `Lift`)
- Modify: `operator_test.go` (remove any `TestElementAt*` tests)

- [ ] **Step 1: Find and remove `ElementAt`**

```bash
grep -n "ElementAt" misc.go operator_take.go
```

Delete the `ElementAt` function and its godoc comment from whichever file it appears in. Also delete `ErrEmpty` if it is only referenced by `ElementAt` — check first:

```bash
grep -n "ErrEmpty" ./*.go
```

If `ErrEmpty` is only used by `ElementAt` and `First`/`Last` return `(zero, false, nil)` for empty pipelines (confirmed — they don't use `ErrEmpty`), delete `ErrEmpty` from `collect.go` as well.

- [ ] **Step 2: Remove `Skip()` from config.go**

Find and delete:
```go
func Skip() ErrorHandler { return ActionDrop() }
```

- [ ] **Step 3: Remove `Lift` from misc.go**

Find and delete:
```go
func Lift[I, O any](fn func(I) (O, error)) func(context.Context, I) (O, error) {
	return LiftFallible(fn)
}
```

- [ ] **Step 4: Remove affected tests from operator_test.go**

```bash
grep -n "ElementAt\|TestElementAt" operator_test.go
```
Delete any test functions that reference `ElementAt`.

- [ ] **Step 5: Run tests**

```bash
task test
```
Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add collect.go misc.go operator_take.go config.go operator_test.go
git commit -m "refactor: remove ElementAt, Skip() alias, Lift alias"
```

---

## Task 3: Rename `SkipLast`→`DropLast` and `SkipUntil`→`DropUntil`

**Files:**
- Modify: `operator_take.go`
- Modify: `operator_test.go`
- Modify: `properties_test.go`
- Modify: `doc/operators.md`
- Modify: `doc/api-matrix.md`

- [ ] **Step 1: Rename in operator_take.go**

```bash
sed -i '' 's/func SkipLast\b/func DropLast/g; s/func SkipUntil\b/func DropUntil/g' operator_take.go
```

Verify the godoc comments also say `DropLast`/`DropUntil` — update them manually if needed.

- [ ] **Step 2: Update all call sites**

```bash
grep -rn "SkipLast\|SkipUntil" . --include="*.go"
```

Replace every occurrence in test files and any example files.

- [ ] **Step 3: Run tests**

```bash
task test
```
Expected: all tests pass.

- [ ] **Step 4: Update docs**

```bash
grep -n "SkipLast\|SkipUntil" doc/operators.md doc/api-matrix.md
```

Replace all occurrences in both files.

- [ ] **Step 5: Commit**

```bash
git add operator_take.go operator_test.go properties_test.go doc/operators.md doc/api-matrix.md
git commit -m "refactor: rename SkipLast→DropLast, SkipUntil→DropUntil"
```

---

## Task 4: Rename `WithLatestFrom`→`LatestFrom` and `WithLatestFromWith`→`LatestFromWith`

**Files:**
- Modify: `fan_combine.go`
- Modify: `operator_test.go`
- Modify: `doc/operators.md`
- Modify: `doc/api-matrix.md`

- [ ] **Step 1: Rename in fan_combine.go**

```bash
sed -i '' 's/func WithLatestFromWith\b/func LatestFromWith/g; s/func WithLatestFrom\b/func LatestFrom/g' fan_combine.go
```

Order matters: rename `WithLatestFromWith` before `WithLatestFrom` to avoid partial matches.

- [ ] **Step 2: Update all call sites**

```bash
grep -rn "WithLatestFrom" . --include="*.go"
```

Replace every occurrence.

- [ ] **Step 3: Run tests**

```bash
task test
```
Expected: all tests pass.

- [ ] **Step 4: Update docs**

```bash
grep -n "WithLatestFrom" doc/operators.md doc/api-matrix.md
```

Replace all occurrences in both files.

- [ ] **Step 5: Commit**

```bash
git add fan_combine.go operator_test.go doc/operators.md doc/api-matrix.md
git commit -m "refactor: rename WithLatestFrom→LatestFrom, WithLatestFromWith→LatestFromWith"
```

---

## Task 5: Rename `FrequenciesStream`→`RunningFrequencies` and `FrequenciesByStream`→`RunningFrequenciesBy`

**Files:**
- Modify: `aggregate.go`
- Modify: `operator_test.go`
- Modify: `properties_test.go`
- Modify: `doc/operators.md`
- Modify: `doc/api-matrix.md`

- [ ] **Step 1: Rename in aggregate.go**

```bash
sed -i '' 's/func FrequenciesByStream\b/func RunningFrequenciesBy/g; s/func FrequenciesStream\b/func RunningFrequencies/g' aggregate.go
```

- [ ] **Step 2: Update all call sites**

```bash
grep -rn "FrequenciesStream\|FrequenciesByStream" . --include="*.go"
```

Replace every occurrence.

- [ ] **Step 3: Run tests**

```bash
task test
```
Expected: all tests pass.

- [ ] **Step 4: Update docs**

```bash
grep -n "FrequenciesStream\|FrequenciesByStream" doc/operators.md doc/api-matrix.md
```

Replace all occurrences.

- [ ] **Step 5: Commit**

```bash
git add aggregate.go operator_test.go properties_test.go doc/operators.md doc/api-matrix.md
git commit -m "refactor: rename FrequenciesStream→RunningFrequencies, FrequenciesByStream→RunningFrequenciesBy"
```

---

## Task 6: compat.go — Remove dead code

Remove the three symbols that are not being relocated: `WindowByTime`, `ConsecutiveDedup`/`ConsecutiveDedupBy`, and `DeadLetter`/`DeadLetterSink`.

**Files:**
- Modify: `compat.go`
- Modify: `operator_test.go`

- [ ] **Step 1: Identify and remove `WindowByTime`**

```bash
grep -n "WindowByTime" compat.go operator_test.go
```

Delete the `WindowByTime` function from `compat.go` and any test functions named `TestWindowByTime*` from `operator_test.go`.

- [ ] **Step 2: Remove `ConsecutiveDedup` and `ConsecutiveDedupBy`**

```bash
grep -n "ConsecutiveDedup" compat.go operator_test.go
```

Delete both functions and their tests.

- [ ] **Step 3: Remove `DeadLetter` and `DeadLetterSink`**

```bash
grep -n "DeadLetter" compat.go operator_test.go
```

Delete both functions and their tests.

- [ ] **Step 4: Run tests**

```bash
task test
```
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add compat.go operator_test.go
git commit -m "refactor: remove WindowByTime, ConsecutiveDedup, DeadLetter from compat.go"
```

---

## Task 7: compat.go — Relocate `MapBatch`, `MapIntersperse`, `EndWith`, `Stage.Or`

**Files:**
- Modify: `compat.go` (remove each symbol after moving)
- Modify: `batch.go` (receive `MapBatch`)
- Modify: `misc.go` (receive `MapIntersperse`, `EndWith`)
- Modify: `pipeline.go` (receive `Stage.Or`)

- [ ] **Step 1: Move `MapBatch` to batch.go**

Find `MapBatch` in `compat.go`. Cut the function and its godoc comment. Paste it at the bottom of `batch.go`. Delete it from `compat.go`.

```bash
grep -n "MapBatch" compat.go batch.go
```
Expected: only in `batch.go` after the move.

- [ ] **Step 2: Move `MapIntersperse` and `EndWith` to misc.go**

Find both functions in `compat.go`. Cut them. Paste at the bottom of `misc.go`. Delete from `compat.go`.

```bash
grep -n "MapIntersperse\|EndWith" compat.go misc.go
```
Expected: only in `misc.go` after the move.

- [ ] **Step 3: Move `Stage.Or` to pipeline.go**

Find the `Or` method on `Stage[I,O]` in `compat.go`. Cut it. In `pipeline.go`, locate the `Stage[I,O]` type definition and paste the method directly after it.

```bash
grep -n "Stage\|func.*Or" compat.go pipeline.go
```
Expected: `Or` only in `pipeline.go` after the move.

- [ ] **Step 4: Run tests**

```bash
task test
```
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add compat.go batch.go misc.go pipeline.go
git commit -m "refactor: relocate MapBatch, MapIntersperse, EndWith, Stage.Or out of compat.go"
```

---

## Task 8: compat.go — Move and generalize `CountBy`/`SumBy`, then delete compat.go

**Files:**
- Modify: `compat.go` (remove `CountBy`, `SumBy`; then delete file)
- Modify: `aggregate.go` (add `RunningCountBy`, `RunningSumBy` with generic key)
- Modify: `operator_test.go` (update any tests)
- Modify: `doc/operators.md`
- Modify: `doc/api-matrix.md`

- [ ] **Step 1: Write the new generic versions in aggregate.go**

Add at the bottom of `aggregate.go`:

```go
// RunningCountBy emits a running count-per-key snapshot after each item.
// The map key type K is determined by keyFn. The emitted map is a new copy
// on each item — safe to retain across iterations.
func RunningCountBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[map[K]int64] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "running_count_by",
		name:   orDefault(cfg.name, "running_count_by"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan map[K]int64 {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan map[K]int64)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan map[K]int64, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()
			outbox := internal.NewBlockingOutbox(ch)
			counts := make(map[K]int64)
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					counts[keyFn(item)]++
					snapshot := make(map[K]int64, len(counts))
					for k, v := range counts {
						snapshot[k] = v
					}
					if err := outbox.Send(ctx, snapshot); err != nil {
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

// RunningSumBy emits a running sum-per-key snapshot after each item.
// The map key type K is determined by keyFn; values are summed using valueFn.
func RunningSumBy[T any, K comparable, V Numeric](p *Pipeline[T], keyFn func(T) K, valueFn func(T) V, opts ...StageOption) *Pipeline[map[K]V] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "running_sum_by",
		name:   orDefault(cfg.name, "running_sum_by"),
		buffer: cfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan map[K]V {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan map[K]V)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan map[K]V, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()
			outbox := internal.NewBlockingOutbox(ch)
			sums := make(map[K]V)
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					sums[keyFn(item)] += valueFn(item)
					snapshot := make(map[K]V, len(sums))
					for k, v := range sums {
						snapshot[k] = v
					}
					if err := outbox.Send(ctx, snapshot); err != nil {
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

- [ ] **Step 2: Write tests for RunningCountBy and RunningSumBy**

Add to `operator_test.go`:

```go
func TestRunningCountBy_Basic(t *testing.T) {
	ctx := context.Background()
	ch := kitsune.NewChannel[string](10)

	snapshots := make(chan map[string]int64, 10)
	done := make(chan error, 1)
	go func() {
		done <- kitsune.RunningCountBy(ch.Source(), func(s string) string { return s }).
			ForEach(func(_ context.Context, m map[string]int64) error {
				snapshots <- m
				return nil
			}).Run(ctx)
	}()

	for _, s := range []string{"a", "b", "a"} {
		if err := ch.Send(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	ch.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	var last map[string]int64
	for len(snapshots) > 0 {
		last = <-snapshots
	}
	if last["a"] != 2 || last["b"] != 1 {
		t.Errorf("final counts: got %v, want {a:2, b:1}", last)
	}
}

func TestRunningSumBy_Basic(t *testing.T) {
	ctx := context.Background()
	type item struct{ key string; val int }
	ch := kitsune.NewChannel[item](10)

	snapshots := make(chan map[string]int, 10)
	done := make(chan error, 1)
	go func() {
		done <- kitsune.RunningSumBy(ch.Source(),
			func(i item) string { return i.key },
			func(i item) int { return i.val },
		).ForEach(func(_ context.Context, m map[string]int) error {
			snapshots <- m
			return nil
		}).Run(ctx)
	}()

	for _, i := range []item{{"x", 10}, {"y", 5}, {"x", 3}} {
		if err := ch.Send(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	ch.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	var last map[string]int
	for len(snapshots) > 0 {
		last = <-snapshots
	}
	if last["x"] != 13 || last["y"] != 5 {
		t.Errorf("final sums: got %v, want {x:13, y:5}", last)
	}
}
```

- [ ] **Step 3: Run tests to verify new functions work**

```bash
task test
```
Expected: all tests pass including the two new ones.

- [ ] **Step 4: Remove `CountBy` and `SumBy` from compat.go**

```bash
grep -n "CountBy\|SumBy" compat.go
```

Delete both functions from `compat.go`. Update any existing tests that reference `kitsune.CountBy` or `kitsune.SumBy` to use `kitsune.RunningCountBy`/`kitsune.RunningSumBy`.

- [ ] **Step 5: Delete compat.go if now empty**

```bash
grep -c "^func " compat.go
```

If the count is 0 (no remaining exported functions), delete the file:

```bash
rm compat.go
```

If there are remaining functions, investigate — they should all have been handled in Tasks 6 and 7.

- [ ] **Step 6: Run tests**

```bash
task test:race
```
Expected: all tests pass with no race conditions.

- [ ] **Step 7: Update docs**

In `doc/operators.md` and `doc/api-matrix.md`: rename `CountBy`→`RunningCountBy`, `SumBy`→`RunningSumBy`. Update signatures to show the generic key type.

- [ ] **Step 8: Commit**

```bash
git add aggregate.go compat.go operator_test.go doc/operators.md doc/api-matrix.md
git commit -m "refactor: replace CountBy/SumBy with generic RunningCountBy/RunningSumBy, dissolve compat.go"
```

---

## Task 9: Final doc pass and verification

**Files:**
- Modify: `doc/operators.md`
- Modify: `doc/api-matrix.md`

- [ ] **Step 1: Verify no old names remain in source**

```bash
grep -rn "BroadcastN\|ElementAt\|SkipLast\|SkipUntil\|WithLatestFrom\|FrequenciesStream\|FrequenciesByStream\|ConsecutiveDedup\|DeadLetter\|WindowByTime\|CountBy\b\|SumBy\b\|Skip()\|func Lift\b" . --include="*.go"
```
Expected: no matches.

- [ ] **Step 2: Verify no old names remain in docs**

```bash
grep -rn "BroadcastN\|SkipLast\|SkipUntil\|WithLatestFrom\|FrequenciesStream\|ConsecutiveDedup\|DeadLetter\|WindowByTime\|CountBy\b\|SumBy\b" doc/
```
Expected: no matches.

- [ ] **Step 3: Run full test suite**

```bash
task test:all
```
Expected: all tests pass, including examples and property tests.

- [ ] **Step 4: Commit**

```bash
git add doc/operators.md doc/api-matrix.md
git commit -m "docs: update operators.md and api-matrix.md for cleanup rename pass"
```
