# Idempotency-key dedupe for `Effect` — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire `EffectPolicy.IdempotencyKey` to a configurable `IdempotencyStore` so that an Effect stage skips items whose key has already been seen. Default per-Run in-memory store auto-attaches when `Idempotent: true` is set with a non-nil key function; user-supplied stores enable cross-Run dedupe.

**Architecture:** New `IdempotencyStore` interface with an atomic `Add(ctx, key) (firstTime, err)` contract; new `memoryIdempotencyStore` default implementation in a new `idempotency.go`. `EffectPolicy` and `effectConfig` gain an `IdempotencyStore` field. The stage loop in `effect.go` resolves the store at build time and, before invoking `runEffectAttempts`, calls `Add(key)`: on duplicate-key it emits a deduped outcome and skips `fn`; on store error it records a per-item failure. `EffectOutcome.Deduped` and `EffectStats.Deduped` surface the count.

**Tech Stack:** Go generics, the existing Effect/runCtx plumbing.

**Module path:** `github.com/zenbaku/go-kitsune`

---

## Background notes

### Why a separate `IdempotencyStore` interface (not `DedupSet` or `Store`)

`DedupSet` has separate `Contains` and `Add` methods; under `Concurrency(n>1)` two workers could race past `Contains` and both proceed to call `fn`. `IdempotencyStore.Add` returns `(firstTime bool, err error)` as a single atomic operation. `Store` (Get/Set) is a more general key-value contract; using it here would either require external synchronisation or new compare-and-set semantics. A dedicated interface is the smallest correct surface.

### When the dedupe check fires

Once per upstream-item arrival, before `runEffectAttempts` starts. Retries within `runEffectAttempts` do NOT re-check the store: a single arrival is one dedupe-eligible event regardless of how many retries the policy permits.

### v1 limitation: "failed first poisons future arrivals"

The key is recorded on `Add` (before `fn` runs), not after success. If the first attempt for key K fails terminally, a later arrival with key K is deduped even though the original failed. The alternative (record only on success) requires an "in-flight" set or compare-and-set-and-finalise semantics; out of scope for v1. Documented.

---

## File structure

| File | Responsibility | Change |
|---|---|---|
| `idempotency.go` (new) | IdempotencyStore interface + default in-memory implementation | Create |
| `idempotency_test.go` (new) | Unit test for memoryIdempotencyStore | Create |
| `effect.go` | Effect operator + EffectPolicy + EffectOutcome + stage loop | Add `Deduped` to outcome; add `IdempotencyStore` to policy/config; resolve store at build; wrap `runEffectAttempts` with dedupe check; update godoc |
| `pipeline.go` | runCtx + effectStat | Add `deduped atomic.Int64`; add `recordEffectDeduped(id)` helper |
| `runsummary.go` | RunSummary + EffectStats | Add `Deduped int64` to `EffectStats`; update godoc |
| `kitsune.go` | Runner.Run summary projection | Read `s.deduped.Load()` into the projection |
| `effect_idempotency_test.go` (new) | Integration tests for dedupe behaviour | Create with five tests |
| `doc/operators.md` | Effect section | Document idempotency dedupe + `Deduped` outcome field |
| `doc/run-summary.md` | EffectStats section | Mention `Deduped` field |
| `doc/roadmap.md` | Roadmap | Mark item `[x]` |
| Memory: `project_higher_level_authoring.md` | User auto-memory | Note dedupe is now wired |

---

## Tasks

### Task 1: `IdempotencyStore` interface and default memory implementation

Create the new file with the interface and the default in-memory store, plus a focused unit test. Stand-alone task — no dependencies on the rest.

**Files:**
- Create: `idempotency.go`
- Create: `idempotency_test.go`

- [ ] **Step 1: Write the failing memory-store test**

Create `idempotency_test.go`:

```go
package kitsune

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMemoryIdempotencyStore_AddFirstTimeThenDuplicate(t *testing.T) {
	s := newMemoryIdempotencyStore()
	ctx := context.Background()

	first, err := s.Add(ctx, "k1")
	if err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if !first {
		t.Errorf("first Add returned firstTime=false, want true")
	}

	second, err := s.Add(ctx, "k1")
	if err != nil {
		t.Fatalf("second Add: %v", err)
	}
	if second {
		t.Errorf("second Add returned firstTime=true, want false")
	}

	// Different key: firstTime=true again.
	other, err := s.Add(ctx, "k2")
	if err != nil {
		t.Fatalf("Add k2: %v", err)
	}
	if !other {
		t.Errorf("Add k2 returned firstTime=false, want true")
	}
}

func TestMemoryIdempotencyStore_RaceFreeUnderConcurrency(t *testing.T) {
	s := newMemoryIdempotencyStore()
	ctx := context.Background()

	const workers = 16
	const adds = 100
	var firstCount atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < adds; i++ {
				first, err := s.Add(ctx, "shared-key")
				if err != nil {
					t.Errorf("Add: %v", err)
					return
				}
				if first {
					firstCount.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	if got := firstCount.Load(); got != 1 {
		t.Errorf("firstCount=%d, want 1 (Add must atomically claim the key exactly once)", got)
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `go test -run TestMemoryIdempotencyStore ./... -count=1`
Expected: build failure (`undefined: newMemoryIdempotencyStore`).

- [ ] **Step 3: Create `idempotency.go`**

Create `idempotency.go`:

```go
package kitsune

import (
	"context"
	"sync"
)

// IdempotencyStore tracks idempotency keys for [Effect] dedupe.
// Implementations must be safe for concurrent use; Add must record the key
// atomically so two concurrent Adds with the same key see exactly one
// firstTime=true return.
//
// The default in-process implementation is attached automatically to an
// [Effect] stage when [EffectPolicy.Idempotent] is true and
// [EffectPolicy.IdempotencyKey] is non-nil. To dedupe across runs (so a
// key recorded in one run is honoured by the next), supply a persistent
// implementation via [EffectPolicy.IdempotencyStore], for example a
// thin wrapper around Redis SETNX.
type IdempotencyStore interface {
	// Add atomically records key as seen. It returns true if key was
	// newly recorded (first time seen) and false if the key was already
	// present. A non-nil error indicates the store could not be queried;
	// the calling Effect treats this as a per-item failure.
	Add(ctx context.Context, key string) (firstTime bool, err error)
}

// memoryIdempotencyStore is the default in-process [IdempotencyStore].
// One is constructed per Effect stage when no user-supplied store is
// configured; its lifetime is the stage goroutine's lifetime (one Run).
type memoryIdempotencyStore struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func newMemoryIdempotencyStore() *memoryIdempotencyStore {
	return &memoryIdempotencyStore{seen: make(map[string]struct{})}
}

// Add implements [IdempotencyStore].
func (s *memoryIdempotencyStore) Add(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[key]; ok {
		return false, nil
	}
	s.seen[key] = struct{}{}
	return true, nil
}
```

- [ ] **Step 4: Run the test and verify it passes**

Run: `go test -run TestMemoryIdempotencyStore ./... -count=1`
Expected: PASS.

- [ ] **Step 5: Run with race**

Run: `go test -race -run TestMemoryIdempotencyStore_RaceFreeUnderConcurrency ./... -count=1`
Expected: PASS, no races.

- [ ] **Step 6: Commit**

```bash
git add idempotency.go idempotency_test.go
git commit -m "$(cat <<'EOF'
feat(idempotency): add IdempotencyStore interface and memory default

New IdempotencyStore.Add(ctx, key) (firstTime, err) atomic check-and-set
contract. Default in-process implementation backs the Effect operator's
auto-attached dedupe; user-supplied stores enable cross-Run dedupe.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Plumb `Deduped` and `IdempotencyStore` fields (no behaviour yet)

Pure additive plumbing: add fields to `EffectOutcome`, `EffectPolicy`, `effectConfig`, `effectStat`, and `EffectStats`. Wire `applyEffect`, `recordEffectDeduped`, and the `EffectStats` projection. No behaviour change yet — Task 3 enables the dedupe check that uses these.

**Files:**
- Modify: `effect.go`
- Modify: `pipeline.go`
- Modify: `runsummary.go`
- Modify: `kitsune.go`

- [ ] **Step 1: Add `Deduped` to `EffectOutcome` in `effect.go`**

Find the `EffectOutcome` struct (around line 21):

```go
type EffectOutcome[I, R any] struct {
	Input   I
	Result  R
	Err     error
	Applied bool
}
```

Replace with:

```go
type EffectOutcome[I, R any] struct {
	Input   I
	Result  R
	Err     error
	Applied bool
	// Deduped is true when the effect was skipped because the item's
	// idempotency key matched a previously-recorded invocation. When
	// Deduped is true, Applied is false, Err is nil, and Result is the
	// zero value of R. See [EffectPolicy.IdempotencyKey] and
	// [IdempotencyStore].
	Deduped bool
}
```

Update the `EffectOutcome` godoc (the lines just above the struct) to mention Deduped after the Applied paragraph:

Find:

```
// On per-attempt timeout, Applied is false and Err carries the timeout, but
// the underlying side-effect may already have been applied; treat Applied as
// a hint, not a guarantee.
```

Append a sentence after it:

```
//
// Deduped is true when the item's idempotency key matched a previously
// recorded invocation; the effect function was not called.
```

- [ ] **Step 2: Add `IdempotencyStore` to `EffectPolicy`**

Find `type EffectPolicy struct` in `effect.go` (around line 50). Add a new field after `IdempotencyKey`:

```go
	// IdempotencyStore overrides the default per-Run in-memory dedupe
	// store. Use when dedupe must survive across Runs (for example, a
	// thin wrapper around Redis SETNX). When nil and Idempotent is true
	// with a non-nil IdempotencyKey, an in-process per-Run store is
	// attached automatically.
	IdempotencyStore IdempotencyStore
```

Also update the godoc on `Idempotent` (line ~69) so it no longer says "v1 records this flag for future use":

Find:

```go
	// Idempotent declares that the effect function tolerates repeated
	// application of the same input without side effects. v1 records this
	// flag for future use; the operator does not de-duplicate retries
	// against a backing store.
	Idempotent bool
```

Replace with:

```go
	// Idempotent declares that the effect function tolerates repeated
	// application of the same input without side effects. When true and
	// IdempotencyKey is non-nil, the operator dedupes items whose key
	// matches a previously-recorded invocation: the effect function is
	// not called and the outcome is emitted with Deduped: true. See
	// [IdempotencyStore].
	Idempotent bool
```

And update `IdempotencyKey` (line ~75):

Find:

```go
	// IdempotencyKey, if non-nil, is the key function used by external
	// idempotent backends to recognise repeats. v1 records the function
	// pointer for future use.
	IdempotencyKey func(any) string
```

Replace with:

```go
	// IdempotencyKey, if non-nil, returns a stable key for an input
	// item. When Idempotent is true, the operator queries
	// [IdempotencyStore] with the key before each attempt. A return of
	// "" opts the item out of dedupe (the effect runs normally).
	IdempotencyKey func(any) string
```

- [ ] **Step 3: Add `idempotencyStore` to `effectConfig`**

Find `type effectConfig struct` (around line 98):

```go
type effectConfig struct {
	retry          RetryStrategy
	required       bool
	requiredSet    bool
	attemptTimeout time.Duration
	idempotent     bool
	idempotencyKey func(any) string
	stageOpts      []StageOption
}
```

Add a new field:

```go
type effectConfig struct {
	retry            RetryStrategy
	required         bool
	requiredSet      bool
	attemptTimeout   time.Duration
	idempotent       bool
	idempotencyKey   func(any) string
	idempotencyStore IdempotencyStore
	stageOpts        []StageOption
}
```

- [ ] **Step 4: Wire `applyEffect` to copy the new field**

Find `applyEffect` (around line 83):

```go
func (pol EffectPolicy) applyEffect(c *effectConfig) {
	c.retry = pol.Retry
	c.requiredSet = true
	c.required = pol.Required
	if pol.AttemptTimeout > 0 {
		c.attemptTimeout = pol.AttemptTimeout
	}
	c.idempotent = pol.Idempotent
	if pol.IdempotencyKey != nil {
		c.idempotencyKey = pol.IdempotencyKey
	}
}
```

Add the new field after the IdempotencyKey block:

```go
func (pol EffectPolicy) applyEffect(c *effectConfig) {
	c.retry = pol.Retry
	c.requiredSet = true
	c.required = pol.Required
	if pol.AttemptTimeout > 0 {
		c.attemptTimeout = pol.AttemptTimeout
	}
	c.idempotent = pol.Idempotent
	if pol.IdempotencyKey != nil {
		c.idempotencyKey = pol.IdempotencyKey
	}
	if pol.IdempotencyStore != nil {
		c.idempotencyStore = pol.IdempotencyStore
	}
}
```

- [ ] **Step 5: Add `deduped` counter to `effectStat`**

In `pipeline.go`, find `type effectStat struct` (around line 117-124):

```go
type effectStat struct {
	name     string
	required bool
	success  atomic.Int64
	failure  atomic.Int64
}
```

Add the `deduped` counter:

```go
type effectStat struct {
	name     string
	required bool
	success  atomic.Int64
	failure  atomic.Int64
	deduped  atomic.Int64
}
```

- [ ] **Step 6: Add `recordEffectDeduped` helper to `runCtx`**

Find `recordEffectOutcome` in `pipeline.go` (search for `func (rc *runCtx) recordEffectOutcome`). Below it, add:

```go
// recordEffectDeduped increments the per-Effect-stage deduped counter for
// id. Called once per item whose idempotency key matched a previously
// recorded invocation; the corresponding effect function was not called.
func (rc *runCtx) recordEffectDeduped(id int64) {
	if s, ok := rc.effectStats[id]; ok {
		s.deduped.Add(1)
	}
}
```

- [ ] **Step 7: Add `Deduped` to `EffectStats` in `runsummary.go`**

Find `type EffectStats struct`:

```go
type EffectStats struct {
	Required bool  `json:"required"`
	Success  int64 `json:"success"`
	Failure  int64 `json:"failure"`
}
```

Replace with:

```go
type EffectStats struct {
	Required bool  `json:"required"`
	Success  int64 `json:"success"`
	Failure  int64 `json:"failure"`
	// Deduped is the count of items whose idempotency key matched a
	// previously-recorded invocation; for these items the effect
	// function was not called.
	Deduped int64 `json:"deduped"`
}
```

Update the godoc above the type to reference `Deduped` after the existing `Failure` description:

Find:

```
// Required is true when the stage was constructed with the default
// required policy (or explicitly with [Required]); false when constructed
// with [BestEffort]. Success counts items that produced a non-error
// outcome; Failure counts items whose Effect call exhausted retries with
// a terminal error (a per-item failure attributed to this stage).
```

Append a sentence:

```
// Deduped counts items whose [EffectPolicy.IdempotencyKey] matched a
// previously-recorded invocation; the effect function was not called.
```

- [ ] **Step 8: Update the projection in `kitsune.go`**

Find the `summary.EffectStats` projection block (around line 365 after the M3+EffectStats work):

```go
summary.EffectStats = make(map[string]EffectStats, len(rc.effectStats))
for _, s := range rc.effectStats {
	summary.EffectStats[s.name] = EffectStats{
		Required: s.required,
		Success:  s.success.Load(),
		Failure:  s.failure.Load(),
	}
}
```

Replace with:

```go
summary.EffectStats = make(map[string]EffectStats, len(rc.effectStats))
for _, s := range rc.effectStats {
	summary.EffectStats[s.name] = EffectStats{
		Required: s.required,
		Success:  s.success.Load(),
		Failure:  s.failure.Load(),
		Deduped:  s.deduped.Load(),
	}
}
```

- [ ] **Step 9: Build and run existing tests for regressions**

Run: `go build ./... && go test -short ./... -count=1`
Expected: clean build; all tests PASS. (No test exercises dedupe yet; the projection just emits Deduped=0 everywhere.)

- [ ] **Step 10: Commit**

```bash
git add effect.go pipeline.go runsummary.go kitsune.go
git commit -m "$(cat <<'EOF'
feat(effect): plumb Deduped field and IdempotencyStore policy

Pure plumbing for the idempotency-key dedupe feature: adds
EffectOutcome.Deduped, EffectPolicy.IdempotencyStore,
effectConfig.idempotencyStore, effectStat.deduped, and
EffectStats.Deduped. The runCtx gains recordEffectDeduped(id) and the
RunSummary projection includes the new counter. No behaviour change
yet; the stage loop wiring lands next.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Wire dedupe into the Effect stage loop (TDD)

The load-bearing change. Write the happy-path failing test first; implement the stage-loop change; verify it passes.

**Files:**
- Modify: `effect.go`
- Create: `effect_idempotency_test.go`

- [ ] **Step 1: Write the failing happy-path test**

Create `effect_idempotency_test.go`:

```go
package kitsune_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/zenbaku/go-kitsune"
)

// TestEffect_Idempotency_DefaultStoreDedupesWithinRun exercises the
// happy path: a source emits duplicate items; with Idempotent: true and
// an IdempotencyKey set, the effect function is called once per unique
// key, and duplicates are emitted as Deduped outcomes.
func TestEffect_Idempotency_DefaultStoreDedupesWithinRun(t *testing.T) {
	ctx := context.Background()

	var fnCalls atomic.Int64
	fn := func(_ context.Context, v int) (int, error) {
		fnCalls.Add(1)
		return v * 10, nil
	}

	src := kitsune.FromSlice([]int{1, 2, 1, 3, 2})
	policy := kitsune.EffectPolicy{
		Required:       true,
		Idempotent:     true,
		IdempotencyKey: func(item any) string { return intKey(item) },
	}

	var outcomes []kitsune.EffectOutcome[int, int]
	summary, err := kitsune.Effect(src, fn, policy,
		kitsune.EffectStageOption(kitsune.WithName("dedupe-effect"))).
		ForEach(func(_ context.Context, o kitsune.EffectOutcome[int, int]) error {
			outcomes = append(outcomes, o)
			return nil
		}).Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := fnCalls.Load(); got != 3 {
		t.Errorf("fnCalls=%d, want 3 (one per unique key 1,2,3)", got)
	}
	if len(outcomes) != 5 {
		t.Fatalf("len(outcomes)=%d, want 5", len(outcomes))
	}

	// Outcomes 0,1,3 (inputs 1,2,3) are the first occurrences and should run fn.
	for _, idx := range []int{0, 1, 3} {
		if outcomes[idx].Deduped {
			t.Errorf("outcomes[%d] (input=%d) Deduped=true, want false", idx, outcomes[idx].Input)
		}
		if !outcomes[idx].Applied {
			t.Errorf("outcomes[%d] (input=%d) Applied=false, want true", idx, outcomes[idx].Input)
		}
		if outcomes[idx].Result != outcomes[idx].Input*10 {
			t.Errorf("outcomes[%d] Result=%d, want %d", idx, outcomes[idx].Result, outcomes[idx].Input*10)
		}
	}

	// Outcomes 2,4 (inputs 1,2 again) are duplicates and should be deduped.
	for _, idx := range []int{2, 4} {
		if !outcomes[idx].Deduped {
			t.Errorf("outcomes[%d] (input=%d) Deduped=false, want true", idx, outcomes[idx].Input)
		}
		if outcomes[idx].Applied {
			t.Errorf("outcomes[%d] (input=%d) Applied=true, want false", idx, outcomes[idx].Input)
		}
		if outcomes[idx].Result != 0 {
			t.Errorf("outcomes[%d] Result=%d, want zero (deduped)", idx, outcomes[idx].Result)
		}
	}

	stats := summary.EffectStats["dedupe-effect"]
	if stats.Success != 3 {
		t.Errorf("EffectStats.Success=%d, want 3", stats.Success)
	}
	if stats.Failure != 0 {
		t.Errorf("EffectStats.Failure=%d, want 0", stats.Failure)
	}
	if stats.Deduped != 2 {
		t.Errorf("EffectStats.Deduped=%d, want 2", stats.Deduped)
	}
}

// intKey returns the input cast to int and stringified, or "" if the
// item is not an int. Used by the test's IdempotencyKey functions.
func intKey(item any) string {
	v, ok := item.(int)
	if !ok {
		return ""
	}
	// Use Sprintf-equivalent without importing fmt: small int range.
	return itoa(v)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	negative := v < 0
	if negative {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
```

(The `itoa` helper avoids importing `fmt` and `strconv` for a single conversion. Reusable across the test file; keep it.)

- [ ] **Step 2: Run the test and verify it fails**

Run: `go test -run TestEffect_Idempotency_DefaultStoreDedupesWithinRun ./... -count=1`
Expected: FAIL — `fnCalls` will be 5 (no dedupe yet) and outcomes won't have `Deduped: true`.

- [ ] **Step 3: Resolve `idemStore` at build time and wire the dedupe check in the stage loop**

In `effect.go`, find the `Effect` function's `build` closure (around line 220-296). Locate the section just before `stage := func(ctx context.Context) error {` (around line 245):

```go
		rc.initDrainNotify(id, out.consumerCount.Load())
		rc.registerEffectStat(id, meta.name, cfg.required)
		drainCh := rc.drainCh(id)
		dryRun := rc.dryRun
		localCfg := cfg // local copy for closure capture
		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		stageName := meta.name
```

Insert idempotency-store resolution just after `localCfg := cfg`:

```go
		rc.initDrainNotify(id, out.consumerCount.Load())
		rc.registerEffectStat(id, meta.name, cfg.required)
		drainCh := rc.drainCh(id)
		dryRun := rc.dryRun
		localCfg := cfg // local copy for closure capture

		// Resolve the idempotency store: prefer user-supplied; otherwise
		// attach a per-stage in-memory store when Idempotent + key fn are set.
		var idemStore IdempotencyStore
		if localCfg.idempotent && localCfg.idempotencyKey != nil {
			if localCfg.idempotencyStore != nil {
				idemStore = localCfg.idempotencyStore
			} else {
				idemStore = newMemoryIdempotencyStore()
			}
		}

		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		stageName := meta.name
```

Now replace the `if !dryRun { ... }` block in the stage loop (around lines 267-277):

```go
					if !dryRun {
						start := time.Now()
						outcome = runEffectAttempts(ctx, item, fn, localCfg)
						hook.OnItem(ctx, stageName, time.Since(start), outcome.Err)
						rc.recordEffectOutcome(id, outcome.Applied)
						if outcome.Err != nil {
							errored++
						} else {
							processed++
						}
					}
```

with the dedupe-aware version:

```go
					if !dryRun {
						skipped := false
						if idemStore != nil {
							key := localCfg.idempotencyKey(item)
							if key != "" {
								firstTime, addErr := idemStore.Add(ctx, key)
								if addErr != nil {
									outcome.Err = addErr
									hook.OnItem(ctx, stageName, 0, addErr)
									rc.recordEffectOutcome(id, false)
									errored++
									skipped = true
								} else if !firstTime {
									outcome.Deduped = true
									hook.OnItem(ctx, stageName, 0, nil)
									rc.recordEffectDeduped(id)
									skipped = true
								}
							}
						}
						if !skipped {
							start := time.Now()
							outcome = runEffectAttempts(ctx, item, fn, localCfg)
							hook.OnItem(ctx, stageName, time.Since(start), outcome.Err)
							rc.recordEffectOutcome(id, outcome.Applied)
							if outcome.Err != nil {
								errored++
							} else {
								processed++
							}
						}
					}
```

- [ ] **Step 4: Build and run the new test**

Run: `go build ./... && go test -run TestEffect_Idempotency_DefaultStoreDedupesWithinRun ./... -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Run the full Effect test suite for regressions**

Run: `go test -run TestEffect ./... -count=1`
Expected: all PASS. None of the existing Effect tests should be affected (they don't set `Idempotent: true`).

- [ ] **Step 6: Commit**

```bash
git add effect.go effect_idempotency_test.go
git commit -m "$(cat <<'EOF'
feat(effect): dedupe items by idempotency key in the stage loop

When EffectPolicy.Idempotent is true and IdempotencyKey is non-nil,
the stage loop calls IdempotencyStore.Add(key) before runEffectAttempts.
On firstTime=false, the effect function is skipped and the outcome is
emitted with Deduped: true. On Add error, the item is recorded as a
per-item failure (effect not invoked). Default store auto-attached;
user-supplied EffectPolicy.IdempotencyStore overrides.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Remaining integration tests

**Files:**
- Modify: `effect_idempotency_test.go`

- [ ] **Step 1: Empty-key opt-out test**

Append to `effect_idempotency_test.go`:

```go
// TestEffect_Idempotency_EmptyKeyOptsOut verifies that returning "" from
// IdempotencyKey opts an item out of dedupe: the effect function runs
// normally and the item is NOT recorded in the store.
func TestEffect_Idempotency_EmptyKeyOptsOut(t *testing.T) {
	ctx := context.Background()

	var fnCalls atomic.Int64
	fn := func(_ context.Context, v int) (int, error) {
		fnCalls.Add(1)
		return v, nil
	}

	src := kitsune.FromSlice([]int{1, 1, 1, 2, 2})
	policy := kitsune.EffectPolicy{
		Required:   true,
		Idempotent: true,
		IdempotencyKey: func(item any) string {
			v, _ := item.(int)
			if v == 1 {
				return "" // opt out for input value 1
			}
			return intKey(item)
		},
	}

	summary, err := kitsune.Effect(src, fn, policy,
		kitsune.EffectStageOption(kitsune.WithName("opt-out"))).
		ForEach(func(_ context.Context, _ kitsune.EffectOutcome[int, int]) error { return nil }).
		Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Three 1s (all opt out, all run) + first 2 (runs) = 4 fn calls. Second 2 is deduped.
	if got := fnCalls.Load(); got != 4 {
		t.Errorf("fnCalls=%d, want 4", got)
	}
	stats := summary.EffectStats["opt-out"]
	if stats.Deduped != 1 {
		t.Errorf("Deduped=%d, want 1", stats.Deduped)
	}
}
```

- [ ] **Step 2: User-supplied store test**

Append:

```go
// countingStore is a fake IdempotencyStore that wraps an inner store and
// counts Add calls; used to verify a user-supplied store is honoured.
type countingStore struct {
	calls atomic.Int64
	inner *memoryIdempotencyStoreFake
}

// memoryIdempotencyStoreFake mirrors the production memory store so the
// test does not depend on internal types.
type memoryIdempotencyStoreFake struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func newFakeStore() *countingStore {
	return &countingStore{inner: &memoryIdempotencyStoreFake{seen: make(map[string]struct{})}}
}

func (c *countingStore) Add(_ context.Context, key string) (bool, error) {
	c.calls.Add(1)
	c.inner.mu.Lock()
	defer c.inner.mu.Unlock()
	if _, ok := c.inner.seen[key]; ok {
		return false, nil
	}
	c.inner.seen[key] = struct{}{}
	return true, nil
}

// TestEffect_Idempotency_UserSuppliedStoreIsUsed verifies that an
// IdempotencyStore set on the EffectPolicy replaces the default in-memory
// store: every dedupe-eligible item triggers a call on the user store.
func TestEffect_Idempotency_UserSuppliedStoreIsUsed(t *testing.T) {
	ctx := context.Background()

	store := newFakeStore()
	fn := func(_ context.Context, v int) (int, error) { return v, nil }
	src := kitsune.FromSlice([]int{1, 2, 3, 1})
	policy := kitsune.EffectPolicy{
		Required:         true,
		Idempotent:       true,
		IdempotencyKey:   intKey,
		IdempotencyStore: store,
	}

	if _, err := kitsune.Effect(src, fn, policy).
		ForEach(func(_ context.Context, _ kitsune.EffectOutcome[int, int]) error { return nil }).
		Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := store.calls.Load(); got != 4 {
		t.Errorf("store.calls=%d, want 4 (one Add per item)", got)
	}
}
```

Imports needed at the top of the file: `"sync"`. Add if not already present.

- [ ] **Step 3: Store-error-counts-as-failure test**

Append:

```go
// erroringStore returns an error from every Add. Used to verify that
// IdempotencyStore failures surface as per-item failures and the effect
// function is NOT invoked.
type erroringStore struct{ err error }

func (e *erroringStore) Add(_ context.Context, _ string) (bool, error) {
	return false, e.err
}

// TestEffect_Idempotency_StoreErrorIsPerItemFailure verifies that an
// IdempotencyStore.Add error is recorded as a per-item failure: the
// outcome carries Err, the failure counter increments, and the effect
// function is not called.
func TestEffect_Idempotency_StoreErrorIsPerItemFailure(t *testing.T) {
	ctx := context.Background()

	var fnCalls atomic.Int64
	fn := func(_ context.Context, v int) (int, error) {
		fnCalls.Add(1)
		return v, nil
	}

	storeErr := errors.New("store-down")
	src := kitsune.FromSlice([]int{1, 2})
	policy := kitsune.EffectPolicy{
		Required:         true,
		Idempotent:       true,
		IdempotencyKey:   intKey,
		IdempotencyStore: &erroringStore{err: storeErr},
	}

	var outcomes []kitsune.EffectOutcome[int, int]
	summary, err := kitsune.Effect(src, fn, policy,
		kitsune.EffectStageOption(kitsune.WithName("erroring"))).
		ForEach(func(_ context.Context, o kitsune.EffectOutcome[int, int]) error {
			outcomes = append(outcomes, o)
			return nil
		}).Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := fnCalls.Load(); got != 0 {
		t.Errorf("fnCalls=%d, want 0 (fn must not run when store errors)", got)
	}
	if len(outcomes) != 2 {
		t.Fatalf("len(outcomes)=%d, want 2", len(outcomes))
	}
	for i, o := range outcomes {
		if !errors.Is(o.Err, storeErr) {
			t.Errorf("outcomes[%d].Err=%v, want %v", i, o.Err, storeErr)
		}
		if o.Deduped {
			t.Errorf("outcomes[%d].Deduped=true, want false (store-error path)", i)
		}
	}
	stats := summary.EffectStats["erroring"]
	if stats.Failure != 2 {
		t.Errorf("Failure=%d, want 2", stats.Failure)
	}
	if stats.Deduped != 0 {
		t.Errorf("Deduped=%d, want 0", stats.Deduped)
	}
}
```

Imports needed: `"errors"`. Add if not already present.

- [ ] **Step 4: Concurrent dedupe test**

Append:

```go
// TestEffect_Idempotency_ConcurrentDedupeIsRaceFree verifies that under
// Concurrency(n), two workers seeing the same key result in exactly one
// fn call. The IdempotencyStore.Add atomic contract carries the load.
func TestEffect_Idempotency_ConcurrentDedupeIsRaceFree(t *testing.T) {
	ctx := context.Background()

	var fnCalls atomic.Int64
	fn := func(_ context.Context, v int) (int, error) {
		fnCalls.Add(1)
		return v, nil
	}

	const N = 50
	items := make([]int, N)
	for i := range items {
		items[i] = 1 // all the same key
	}
	src := kitsune.FromSlice(items)
	policy := kitsune.EffectPolicy{
		Required:       true,
		Idempotent:     true,
		IdempotencyKey: intKey,
	}

	summary, err := kitsune.Effect(src, fn, policy,
		kitsune.EffectStageOption(kitsune.WithName("concurrent")),
		kitsune.EffectStageOption(kitsune.Concurrency(4))).
		ForEach(func(_ context.Context, _ kitsune.EffectOutcome[int, int]) error { return nil }).
		Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := fnCalls.Load(); got != 1 {
		t.Errorf("fnCalls=%d, want 1 (exactly one worker should win the race)", got)
	}
	stats := summary.EffectStats["concurrent"]
	if stats.Success != 1 {
		t.Errorf("Success=%d, want 1", stats.Success)
	}
	if stats.Deduped != N-1 {
		t.Errorf("Deduped=%d, want %d", stats.Deduped, N-1)
	}
}
```

- [ ] **Step 5: Run all four tests**

Run: `go test -run TestEffect_Idempotency ./... -count=1 -v`
Expected: 5 PASS (the happy-path test from Task 3 plus the four added here).

- [ ] **Step 6: Run with race detector**

Run: `go test -race -run TestEffect_Idempotency ./... -count=1`
Expected: PASS, no races.

- [ ] **Step 7: Commit**

```bash
git add effect_idempotency_test.go
git commit -m "$(cat <<'EOF'
test(effect): cover empty key, user store, store error, concurrent dedupe

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Documentation, roadmap, memory

**Files:**
- Modify: `doc/operators.md`
- Modify: `doc/run-summary.md`
- Modify: `doc/roadmap.md`
- Modify: `~/.claude/projects/-Users-jonathan-projects-go-kitsune/memory/project_higher_level_authoring.md`

- [ ] **Step 1: Document dedupe in the Effect section of `doc/operators.md`**

Open `doc/operators.md` and find the section for `Effect` (search for "## Effect" or similar). Find the subsection covering `EffectPolicy` (or the closest equivalent). Insert a new subsection after the existing policy-fields documentation:

```markdown
### Idempotency-key dedupe

When `EffectPolicy.Idempotent` is `true` and `EffectPolicy.IdempotencyKey` is non-nil, the operator dedupes items whose key matches a previously-seen invocation: the effect function is not called, and the outcome is emitted with `Deduped: true`.

By default a per-Run in-memory store is auto-attached; cross-Run dedupe (so a key recorded in one run is honoured by the next) requires a user-supplied implementation of `IdempotencyStore` set on `EffectPolicy.IdempotencyStore`. A typical persistent implementation wraps Redis SETNX or a database UNIQUE-constrained insert.

Behaviour at a glance:

- `IdempotencyKey(item)` returning `""` opts the item out of dedupe.
- `IdempotencyStore.Add` errors are surfaced as per-item failures: the effect function is not called and the item counts as a `Failure`.
- Dedupe records the key on `Add` (before retries), so a failed first attempt does NOT release the key for a future retry. v1 limitation; if the original attempt failed, the user is responsible for clearing the key out of band.
- Dedupe is race-free under `Concurrency(n)`: the `IdempotencyStore.Add` contract requires atomic check-and-set.

`RunSummary.EffectStats[stage].Deduped` exposes the number of skipped items per stage.
```

- [ ] **Step 2: Update the `EffectStats` paragraph in `doc/run-summary.md`**

Find the `EffectStats` paragraph (added by the previous shipped feature). The existing text describes `Required`, `Success`, `Failure`. Append a sentence:

After:

```
…each value carries `Required bool` (`true` for the default required policy or explicit `Required()`; `false` for `BestEffort()`), `Success int64` (count of items that produced a non-error outcome), and `Failure int64` (count of items whose effect exhausted retries with a terminal error).
```

Insert before the closing sentence about Metrics.Stages:

```
A fourth field, `Deduped int64`, counts items skipped because their idempotency key matched a previously-recorded invocation; see [Idempotency-key dedupe](operators.md#idempotency-key-dedupe).
```

- [ ] **Step 3: Mark roadmap item complete**

In `doc/roadmap.md`, find the line:

```markdown
- [ ] **Idempotency-key-driven deduplication for `Effect`**: ...
```

Replace with:

```markdown
- [x] **Idempotency-key-driven deduplication for `Effect`** *(shipped 2026-04-26)*: New `IdempotencyStore` interface (`Add(ctx, key) (firstTime, err)`). When `EffectPolicy.Idempotent` is true with a non-nil `IdempotencyKey`, the operator dedupes items whose key matches a previously-recorded invocation: the effect function is not called and the outcome is emitted with `Deduped: true`. Default per-Run in-memory store auto-attached; user-supplied `EffectPolicy.IdempotencyStore` enables cross-Run dedupe. `EffectOutcome.Deduped` and `EffectStats.Deduped` surface the count. Plan at `docs/superpowers/plans/2026-04-26-effect-idempotency-dedupe.md`.
```

- [ ] **Step 4: Append the memory note**

Append to `~/.claude/projects/-Users-jonathan-projects-go-kitsune/memory/project_higher_level_authoring.md`:

```markdown
Idempotency-key dedupe for `Effect` is now wired: `EffectPolicy.Idempotent + IdempotencyKey` triggers an auto-attached per-Run `IdempotencyStore` (or user-supplied for cross-Run); duplicates emit `EffectOutcome{Deduped: true}` and increment `EffectStats.Deduped` (2026-04-26).
```

The memory file is outside the repo; do NOT commit it.

- [ ] **Step 5: Commit docs and roadmap**

```bash
git add doc/operators.md doc/run-summary.md doc/roadmap.md
git commit -m "$(cat <<'EOF'
docs: document Effect idempotency dedupe; mark roadmap item complete

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Final verification

- [ ] **Step 1: Full short test suite**

Run: `task test`
Expected: PASS.

- [ ] **Step 2: Race**

Run: `task test:race`
Expected: PASS, no races.

- [ ] **Step 3: Property tests**

Run: `task test:property`
Expected: PASS.

- [ ] **Step 4: Examples**

Run: `task test:examples`
Expected: PASS.

- [ ] **Step 5: Inform the user**

Concise report: number of commits, gates passed, one-line summary of what's now wired.

---

## Self-review

- **Spec coverage:**
  - `IdempotencyStore` interface and memory default → Task 1.
  - `EffectPolicy.IdempotencyStore` field, `EffectOutcome.Deduped` field, `effectConfig.idempotencyStore` field, `effectStat.deduped` counter, `EffectStats.Deduped` field, projection wiring → Task 2.
  - Stage-loop dedupe check → Task 3.
  - Five tests (happy path, empty key, user store, store error, concurrent) → Tasks 3 + 4.
  - Godoc updates on `Idempotent` and `IdempotencyKey` (no longer "v1 records this") → Task 2.
  - operators.md / run-summary.md / roadmap / memory → Task 5.
  - Final gates → Task 6.
- **Type-name consistency:** `IdempotencyStore`, `Add(ctx, key) (firstTime bool, err error)`, `memoryIdempotencyStore`, `newMemoryIdempotencyStore`, `effectConfig.idempotencyStore`, `effectStat.deduped`, `recordEffectDeduped`, `EffectOutcome.Deduped`, `EffectStats.Deduped` are used consistently across tasks.
- **No placeholders.** Every step has concrete code or commands.
- **Risk:** Task 3 step 3 inserts code in two locations of `Effect`'s build closure. The diff is anchored on the existing `localCfg := cfg` line and the existing `if !dryRun {` block — both stable in the current code. If those move, find the equivalent.
- **Sequencing:** Task 1 is independent. Task 2 must precede Task 3 (the stage-loop change references the fields plumbed in Task 2). Task 4 depends on Task 3 (uses the dedupe behaviour). Task 5 documents what landed. Task 6 is the final gate.
