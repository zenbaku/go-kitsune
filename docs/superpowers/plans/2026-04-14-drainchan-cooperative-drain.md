# DrainChan Cooperative Drain — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace per-teardown `go internal.DrainChan(inCh)` goroutines with a cooperative drain protocol so that linear pipelines with early-exit operators (`Take`, `TakeWhile`) spawn zero drain goroutines at teardown.

**Architecture:** A `drainEntry` per producer stage holds a shared channel and an atomic ref-count. When every consumer of that stage exits, the ref-count hits zero and the channel is closed; the producer's select loop sees this and exits cleanly, cascading the signal upstream without any extra goroutines. This prototype converts the serial `Map` paths, `Take`/`TakeWhile`/`Drop`/`DropWhile`, and the serial `ForEach` paths. Concurrent and fast-path operators are listed in the roadmap follow-on item.

**Tech Stack:** Go standard library (`sync/atomic`, channel select), `runtime.NumGoroutine` for the goroutine-count test, `pgregory.net/rapid` is NOT used (no property tests for this feature).

---

## File Map

| File | Change |
|---|---|
| `pipeline.go` | Add `drainEntry` struct, `drainNotify` field on `runCtx`, and three methods: `initDrainNotify`, `signalDrain`, `drainCh` |
| `operator_map.go` | Rename `result` to `out`; add `drainFn`/`drainCh` params to `mapSerial` and `mapSerialFastPath`; init drain notify in `Map`'s `build` for serial paths only |
| `operator_take.go` | Convert `Take`, `TakeWhile`, `Drop`, `DropWhile`: use `var out` pointer, init drain notify, replace DrainChan defer with `signalDrain`, add `drainCh` to selects |
| `terminal.go` | Add `drainFn func()` param to `forEachFastPath` and `forEachSerial`; pass `rc.signalDrain(p.id)` closure from `ForEach` terminal builder for serial paths |
| `drain_test.go` | Add `TestCooperativeDrainGoroutineCount` |
| `drain_bench_test.go` | New file — `BenchmarkDrainBurst` |
| `doc/roadmap.md` | Mark DrainChan item `[x]`; add new `[ ]` item for full rollout |

---

## Task 1: Add drain infrastructure to `pipeline.go`

**Files:**
- Modify: `pipeline.go`

- [ ] **Step 1: Add the `drainEntry` struct and the `drainNotify` field**

  Open `pipeline.go`. After the closing brace of `runCtx` (line ~116), add the struct definition and a new field inside `runCtx`. The struct goes ABOVE `runCtx`; the field goes INSIDE it.

  Add this struct immediately before the `runCtx` type declaration:

  ```go
  // drainEntry coordinates the cooperative-drain protocol for one producer stage.
  // refs is decremented by each consumer that exits; when it reaches zero the
  // ch is closed, unblocking the producer's select loop.
  type drainEntry struct {
  	ch   chan struct{}
  	refs atomic.Int32
  }
  ```

  Inside `runCtx`, add after the `done / signalDone` pair:

  ```go
  // drainNotify maps a producer stage ID to its drain entry.
  // Populated during build(); consumers call signalDrain when they exit.
  drainNotify map[int64]*drainEntry
  ```

- [ ] **Step 2: Initialize `drainNotify` in `newRunCtx`**

  In `newRunCtx()`, add `drainNotify: make(map[int64]*drainEntry),` alongside the existing `chans` initialization:

  ```go
  return &runCtx{
  	chans:       make(map[int64]any),
  	drainNotify: make(map[int64]*drainEntry),
  	refs:        newRefRegistry(),
  	done:        done,
  	signalDone:  func() { once.Do(func() { close(done) }) },
  }
  ```

- [ ] **Step 3: Add the three drain methods**

  Add after `setChan`:

  ```go
  // initDrainNotify registers a drain entry for producerID with the given
  // consumer count. Call once per stage during build(). consumerCount must be
  // the value of Pipeline.consumerCount at Run time (after all track() calls).
  // Uses max(1, consumerCount) so a pipeline that is used as a dead-end still
  // receives a well-formed entry.
  func (rc *runCtx) initDrainNotify(producerID int64, consumerCount int32) {
  	e := &drainEntry{ch: make(chan struct{})}
  	n := consumerCount
  	if n < 1 {
  		n = 1
  	}
  	e.refs.Store(n)
  	rc.drainNotify[producerID] = e
  }

  // signalDrain decrements the ref count for producerID. When the count
  // reaches zero the drain channel is closed, waking the producer's select.
  // Safe to call even if producerID has no registered entry (no-op).
  func (rc *runCtx) signalDrain(producerID int64) {
  	if e, ok := rc.drainNotify[producerID]; ok {
  		if e.refs.Add(-1) == 0 {
  			close(e.ch)
  		}
  	}
  }

  // drainCh returns the drain-notification channel for id.
  // Returns nil when id has no registered entry; a nil channel in a select
  // blocks forever, which is the correct fallback for unconverted stages.
  func (rc *runCtx) drainCh(id int64) <-chan struct{} {
  	if e, ok := rc.drainNotify[id]; ok {
  		return e.ch
  	}
  	return nil
  }
  ```

- [ ] **Step 4: Verify existing tests still pass**

  ```
  task test
  ```

  Expected: all tests pass, no compilation errors.

- [ ] **Step 5: Commit**

  ```bash
  git add pipeline.go
  git commit -m "feat(drain): add cooperative-drain infrastructure to runCtx"
  ```

---

## Task 2: Write the failing goroutine-count test

**Files:**
- Modify: `drain_test.go`

- [ ] **Step 1: Add import and test**

  Open `drain_test.go`. Add `"runtime"` to the import block. Then append this test at the bottom of the file:

  ```go
  // TestCooperativeDrainGoroutineCount verifies that a linear Map→Take(1)→ForEach
  // pipeline tears down without spawning drain goroutines.
  // With the old DrainChan approach, each stage spawns one goroutine on teardown
  // (stages+1 goroutines above baseline). With cooperative drain, zero extra
  // goroutines should remain after Run returns.
  func TestCooperativeDrainGoroutineCount(t *testing.T) {
  	const stages = 10

  	// Allow any test-harness goroutines to settle before taking the baseline.
  	runtime.Gosched()
  	baseline := runtime.NumGoroutine()

  	p := kitsune.Repeatedly(func() int { return 1 })
  	for range stages {
  		p = kitsune.Map(p, func(_ context.Context, v int) (int, error) {
  			return v, nil
  		})
  	}

  	err := kitsune.Take(p, 1).ForEach(func(_ context.Context, _ int) error {
  		return nil
  	}).Run(context.Background())
  	if err != nil {
  		t.Fatalf("unexpected error: %v", err)
  	}

  	// Give any residual goroutines a scheduling window to exit.
  	runtime.Gosched()

  	after := runtime.NumGoroutine()
  	leaked := after - baseline
  	// Allow 1 for any test-framework background goroutine.
  	if leaked > 1 {
  		t.Errorf("goroutine burst: %d goroutines remain above baseline after teardown (baseline=%d after=%d); expected ≤1",
  			leaked, baseline, after)
  	}
  }
  ```

- [ ] **Step 2: Run the test and confirm it fails**

  ```
  go test -run TestCooperativeDrainGoroutineCount -v ./...
  ```

  Expected: `FAIL` — the test reports goroutines above baseline because Map and ForEach still use `go DrainChan`.

- [ ] **Step 3: Commit the failing test**

  ```bash
  git add drain_test.go
  git commit -m "test(drain): add goroutine-count assertion for cooperative drain"
  ```

---

## Task 3: Convert `ForEach` terminal serial and fast paths

**Files:**
- Modify: `terminal.go`

The ForEach terminal is the first link to convert because it initiates the drain signal for its upstream. Terminals have no output channel, so only the `drainFn` changes — no `drainCh` is needed.

- [ ] **Step 1: Update `forEachFastPath` signature and body**

  Current signature:

  ```go
  func forEachFastPath[T any](inCh chan T, fn func(context.Context, T) error) stageFunc {
  ```

  New signature and defer:

  ```go
  func forEachFastPath[T any](inCh chan T, fn func(context.Context, T) error, drainFn func()) stageFunc {
  	return func(ctx context.Context) error {
  		defer drainFn() // was: go internal.DrainChan((<-chan T)(inCh))
  		// ... rest of body unchanged ...
  ```

  Remove the old `defer func() { go internal.DrainChan((<-chan T)(inCh)) }()` line.

- [ ] **Step 2: Update `forEachSerial` signature and body**

  Current signature:

  ```go
  func forEachSerial[T any](inCh chan T, fn func(context.Context, T) error, cfg stageConfig, hook internal.Hook) stageFunc {
  ```

  New signature and defer:

  ```go
  func forEachSerial[T any](inCh chan T, fn func(context.Context, T) error, cfg stageConfig, hook internal.Hook, drainFn func()) stageFunc {
  	// ...
  	return func(ctx context.Context) error {
  		defer drainFn() // was: go internal.DrainChan((<-chan T)(inCh))
  		// ... rest of body unchanged ...
  ```

  Remove the old DrainChan defer line.

- [ ] **Step 3: Update the `ForEach` terminal builder to pass `drainFn`**

  In `(p *Pipeline[T]) ForEach`, inside the `terminal` closure, locate where `inCh` is assigned and stage is selected. Add `drainFn` construction and pass it to the converted paths. Leave concurrent/ordered paths unchanged:

  ```go
  inCh := p.build(rc)
  drainFn := func() { rc.signalDrain(p.id) }

  var stage stageFunc
  switch {
  case n > 1 && cfg.ordered:
  	stage = forEachOrdered(inCh, fn, cfg, hook) // not converted: uses DrainChan internally
  case n > 1:
  	stage = forEachConcurrent(inCh, fn, cfg, hook) // not converted
  case isFastPathEligible(cfg, hook):
  	stage = forEachFastPath(inCh, fn, drainFn)
  default:
  	stage = forEachSerial(inCh, fn, cfg, hook, drainFn)
  }
  ```

- [ ] **Step 4: Run tests**

  ```
  task test
  ```

  Expected: all existing tests pass. The goroutine-count test still fails (Map is not converted yet).

- [ ] **Step 5: Commit**

  ```bash
  git add terminal.go
  git commit -m "feat(drain): convert ForEach serial/fast paths to cooperative drain"
  ```

---

## Task 4: Convert `Take`, `TakeWhile`, `Drop`, `DropWhile`

**Files:**
- Modify: `operator_take.go`

These operators initiate drain signals toward their upstream AND need to respond to drain signals from their downstream (if ForEach signals them). The pattern for each is identical: capture the returned pipeline pointer, initialize drain notify, replace DrainChan defer, add `drainCh` to selects.

- [ ] **Step 1: Convert `Take`**

  Find `Take`'s `build` closure. Add `var out *Pipeline[T]` before the closure, change `return newPipeline(...)` to `out = newPipeline(...)`, and update the stage function:

  ```go
  var out *Pipeline[T]
  build := func(rc *runCtx) chan T {
  	if existing := rc.getChan(id); existing != nil {
  		return existing.(chan T)
  	}
  	inCh := p.build(rc)
  	buf := rc.defaultBufSize()
  	ch := make(chan T, buf)
  	m := meta
  	m.buffer = buf
  	m.getChanLen = func() int { return len(ch) }
  	m.getChanCap = func() int { return cap(ch) }
  	rc.setChan(id, ch)
  	rc.initDrainNotify(id, out.consumerCount.Load())
  	drainCh := rc.drainCh(id)
  	drainFn := func() { rc.signalDrain(p.id) }
  	signalDone := rc.signalDone
  	stage := func(ctx context.Context) error {
  		defer close(ch)
  		defer drainFn() // was: go internal.DrainChan(inCh)
  		defer signalDone()

  		count := 0
  		for {
  			if count >= n {
  				return nil
  			}
  			select {
  			case item, ok := <-inCh:
  				if !ok {
  					return nil
  				}
  				select {
  				case ch <- item:
  					count++
  				case <-ctx.Done():
  					return ctx.Err()
  				case <-drainCh:
  					return nil
  				}
  			case <-ctx.Done():
  				return ctx.Err()
  			case <-drainCh:
  				return nil
  			}
  		}
  	}
  	rc.add(stage, m)
  	return ch
  }
  out = newPipeline(id, meta, build)
  return out
  ```

- [ ] **Step 2: Convert `TakeWhile`**

  Same pattern as Take. The stage function becomes:

  ```go
  var out *Pipeline[T]
  build := func(rc *runCtx) chan T {
  	if existing := rc.getChan(id); existing != nil {
  		return existing.(chan T)
  	}
  	inCh := p.build(rc)
  	buf := rc.defaultBufSize()
  	ch := make(chan T, buf)
  	m := meta
  	m.buffer = buf
  	m.getChanLen = func() int { return len(ch) }
  	m.getChanCap = func() int { return cap(ch) }
  	rc.setChan(id, ch)
  	rc.initDrainNotify(id, out.consumerCount.Load())
  	drainCh := rc.drainCh(id)
  	drainFn := func() { rc.signalDrain(p.id) }
  	signalDone := rc.signalDone
  	stage := func(ctx context.Context) error {
  		defer close(ch)
  		defer drainFn() // was: go internal.DrainChan(inCh)
  		defer signalDone()

  		for {
  			select {
  			case item, ok := <-inCh:
  				if !ok {
  					return nil
  				}
  				if !pred(item) {
  					return nil
  				}
  				select {
  				case ch <- item:
  				case <-ctx.Done():
  					return ctx.Err()
  				case <-drainCh:
  					return nil
  				}
  			case <-ctx.Done():
  				return ctx.Err()
  			case <-drainCh:
  				return nil
  			}
  		}
  	}
  	rc.add(stage, m)
  	return ch
  }
  out = newPipeline(id, meta, build)
  return out
  ```

- [ ] **Step 3: Convert `Drop`**

  `Drop` does NOT call `signalDone` (it forwards all items after dropping the first n). The drain pattern is the same otherwise:

  ```go
  var out *Pipeline[T]
  build := func(rc *runCtx) chan T {
  	if existing := rc.getChan(id); existing != nil {
  		return existing.(chan T)
  	}
  	inCh := p.build(rc)
  	buf := rc.defaultBufSize()
  	ch := make(chan T, buf)
  	m := meta
  	m.buffer = buf
  	m.getChanLen = func() int { return len(ch) }
  	m.getChanCap = func() int { return cap(ch) }
  	rc.setChan(id, ch)
  	rc.initDrainNotify(id, out.consumerCount.Load())
  	drainCh := rc.drainCh(id)
  	drainFn := func() { rc.signalDrain(p.id) }
  	stage := func(ctx context.Context) error {
  		defer close(ch)
  		defer drainFn() // was: go internal.DrainChan(inCh)

  		count := 0
  		for {
  			select {
  			case item, ok := <-inCh:
  				if !ok {
  					return nil
  				}
  				if count < n {
  					count++
  					continue
  				}
  				select {
  				case ch <- item:
  				case <-ctx.Done():
  					return ctx.Err()
  				case <-drainCh:
  					return nil
  				}
  			case <-ctx.Done():
  				return ctx.Err()
  			case <-drainCh:
  				return nil
  			}
  		}
  	}
  	rc.add(stage, m)
  	return ch
  }
  out = newPipeline(id, meta, build)
  return out
  ```

- [ ] **Step 4: Convert `DropWhile`**

  ```go
  var out *Pipeline[T]
  build := func(rc *runCtx) chan T {
  	if existing := rc.getChan(id); existing != nil {
  		return existing.(chan T)
  	}
  	inCh := p.build(rc)
  	buf := rc.defaultBufSize()
  	ch := make(chan T, buf)
  	m := meta
  	m.buffer = buf
  	m.getChanLen = func() int { return len(ch) }
  	m.getChanCap = func() int { return cap(ch) }
  	rc.setChan(id, ch)
  	rc.initDrainNotify(id, out.consumerCount.Load())
  	drainCh := rc.drainCh(id)
  	drainFn := func() { rc.signalDrain(p.id) }
  	signalDone := rc.signalDone
  	stage := func(ctx context.Context) error {
  		defer close(ch)
  		defer drainFn() // was: go internal.DrainChan(inCh)
  		defer signalDone()

  		dropping := true
  		for {
  			select {
  			case item, ok := <-inCh:
  				if !ok {
  					return nil
  				}
  				if dropping && pred(item) {
  					continue
  				}
  				dropping = false
  				select {
  				case ch <- item:
  				case <-ctx.Done():
  					return ctx.Err()
  				case <-drainCh:
  					return nil
  				}
  			case <-ctx.Done():
  				return ctx.Err()
  			case <-drainCh:
  				return nil
  			}
  		}
  	}
  	rc.add(stage, m)
  	return ch
  }
  out = newPipeline(id, meta, build)
  return out
  ```

  Note: `DropWhile` calls `signalDone` because it is logically an early-exit operator (items before the predicate becomes false are silently discarded and the pipeline effectively starts mid-stream). If your reading of the original code shows it does NOT call `signalDone`, omit that line.

- [ ] **Step 5: Run tests**

  ```
  task test:race
  ```

  Expected: all tests pass, no races. The goroutine-count test still fails (Map is not converted).

- [ ] **Step 6: Commit**

  ```bash
  git add operator_take.go
  git commit -m "feat(drain): convert Take/TakeWhile/Drop/DropWhile to cooperative drain"
  ```

---

## Task 5: Convert `Map` serial paths

**Files:**
- Modify: `operator_map.go`

This is the most involved task. `Map` has multiple paths: serial, serial-fast-path, concurrent, and ordered. Only the serial paths are converted; concurrent and ordered retain their existing DrainChan behavior.

- [ ] **Step 1: Update `mapSerial` signature and body**

  Current signature:

  ```go
  func mapSerial[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), cfg stageConfig, hook internal.Hook) stageFunc {
  ```

  New signature:

  ```go
  func mapSerial[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), cfg stageConfig, hook internal.Hook, drainFn func(), drainCh <-chan struct{}) stageFunc {
  ```

  Inside the returned `func(ctx context.Context) error`:

  ```go
  defer drainFn() // was: go internal.DrainChan(inCh)
  ```

  In the `inner` function's select loop, add `drainCh` to the outer case AND a pre-flight check before the outbox send:

  ```go
  inner := func() error {
  	for {
  		select {
  		case item, ok := <-inCh:
  			if !ok {
  				return nil
  			}
  			itemCtx, cancelItem := itemContext(ctx, cfg)
  			start := time.Now()
  			val, err, attempt := internal.ProcessItem(itemCtx, fn, item, cfg.errorHandler, ctxMapper)
  			dur := time.Since(start)
  			cancelItem()
  			if err == internal.ErrSkipped {
  				errs++
  				hook.OnItem(ctx, cfg.name, dur, err)
  				continue
  			}
  			if err != nil {
  				errs++
  				hook.OnItem(ctx, cfg.name, dur, err)
  				return internal.WrapStageErr(cfg.name, err, attempt)
  			}
  			processed++
  			hook.OnItem(ctx, cfg.name, dur, nil)
  			// Pre-flight drain check: if downstream is already gone, exit without
  			// blocking on the outbox send. Note: a race remains if drainCh fires
  			// after this check but before outbox.Send; full resolution requires
  			// outbox changes tracked in the roadmap follow-on item.
  			select {
  			case <-drainCh:
  				return nil
  			default:
  			}
  			if err := outbox.Send(ctx, val); err != nil {
  				return err
  			}
  		case <-ctx.Done():
  			return ctx.Err()
  		case <-drainCh:
  			return nil
  		}
  	}
  }
  ```

- [ ] **Step 2: Update `mapSerialFastPath` signature and body**

  Current signature:

  ```go
  func mapSerialFastPath[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), name string) stageFunc {
  ```

  New signature:

  ```go
  func mapSerialFastPath[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), name string, drainFn func(), drainCh <-chan struct{}) stageFunc {
  ```

  Inside the returned function:

  ```go
  defer drainFn() // was: go internal.DrainChan(inCh)
  ```

  Replace the plain send `outCh <- result` with a select that includes `drainCh`:

  ```go
  select {
  case outCh <- result:
  case <-drainCh:
  	return nil
  }
  ```

- [ ] **Step 3: Update `Map`'s `build` closure**

  Rename the `result` local variable to `out` and capture it via pointer so `build` can read `out.consumerCount` at run time. Only the serial paths get drain notify initialized; concurrent/ordered paths keep their existing behavior.

  Before the `build` closure:

  ```go
  var out *Pipeline[O]
  ```

  Inside `build`, after `rc.setChan(id, ch)` and the cache-wrapping block, add the drain setup for the two serial paths:

  ```go
  var stage stageFunc
  switch {
  case cfg.concurrency > 1 && cfg.ordered:
  	stage = mapOrdered(inCh, ch, actualFn, cfg, hook) // not converted
  case cfg.concurrency > 1:
  	stage = mapConcurrent(inCh, ch, actualFn, cfg, hook) // not converted
  case isFastPathEligible(cfg, hook) && cfg.cacheConfig == nil:
  	rc.initDrainNotify(id, out.consumerCount.Load())
  	drainFn := func() { rc.signalDrain(p.id) }
  	drainCh := rc.drainCh(id)
  	stage = mapSerialFastPath(inCh, ch, actualFn, cfg.name, drainFn, drainCh)
  default:
  	rc.initDrainNotify(id, out.consumerCount.Load())
  	drainFn := func() { rc.signalDrain(p.id) }
  	drainCh := rc.drainCh(id)
  	stage = mapSerial(inCh, ch, actualFn, cfg, hook, drainFn, drainCh)
  }
  ```

  After the closure:

  ```go
  out = newPipeline(id, meta, build)
  ```

  In the `fastPathCfg` block that sets `fusionEntry`, replace every reference to `result` with `out`:

  ```go
  if fastPathCfg {
  	meta.hasFusionEntry = true
  	meta.getConsumerCount = func() int32 { return out.consumerCount.Load() }
  	// ... fusionEntry closure already uses result (captured); update to out ...
  	out.fusionEntry = func(rc *runCtx, sink func(context.Context, O) error) stageFunc {
  		// ... unchanged body ...
  	}
  }
  return out
  ```

- [ ] **Step 4: Run the goroutine-count test**

  ```
  go test -run TestCooperativeDrainGoroutineCount -v .
  ```

  Expected: `PASS` — goroutines above baseline is 0 or 1.

- [ ] **Step 5: Run the full test suite with race detector**

  ```
  task test:race
  ```

  Expected: all tests pass, no races.

- [ ] **Step 6: Commit**

  ```bash
  git add operator_map.go
  git commit -m "feat(drain): convert Map serial paths to cooperative drain"
  ```

---

## Task 6: Add the benchmark

**Files:**
- Create: `drain_bench_test.go`

- [ ] **Step 1: Create the benchmark file**

  ```go
  package kitsune_test

  import (
  	"context"
  	"testing"

  	kitsune "github.com/zenbaku/go-kitsune"
  )

  // BenchmarkDrainBurst measures the cost of a full pipeline run+teardown cycle
  // for a linear Repeatedly→Map(×20)→Take(1)→ForEach pipeline.
  // With cooperative drain, teardown spawns zero additional goroutines.
  // Compare against testdata/bench/baseline.txt using benchstat.
  func BenchmarkDrainBurst(b *testing.B) {
  	const stages = 20

  	b.ReportAllocs()
  	for b.N > 0 {
  		b.N--
  		p := kitsune.Repeatedly(func() int { return 1 })
  		for range stages {
  			p = kitsune.Map(p, func(_ context.Context, v int) (int, error) {
  				return v, nil
  			})
  		}
  		_ = kitsune.Take(p, 1).ForEach(func(_ context.Context, _ int) error {
  			return nil
  		}).Run(context.Background())
  	}
  }
  ```

- [ ] **Step 2: Run the benchmark and record output**

  ```
  go test -bench BenchmarkDrainBurst -benchmem -count=6 . 2>&1 | tee /tmp/drain_bench.txt
  cat /tmp/drain_bench.txt
  ```

  Note the ns/op and allocs/op in your response for comparison against future runs.

- [ ] **Step 3: Commit**

  ```bash
  git add drain_bench_test.go
  git commit -m "bench(drain): add BenchmarkDrainBurst for teardown goroutine pressure"
  ```

---

## Task 7: Update the roadmap

**Files:**
- Modify: `doc/roadmap.md`

- [ ] **Step 1: Mark the existing DrainChan item done**

  Find the line starting with `- [ ] **\`DrainChan\` goroutine burst on mass teardown**` and change `[ ]` to `[x]`.

- [ ] **Step 2: Add the follow-on rollout item**

  Immediately after the now-checked DrainChan item, insert a new unchecked item:

  ```markdown
  - [ ] **Full cooperative-drain rollout**: The prototype converted `Map` (serial paths), `Filter` (serial path — TBD), `Take`, `TakeWhile`, `Drop`, `DropWhile`, and the serial `ForEach` paths. The following operators still use `go internal.DrainChan(inCh)` and must be converted before cooperative drain is library-wide: `Map` concurrent/ordered variants; `mapSerialFastPath` fused path; `Filter` (all paths); `Batch`, `BufferWith`, `ChunkBy`, `ChunkWhile`, `SlidingWindow`, `SessionWindow` (`batch.go`); `FlatMap`, `ConcatMap` (`operator_flatmap.go`); `Merge`, `Amb`, `Zip`, `CombineLatest`, `WithLatestFrom`; `TakeUntil`, `SkipUntil`; `GroupByStream`, `Balance`, `Partition`, `Broadcast`; `MapWith`, `MapWithKey`, `FlatMapWith`, `FlatMapWithKey`; `Scan`, `Reduce`, `Aggregate`; `LookupBy`, `Enrich`; all source operators (`Ticker`, `Repeatedly`, `Generate`, `FromSlice`, etc.). Multi-input and fan-out operators require reference counting in `signalDrain` before the drain channel can be safely closed.
  ```

- [ ] **Step 3: Run tests to confirm no regressions**

  ```
  task test
  ```

  Expected: all tests pass.

- [ ] **Step 4: Commit**

  ```bash
  git add doc/roadmap.md
  git commit -m "docs(roadmap): mark DrainChan prototype done; add full-rollout follow-on item"
  ```

---

## Self-review

**Spec coverage:**
- [x] `drainEntry` struct + `drainNotify` map + three methods (Task 1)
- [x] `consumerCount` read via captured `out` pointer (Tasks 3–5)
- [x] `signalDrain` instead of DrainChan defer, `drainCh` in selects (Tasks 3–5)
- [x] Terminals use `drainFn` only, no `drainCh` (Task 3)
- [x] Multi-consumer/concurrent paths retain DrainChan (Tasks 3, 5)
- [x] Goroutine-count test (Task 2)
- [x] Benchmark (Task 6)
- [x] Roadmap updated with follow-on item listing all unconverted operators (Task 7)

**Known prototype limitation documented in both spec and roadmap:** the pre-flight drain check in `mapSerial` before `outbox.Send` has a narrow race where drainCh fires mid-send; full elimination requires adding drainCh to `blockingOutbox.Send`, which is part of the full-rollout work.
