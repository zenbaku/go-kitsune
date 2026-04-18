# SessionWindow Multi-Session Property Tests Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add two property-based tests that cover the missing SessionWindow multi-session splitting law: that items separated by more than `gap` appear in different sessions.

**Architecture:** Both tests go in `properties_test.go` (package `kitsune_test`). They use `testkit.NewTestClock()` passed via `kitsune.WithClock` to control time deterministically, and `kitsune.NewChannel[int]` to push items into the pipeline from the test goroutine. `pipelineStartup` (10ms, defined in `clock_test.go`, same package) guards each send-then-advance sequence. The existing single-session test comment is updated to reflect that multi-session is now covered.

**Tech Stack:** Go 1.23, `pgregory.net/rapid`, `github.com/zenbaku/go-kitsune/testkit`.

---

## Files

| File | Action | What changes |
|---|---|---|
| `properties_test.go` | Modify | Add `testkit` import; add 2 new property test functions; update stale comment in existing test |
| `doc/roadmap.md` | Modify | Mark the windowing property tests item `[x]` (it was re-opened; now complete) |

---

## Background: how the tests work

`SessionWindow` uses an internal gap timer. When `WithClock(clock)` is passed, that timer uses the virtual clock instead of wall time. The test pattern is:

1. Start the pipeline in a goroutine.
2. Sleep `pipelineStartup` (10ms) to let the goroutine reach its select loop.
3. Send items via `NewChannel.Send`.
4. Sleep `pipelineStartup` again so the pipeline goroutine has consumed those items and reset its timer.
5. Call `clock.Advance(gap)` — the virtual timer fires, flushing the session.
6. Repeat for each batch.
7. Close the channel; wait for `done`.
8. Drain the `sessions` channel and compare to expected.

The `sessions` channel is buffered (64) so no session is dropped between steps.

---

## Task 1: Add `testkit` import and `TestPropSessionWindowMultiSession`

**Files:**
- Modify: `properties_test.go`

- [ ] **Step 1: Add `testkit` to the import block**

The current import block in `properties_test.go` (lines 3-16) is:

```go
import (
	"context"
	"flag"
	"os"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"

	kitsune "github.com/zenbaku/go-kitsune"
)
```

Replace it with:

```go
import (
	"context"
	"flag"
	"os"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/testkit"
)
```

- [ ] **Step 2: Add `TestPropSessionWindowMultiSession` after `TestPropSessionWindowLargeGapSingleSession`**

`TestPropSessionWindowLargeGapSingleSession` ends around line 1661. Add the new test immediately after it, before the `ExpandMap` section:

```go
// TestPropSessionWindowMultiSession verifies the core multi-session splitting
// law: items sent in distinct batches separated by a full gap advance each land
// in their own session, in arrival order.
//
// Each property run draws 1-4 batches of 0-5 items. Empty batches are skipped
// (SessionWindow never emits an empty session). The virtual clock is advanced
// by exactly gap after each non-empty batch to trigger a flush.
func TestPropSessionWindowMultiSession(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		numBatches := rapid.IntRange(1, 4).Draw(t, "numBatches")
		batches := make([][]int, numBatches)
		for i := range batches {
			batches[i] = rapid.SliceOfN(rapid.IntRange(-100, 100), 0, 5).Draw(t, "batch")
		}

		const gap = time.Second
		clock := testkit.NewTestClock()
		ch := kitsune.NewChannel[int](32)

		sessions := make(chan []int, 64)
		done := make(chan error, 1)
		go func() {
			done <- kitsune.SessionWindow(ch.Source(), gap, kitsune.WithClock(clock)).
				ForEach(func(_ context.Context, s []int) error {
					sessions <- s
					return nil
				}).Run(context.Background())
		}()
		time.Sleep(pipelineStartup)

		var expected [][]int
		for _, batch := range batches {
			if len(batch) == 0 {
				continue
			}
			for _, v := range batch {
				if err := ch.Send(context.Background(), v); err != nil {
					t.Fatalf("Send error: %v", err)
				}
			}
			time.Sleep(pipelineStartup) // let pipeline consume items before advancing
			clock.Advance(gap)
			time.Sleep(pipelineStartup) // let flush propagate to sessions channel
			expected = append(expected, batch)
		}

		ch.Close()
		if err := <-done; err != nil {
			t.Fatalf("pipeline error: %v", err)
		}

		// Drain all sessions.
		close(sessions)
		var got [][]int
		for s := range sessions {
			got = append(got, s)
		}

		if len(got) != len(expected) {
			t.Fatalf("session count: got %d, want %d\n  got:  %v\n  want: %v",
				len(got), len(expected), got, expected)
		}
		for i, want := range expected {
			if !slices.Equal(got[i], want) {
				t.Fatalf("session[%d]: got %v, want %v", i, got[i], want)
			}
		}
	})
}
```

- [ ] **Step 3: Run the test to confirm it passes**

```
go test -run TestPropSessionWindowMultiSession -v -count=1 .
```

Expected: PASS, 100 checks.

---

## Task 2: Add `TestPropSessionWindowEachItemAlone`

**Files:**
- Modify: `properties_test.go`

- [ ] **Step 1: Add the test immediately after `TestPropSessionWindowMultiSession`**

```go
// TestPropSessionWindowEachItemAlone verifies that each item sent in isolation
// (with a full gap advance between each one) produces exactly one single-element
// session. This is the strongest form of the splitting law.
func TestPropSessionWindowEachItemAlone(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := rapid.SliceOfN(rapid.IntRange(-100, 100), 0, 6).Draw(t, "src")

		const gap = time.Second
		clock := testkit.NewTestClock()
		ch := kitsune.NewChannel[int](32)

		sessions := make(chan []int, 64)
		done := make(chan error, 1)
		go func() {
			done <- kitsune.SessionWindow(ch.Source(), gap, kitsune.WithClock(clock)).
				ForEach(func(_ context.Context, s []int) error {
					sessions <- s
					return nil
				}).Run(context.Background())
		}()
		time.Sleep(pipelineStartup)

		for _, v := range src {
			if err := ch.Send(context.Background(), v); err != nil {
				t.Fatalf("Send error: %v", err)
			}
			time.Sleep(pipelineStartup)
			clock.Advance(gap)
			time.Sleep(pipelineStartup)
		}

		ch.Close()
		if err := <-done; err != nil {
			t.Fatalf("pipeline error: %v", err)
		}

		close(sessions)
		var got [][]int
		for s := range sessions {
			got = append(got, s)
		}

		if len(got) != len(src) {
			t.Fatalf("session count: got %d, want %d (src=%v got=%v)",
				len(got), len(src), src, got)
		}
		for i, v := range src {
			if len(got[i]) != 1 || got[i][0] != v {
				t.Fatalf("session[%d]: got %v, want [%d]", i, got[i], v)
			}
		}
	})
}
```

- [ ] **Step 2: Run both new tests**

```
go test -run TestPropSessionWindow -v -count=1 .
```

Expected: all 3 SessionWindow property tests PASS (including the existing large-gap one).

---

## Task 3: Update the stale comment and run the full suite

**Files:**
- Modify: `properties_test.go`
- Modify: `doc/roadmap.md`

- [ ] **Step 1: Update the stale comment in `TestPropSessionWindowLargeGapSingleSession`**

Find this comment block (around line 1629-1635):

```go
// TestPropSessionWindowLargeGapSingleSession verifies that when the gap is
// much larger than any realistic inter-item delay, all items from a
// synchronous source (FromSlice) land in exactly one session.
//
// Multi-session properties require precise timing control (a mock clock) and
// are not suited to rapid property tests; they are covered by example-based
// tests in window_test.go using testkit's controlled clock.
```

Replace with:

```go
// TestPropSessionWindowLargeGapSingleSession verifies that when the gap is
// much larger than any realistic inter-item delay, all items from a
// synchronous source (FromSlice) land in exactly one session.
// Multi-session splitting is covered by TestPropSessionWindowMultiSession and
// TestPropSessionWindowEachItemAlone.
```

- [ ] **Step 2: Run the full property suite**

```
task test:property
```

Expected: all property tests PASS.

- [ ] **Step 3: Run the full test suite**

```
task test
```

Expected: all tests PASS.

- [ ] **Step 4: Mark the roadmap item done**

In `doc/roadmap.md`, find:

```markdown
- [x] **Property tests for windowing operators** *(re-open: marked done in roadmap but tests are absent)*: `Batch`, `BufferWith`, `SlidingWindow`, `SessionWindow`, `ChunkBy`, and `ChunkWhile` have no property-based tests.
```

The `*(re-open: ...)*` note is now resolved. Update to remove it:

```markdown
- [x] **Property tests for windowing operators**: `Batch`, `BufferWith`, `SlidingWindow`, `SessionWindow`, `ChunkBy`, and `ChunkWhile` have no property-based tests.
```

- [ ] **Step 5: Commit everything**

```bash
git add properties_test.go doc/roadmap.md
git commit -m "test(sessionwindow): add multi-session and each-item-alone property tests

The existing large-gap test only covered the trivial single-session case.
These two tests cover the core splitting law using testkit.NewTestClock()
and WithClock for deterministic gap-timer control."
```
