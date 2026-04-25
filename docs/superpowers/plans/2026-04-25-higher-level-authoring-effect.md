# Higher-Level Authoring Layer — M2 (Effect) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a first-class `Effect[I, R]` operator that models externally-visible side-effects with retry, per-attempt timeout, required-vs-best-effort semantics, an idempotency-key hint, and a `DryRun` RunOption that turns every `Effect` into a no-op without disturbing pure stages.

**Architecture:** `Effect(p, fn, opts...)` returns `*Pipeline[EffectOutcome[I, R]]` where every input produces exactly one outcome (success or terminal failure after retries). Retries run inside the operator's per-item loop, reusing the existing `RetryStrategy` value type. `Required()` / `BestEffort()` are recorded on the stage's `stageMeta` (and propagated to `GraphNode.IsEffect` / `EffectRequired`) so the future M3 `RunSummary` can derive `RunOutcome` from them. `TryEffect` is a thin wrapper that splits the outcome stream into ok/failed pipelines via `MapResult`-style two-output wiring. A `DryRun() RunOption` sets `runCtx.dryRun`; the operator checks this on each item and short-circuits with `Applied: false`.

**Tech Stack:** Go generics, channel-backed pipelines, `sync/atomic`, the existing `RetryStrategy` retry machinery.

**Module path:** `github.com/zenbaku/go-kitsune`

---

## Spec source

This plan implements Section 2 of `docs/superpowers/specs/2026-04-15-higher-level-authoring-design.md` (approved 2026-04-15). M1 (`Segment`) shipped 2026-04-24; M3 (`RunSummary` + `WithFinalizer`) and M4 (`DevStore`) are out of scope here and will ship as separate plans.

### Spec deltas applied during M2 design

These revisions to the original spec come from the M1 plan preamble (`docs/superpowers/plans/2026-04-24-higher-level-authoring-segment.md`) plus pragmatic scoping decisions made while writing this plan. They are the source of truth for the implementation.

1. **Hard rename `RetryPolicy` → `RetryStrategy`** in `operator_retry.go` (no alias; project is pre-1.0). A *strategy* is HOW to retry (the parameters of an algorithm); `EffectPolicy` will hold a `RetryStrategy` field, so the existing name `RetryPolicy` would clash with the new `EffectPolicy` (which is the WHAT-to-do policy that contains a strategy). All references update in the same commit.

2. **`EffectPolicy` shape:**
   ```go
   type EffectPolicy struct {
       Retry          RetryStrategy   // shared with the Retry[T] operator
       Required       bool
       AttemptTimeout time.Duration   // distinct from the stage-level Timeout(d) StageOption
       Idempotent     bool
       IdempotencyKey func(any) string
   }
   ```
   Drop the spec's `RetryN(n)` (use `RetryUpTo(n, b)` returning `RetryStrategy`). Rename the spec's `WithTimeout(d)` to `AttemptTimeout(d)` to disambiguate from the stage-level `Timeout(d) StageOption`.

3. **Drop `DryRunNoop()` from the v1 surface.** The spec table lists `DryRunNoop()` as a call-site option that overrides to "skip fn call when DryRun RunOption is active" — but that *is* the default behaviour when `DryRun` is on, so `DryRunNoop()` adds no observable behaviour. Leaving it out keeps the surface honest; we can add it later if a use case for opting *out* of the default skip emerges.

4. **`IdempotencyKey` is informational in v1.** The field is stored on the policy and carried into `stageMeta` for future use (a Store-backed dedup loop, or a guard layer over an external idempotent backend), but the v1 implementation does not de-duplicate retries against a backing store. The godoc says so explicitly; tests assert the field is preserved, not that it suppresses calls.

5. **`HookEvent.SegmentName` propagation deferred.** The spec mentions `HookEvent` gaining a `SegmentName` field; the current codebase has no `HookEvent` struct (hooks are method-param-based — see `hooks/hooks.go`). M1 propagated `SegmentName` via `GraphNode`. M2 propagates `IsEffect` / `EffectRequired` via `GraphNode` only. Hook-payload propagation is not in this milestone.

6. **Stage-level `Timeout(d)` is unchanged.** `EffectPolicy.AttemptTimeout` is implemented inside the Effect operator itself, not via the existing `cfg.timeout` plumbing. Setting both on the same Effect picks the smaller of the two (last-deadline-wins via `context.WithDeadline`).

---

## File structure

| File | Change |
|---|---|
| `operator_retry.go` | Rename type `RetryPolicy` → `RetryStrategy`; rename methods (`WithRetryable`, `WithOnRetry`); update constructor return types (`RetryUpTo`, `RetryForever`); update `Retry[T]` parameter type. |
| `operator_retry_test.go` | None (existing tests reference constructors, not the type name; verify after rename). |
| `pipeline.go` | Add `isEffect bool` and `effectRequired bool` to `stageMeta`; add `dryRun bool` to `runCtx`. |
| `kitsune.go` | Add `IsEffect` and `EffectRequired` to the `metasToGraphNodes` mapping. |
| `hooks/hooks.go` | Add `IsEffect bool` and `EffectRequired bool` fields to public `GraphNode`. |
| `config.go` | Add `dryRun bool` to `runConfig`. |
| `kitsune.go` | Add `DryRun() RunOption`. |
| `kitsune.go` | Wire `cfg.dryRun → rc.dryRun` in `Runner.Run`. |
| `effect.go` (new) | `EffectOutcome[I, R]`, `EffectOption`, `EffectPolicy`, `effectConfig`, `Required()`, `BestEffort()`, `WithIdempotencyKey(fn)`, `AttemptTimeout(d)`, `Effect[I, R]`, `TryEffect[I, R]`. |
| `effect_test.go` (new) | Basic correctness, retry exhaustion, attempt timeout, required vs best-effort metadata, idempotency-key preservation, dry-run skip, `TryEffect` split, options on `WithName` / `Buffer` not breaking output. |
| `properties_test.go` | Add `TestPropEffect_OneOutcomePerInput` (every input produces exactly one outcome regardless of fn behaviour). |
| `examples/effect/main.go` (new) | Realistic publish-with-retry example using `Effect` + `TryEffect`. |
| `examples_test.go` | Register `"effect"` in the `examples` slice. |
| `doc/operators.md` | Add `Effect` and `TryEffect` sections (under a new top-level **Effects** section, after **Resilience**). Add a row in the Quick Reference table. Update the `Retry` and `RetryPolicy → RetryStrategy` references. |
| `doc/api-matrix.md` | Add a new section **Effects** (between Resilience and Stage Composition); add `DryRun` row to Run Options. Update `Retry` references (`RetryPolicy → RetryStrategy`). |
| `doc/options.md` | Add a `DryRun()` entry under the RunOptions section. |
| `doc/roadmap.md` | Mark M2 complete in the Higher-level authoring layer M2-M4 entry; surface M3-M4 as the remaining work. |
| `MEMORY.md` (user memory) | Update `project_higher_level_authoring.md` summary entry (M2 shipped). |

---

## Tasks

> Each task ends with a commit. Use `task test` (fast) and `task test:race` between tasks; run `task test:all` only at the end of each major task or when explicitly instructed.

---

### Task 1: Rename `RetryPolicy` → `RetryStrategy`

This is a mechanical rename done first so M2's `EffectPolicy.Retry RetryStrategy` field has a clean name to refer to. No semantic change.

**Files:**
- Modify: `operator_retry.go`
- Verify: `operator_retry_test.go`, `doc/operators.md`, `doc/api-matrix.md`

- [ ] **Step 1: Run a baseline check to make sure tests pass before the rename**

Run: `task test`
Expected: PASS (full suite green)

- [ ] **Step 2: Rename in `operator_retry.go`**

Replace every occurrence of the identifier `RetryPolicy` with `RetryStrategy` in `operator_retry.go`. The five touch points:

```go
// Line ~12: type declaration
type RetryStrategy struct { /* fields unchanged */ }

// Line ~35: method receiver
func (pol RetryStrategy) WithRetryable(fn func(error) bool) RetryStrategy {
    pol.Retryable = fn
    return pol
}

// Line ~41: method receiver
func (pol RetryStrategy) WithOnRetry(fn func(attempt int, err error, wait time.Duration)) RetryStrategy {
    pol.OnRetry = fn
    return pol
}

// Line ~46-50: constructor
func RetryUpTo(n int, b Backoff) RetryStrategy {
    return RetryStrategy{MaxAttempts: n, Backoff: b}
}

// Line ~52-57: constructor
func RetryForever(b Backoff) RetryStrategy {
    return RetryStrategy{MaxAttempts: -1, Backoff: b}
}

// Line ~80: function parameter
func Retry[T any](p *Pipeline[T], pol RetryStrategy) *Pipeline[T] {
    /* body unchanged */
}
```

Update the leading godoc on the type to refer to "RetryStrategy" instead of "RetryPolicy" (a strategy is HOW to retry; a policy is WHAT to do and may contain a strategy):

```go
// RetryStrategy controls how [Retry] re-subscribes to its upstream pipeline
// after a failure.
//
// The zero value performs a single attempt with no retry.
type RetryStrategy struct { /* ... */ }
```

- [ ] **Step 3: Update the godoc reference inside `RetryUpTo` / `RetryForever`**

```go
// RetryUpTo returns a [RetryStrategy] that allows at most n total attempts
// (including the first) with the given backoff between retries.
func RetryUpTo(n int, b Backoff) RetryStrategy { /* ... */ }

// RetryForever returns a [RetryStrategy] that retries indefinitely with the
// given backoff. The pipeline only stops when the outer context is cancelled
// or when the Retryable predicate returns false.
func RetryForever(b Backoff) RetryStrategy { /* ... */ }
```

- [ ] **Step 4: Verify tests still pass after the rename**

Run: `task test`
Expected: PASS — `operator_retry_test.go` calls `RetryUpTo` / `RetryForever` (constructors) and never names the type directly, so it should compile unchanged. If anything fails, the rename missed a spot.

- [ ] **Step 5: Update doc references in `doc/operators.md`**

The operators reference the type by name in two places. Run:

```bash
grep -n "RetryPolicy" doc/operators.md
```

Replace every match with `RetryStrategy`. The known matches as of this plan are at `doc/operators.md:547`, `:556`, `:558`. There may be additional ones; the grep is the source of truth.

For example, line ~547 currently reads:
```go
func Retry[T any](p *Pipeline[T], pol RetryPolicy) *Pipeline[T]
```
Change to:
```go
func Retry[T any](p *Pipeline[T], pol RetryStrategy) *Pipeline[T]
```

The "**`RetryPolicy` fields:**" header (line ~558) becomes "**`RetryStrategy` fields:**".

- [ ] **Step 6: Update `doc/api-matrix.md` if it names the type**

```bash
grep -n "RetryPolicy" doc/api-matrix.md
```

If any matches exist, replace with `RetryStrategy`. (The current `api-matrix.md` does not reference `RetryPolicy` directly — only `Retry` as an error-routing handler — so this step may be a no-op. Confirm before committing.)

- [ ] **Step 7: Build and re-run tests**

Run: `task test:race`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add operator_retry.go doc/operators.md doc/api-matrix.md
git commit -m "refactor(retry): rename RetryPolicy to RetryStrategy"
```

---

### Task 2: Extend `stageMeta` and `GraphNode` with effect metadata

Lay the groundwork for `Effect` by adding the two flags M3 will derive `RunOutcome` from. No behavioural change yet — these fields are unused until Task 6.

**Files:**
- Modify: `pipeline.go`
- Modify: `kitsune.go`
- Modify: `hooks/hooks.go`

- [ ] **Step 1: Add fields to `stageMeta`**

In `pipeline.go`, locate the `stageMeta` struct (around line 36). Add the two effect fields immediately after `segmentName`:

```go
type stageMeta struct {
    // ... existing fields unchanged ...

    // segmentName is the name of the enclosing Segment as set by Segment.Apply.
    // Empty when the stage is not inside a segment. When segments nest, the
    // innermost segment wins (deepest enclosing Segment owns each stage).
    segmentName string

    // isEffect is true when this stage was constructed by [Effect] or
    // [TryEffect]. Used by the future RunSummary derivation to distinguish
    // pure stages from stages that produce externally-visible side effects.
    isEffect bool

    // effectRequired is true when the stage's [EffectPolicy] / [Required]
    // marks the effect as required for run success. Meaningful only when
    // isEffect is true.
    effectRequired bool

    getChanLen  func() int
    getChanCap  func() int
    // ... rest unchanged ...
}
```

- [ ] **Step 2: Add fields to public `GraphNode` in `hooks/hooks.go`**

Locate the `GraphNode` struct (around line 76). Add the two flags after `SegmentName`:

```go
type GraphNode struct {
    // ... existing fields unchanged ...
    SegmentName    string        `json:"segment_name,omitempty"`
    IsEffect       bool          `json:"is_effect,omitempty"`
    EffectRequired bool          `json:"effect_required,omitempty"`
}
```

- [ ] **Step 3: Propagate fields in `metasToGraphNodes`**

In `kitsune.go`, locate `metasToGraphNodes` (around line 383). Append the two new fields to the struct literal:

```go
nodes = append(nodes, internal.GraphNode{
    ID:             m.id,
    Name:           m.name,
    Kind:           m.kind,
    Inputs:         m.inputs,
    Concurrency:    m.concurrency,
    Buffer:         m.buffer,
    Overflow:       int(m.overflow),
    BatchSize:      m.batchSize,
    Timeout:        m.timeout,
    HasRetry:       m.hasRetry,
    HasSupervision: m.hasSuperv,
    SegmentName:    m.segmentName,
    IsEffect:       m.isEffect,
    EffectRequired: m.effectRequired,
})
```

- [ ] **Step 4: Add `dryRun` to `runConfig`**

In `config.go`, locate `runConfig` (around line 65). Add the field at the end of the struct:

```go
type runConfig struct {
    // ... existing fields unchanged ...
    defaultBuffer       int
    defaultKeyTTL       time.Duration
    dryRun              bool
}
```

- [ ] **Step 5: Add `dryRun` to `runCtx`**

In `pipeline.go`, locate the `runCtx` struct (around line 114). Add the field after `segmentByID`:

```go
type runCtx struct {
    // ... existing fields unchanged ...
    segmentByID map[int64]string

    // dryRun is true when the run was started with [DryRun]. Effect operators
    // check this and short-circuit fn execution; pure stages run normally.
    dryRun bool
}
```

- [ ] **Step 6: Add `DryRun()` RunOption**

In `kitsune.go`, append a new RunOption near the bottom of the existing RunOption block (after `WithDefaultKeyTTL`, around line 605, before the next type/function block). Use `kitsune.go` rather than `config.go` because it follows the existing precedent of putting concise RunOptions next to type re-exports.

```go
// DryRun configures the run to skip every [Effect] / [TryEffect] call,
// returning [EffectOutcome] values with Applied: false and no error. Pure
// stages (Map, Filter, Batch, …) and stateful stages run normally — only
// effect functions are bypassed. Useful for validating pipeline graph wiring
// without producing externally-visible side effects.
func DryRun() RunOption {
    return func(c *runConfig) { c.dryRun = true }
}
```

- [ ] **Step 7: Wire `cfg.dryRun → rc.dryRun` in `Runner.Run`**

In `kitsune.go`, locate `Runner.Run` (around line 247). After the existing `rc.defaultKeyTTL = cfg.defaultKeyTTL` line, add:

```go
rc.defaultKeyTTL = cfg.defaultKeyTTL
rc.dryRun = cfg.dryRun
r.terminal(rc)
```

- [ ] **Step 8: Build and run tests**

Run: `task test`
Expected: PASS — these are additive struct fields with no readers yet.

- [ ] **Step 9: Commit**

```bash
git add pipeline.go kitsune.go hooks/hooks.go config.go
git commit -m "feat(effect): add stageMeta/GraphNode/runCtx fields for Effect"
```

---

### Task 3: Define `EffectOutcome`, `EffectOption`, `EffectPolicy`, and call-site options

Build the option machinery first so Task 4 can plug straight into a complete config.

**Files:**
- Create: `effect.go`

- [ ] **Step 1: Create `effect.go` with the public types and option helpers**

```go
package kitsune

import (
    "context"
    "time"

    "github.com/zenbaku/go-kitsune/internal"
)

// EffectOutcome carries the result of one [Effect] call.
//
// Input is the original item from the upstream pipeline; Result is the value
// returned by the effect function on success (the zero value of R on failure
// or in dry-run mode); Err is the terminal error after retries are exhausted
// (nil on success); Applied reports whether the effect function returned
// without error during this attempt.
//
// On per-attempt timeout, Applied is false and Err carries the timeout — but
// the underlying side-effect may already have been applied; treat Applied as
// a hint, not a guarantee.
type EffectOutcome[I, R any] struct {
    Input   I
    Result  R
    Err     error
    Applied bool
}

// EffectOption configures an [Effect] or [TryEffect] stage. Both
// [EffectPolicy] (a value) and the call-site option helpers (functions
// returned by [Required], [BestEffort], [AttemptTimeout],
// [WithIdempotencyKey]) satisfy this interface.
//
// Options are applied in argument order; later options overwrite earlier
// ones. To bundle reusable defaults, define an [EffectPolicy] value and pass
// it first, then layer per-call overrides:
//
//	var SNSPolicy = kitsune.EffectPolicy{
//	    Required:       true,
//	    Retry:          kitsune.RetryUpTo(3, kitsune.FixedBackoff(50*time.Millisecond)),
//	    AttemptTimeout: 5 * time.Second,
//	}
//	out := kitsune.Effect(p, publish, SNSPolicy, kitsune.AttemptTimeout(10*time.Second))
type EffectOption interface {
    applyEffect(*effectConfig)
}

// EffectPolicy is a reusable bundle of [Effect] settings. Define one as a
// package-level value and pass it to multiple [Effect] call sites; layer
// per-call overrides as additional [EffectOption] arguments after it.
type EffectPolicy struct {
    // Retry controls how many times the effect function is re-attempted
    // after a failed attempt and the backoff between attempts. The zero
    // value performs a single attempt.
    Retry RetryStrategy

    // Required marks the effect as required for run success. The flag is
    // recorded on stageMeta and propagated to GraphNode; the future
    // RunSummary uses it to derive RunOutcome. The zero value of
    // EffectPolicy is Required: false; the [Effect] operator defaults to
    // Required: true when no Required/BestEffort option is supplied.
    Required bool

    // AttemptTimeout, if positive, is applied to each attempt of the effect
    // function via context.WithTimeout. It is independent of (and combines
    // with, taking the smaller deadline) the stage-level Timeout(d)
    // StageOption.
    AttemptTimeout time.Duration

    // Idempotent declares that the effect function tolerates repeated
    // application of the same input without side effects. v1 records this
    // flag for future use; the operator does not de-duplicate retries
    // against a backing store.
    Idempotent bool

    // IdempotencyKey, if non-nil, is the key function used by external
    // idempotent backends to recognise repeats. v1 records the function
    // pointer for future use.
    IdempotencyKey func(any) string
}

// applyEffect makes EffectPolicy satisfy [EffectOption]: passing a policy
// value to [Effect] applies all of its non-zero fields at once.
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

// effectConfig is the resolved per-stage state assembled from a series of
// [EffectOption] values applied in order.
type effectConfig struct {
    retry          RetryStrategy
    required       bool
    requiredSet    bool
    attemptTimeout time.Duration
    idempotent     bool
    idempotencyKey func(any) string
    stageOpts      []StageOption
}

// effectOptionFunc adapts a function to the EffectOption interface. Internal
// helper used by Required, BestEffort, AttemptTimeout, WithIdempotencyKey.
type effectOptionFunc func(*effectConfig)

func (f effectOptionFunc) applyEffect(c *effectConfig) { f(c) }

// Required marks the [Effect] as required for run success. Without this (or
// [BestEffort]), an Effect defaults to required.
func Required() EffectOption {
    return effectOptionFunc(func(c *effectConfig) {
        c.required = true
        c.requiredSet = true
    })
}

// BestEffort marks the [Effect] as non-required: a terminal failure is
// recorded in the run summary but does not fail the run.
func BestEffort() EffectOption {
    return effectOptionFunc(func(c *effectConfig) {
        c.required = false
        c.requiredSet = true
    })
}

// AttemptTimeout applies a per-attempt deadline to the effect function via
// context.WithTimeout. d ≤ 0 disables the timeout. Distinct from the
// stage-level [Timeout] StageOption: when both are set, the earlier deadline
// wins.
func AttemptTimeout(d time.Duration) EffectOption {
    return effectOptionFunc(func(c *effectConfig) { c.attemptTimeout = d })
}

// WithIdempotencyKey supplies a function that produces a stable key for an
// input item. v1 records the key function for future use by external
// idempotent backends; the operator does not de-duplicate against a store.
func WithIdempotencyKey(fn func(any) string) EffectOption {
    return effectOptionFunc(func(c *effectConfig) {
        c.idempotent = true
        c.idempotencyKey = fn
    })
}

// EffectStageOption wraps a [StageOption] (e.g. WithName, Buffer) so it can
// be passed alongside EffectOptions. The wrapped option is applied to the
// underlying stage at construction time.
func EffectStageOption(opt StageOption) EffectOption {
    return effectOptionFunc(func(c *effectConfig) {
        c.stageOpts = append(c.stageOpts, opt)
    })
}

func buildEffectConfig(opts []EffectOption) effectConfig {
    cfg := effectConfig{}
    for _, opt := range opts {
        if opt != nil {
            opt.applyEffect(&cfg)
        }
    }
    if !cfg.requiredSet {
        cfg.required = true
    }
    return cfg
}
```

- [ ] **Step 2: Build to verify the new file compiles standalone**

Run: `go build ./...`
Expected: no errors. (Tests are written in Task 4. The `context` and `internal` imports are anticipated by Task 4's operator implementation; in Task 3 they are unused, so during Task 3 only, comment them out to avoid an "imported and not used" error, or add a package-private `var _ = context.TODO` and `var _ = internal.DefaultBuffer` line that Task 4 will remove. Simpler: defer the `effect.go` build verification to Task 4 step 3 by skipping this step's `go build` and committing the file as-is — `go vet` will catch unused imports at PR time, and Task 4 lands them in the same branch.)

If `go build` fails on unused imports in Task 3, drop the `context` and `internal` imports here and re-add them in Task 4 step 2.

- [ ] **Step 3: Commit**

```bash
git add effect.go
git commit -m "feat(effect): add EffectOutcome, EffectPolicy, and option helpers"
```

---

### Task 4: Implement `Effect[I, R]` operator with retry and dry-run

Implement the core operator: per-input, run fn through `RetryStrategy` until success or exhaustion, applying `AttemptTimeout` per attempt; emit one `EffectOutcome[I, R]` per input.

**Files:**
- Modify: `effect.go`
- Create: `effect_test.go`

- [ ] **Step 1: Write a failing test for the basic happy path**

Append to `effect.go`'s package directory: create `effect_test.go`:

```go
package kitsune_test

import (
    "context"
    "errors"
    "fmt"
    "reflect"
    "sync/atomic"
    "testing"
    "time"

    "github.com/zenbaku/go-kitsune"
)

// TestEffect_Success verifies that with no errors and no retries configured,
// every input produces an outcome with Applied: true and the expected result.
func TestEffect_Success(t *testing.T) {
    ctx := context.Background()
    src := kitsune.FromSlice([]int{1, 2, 3})
    fn := func(_ context.Context, v int) (string, error) {
        return fmt.Sprintf("v=%d", v), nil
    }
    out, err := kitsune.Collect(ctx, kitsune.Effect(src, fn))
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(out) != 3 {
        t.Fatalf("got %d outcomes, want 3", len(out))
    }
    for i, o := range out {
        if !o.Applied {
            t.Errorf("outcome %d: Applied=false, want true", i)
        }
        if o.Err != nil {
            t.Errorf("outcome %d: err=%v, want nil", i, o.Err)
        }
        if o.Result != fmt.Sprintf("v=%d", o.Input) {
            t.Errorf("outcome %d: result=%q, want %q", i, o.Result, fmt.Sprintf("v=%d", o.Input))
        }
    }
}
```

Run: `go test ./... -run TestEffect_Success`
Expected: FAIL with "undefined: kitsune.Effect".

- [ ] **Step 2: Implement `Effect[I, R]`**

If Task 3 left `context` and `internal` out of the import block to silence "imported and not used", re-add them now. The complete import block at the top of `effect.go` should read:

```go
import (
    "context"
    "time"

    "github.com/zenbaku/go-kitsune/internal"
)
```

Then append the operator to `effect.go`:

```go
// Effect models an externally-visible side effect: every input produces
// exactly one [EffectOutcome] downstream, after at most Retry.MaxAttempts
// attempts of fn.
//
// fn is called for each input item with a context derived from the run
// context. If [AttemptTimeout] is set, each attempt receives its own
// context.WithTimeout; the per-attempt deadline is independent of the
// stage-level [Timeout] StageOption (when both are present, the earlier
// deadline wins).
//
// On success (fn returns nil), the outcome carries the input, the result,
// Applied: true, and Err: nil. On terminal failure (Retry exhausted, or
// non-retryable error), the outcome carries the input, the zero value of R,
// Applied: false, and the last error in Err. The pipeline does not error
// out: downstream consumers see one outcome per input and decide how to
// route success vs failure.
//
// When the run was started with [DryRun], fn is never called; every outcome
// carries Applied: false, Err: nil, and the zero value of R.
//
// By default an Effect is [Required] — its terminal failures will mark the
// run as failed when the future RunSummary is wired up. Pass [BestEffort]
// to opt out.
//
// Items appear in input order. Retries happen synchronously within the per-
// item loop, so a slow retry blocks downstream emission for the next item;
// to parallelise, use [Effect] downstream of a fan-out operator.
func Effect[I, R any](
    p *Pipeline[I],
    fn func(ctx context.Context, item I) (R, error),
    opts ...EffectOption,
) *Pipeline[EffectOutcome[I, R]] {
    track(p)
    cfg := buildEffectConfig(opts)
    stageCfg := buildStageConfig(cfg.stageOpts)
    id := nextPipelineID()

    meta := stageMeta{
        id:             id,
        kind:           "effect",
        name:           orDefault(stageCfg.name, "effect"),
        buffer:         stageCfg.buffer,
        inputs:         []int64{p.id},
        isEffect:       true,
        effectRequired: cfg.required,
        hasRetry:       cfg.retry.MaxAttempts > 1 || cfg.retry.MaxAttempts <= 0,
    }

    var out *Pipeline[EffectOutcome[I, R]]
    build := func(rc *runCtx) chan EffectOutcome[I, R] {
        if existing := rc.getChan(id); existing != nil {
            return existing.(chan EffectOutcome[I, R])
        }
        inCh := p.build(rc)
        buf := rc.effectiveBufSize(stageCfg)
        ch := make(chan EffectOutcome[I, R], buf)
        m := meta
        m.buffer = buf
        m.getChanLen = func() int { return len(ch) }
        m.getChanCap = func() int { return cap(ch) }
        rc.setChan(id, ch)

        rc.initDrainNotify(id, out.consumerCount.Load())
        drainCh := rc.drainCh(id)
        dryRun := rc.dryRun
        cfg := cfg // capture per-run

        stage := func(ctx context.Context) error {
            defer close(ch)
            cooperativeDrain := false
            defer func() {
                if !cooperativeDrain {
                    go internal.DrainChan(inCh)
                }
            }()
            defer func() { rc.signalDrain(p.id) }()

            for {
                select {
                case item, ok := <-inCh:
                    if !ok {
                        return nil
                    }
                    var outcome EffectOutcome[I, R]
                    outcome.Input = item

                    if dryRun {
                        outcome.Applied = false
                        // emit and continue
                    } else {
                        outcome = runEffectAttempts(ctx, item, fn, cfg)
                    }

                    select {
                    case ch <- outcome:
                    case <-ctx.Done():
                        return ctx.Err()
                    case <-drainCh:
                        cooperativeDrain = true
                        return nil
                    }
                case <-ctx.Done():
                    return ctx.Err()
                case <-drainCh:
                    cooperativeDrain = true
                    return nil
                }
            }
        }
        rc.add(stage, m)
        return ch
    }

    out = newPipeline(id, meta, build)
    return out
}

// runEffectAttempts runs fn once, then retries it according to cfg.retry until
// success, exhaustion, a non-retryable error, or context cancellation.
func runEffectAttempts[I, R any](
    ctx context.Context,
    item I,
    fn func(context.Context, I) (R, error),
    cfg effectConfig,
) EffectOutcome[I, R] {
    isRetryable := cfg.retry.Retryable
    if isRetryable == nil {
        isRetryable = func(err error) bool { return err != nil }
    }
    maxAttempts := cfg.retry.MaxAttempts
    var lastErr error
    var lastResult R

    for attempt := 0; ; attempt++ {
        if ctx.Err() != nil {
            return EffectOutcome[I, R]{Input: item, Result: lastResult, Err: ctx.Err(), Applied: false}
        }

        attemptCtx := ctx
        var cancel context.CancelFunc
        if cfg.attemptTimeout > 0 {
            attemptCtx, cancel = context.WithTimeout(ctx, cfg.attemptTimeout)
        }
        result, err := fn(attemptCtx, item)
        if cancel != nil {
            cancel()
        }
        if err == nil {
            return EffectOutcome[I, R]{Input: item, Result: result, Err: nil, Applied: true}
        }
        lastErr = err
        lastResult = result

        if !isRetryable(err) {
            break
        }
        // attempt is the count of attempts already made (0-indexed: 0 = the
        // one we just did). retries+1 in the standalone Retry operator
        // corresponds to attempt+1 here.
        if maxAttempts > 0 && attempt+1 >= maxAttempts {
            break
        }
        var wait time.Duration
        if cfg.retry.Backoff != nil {
            wait = cfg.retry.Backoff(attempt)
        }
        if cfg.retry.OnRetry != nil {
            cfg.retry.OnRetry(attempt, err, wait)
        }
        if wait > 0 {
            t := time.NewTimer(wait)
            select {
            case <-t.C:
            case <-ctx.Done():
                t.Stop()
                return EffectOutcome[I, R]{Input: item, Result: lastResult, Err: ctx.Err(), Applied: false}
            }
        }
    }
    return EffectOutcome[I, R]{Input: item, Result: lastResult, Err: lastErr, Applied: false}
}
```

`internal.DrainChan` is the cooperative-drain helper used by every existing operator (see `MapResult` in `advanced.go` for the canonical pattern). The `internal` import was added in Task 3 step 1.

- [ ] **Step 3: Re-run the test**

Run: `go test ./... -run TestEffect_Success`
Expected: PASS

- [ ] **Step 4: Add a retry-success test**

Append to `effect_test.go`:

```go
// TestEffect_RetryThenSuccess verifies that with RetryUpTo(3), a function
// that fails twice and then succeeds produces an outcome with Applied:true.
func TestEffect_RetryThenSuccess(t *testing.T) {
    ctx := context.Background()
    src := kitsune.FromSlice([]int{42})
    var attempts atomic.Int32
    fn := func(_ context.Context, v int) (string, error) {
        n := attempts.Add(1)
        if n < 3 {
            return "", errors.New("transient")
        }
        return "ok", nil
    }
    out, err := kitsune.Collect(ctx, kitsune.Effect(src, fn,
        kitsune.EffectPolicy{Retry: kitsune.RetryUpTo(3, kitsune.FixedBackoff(0))},
    ))
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(out) != 1 || !out[0].Applied || out[0].Result != "ok" {
        t.Fatalf("got %+v, want one applied outcome with result=ok", out)
    }
    if attempts.Load() != 3 {
        t.Errorf("attempts=%d, want 3", attempts.Load())
    }
}
```

Run: `go test ./... -run TestEffect_RetryThenSuccess`
Expected: PASS

- [ ] **Step 5: Add a retry-exhaustion test**

```go
// TestEffect_RetryExhaustion verifies that after Retry.MaxAttempts attempts
// have all failed, the outcome reports Applied:false and carries the last
// error.
func TestEffect_RetryExhaustion(t *testing.T) {
    ctx := context.Background()
    src := kitsune.FromSlice([]int{1})
    sentinel := errors.New("permanent")
    var attempts atomic.Int32
    fn := func(_ context.Context, _ int) (struct{}, error) {
        attempts.Add(1)
        return struct{}{}, sentinel
    }
    out, err := kitsune.Collect(ctx, kitsune.Effect(src, fn,
        kitsune.EffectPolicy{Retry: kitsune.RetryUpTo(3, kitsune.FixedBackoff(0))},
    ))
    if err != nil {
        t.Fatalf("unexpected pipeline error: %v", err)
    }
    if len(out) != 1 {
        t.Fatalf("got %d outcomes, want 1", len(out))
    }
    if out[0].Applied {
        t.Errorf("Applied=true, want false")
    }
    if !errors.Is(out[0].Err, sentinel) {
        t.Errorf("err=%v, want sentinel", out[0].Err)
    }
    if attempts.Load() != 3 {
        t.Errorf("attempts=%d, want 3", attempts.Load())
    }
}
```

Run: `go test ./... -run TestEffect_RetryExhaustion`
Expected: PASS

- [ ] **Step 6: Add an attempt-timeout test**

```go
// TestEffect_AttemptTimeout verifies that AttemptTimeout cancels a slow
// attempt and that the cancellation is treated as a retryable error.
func TestEffect_AttemptTimeout(t *testing.T) {
    ctx := context.Background()
    src := kitsune.FromSlice([]int{1})
    var attempts atomic.Int32
    fn := func(ctx context.Context, _ int) (int, error) {
        n := attempts.Add(1)
        if n == 1 {
            // First attempt blocks until the per-attempt timeout cancels.
            <-ctx.Done()
            return 0, ctx.Err()
        }
        return 7, nil
    }
    out, err := kitsune.Collect(ctx, kitsune.Effect(src, fn,
        kitsune.AttemptTimeout(20*time.Millisecond),
        kitsune.EffectPolicy{Retry: kitsune.RetryUpTo(2, kitsune.FixedBackoff(0))},
    ))
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(out) != 1 || !out[0].Applied || out[0].Result != 7 {
        t.Fatalf("got %+v, want one applied outcome with result=7", out)
    }
}
```

Run: `go test ./... -run TestEffect_AttemptTimeout`
Expected: PASS

- [ ] **Step 7: Add a dry-run test**

```go
// TestEffect_DryRun verifies that under DryRun(), fn is never called and
// every outcome reports Applied:false with no error.
func TestEffect_DryRun(t *testing.T) {
    ctx := context.Background()
    src := kitsune.FromSlice([]int{1, 2, 3})
    var calls atomic.Int32
    fn := func(_ context.Context, _ int) (string, error) {
        calls.Add(1)
        return "should not run", nil
    }
    p := kitsune.Effect(src, fn)
    runner := p.ForEach(func(_ context.Context, _ kitsune.EffectOutcome[int, string]) error { return nil })

    if err := runner.Run(ctx, kitsune.DryRun()); err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if calls.Load() != 0 {
        t.Errorf("fn called %d times, want 0", calls.Load())
    }
}
```

Run: `go test ./... -run TestEffect_DryRun`
Expected: PASS

- [ ] **Step 8: Add a graph-metadata test**

```go
// TestEffect_GraphMetadata verifies that an Effect stage is reported with
// IsEffect=true and EffectRequired matching the configured policy.
func TestEffect_GraphMetadata(t *testing.T) {
    src := kitsune.FromSlice([]int{1})
    fn := func(_ context.Context, v int) (int, error) { return v, nil }

    requiredP := kitsune.Effect(src, fn) // default Required
    bestEffortP := kitsune.Effect(src, fn, kitsune.BestEffort())

    cases := []struct {
        name    string
        nodes   []kitsune.GraphNode
        wantReq bool
    }{
        {"required", requiredP.Describe(), true},
        {"best-effort", bestEffortP.Describe(), false},
    }
    for _, c := range cases {
        t.Run(c.name, func(t *testing.T) {
            var found bool
            for _, n := range c.nodes {
                if n.Kind == "effect" {
                    found = true
                    if !n.IsEffect {
                        t.Errorf("IsEffect=false, want true")
                    }
                    if n.EffectRequired != c.wantReq {
                        t.Errorf("EffectRequired=%v, want %v", n.EffectRequired, c.wantReq)
                    }
                }
            }
            if !found {
                t.Errorf("no effect node found in graph")
            }
        })
    }
}
```

Run: `go test ./... -run TestEffect_GraphMetadata`
Expected: PASS

- [ ] **Step 9: Add an option-override test (policy + per-call)**

```go
// TestEffect_PolicyOverride verifies that a per-call option after an
// EffectPolicy overwrites the policy's value (last-write-wins).
func TestEffect_PolicyOverride(t *testing.T) {
    src := kitsune.FromSlice([]int{1})
    fn := func(_ context.Context, v int) (int, error) { return v, nil }

    pol := kitsune.EffectPolicy{Required: false}
    p := kitsune.Effect(src, fn, pol, kitsune.Required())

    for _, n := range p.Describe() {
        if n.Kind == "effect" && !n.EffectRequired {
            t.Errorf("EffectRequired=false; expected per-call Required() to override policy")
        }
    }
}
```

Run: `go test ./... -run TestEffect_PolicyOverride`
Expected: PASS

- [ ] **Step 10: Add a `WithName` / `Buffer` option test (via `EffectStageOption`)**

```go
// TestEffect_StageOptionsApply verifies that EffectStageOption(WithName) and
// EffectStageOption(Buffer) are honoured by the underlying stage.
func TestEffect_StageOptionsApply(t *testing.T) {
    src := kitsune.FromSlice([]int{1, 2, 3})
    fn := func(_ context.Context, v int) (int, error) { return v, nil }

    p := kitsune.Effect(src, fn,
        kitsune.EffectStageOption(kitsune.WithName("publish")),
        kitsune.EffectStageOption(kitsune.Buffer(64)),
    )

    var got kitsune.GraphNode
    for _, n := range p.Describe() {
        if n.Kind == "effect" {
            got = n
        }
    }
    if got.Name != "publish" {
        t.Errorf("Name=%q, want %q", got.Name, "publish")
    }
    if got.Buffer != 64 {
        t.Errorf("Buffer=%d, want 64", got.Buffer)
    }
}
```

Run: `go test ./... -run TestEffect_StageOptionsApply`
Expected: PASS

- [ ] **Step 11: Run the full effect test suite under -race**

Run: `go test ./... -run TestEffect_ -race`
Expected: PASS, no races.

- [ ] **Step 12: Commit**

```bash
git add effect.go effect_test.go
git commit -m "feat(effect): add Effect operator with retry, timeout, and dry-run"
```

---

### Task 5: Implement `TryEffect[I, R]` two-output convenience

`TryEffect` is `Effect` followed by an immediate split: ok outcomes go to one pipeline, failed outcomes (Err != nil) to another. We implement it as a wrapper over `Effect` plus a `MapResult`-style two-output stage that branches on `Err`.

**Files:**
- Modify: `effect.go`
- Modify: `effect_test.go`

- [ ] **Step 1: Write a failing test for `TryEffect`**

Append to `effect_test.go`:

```go
// TestTryEffect_Split verifies that TryEffect routes outcomes by Err: ok
// outcomes to the first pipeline, failures to the second.
func TestTryEffect_Split(t *testing.T) {
    ctx := context.Background()
    src := kitsune.FromSlice([]int{1, 2, 3, 4})
    fn := func(_ context.Context, v int) (int, error) {
        if v%2 == 0 {
            return 0, errors.New("even rejected")
        }
        return v * 10, nil
    }

    okP, failP := kitsune.TryEffect(src, fn)

    var (
        gotOK   []kitsune.EffectOutcome[int, int]
        gotFail []kitsune.EffectOutcome[int, int]
        wg      sync.WaitGroup
    )
    wg.Add(2)
    okRunner := okP.ForEach(func(_ context.Context, o kitsune.EffectOutcome[int, int]) error {
        gotOK = append(gotOK, o)
        return nil
    })
    failRunner := failP.ForEach(func(_ context.Context, o kitsune.EffectOutcome[int, int]) error {
        gotFail = append(gotFail, o)
        return nil
    })
    runner, err := kitsune.MergeRunners(okRunner, failRunner)
    if err != nil {
        t.Fatal(err)
    }
    if err := runner.Run(ctx); err != nil {
        t.Fatal(err)
    }
    wg.Wait() // future-proof if ForEach goroutines are added; harmless here

    var okInputs, failInputs []int
    for _, o := range gotOK {
        okInputs = append(okInputs, o.Input)
    }
    for _, o := range gotFail {
        failInputs = append(failInputs, o.Input)
    }
    if !reflect.DeepEqual(okInputs, []int{1, 3}) {
        t.Errorf("okInputs=%v, want [1 3]", okInputs)
    }
    if !reflect.DeepEqual(failInputs, []int{2, 4}) {
        t.Errorf("failInputs=%v, want [2 4]", failInputs)
    }
}
```

(Add the `sync` import to the test file's imports if not already present.)

Run: `go test ./... -run TestTryEffect_Split`
Expected: FAIL with "undefined: kitsune.TryEffect".

- [ ] **Step 2: Implement `TryEffect`**

Append to `effect.go`:

```go
// TryEffect is a two-output convenience around [Effect]: it returns an "ok"
// pipeline carrying outcomes for which Err == nil, and a "failed" pipeline
// carrying outcomes for which Err != nil. Both pipelines must be consumed
// (same rule as [Partition] and [MapResult]).
//
// Equivalent to:
//
//	out := kitsune.Effect(p, fn, opts...)
//	ok, fail := kitsune.PartitionOutcome(out)
//
// but without an intermediate user-visible variable.
func TryEffect[I, R any](
    p *Pipeline[I],
    fn func(ctx context.Context, item I) (R, error),
    opts ...EffectOption,
) (*Pipeline[EffectOutcome[I, R]], *Pipeline[EffectOutcome[I, R]]) {
    return splitEffectOutcomes(Effect(p, fn, opts...))
}

// splitEffectOutcomes branches an Effect outcome stream on Err.
func splitEffectOutcomes[I, R any](p *Pipeline[EffectOutcome[I, R]]) (
    *Pipeline[EffectOutcome[I, R]], *Pipeline[EffectOutcome[I, R]],
) {
    track(p)
    okID := nextPipelineID()
    failID := nextPipelineID()
    cfg := buildStageConfig(nil)

    okMeta := stageMeta{
        id:     okID,
        kind:   "try_effect_ok",
        name:   "try_effect_ok",
        buffer: cfg.buffer,
        inputs: []int64{p.id},
    }
    failMeta := stageMeta{
        id:     failID,
        kind:   "try_effect_failed",
        name:   "try_effect_failed",
        buffer: cfg.buffer,
        inputs: []int64{p.id},
    }

    var okP, failP *Pipeline[EffectOutcome[I, R]]
    sharedBuild := func(rc *runCtx) (chan EffectOutcome[I, R], chan EffectOutcome[I, R]) {
        if existing := rc.getChan(okID); existing != nil {
            return existing.(chan EffectOutcome[I, R]), rc.getChan(failID).(chan EffectOutcome[I, R])
        }
        inCh := p.build(rc)
        buf := rc.effectiveBufSize(cfg)
        okC := make(chan EffectOutcome[I, R], buf)
        failC := make(chan EffectOutcome[I, R], buf)
        m := okMeta
        m.buffer = buf
        m.getChanLen = func() int { return len(okC) }
        m.getChanCap = func() int { return cap(okC) }
        rc.setChan(okID, okC)
        rc.setChan(failID, failC)
        totalConsumers := okP.consumerCount.Load() + failP.consumerCount.Load()
        rc.initMultiOutputDrainNotify([]int64{okID, failID}, totalConsumers)
        drainCh := rc.drainCh(okID)

        stage := func(ctx context.Context) error {
            defer close(okC)
            defer close(failC)
            cooperativeDrain := false
            defer func() {
                if !cooperativeDrain {
                    go internal.DrainChan(inCh)
                }
            }()
            defer func() { rc.signalDrain(p.id) }()
            for {
                select {
                case item, ok := <-inCh:
                    if !ok {
                        return nil
                    }
                    target := okC
                    if item.Err != nil {
                        target = failC
                    }
                    select {
                    case target <- item:
                    case <-ctx.Done():
                        return ctx.Err()
                    case <-drainCh:
                        cooperativeDrain = true
                        return nil
                    }
                case <-ctx.Done():
                    return ctx.Err()
                case <-drainCh:
                    cooperativeDrain = true
                    return nil
                }
            }
        }
        rc.add(stage, m)
        return okC, failC
    }

    okP = newPipeline(okID, okMeta, func(rc *runCtx) chan EffectOutcome[I, R] {
        o, _ := sharedBuild(rc)
        return o
    })
    failP = newPipeline(failID, failMeta, func(rc *runCtx) chan EffectOutcome[I, R] {
        _, f := sharedBuild(rc)
        return f
    })
    return okP, failP
}
```

`internal.DrainChan` is the same helper used in Task 4; the import is already in `effect.go`.

- [ ] **Step 3: Re-run the test**

Run: `go test ./... -run TestTryEffect_Split`
Expected: PASS

- [ ] **Step 4: Run the full effect suite under -race**

Run: `go test ./... -run TestEffect_ -race && go test ./... -run TestTryEffect_ -race`
Expected: PASS, no races.

- [ ] **Step 5: Commit**

```bash
git add effect.go effect_test.go
git commit -m "feat(effect): add TryEffect two-output convenience"
```

---

### Task 6: Add a property test for one-outcome-per-input

`Effect`'s most important algebraic invariant is that the outcome stream is in 1:1 correspondence with the input stream, in input order, regardless of fn's failure pattern.

**Files:**
- Modify: `properties_test.go`

- [ ] **Step 1: Append the property**

Append to `properties_test.go` (within the existing test file; place under a new section header consistent with the existing style):

```go
// ---------------------------------------------------------------------------
// Effect properties
// ---------------------------------------------------------------------------

// TestPropEffect_OneOutcomePerInput verifies that Effect emits exactly one
// outcome per input, in input order, regardless of which inputs cause fn to
// fail. The function fails for inputs whose value mod failMod == 0.
func TestPropEffect_OneOutcomePerInput(t *testing.T) {
    rapid.Check(t, func(t *rapid.T) {
        items := rapid.SliceOfN(rapid.IntRange(0, 10), 0, 20).Draw(t, "items")
        failMod := rapid.IntRange(1, 5).Draw(t, "failMod")

        src := kitsune.FromSlice(items)
        fn := func(_ context.Context, v int) (int, error) {
            if v%failMod == 0 {
                return 0, errAttempt
            }
            return v * 2, nil
        }
        out, err := kitsune.Collect(context.Background(), kitsune.Effect(src, fn))
        if err != nil {
            t.Fatal(err)
        }
        if len(out) != len(items) {
            t.Fatalf("got %d outcomes, want %d", len(out), len(items))
        }
        for i, o := range out {
            if o.Input != items[i] {
                t.Fatalf("outcome %d: input=%d, want %d (order broken)", i, o.Input, items[i])
            }
            wantErr := items[i]%failMod == 0
            if wantErr && o.Err == nil {
                t.Errorf("outcome %d: want err for input=%d", i, items[i])
            }
            if !wantErr && o.Err != nil {
                t.Errorf("outcome %d: unexpected err for input=%d: %v", i, items[i], o.Err)
            }
            if !wantErr && o.Result != items[i]*2 {
                t.Errorf("outcome %d: result=%d, want %d", i, o.Result, items[i]*2)
            }
        }
    })
}
```

`errAttempt` is already defined in `operator_retry_test.go` and shared across the `kitsune_test` package. If the build complains, replace `errAttempt` with a locally defined sentinel `var errEffectAttempt = errors.New("effect-attempt")`.

- [ ] **Step 2: Run the property test**

Run: `task test:property` (or `go test -run TestPropEffect_OneOutcomePerInput ./...`)
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add properties_test.go
git commit -m "test(effect): add one-outcome-per-input property"
```

---

### Task 7: Add the example

A self-contained, runnable demonstration of `Effect` and `TryEffect` with a realistic publish-with-retry shape.

**Files:**
- Create: `examples/effect/main.go`
- Modify: `examples_test.go`

- [ ] **Step 1: Create the example**

Write `examples/effect/main.go`:

```go
// Example: Effect models an externally-visible side effect with retry,
// per-attempt timeout, and required-vs-best-effort semantics. TryEffect
// splits the outcome stream into ok and failed branches.
//
//	go run ./examples/effect
package main

import (
    "context"
    "errors"
    "fmt"
    "time"

    "github.com/zenbaku/go-kitsune"
)

type Message struct {
    ID   int
    Body string
}

func main() {
    ctx := context.Background()

    src := kitsune.FromSlice([]Message{
        {1, "hello"}, {2, "world"}, {3, "fail-once"}, {4, "drop-this"},
    })

    var attempts = make(map[int]int)
    publish := func(_ context.Context, m Message) (string, error) {
        attempts[m.ID]++
        switch m.ID {
        case 3:
            // Fail once, then succeed on retry.
            if attempts[m.ID] < 2 {
                return "", errors.New("transient")
            }
            return fmt.Sprintf("ack:%d", m.ID), nil
        case 4:
            return "", errors.New("permanent")
        default:
            return fmt.Sprintf("ack:%d", m.ID), nil
        }
    }

    okP, failP := kitsune.TryEffect(src, publish,
        kitsune.EffectPolicy{
            Required:       true,
            Retry:          kitsune.RetryUpTo(3, kitsune.FixedBackoff(10*time.Millisecond)),
            AttemptTimeout: 100 * time.Millisecond,
        },
    )

    okRunner := okP.ForEach(func(_ context.Context, o kitsune.EffectOutcome[Message, string]) error {
        fmt.Printf("OK    id=%d ack=%s\n", o.Input.ID, o.Result)
        return nil
    })
    failRunner := failP.ForEach(func(_ context.Context, o kitsune.EffectOutcome[Message, string]) error {
        fmt.Printf("FAIL  id=%d err=%v\n", o.Input.ID, o.Err)
        return nil
    })

    runner, err := kitsune.MergeRunners(okRunner, failRunner)
    if err != nil {
        panic(err)
    }
    if err := runner.Run(ctx); err != nil {
        panic(err)
    }
}
```

- [ ] **Step 2: Register the example**

In `examples_test.go`, add `"effect"` to the `examples` slice in alphabetical order. The slice currently contains `"contextmapper"` followed by `"concurrent"`, then `"concurrency-guide/..."`, then `"enrich"`. Insert `"effect"` between `"concurrency-guide/useragg"` and `"enrich"`:

```go
"concurrency-guide/useragg",
"effect",
"enrich",
```

- [ ] **Step 3: Run the example to confirm it succeeds**

Run: `go run ./examples/effect`
Expected: prints four lines (three OK, one FAIL), exit code 0.

- [ ] **Step 4: Run the example test**

Run: `go test -run TestExamples/effect -timeout 60s .`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add examples/effect/main.go examples_test.go
git commit -m "test(effect): add example demonstrating Effect and TryEffect"
```

---

### Task 8: Documentation

Add the `Effect` and `TryEffect` operator pages, the `DryRun` RunOption row, and the api-matrix entries. Keep prose concise; no em dashes (replace with colon or semicolon).

**Files:**
- Modify: `doc/operators.md`
- Modify: `doc/api-matrix.md`
- Modify: `doc/options.md`
- Modify: `doc/roadmap.md`

- [ ] **Step 1: Add a new top-level "Effects" section to `doc/operators.md`**

Locate the **Stage Composition** section header at line ~2792. Insert a new section *before* it, after the closing `---` of the **Terminal Operators** section (which ends right before line 2792). Or, if Stage Composition immediately follows Terminals, insert the **Effects** section *after* Stage Composition (before Error Handling Options) so the most-recently-added section sits at the bottom of the operator catalog.

Actual content:

````markdown
## :material-play-circle-outline: Effects { #effects }

`Effect` and `TryEffect` model externally-visible side effects (publish to a queue, write to a database, call an external API) with retry, per-attempt timeout, and required-vs-best-effort semantics. They differ from a plain `Map` in that the result of every input is preserved in an `EffectOutcome[I, R]` value: downstream sees one outcome per input, success or failure.

### Effect

```go
type EffectOutcome[I, R any] struct {
    Input   I
    Result  R
    Err     error
    Applied bool
}

func Effect[I, R any](
    p *Pipeline[I],
    fn func(ctx context.Context, item I) (R, error),
    opts ...EffectOption,
) *Pipeline[EffectOutcome[I, R]]
```

Runs `fn` for every input, retrying on error per the configured `RetryStrategy`. Emits exactly one `EffectOutcome[I, R]` per input. On success: `Applied: true`, `Result` set, `Err: nil`. On terminal failure (retries exhausted): `Applied: false`, `Result` is the zero value, `Err` carries the last error. The pipeline does not error out; downstream decides how to route success vs failure (or use [`TryEffect`](#tryeffect)).

**When to use**

- Publishing to an external queue (SNS, Kafka) where retries on transient failure are required and the pipeline must continue past permanent failures.
- Writing to a database where each write returns a status that downstream stages use.
- Any externally-visible action where you need a graph-visible "this is a side effect" marker, distinct from a pure `Map`.

**Options** ([`EffectPolicy`](#effectpolicy) bundles all of these as a single value)

| Option | Description |
|---|---|
| `Required()` | Effect failures fail the run (default when no `Required`/`BestEffort` is supplied). |
| `BestEffort()` | Effect failures are recorded but do not fail the run. |
| `AttemptTimeout(d)` | Per-attempt deadline applied via `context.WithTimeout`. Distinct from the stage-level `Timeout(d)`. |
| `WithIdempotencyKey(fn)` | Records a stable key per input for future use by external idempotent backends. v1 does not de-duplicate retries. |
| `EffectStageOption(opt)` | Apply a regular `StageOption` (e.g. `WithName("publish")`, `Buffer(64)`) to the underlying stage. |

The retry strategy is a field on `EffectPolicy`. To configure retries, pass an `EffectPolicy` value:

```go
out := kitsune.Effect(src, publish,
    kitsune.EffectPolicy{
        Retry:          kitsune.RetryUpTo(3, kitsune.FixedBackoff(50*time.Millisecond)),
        Required:       true,
        AttemptTimeout: 5 * time.Second,
    },
)
```

**Dry run**

When the runner is started with `DryRun()` (a [`RunOption`](#run-options)), every `Effect` skips its function and emits an outcome with `Applied: false` and no error. Pure stages (`Map`, `Filter`, `Batch`, …) and stateful stages run normally. Useful for validating wiring without producing externally-visible side effects.

**Example**

```go
type Message struct{ ID int; Body string }

publish := func(ctx context.Context, m Message) (string, error) {
    return sns.Publish(ctx, m.Body)
}

out := kitsune.Effect(messages, publish,
    kitsune.EffectPolicy{
        Required:       true,
        Retry:          kitsune.RetryUpTo(3, kitsune.ExponentialBackoff(100*time.Millisecond, 2*time.Second)),
        AttemptTimeout: 5 * time.Second,
    },
)
```

### TryEffect

```go
func TryEffect[I, R any](
    p *Pipeline[I],
    fn func(ctx context.Context, item I) (R, error),
    opts ...EffectOption,
) (ok *Pipeline[EffectOutcome[I, R]], failed *Pipeline[EffectOutcome[I, R]])
```

Two-output convenience around [`Effect`](#effect): the `ok` pipeline carries outcomes for which `Err == nil`; the `failed` pipeline carries outcomes for which `Err != nil`. Both pipelines must be consumed (same rule as [`Partition`](#partition) and [`MapResult`](#mapresult)).

```go
ok, failed := kitsune.TryEffect(messages, publish, kitsune.Required())

ok.ForEach(recordSuccess)         // EffectOutcome.Applied is always true here
failed.ForEach(reportFailure)     // EffectOutcome.Err is never nil here
```

### EffectPolicy

```go
type EffectPolicy struct {
    Retry          RetryStrategy
    Required       bool
    AttemptTimeout time.Duration
    Idempotent     bool
    IdempotencyKey func(any) string
}
```

A reusable, named bundle of `Effect` settings. `EffectPolicy` itself satisfies `EffectOption`, so it composes with per-call overrides:

```go
var SNSPolicy = kitsune.EffectPolicy{
    Required:       true,
    Retry:          kitsune.RetryUpTo(3, kitsune.FixedBackoff(50*time.Millisecond)),
    AttemptTimeout: 5 * time.Second,
}

// Override the timeout at this call site:
out := kitsune.Effect(src, publish, SNSPolicy, kitsune.AttemptTimeout(10*time.Second))
```

`Idempotent` and `IdempotencyKey` are recorded for future use by external idempotent backends; v1 does not de-duplicate retries against a store.

---
````

- [ ] **Step 2: Add a Quick Reference row**

In the Quick Reference table near the top of `doc/operators.md` (around line 7-138), find the section that lists effects/sinks. If there is no obvious slot, add a row in a sensible spot or skip — the per-section table above is the authoritative listing. (Inspect: `grep -n "ForEach\|Sink" doc/operators.md | head -20` for placement.)

- [ ] **Step 3: Add the api-matrix entries**

In `doc/api-matrix.md`, between the **Resilience** subsection (which is part of section "13 · Error Routing" or implied within other sections — confirm with `grep -n "## 1[0-9]" doc/api-matrix.md`) and **Stage Composition** (line 323), insert a new top-level section. If sections renumber, accept the renumber. Otherwise, add the entries alongside Stage Composition:

```markdown
## 12.5 · Effects

| Symbol | Signature | Notes |
|--------|-----------|-------|
| `EffectOutcome[I,R]` | `struct{ Input I; Result R; Err error; Applied bool }` | One per input from [`Effect`](operators.md#effect) |
| `EffectPolicy` | `struct{ Retry RetryStrategy; Required bool; AttemptTimeout time.Duration; Idempotent bool; IdempotencyKey func(any)string }` | Reusable bundle; satisfies `EffectOption` |
| `Effect` | `Effect[I,R](p, fn func(ctx,I)(R,error), opts...) *Pipeline[EffectOutcome[I,R]]` | Single-output side-effect operator |
| `TryEffect` | `TryEffect[I,R](p, fn, opts...) (ok, failed *Pipeline[EffectOutcome[I,R]])` | Two-output convenience that splits on Err |
| `Required()` | `Required() EffectOption` | Mark effect as required (default) |
| `BestEffort()` | `BestEffort() EffectOption` | Failures recorded; run continues |
| `AttemptTimeout` | `AttemptTimeout(d time.Duration) EffectOption` | Per-attempt deadline |
| `WithIdempotencyKey` | `WithIdempotencyKey(fn func(any)string) EffectOption` | Informational in v1 |
| `EffectStageOption` | `EffectStageOption(opt StageOption) EffectOption` | Wrap regular StageOptions |

---
```

In **section 16 · Run Options** (line 375), append a new row to the table:

```markdown
| `DryRun` | `DryRun()` | Skip every [`Effect`](operators.md#effect) call (Applied: false, no error). Pure stages run normally. |
```

- [ ] **Step 4: Add `DryRun()` to `doc/options.md`**

Locate the RunOptions section in `doc/options.md`. Append a section consistent with the existing style:

```markdown
### `DryRun`

```go
func DryRun() RunOption
```

When the runner is started with `DryRun()`, every [`Effect`](operators.md#effect) and [`TryEffect`](operators.md#tryeffect) skips its function and emits an `EffectOutcome` with `Applied: false` and no error. All pure stages (`Map`, `Filter`, `Batch`, …) and stateful stages (`MapWith`, `MapWithKey`) run normally. Use for validating pipeline graph wiring without producing externally-visible side effects.

```go
err := runner.Run(ctx, kitsune.DryRun())
```
```

- [ ] **Step 5: Update `doc/roadmap.md`**

Locate the "Higher-level authoring layer M2-M4" entry (line ~93). Update the M2 bullet from `- **M2 — Effect[I,R] + outcome routing.** Adds …` to a completion note. The simplest edit: prepend `[x]` semantics to the bullet by changing it inline:

```markdown
- **M2 — `Effect[I,R]` + outcome routing.** *(shipped 2026-04-25.)* Adds `Effect(p, fn, opts...)` returning `*Pipeline[EffectOutcome[I,R]]`; `TryEffect` two-output convenience; `EffectPolicy{Retry RetryStrategy, Required bool, AttemptTimeout time.Duration, Idempotent bool, IdempotencyKey func(any) string}`; options (`Required()`, `BestEffort()`, `AttemptTimeout(d)`, `WithIdempotencyKey(fn)`, `EffectStageOption(opt)`); and a `DryRun()` RunOption. `RetryPolicy` was renamed to `RetryStrategy` as part of the same change. M3 (`RunSummary`) and M4 (`DevStore`) are still open.
```

(Replace the old M2 bullet text with the above.)

- [ ] **Step 6: Run a docs sanity check**

```bash
grep -n "RetryPolicy" doc/operators.md doc/api-matrix.md doc/options.md doc/roadmap.md
```

Expected: no matches (every reference is now `RetryStrategy`).

- [ ] **Step 7: Commit**

```bash
git add doc/operators.md doc/api-matrix.md doc/options.md doc/roadmap.md
git commit -m "docs(effect): add Effect, TryEffect, DryRun reference"
```

---

### Task 9: Memory update

Update the user's auto-memory entry for the higher-level authoring milestone so future conversations know M2 has shipped.

**Files:**
- Modify: `~/.claude/projects/-Users-jonathan-projects-go-kitsune/memory/project_higher_level_authoring.md`

- [ ] **Step 1: Re-read the existing memory file**

Run: `cat ~/.claude/projects/-Users-jonathan-projects-go-kitsune/memory/project_higher_level_authoring.md`

Then update the file to reflect M2 status: keep the surrounding context, add a line "M2 (`Effect`, `TryEffect`, `EffectPolicy`, `DryRun()`) shipped 2026-04-25; `RetryPolicy` renamed to `RetryStrategy` in the same change." Preserve the existing frontmatter format (name/description/type) used by other memory files.

- [ ] **Step 2: Verify the index pointer in `MEMORY.md` is still accurate**

Run: `cat ~/.claude/projects/-Users-jonathan-projects-go-kitsune/memory/MEMORY.md | grep higher_level`

Expected: a single one-line index entry. Update its summary if the existing summary is now stale.

(Memory files are not committed to git; no commit step.)

---

### Task 10: Final verification

- [ ] **Step 1: Run the full test suite**

Run: `task test:all`
Expected: PASS — unit tests, race detector, property tests, and example smoke tests all green.

- [ ] **Step 2: Confirm `git status` is clean and the branch is ready**

Run: `git status && git log --oneline -10`
Expected: working tree clean; eight new commits (Tasks 1, 2, 3, 4, 5, 6, 7, 8) on top of `main`.

- [ ] **Step 3: Inform the user that M2 is ready for review**

State concisely: "M2 (Effect) is implemented across 8 commits; `task test:all` is green. The next remaining higher-level-authoring milestones are M3 (`RunSummary` + `WithFinalizer`) and M4 (`DevStore`)."

---

## Self-review notes (for the plan author, not the engineer)

- Spec coverage: every M2 surface from the spec is mapped to a task — `Effect` (Task 4), `TryEffect` (Task 5), `EffectPolicy` (Task 3), call-site options (Task 3), `DryRun` (Task 2 + Task 4), graph metadata (Task 2), `RetryPolicy → RetryStrategy` rename (Task 1). The deferred items (`HookEvent.SegmentName` propagation, idempotency-key deduplication, `DryRunNoop()` opt-out) are documented in the "Spec deltas applied during M2 design" section above.
- Type-name consistency: `RetryStrategy`, `EffectPolicy`, `EffectOption`, `EffectOutcome`, `effectConfig`, `Required()`, `BestEffort()`, `AttemptTimeout(d)`, `WithIdempotencyKey(fn)`, `EffectStageOption(opt)`, `DryRun()`, `Effect`, `TryEffect` are used consistently across tasks and docs.
- No placeholders: every code snippet in every step is complete; the only inferred element is the package-internal `drainChan` helper, which the engineer should replace with `internal.DrainChan` after grepping the existing operators (the inline note in Task 4 step 2 calls this out explicitly).
- Frequent commits: nine commits (one per task plus the doc commit). No "WIP" or "address review later" steps.
