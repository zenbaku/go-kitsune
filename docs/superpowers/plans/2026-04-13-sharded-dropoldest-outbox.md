# Sharded DropOldest Outbox Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate cross-worker mutex contention in `dropOldestOutbox` when both `Overflow(DropOldest)` and `Concurrency(n > 1)` are active. Workers will be partitioned across independent shards (worker `i` uses shard `i % n`), so each shard only serialises its own goroutine's drain-and-resend slow path; the fast path (buffer has space) is already lock-free and stays that way.

**Architecture:** A new `dropOldestShard[T]` type lives alongside the existing `dropOldestOutbox[T]` in `internal/outbox.go`. It has the same `Send` logic but takes a shared `*atomic.Int64` dropped counter (so `Outbox.Dropped()` reports the aggregate across shards). A factory `NewShardedDropOldestOutbox[T](ch, n, hook, name) []Outbox[T]` returns `n` shards that share the same underlying channel and counter. The concurrent operators (`mapConcurrent`, `flatMapConcurrent`) pick a shard per spawned worker using a dispatcher-local `workerIdx` counter; each spawned goroutine captures its shard at spawn time. Activation is gated on `cfg.overflow == OverflowDropOldest && cfg.concurrency > 1` so single-worker and non-DropOldest pipelines take the existing `NewOutbox` path unchanged. `mapOrdered` and `flatMapOrdered` are not touched: their drainer goroutine is the sole sender and has no cross-worker contention.

**Tech Stack:** Go 1.22+, `sync.Mutex`, `sync/atomic`, existing `internal/outbox.go` helpers, `pgregory.net/rapid` not required for this change (race detector is the primary safety net).

---

### Task 1: Add `dropOldestShard[T]` and `NewShardedDropOldestOutbox` with TDD

**Files:**
- Create: `/Users/jonathan/projects/go-kitsune/internal/outbox_test.go`
- Modify: `/Users/jonathan/projects/go-kitsune/internal/outbox.go` (append after line 126, update factory section ending line 147)

- [ ] Create `/Users/jonathan/projects/go-kitsune/internal/outbox_test.go` with the following content (this test MUST fail first since `NewShardedDropOldestOutbox` does not yet exist):

```go
package internal

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// TestShardedDropOldestOutboxBasic verifies that a single shard behaves like
// the non-sharded dropOldestOutbox: the fast path sends without blocking, and
// the slow path evicts the oldest buffered item when full.
func TestShardedDropOldestOutboxBasic(t *testing.T) {
	ch := make(chan int, 2)
	boxes := NewShardedDropOldestOutbox[int](ch, 1, nil, "t")
	if len(boxes) != 1 {
		t.Fatalf("want 1 shard, got %d", len(boxes))
	}
	ctx := context.Background()
	// Fill the buffer.
	if err := boxes[0].Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := boxes[0].Send(ctx, 2); err != nil {
		t.Fatal(err)
	}
	// Third send must evict 1 and enqueue 3.
	if err := boxes[0].Send(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if got := <-ch; got != 2 {
		t.Fatalf("want 2 (oldest after eviction), got %d", got)
	}
	if got := <-ch; got != 3 {
		t.Fatalf("want 3 (newest), got %d", got)
	}
	if boxes[0].Dropped() != 1 {
		t.Fatalf("want 1 drop, got %d", boxes[0].Dropped())
	}
}

// TestShardedDropOldestOutboxSharedCounter verifies that drops from different
// shards accumulate in the shared counter reported by Dropped() on any shard.
func TestShardedDropOldestOutboxSharedCounter(t *testing.T) {
	ch := make(chan int, 1)
	boxes := NewShardedDropOldestOutbox[int](ch, 2, nil, "t")
	if len(boxes) != 2 {
		t.Fatalf("want 2 shards, got %d", len(boxes))
	}
	ctx := context.Background()
	// Fill the shared channel via shard 0.
	if err := boxes[0].Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
	// Shard 1 must evict (buffer full) and then report the drop in the
	// shared counter.
	if err := boxes[1].Send(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if got := <-ch; got != 2 {
		t.Fatalf("want 2 (newest), got %d", got)
	}
	// Both shards must see the same drop count because they share the counter.
	if boxes[0].Dropped() != 1 {
		t.Fatalf("shard 0: want 1 drop, got %d", boxes[0].Dropped())
	}
	if boxes[1].Dropped() != 1 {
		t.Fatalf("shard 1: want 1 drop, got %d", boxes[1].Dropped())
	}
}

// TestShardedDropOldestOutboxIndependentMutexes verifies that a contended
// drain on one shard does not block another shard's send. Run with -race to
// catch ordering bugs.
func TestShardedDropOldestOutboxIndependentMutexes(t *testing.T) {
	ch := make(chan int, 4)
	boxes := NewShardedDropOldestOutbox[int](ch, 4, nil, "t")
	ctx := context.Background()

	const perShard = 200
	var wg sync.WaitGroup
	var totalSends atomic.Int64
	for i := 0; i < len(boxes); i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < perShard; j++ {
				if err := boxes[idx].Send(ctx, idx*perShard+j); err != nil {
					t.Errorf("shard %d send: %v", idx, err)
					return
				}
				totalSends.Add(1)
			}
		}(i)
	}

	// Drain in parallel so senders can make progress.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		count := 0
		for range ch {
			count++
			if count == perShard*4 {
				return
			}
		}
	}()

	wg.Wait()
	close(ch)
	<-drained

	if totalSends.Load() != int64(len(boxes))*perShard {
		t.Fatalf("want %d sends, got %d", int64(len(boxes))*perShard, totalSends.Load())
	}
}
```

- [ ] Run: `cd /Users/jonathan/projects/go-kitsune && go test ./internal/ -run TestShardedDropOldestOutbox -v`  Expected: compile error / undefined `NewShardedDropOldestOutbox` (this is the RED step of TDD).

- [ ] Modify `/Users/jonathan/projects/go-kitsune/internal/outbox.go`: add the new shard type and factory by replacing the factory section (lines 128-147). The final file should end with:

```go
// ---------------------------------------------------------------------------
// DropOldest — sharded variant for concurrent workers
// ---------------------------------------------------------------------------

// dropOldestShard is one shard of a sharded DropOldest outbox. Each shard owns
// its own sync.Mutex so concurrent senders routed to different shards never
// contend on the slow path. All shards share the same underlying channel and
// a single atomic dropped counter, so Dropped() reports the aggregate across
// shards regardless of which shard is queried.
//
// Send logic is identical to dropOldestOutbox: fast path is a lock-free
// non-blocking send; slow path holds this shard's mutex while evicting the
// oldest buffered item and enqueueing the new one.
type dropOldestShard[T any] struct {
	ch      chan T
	mu      sync.Mutex
	hook    Hook
	name    string
	dropped *atomic.Int64 // shared across all shards
}

func (o *dropOldestShard[T]) Send(ctx context.Context, item T) error {
	// Fast path: buffer has space — no lock needed.
	select {
	case o.ch <- item:
		return nil
	default:
	}

	// Slow path: buffer is full — hold this shard's lock while we drain + resend.
	o.mu.Lock()
	defer o.mu.Unlock()

	select {
	case o.ch <- item:
		// A concurrent drain (by a downstream reader or a sibling shard) freed
		// space between the fast-path check and us acquiring the lock.
		return nil
	default:
	}

	// Evict the oldest item.
	select {
	case old := <-o.ch:
		o.dropped.Add(1)
		if oh, ok := o.hook.(OverflowHook); ok {
			oh.OnDrop(ctx, o.name, old)
		}
	default:
		// Defensive: the channel appeared full but is now empty. A sibling
		// shard or downstream reader drained it. Fall through to the send.
	}

	// Now there should be space. A sibling shard holding its own lock may
	// race to fill the slot; if so, drop this item rather than block.
	select {
	case o.ch <- item:
	default:
		o.dropped.Add(1)
		if oh, ok := o.hook.(OverflowHook); ok {
			oh.OnDrop(ctx, o.name, item)
		}
	}
	return nil
}

func (o *dropOldestShard[T]) Dropped() int64 { return o.dropped.Load() }

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

// NewBlockingOutbox creates a blocking outbox backed by ch.
func NewBlockingOutbox[T any](ch chan T) Outbox[T] {
	return &blockingOutbox[T]{ch: ch}
}

// NewOutbox creates the appropriate Outbox for the given overflow strategy.
// For concurrent DropOldest stages, prefer NewShardedDropOldestOutbox which
// eliminates cross-worker contention on the slow path.
func NewOutbox[T any](ch chan T, overflow Overflow, hook Hook, name string) Outbox[T] {
	switch overflow {
	case OverflowDropNewest:
		return &dropNewestOutbox[T]{ch: ch, hook: hook, name: name}
	case OverflowDropOldest:
		return &dropOldestOutbox[T]{ch: ch, hook: hook, name: name}
	default:
		return &blockingOutbox[T]{ch: ch}
	}
}

// NewShardedDropOldestOutbox creates n DropOldest shards that share the same
// underlying channel and a single atomic dropped counter. Each shard has its
// own sync.Mutex, so workers routed to different shards never contend on the
// slow path.
//
// Callers must route each worker to exactly one shard (typically by
// worker-index modulo n). n must be >= 1; if n < 1 a single shard is returned.
func NewShardedDropOldestOutbox[T any](ch chan T, n int, hook Hook, name string) []Outbox[T] {
	if n < 1 {
		n = 1
	}
	dropped := new(atomic.Int64)
	boxes := make([]Outbox[T], n)
	for i := 0; i < n; i++ {
		boxes[i] = &dropOldestShard[T]{
			ch:      ch,
			hook:    hook,
			name:    name,
			dropped: dropped,
		}
	}
	return boxes
}
```

- [ ] Run: `cd /Users/jonathan/projects/go-kitsune && go test ./internal/ -run TestShardedDropOldestOutbox -v`  Expected: `PASS` for all three sub-tests.

- [ ] Run: `cd /Users/jonathan/projects/go-kitsune && go test ./internal/ -race -run TestShardedDropOldestOutbox`  Expected: `PASS`, no race reports.

- [ ] Run: `cd /Users/jonathan/projects/go-kitsune && go vet ./internal/`  Expected: no output.

- [ ] Commit: `git add internal/outbox.go internal/outbox_test.go && git commit -m "feat(internal): add sharded DropOldest outbox primitive"` (write the message via a HEREDOC so it contains a one-line body explaining per-shard mutex + shared counter semantics and ends with the Co-Authored-By trailer).

---

### Task 2: Route `mapConcurrent` workers across DropOldest shards

**Files:**
- Modify: `/Users/jonathan/projects/go-kitsune/operator_map.go:294-377`

- [ ] Replace the body of `mapConcurrent` so that when `cfg.overflow == internal.OverflowDropOldest && cfg.concurrency > 1`, the dispatcher allocates `cfg.concurrency` shards and hands each spawned goroutine its own shard by worker index. All other overflow modes (and `concurrency == 1`) continue through `internal.NewOutbox`. The replacement function is:

```go
func mapConcurrent[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), cfg stageConfig, hook internal.Hook) stageFunc {
	var ctxMapper func(I) context.Context
	if raw := cfg.contextMapperFn; raw != nil {
		ctxMapper = raw.(func(I) context.Context)
	}
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			// When DropOldest and Concurrency > 1 are both active, shard the
			// outbox so workers do not contend on a single mutex under
			// sustained backpressure. Each spawned goroutine captures its own
			// shard at spawn time; the dispatcher is the sole writer of
			// workerIdx so no locking is required here.
			var boxes []internal.Outbox[O]
			if cfg.overflow == internal.OverflowDropOldest && cfg.concurrency > 1 {
				boxes = internal.NewShardedDropOldestOutbox(outCh, cfg.concurrency, hook, cfg.name)
			} else {
				boxes = []internal.Outbox[O]{internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)}
			}
			workerIdx := 0

			sem := make(chan struct{}, cfg.concurrency)
			errCh := make(chan error, 1)
			var wg sync.WaitGroup

			func() {
				for {
					select {
					case item, ok := <-inCh:
						if !ok {
							return
						}
						// Use innerCtx.Done() only — reading errCh here would drain it
						// before the final select below can return it to Supervise.
						select {
						case sem <- struct{}{}:
						case <-innerCtx.Done():
							return
						}
						outbox := boxes[workerIdx%len(boxes)]
						workerIdx++
						wg.Add(1)
						go func(it I, outbox internal.Outbox[O]) {
							defer wg.Done()
							defer func() { <-sem }()

							itemCtx, cancelItem := itemContext(innerCtx, cfg)
							start := time.Now()
							val, err, attempt := internal.ProcessItem(itemCtx, fn, it, cfg.errorHandler, ctxMapper)
							dur := time.Since(start)
							cancelItem()
							if err == internal.ErrSkipped {
								errCount.Add(1)
								hook.OnItem(ctx, cfg.name, dur, err)
								return
							}
							if err != nil {
								errCount.Add(1)
								hook.OnItem(ctx, cfg.name, dur, err)
								reportErr(errCh, internal.WrapStageErr(cfg.name, err, attempt))
								cancel()
								return
							}
							procCount.Add(1)
							hook.OnItem(ctx, cfg.name, dur, nil)
							if sendErr := outbox.Send(innerCtx, val); sendErr != nil {
								reportErr(errCh, sendErr)
								cancel()
							}
						}(item, outbox)
					case <-innerCtx.Done():
						return
					}
				}
			}()

			wg.Wait()

			select {
			case err := <-errCh:
				return err
			default:
				return nil
			}
		}

		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}
```

- [ ] Run: `cd /Users/jonathan/projects/go-kitsune && go build ./...`  Expected: no output.

- [ ] Run: `cd /Users/jonathan/projects/go-kitsune && go test ./... -run TestOverflowDropOldest -v`  Expected: `PASS` for `TestOverflowDropOldest` and `TestOverflowDropOldestConcurrent`.

- [ ] Run: `cd /Users/jonathan/projects/go-kitsune && go test ./... -race -run TestOverflow`  Expected: `PASS`, no race reports.

- [ ] Commit: `git add operator_map.go && git commit -m "perf(map): route concurrent DropOldest through sharded outbox"` (HEREDOC body: "Workers now own a shard each; no cross-worker mutex under sustained backpressure." + Co-Authored-By trailer).

---

### Task 3: Route `flatMapConcurrent` workers across DropOldest shards

**Files:**
- Modify: `/Users/jonathan/projects/go-kitsune/operator_flatmap.go:174-250`

- [ ] Replace the body of `flatMapConcurrent` with the following (same shard pattern; the goroutine closure now receives `outbox` as a second parameter and `send` captures it):

```go
// flatMapConcurrent does not support supervision (Supervise stage option is silently
// ignored when Concurrency > 1). Use Concurrency(1) to enable supervision on FlatMap.
func flatMapConcurrent[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I, func(O) error) error, cfg stageConfig, hook internal.Hook) stageFunc {
	var ctxMapper func(I) context.Context
	if raw := cfg.contextMapperFn; raw != nil {
		ctxMapper = raw.(func(I) context.Context)
	}
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			// When DropOldest and Concurrency > 1 are both active, shard the
			// outbox so workers do not contend on a single mutex under
			// sustained backpressure. See mapConcurrent for the same pattern.
			var boxes []internal.Outbox[O]
			if cfg.overflow == internal.OverflowDropOldest && cfg.concurrency > 1 {
				boxes = internal.NewShardedDropOldestOutbox(outCh, cfg.concurrency, hook, cfg.name)
			} else {
				boxes = []internal.Outbox[O]{internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)}
			}
			workerIdx := 0

			sem := make(chan struct{}, cfg.concurrency)
			errCh := make(chan error, 1)
			var wg sync.WaitGroup

			func() {
				for {
					select {
					case item, ok := <-inCh:
						if !ok {
							return
						}
						// Use innerCtx.Done() only — reading errCh here would drain it
						// before the final select below can return it to Supervise.
						select {
						case sem <- struct{}{}:
						case <-innerCtx.Done():
							return
						}
						outbox := boxes[workerIdx%len(boxes)]
						workerIdx++
						wg.Add(1)
						go func(it I, outbox internal.Outbox[O]) {
							defer wg.Done()
							defer func() { <-sem }()

							itemCtx, cancelItem := itemContext(innerCtx, cfg)
							send := func(v O) error { return outbox.Send(innerCtx, v) }
							start := time.Now()
							err, attempt := internal.ProcessFlatMapItem(itemCtx, fn, it, cfg.errorHandler, send, ctxMapper)
							dur := time.Since(start)
							cancelItem()
							if err != nil && err != internal.ErrSkipped {
								errCount.Add(1)
								hook.OnItem(ctx, cfg.name, dur, err)
								reportErr(errCh, internal.WrapStageErr(cfg.name, err, attempt))
								cancel()
								return
							}
							if err == nil {
								procCount.Add(1)
								hook.OnItem(ctx, cfg.name, dur, nil)
							}
						}(item, outbox)
					case <-innerCtx.Done():
						return
					}
				}
			}()

			wg.Wait()

			select {
			case err := <-errCh:
				return err
			default:
				return nil
			}
		}

		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}
```

- [ ] Run: `cd /Users/jonathan/projects/go-kitsune && go build ./...`  Expected: no output.

- [ ] Run: `cd /Users/jonathan/projects/go-kitsune && go test ./... -run 'TestFlatMap|TestOverflow' -v`  Expected: `PASS` for all matching tests.

- [ ] Run: `cd /Users/jonathan/projects/go-kitsune && go test ./... -race -run 'TestFlatMap|TestOverflow'`  Expected: `PASS`, no race reports.

- [ ] Commit: `git add operator_flatmap.go && git commit -m "perf(flatmap): route concurrent DropOldest through sharded outbox"` (HEREDOC body mirrors Task 2; Co-Authored-By trailer).

---

### Task 4: Add race-stress test for sharded outbox under sustained backpressure

**Files:**
- Modify: `/Users/jonathan/projects/go-kitsune/overflow_test.go` (append new test after `TestOverflowDropOldestConcurrent` at line 153)

- [ ] Insert the following test immediately after the closing brace of `TestOverflowDropOldestConcurrent` (i.e. after line 153), before the `// Overflow hook` banner at line 155:

```go
// TestOverflowDropOldestShardedStress exercises the sharded DropOldest outbox
// under sustained backpressure with Concurrency(8) and a tiny Buffer(2), which
// keeps the slow path hot. Run with -race to catch any regression in
// cross-shard ordering: the sharded slow path is only safe because each shard
// owns its own mutex, the underlying channel tolerates concurrent send + recv,
// and the shared dropped counter is atomic.
func TestOverflowDropOldestShardedStress(t *testing.T) {
	const n = 20_000
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}

	h := &dropCountHook{Hook: kitsune.LogHook(slog.New(slog.NewTextHandler(io.Discard, nil)))}
	var received atomic.Int64

	err := kitsune.Map(
		kitsune.FromSlice(items),
		func(_ context.Context, v int) (int, error) { return v, nil },
		kitsune.Concurrency(8),
		kitsune.Buffer(2),
		kitsune.Overflow(kitsune.DropOldest),
	).ForEach(func(_ context.Context, _ int) error {
		// No sleep: maximum throughput. The sharded outbox should still
		// make forward progress without deadlock or races.
		received.Add(1)
		return nil
	}).Run(context.Background(), kitsune.WithHook(h))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if received.Load() == 0 {
		t.Fatal("expected some items to be received")
	}
	// Sanity: received + dropped must not exceed n (no phantom items).
	if received.Load()+h.drops.Load() > int64(n) {
		t.Fatalf("received(%d) + dropped(%d) > %d", received.Load(), h.drops.Load(), n)
	}
}
```

- [ ] Run: `cd /Users/jonathan/projects/go-kitsune && go test ./... -run TestOverflowDropOldestShardedStress -v`  Expected: `PASS`.

- [ ] Run: `cd /Users/jonathan/projects/go-kitsune && go test ./... -race -run TestOverflowDropOldestShardedStress -count=3`  Expected: `PASS` on all three runs, no race reports.

- [ ] Run: `cd /Users/jonathan/projects/go-kitsune && task test:race`  Expected: full race suite green.

- [ ] Commit: `git add overflow_test.go && git commit -m "test(overflow): add sharded DropOldest stress test under Concurrency(8)"` (HEREDOC body + Co-Authored-By trailer).

---

### Task 5: Document sharding in the `DropOldest` godoc

**Files:**
- Modify: `/Users/jonathan/projects/go-kitsune/config.go:280-290`

- [ ] Update the godoc block immediately above `DropOldest OverflowStrategy = internal.OverflowDropOldest` so the paragraph about mutex contention reflects the new behaviour. Replace lines 280-290 with:

```go
	// When the buffer has space the send is lock-free. When the buffer is full a
	// sync.Mutex is held while the oldest item is drained and the new item is
	// inserted, serialising concurrent senders on the slow path.
	//
	// With [Concurrency] > 1, the outbox is sharded: worker i uses shard i % n,
	// and each shard owns its own mutex. Workers routed to different shards
	// never contend on the slow path; each shard still serialises its own
	// drain-and-resend pair so buffer ordering is preserved. All shards share
	// the same underlying channel and a single atomic dropped counter, so drop
	// totals reported by the overflow hook remain accurate.
	//
	// With [Concurrency] = 1 (the default), a single non-sharded outbox is
	// used. For drop semantics without any mutex, consider [DropNewest].
	// See "Overflow strategies" in doc/tuning.md for a full comparison.
	DropOldest OverflowStrategy = internal.OverflowDropOldest
```

- [ ] Run: `cd /Users/jonathan/projects/go-kitsune && go build ./... && go vet ./...`  Expected: no output.

- [ ] Run: `cd /Users/jonathan/projects/go-kitsune && go doc . DropOldest`  Expected: godoc output shows the updated sharding paragraph (manual visual check).

- [ ] Commit: `git add config.go && git commit -m "docs(overflow): document DropOldest sharding under Concurrency > 1"` (HEREDOC body + Co-Authored-By trailer).

---

### Task 6: Mark the roadmap item done

**Files:**
- Modify: `/Users/jonathan/projects/go-kitsune/doc/roadmap.md:43`

- [ ] Replace the unchecked bullet on line 43 with the checked version:

```markdown
- [x] **Sharded `DropOldest` outbox**: `dropOldestOutbox` holds a single mutex protecting drain-and-resend when the buffer is full. Under sustained backpressure with `Concurrency(n)`, all n workers serialize on this mutex; exactly the scenario `DropOldest` is designed for becomes its hot path. Implemented a sharded outbox (worker i uses shard i % n) to eliminate cross-worker contention when both `Overflow(DropOldest)` and `Concurrency(n > 1)` are active.
```

(Note: the original bullet contained an em dash; the completed version uses a semicolon per project style.)

- [ ] Run: `cd /Users/jonathan/projects/go-kitsune && task test:all`  Expected: entire test suite green, including property tests and examples.

- [ ] Commit: `git add doc/roadmap.md && git commit -m "docs(roadmap): mark sharded DropOldest outbox done"` (HEREDOC body + Co-Authored-By trailer).

---

## Verification checklist (after all tasks)

- [ ] `go test ./internal/ -race -run TestShardedDropOldestOutbox` passes.
- [ ] `go test ./... -race -run 'TestOverflow|TestFlatMap'` passes.
- [ ] `task test:race` passes end-to-end.
- [ ] `task test:all` passes end-to-end.
- [ ] `go doc . DropOldest` shows the sharding paragraph.
- [ ] `doc/roadmap.md` bullet is `[x]` and has no em dash.
- [ ] `git log --oneline` shows six commits, one per task, each with the Co-Authored-By trailer.
