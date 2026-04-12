# RunHandle Correctness Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix two correctness bugs in `kitsune.go`: a timer goroutine leak in `runWithDrain` and a single-consumer footgun in `RunHandle` that causes concurrent `Wait()`/`Err()` callers to deadlock.

**Architecture:** Both fixes are confined to `kitsune.go`. The `RunHandle` redesign removes the shared `errCh` channel, stores the result in a plain `err error` field written before `close(done)` (the close acts as the memory barrier), and changes `Err()` from `<-chan error` to a non-blocking `error` accessor. The timer leak fix replaces `time.After` with `time.NewTimer`+`Stop()`.

**Tech Stack:** Go standard library only (`sync`, `sync/atomic` not needed — write-before-close provides the memory ordering guarantee).

---

## File map

| File | Change |
|------|--------|
| `kitsune.go` | Fix `runWithDrain` timer leak; redesign `RunHandle` |
| `runasync_test.go` | Update `TestRunAsync_Err_Channel` for new API; add concurrent-Wait and non-blocking-Err regression tests |

---

### Task 1: Fix `runWithDrain` timer goroutine leak

**Files:**
- Modify: `kitsune.go:306-333`
- Test: `drain_test.go` (existing tests cover this path; no new test needed — race detector catches the leak)

- [ ] **Step 1: Write a failing test that detects the leak**

Add to `drain_test.go`:

```go
func TestWithDrainTimerLeak(t *testing.T) {
	// This test verifies that runWithDrain does not leak a goroutine when the
	// drain completes before the timeout fires. Run with -race or check goroutine
	// count; here we simply exercise the path and rely on the race detector.
	ctx, cancel := context.WithCancel(context.Background())

	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		yield(1)
		<-ctx.Done()
		return nil
	})

	done := make(chan error, 1)
	go func() {
		done <- p.ForEach(func(_ context.Context, _ int) error { return nil }).
			Run(ctx, kitsune.WithDrain(10*time.Second)) // long timeout — should not fire
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pipeline did not complete after context cancel")
	}
	// If time.After was used, its goroutine would still be running here for ~10s.
	// The race detector or goleak would surface this. The test is a correctness
	// guard; the goroutine leak itself is only reliably visible with goleak.
}
```

- [ ] **Step 2: Run the test**

```bash
cd /Users/jonathan/projects/go-kitsune && go test -run TestWithDrainTimerLeak -race -v ./...
```

Expected: PASS (the test passes even with the bug because the leak is temporal, not a hang; this test guards against regressions).

- [ ] **Step 3: Fix `runWithDrain` in `kitsune.go`**

Replace lines 310-323 (the inner goroutine) so it uses `time.NewTimer` and stops it when drain completes first. The full function becomes:

```go
func runWithDrain(parentCtx context.Context, drainTimeout time.Duration, signalDone func(), stages []func(context.Context) error) error {
	drainCtx, drainCancel := context.WithCancel(context.Background())
	defer drainCancel()

	go func() {
		select {
		case <-parentCtx.Done():
			// Phase 1: stop sources cleanly (closes rc.done; Generate watches it).
			signalDone()
			// Phase 2: wait for natural drain or hard-stop timeout.
			timer := time.NewTimer(drainTimeout)
			select {
			case <-timer.C:
				drainCancel()
			case <-drainCtx.Done():
				timer.Stop()
			}
		case <-drainCtx.Done():
		}
	}()

	err := internal.RunStages(drainCtx, stages)
	// Suppress context errors caused by the drain timeout hard-stop — they are
	// expected when drainCtx was cancelled after the timeout, not genuine errors.
	if err != nil && parentCtx.Err() != nil &&
		(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return nil
	}
	return err
}
```

- [ ] **Step 4: Run all drain tests with race detector**

```bash
cd /Users/jonathan/projects/go-kitsune && go test -run TestWithDrain -race -v ./...
```

Expected: all three existing drain tests plus the new one PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jonathan/projects/go-kitsune
git add kitsune.go drain_test.go
git commit -m "fix: stop timer in runWithDrain to prevent goroutine leak"
```

---

### Task 2: Redesign `RunHandle` — remove `errCh`, add `err` field

**Files:**
- Modify: `kitsune.go:183-353`
- Modify: `runasync_test.go` (update existing test; add two new tests)

The root cause: `Wait()` and `Err()` both consume from the same buffered-1 channel.
The fix: store the result in `h.err` before `close(h.done)`. The channel close is a
happens-before barrier; any goroutine that reads `h.err` after unblocking on `<-h.done`
is guaranteed to see the written value.

- [ ] **Step 1: Write failing regression test for concurrent `Wait()`**

Add to `runasync_test.go`:

```go
func TestRunAsync_ConcurrentWait(t *testing.T) {
	// Two concurrent Wait() callers must both receive the result without
	// either blocking forever. This was impossible with the old errCh design.
	boom := errors.New("boom")
	p := kitsune.FromSlice([]int{1})
	h := kitsune.Map(p, func(_ context.Context, _ int) (int, error) {
		return 0, boom
	}).ForEach(func(_ context.Context, _ int) error { return nil }).Build().RunAsync(context.Background())

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = h.Wait()
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("one or both concurrent Wait() calls blocked forever")
	}
	for i, err := range errs {
		if !errors.Is(err, boom) {
			t.Fatalf("caller %d: expected boom, got %v", i, err)
		}
	}
}
```

- [ ] **Step 2: Run the new test to confirm it fails (or hangs)**

```bash
cd /Users/jonathan/projects/go-kitsune && go test -run TestRunAsync_ConcurrentWait -race -v -timeout 15s ./...
```

Expected: test times out / fails because one `Wait()` goroutine blocks forever after the other consumes from `errCh`.

- [ ] **Step 3: Write failing test for non-blocking `Err() error`**

Add to `runasync_test.go`:

```go
func TestRunAsync_ErrNonBlocking(t *testing.T) {
	// Err() before Done() closes must return nil (non-blocking).
	// Err() after Done() closes must return the pipeline error.
	boom := errors.New("boom")
	p := kitsune.FromSlice([]int{1})
	h := kitsune.Map(p, func(_ context.Context, _ int) (int, error) {
		return 0, boom
	}).ForEach(func(_ context.Context, _ int) error { return nil }).Build().RunAsync(context.Background())

	// Before completion: Err() returns nil without blocking.
	// (Race window: if pipeline completes instantly this may be non-nil — that's fine.)

	// Wait for completion.
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done() did not close")
	}

	// After completion: Err() returns the stored error.
	if err := h.Err(); !errors.Is(err, boom) {
		t.Fatalf("expected boom after Done(), got %v", err)
	}
	// Safe to call multiple times.
	if err := h.Err(); !errors.Is(err, boom) {
		t.Fatalf("second Err() call: expected boom, got %v", err)
	}
}
```

- [ ] **Step 4: Run the new Err test to confirm it fails**

```bash
cd /Users/jonathan/projects/go-kitsune && go test -run TestRunAsync_ErrNonBlocking -race -v -timeout 15s ./...
```

Expected: compile error because current `h.Err()` returns `<-chan error`, not `error`.

- [ ] **Step 5: Replace `TestRunAsync_Err_Channel` with a test for the new `Err()` signature**

In `runasync_test.go`, **delete** the existing `TestRunAsync_Err_Channel` function entirely (lines 153-167 in the original file), then add the replacement below. The old test uses `<-h.Err()` in a select, which will no longer compile once `Err()` returns `error`.

```go
func TestRunAsync_Err_AfterDone(t *testing.T) {
	boom := errors.New("boom")
	p := kitsune.FromSlice([]int{1})
	h := kitsune.Map(p, func(_ context.Context, _ int) (int, error) {
		return 0, boom
	}).ForEach(func(_ context.Context, _ int) error { return nil }).Build().RunAsync(context.Background())

	select {
	case <-h.Done():
		if err := h.Err(); !errors.Is(err, boom) {
			t.Fatalf("expected boom, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Done() did not close")
	}
}
```

- [ ] **Step 6: Redesign `RunHandle` in `kitsune.go`**

Replace the `RunHandle` struct definition, its methods, and the `RunAsync` function:

```go
// RunHandle is returned by [Runner.RunAsync]. It provides multiple ways to
// observe the pipeline's completion and to pause/resume source stages.
type RunHandle struct {
	done chan struct{}
	err  error // written before done is closed; safe to read after <-done returns
	gate *Gate
}

// Wait blocks until the pipeline completes and returns its error (or nil).
// Safe to call from multiple goroutines concurrently.
func (h *RunHandle) Wait() error {
	<-h.done
	return h.err
}

// Done returns a channel that is closed when the pipeline completes.
// Use this in a select alongside other channels.
func (h *RunHandle) Done() <-chan struct{} {
	return h.done
}

// Err returns the pipeline's terminal error. It is non-blocking: if the
// pipeline has not yet completed it returns nil. Use Done() in a select and
// then call Err() to retrieve the result, or use Wait() to block until done.
// Safe to call from multiple goroutines concurrently.
func (h *RunHandle) Err() error {
	select {
	case <-h.done:
		return h.err
	default:
		return nil
	}
}

// Pause stops sources from emitting new items. In-flight items continue
// draining through downstream stages. Safe to call multiple times.
// Has no effect after the pipeline has completed.
func (h *RunHandle) Pause() { h.gate.Pause() }

// Resume allows sources to emit items again after a [RunHandle.Pause].
// Safe to call multiple times. Has no effect if the pipeline is not paused.
func (h *RunHandle) Resume() { h.gate.Resume() }

// Paused reports whether the pipeline is currently paused.
func (h *RunHandle) Paused() bool { return h.gate.Paused() }
```

And update `RunAsync`:

```go
// RunAsync starts the pipeline in a background goroutine and returns a
// [RunHandle] for observing completion and controlling execution.
// A [Gate] is created automatically and exposed via [RunHandle.Pause] and
// [RunHandle.Resume]. Pass [WithPauseGate] to supply your own gate instead.
func (r *Runner) RunAsync(ctx context.Context, opts ...RunOption) *RunHandle {
	cfg := buildRunConfig(opts)
	gate := cfg.gate
	if gate == nil {
		gate = internal.NewGate()
		opts = append(opts, WithPauseGate(gate))
	}
	h := &RunHandle{done: make(chan struct{}), gate: gate}
	go func() {
		h.err = r.Run(ctx, opts...)
		close(h.done)
	}()
	return h
}
```

- [ ] **Step 7: Run all RunAsync tests with race detector**

```bash
cd /Users/jonathan/projects/go-kitsune && go test -run TestRunAsync -race -v ./...
```

Expected: all tests PASS including the two new ones and the renamed `TestRunAsync_Err_AfterDone`.

- [ ] **Step 8: Run full test suite**

```bash
cd /Users/jonathan/projects/go-kitsune && task test:race
```

Expected: all tests PASS, no races.

- [ ] **Step 9: Commit**

```bash
cd /Users/jonathan/projects/go-kitsune
git add kitsune.go runasync_test.go
git commit -m "fix: redesign RunHandle to allow concurrent Wait() and non-blocking Err()"
```

---

### Task 3: Update roadmap and godoc reference in `config.go`

**Files:**
- Modify: `doc/roadmap.md` (mark both items done)
- Modify: `config.go` (update the `RunHandle` godoc reference on the `WithDrainTimeout` / `RunAsync` comment if it mentions `Err()` channel semantics)

- [ ] **Step 1: Check `config.go` for references to `Err()` channel**

```bash
grep -n "Err()\|errCh\|error channel" /Users/jonathan/projects/go-kitsune/config.go
```

Update any comment that says `Err()` returns a channel.

- [ ] **Step 2: Mark roadmap items done**

In `doc/roadmap.md`, change the two items from `- [ ]` to `- [x]`:

```
- [x] **`runWithDrain` timer goroutine leak**: ...
- [x] **`RunHandle.errCh` single-consumer footgun**: ...
```

- [ ] **Step 3: Final full test run**

```bash
cd /Users/jonathan/projects/go-kitsune && task test:all
```

Expected: all tests PASS.

- [ ] **Step 4: Commit**

```bash
cd /Users/jonathan/projects/go-kitsune
git add doc/roadmap.md config.go
git commit -m "chore: mark RunHandle and runWithDrain correctness fixes done in roadmap"
```
