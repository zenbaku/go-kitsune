# Higher-Level Authoring Layer — M1 (Segment) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a named, graph-stamped `Segment[I, O]` type that wraps a `Stage[I, O]`, makes its constituent stages visible as a business unit in `Pipeline.Describe()` and the inspector dashboard, and composes interchangeably with `Stage` via `Then` and `Through`.

**Architecture:** Introduce a `Composable[I, O]` interface that both `Stage[I, O]` (a function type with an existing `Apply` method) and `Segment[I, O]` (a new struct) satisfy. Widen `Then` and `Pipeline.Through` to accept `Composable`. At construction time, `Segment.Apply` snapshots the global stage-ID counter before and after delegating to its inner `Stage`; the IDs in the diff are the segment's children. A wrapper around the output's `build` closure registers those IDs in `runCtx.segmentByID` at run/Describe time, and `runCtx.add` stamps `stageMeta.segmentName` from that map. `metasToGraphNodes` propagates the field to the public `GraphNode`. Nested segments resolve to the innermost name (deepest enclosing segment wins per stage).

**Tech Stack:** Go generics, channel-backed pipelines, `sync/atomic` for the stage-ID counter.

**Module path:** `github.com/zenbaku/go-kitsune`

---

## Spec source

This plan implements Section 1 of `docs/superpowers/specs/2026-04-15-higher-level-authoring-design.md` (approved 2026-04-15). Sections 2–4 (`Effect`, `RunSummary` + `WithFinalizer`, `DevStore`) are out of scope for this plan and ship in later milestones.

## Notes for the M2 (Effects) author

Two design decisions made during M1 design that affect M2. Apply these when planning M2; do **not** apply them in this plan.

1. **Hard rename `RetryPolicy` → `RetryStrategy`** (no alias; project is pre-1.0 with no users). Affects `operator_retry.go:12` (struct), `operator_retry.go:35,41` (`WithRetryable`, `WithOnRetry`), `operator_retry.go:48,55` (`RetryUpTo`, `RetryForever`), `operator_retry.go:80` (`Retry[T]` parameter). Rationale: a *strategy* is HOW to retry (parameters of an algorithm); the *policy* is WHAT to do, and a Policy contains a Strategy.

2. **`EffectPolicy` shape.**
   ```go
   type EffectPolicy struct {
       Retry          RetryStrategy   // shared with the Retry[T] operator
       Required       bool
       AttemptTimeout time.Duration   // distinct from the stage-level Timeout(d) StageOption
       Idempotent     bool
       IdempotencyKey func(any) string
   }
   ```
   Drop `RetryN(n)` from the spec; reuse `RetryUpTo(n, b)` returning `RetryStrategy`. Rename the spec's `WithTimeout(d)` to `AttemptTimeout(d)` to disambiguate from the existing stage-level `Timeout(d)`.

3. **`HookEvent` translation.** The spec mentions `HookEvent` gaining a `SegmentName` field. The current codebase has no `HookEvent` struct; hooks are method-param based (e.g. `GraphHook.OnGraph(nodes []GraphNode)`). M1 propagates `SegmentName` via `GraphNode`. M2 should propagate to `BufferStatus` (in `hooks/hooks.go:91`) and any per-stage hook payloads it touches; that work is **not** in this M1 plan.

---

## File structure

| File | Change |
|---|---|
| `stage.go` | Add `Composable[I, O]` interface; widen `Then` parameter type; widen `Pipeline.Through` parameter type |
| `pipeline.go` | Add `segmentName string` to `stageMeta`; add `segmentByID map[int64]string` to `runCtx`; initialise it in `newRunCtx`; mutate `runCtx.add` to stamp from the map |
| `segment.go` (new) | `Segment[I, O]` struct, `NewSegment`, `Apply`, `SegmentOption` placeholder interface |
| `kitsune.go` | Propagate `SegmentName` in `metasToGraphNodes` |
| `hooks/hooks.go` | Add `SegmentName string` field to public `GraphNode` struct |
| `segment_test.go` (new) | Tests: Composable-Stage satisfaction; Then accepts Composable; Through accepts Composable; Describe propagates SegmentName; single-segment stamps all inner stages and nothing outside; nested innermost wins |
| `examples/segment/main.go` (new) | Self-contained example using two named segments composed with `Then` |
| `examples_test.go` | Register `"segment"` in the examples slice |
| `doc/operators.md` | New "Segment" section under "Stage Composition" (`#stage-composition`) |
| `doc/api-matrix.md` | Add Segment row in the Stage Composition section |
| `doc/roadmap.md` | Mark "First-class composable segment type" Long-term item complete |

---

## Tasks

### Task 1: Add `Composable[I, O]` interface

**Files:**
- Modify: `stage.go`
- Create: `segment_test.go`

- [ ] **Step 1: Write the failing test**

Create `segment_test.go`:

```go
package kitsune_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/zenbaku/go-kitsune"
)

// composableFunc is a non-Stage type that implements Composable, used in
// later tests to verify Then/Through truly accept the interface and not
// just Stage values.
type composableFunc[I, O any] func(*kitsune.Pipeline[I]) *kitsune.Pipeline[O]

func (f composableFunc[I, O]) Apply(p *kitsune.Pipeline[I]) *kitsune.Pipeline[O] {
	return f(p)
}

// TestComposable_StageSatisfies asserts that Stage[I,O] satisfies the
// Composable[I,O] interface. The check is done at compile time via assignment.
func TestComposable_StageSatisfies(t *testing.T) {
	var s kitsune.Stage[int, int] = func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v + 1, nil })
	}
	var c kitsune.Composable[int, int] = s
	got, err := kitsune.Collect(context.Background(), c.Apply(kitsune.FromSlice([]int{1, 2, 3})))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int{2, 3, 4}) {
		t.Errorf("got %v, want [2 3 4]", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails to compile**

Run: `go build ./...`
Expected: FAIL with `undefined: kitsune.Composable`.

- [ ] **Step 3: Add the interface**

Edit `stage.go`. Insert after line 23 (`type Stage[I, O any] func(*Pipeline[I]) *Pipeline[O]`) and before the `Then` declaration:

```go
// Composable is the unifying interface for things that transform a
// *Pipeline[I] into a *Pipeline[O]. Both [Stage] (a function type with an
// Apply method) and [Segment] (a struct) implement Composable, allowing
// them to compose interchangeably with [Then] and [Pipeline.Through].
type Composable[I, O any] interface {
	Apply(p *Pipeline[I]) *Pipeline[O]
}
```

- [ ] **Step 4: Run the test**

Run: `go test -run TestComposable_StageSatisfies .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add stage.go segment_test.go
git commit -m "$(cat <<'EOF'
feat(stage): add Composable[I,O] interface

Stage already has an Apply method, so it satisfies Composable without
modification. Segment (added in a later commit) will also satisfy it,
enabling Then and Through to accept either form.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Widen `Then` to accept `Composable`

**Files:**
- Modify: `stage.go:29`
- Modify: `segment_test.go`

- [ ] **Step 1: Write the failing test**

Append to `segment_test.go`:

```go
// TestComposable_ThenAcceptsComposable verifies Then accepts any value
// implementing Composable[I,M] / Composable[M,O], not only Stage values.
// We pass a composableFunc (non-Stage) on the left and a Stage on the right
// to prove heterogeneous composition works.
func TestComposable_ThenAcceptsComposable(t *testing.T) {
	increment := composableFunc[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v + 1, nil })
	})
	double := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v * 2, nil })
	})

	composed := kitsune.Then[int, int, int](increment, double)
	got, err := kitsune.Collect(context.Background(), composed.Apply(kitsune.FromSlice([]int{1, 2, 3})))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{4, 6, 8}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestComposable_ThenAcceptsComposable .`
Expected: FAIL — `Then` parameters are typed as `Stage[...]`, so `composableFunc` is not assignable.

- [ ] **Step 3: Update `Then` signature**

Edit `stage.go`. Replace the existing `Then`:

```go
func Then[I, M, O any](s Stage[I, M], next Stage[M, O]) Stage[I, O] {
	return func(p *Pipeline[I]) *Pipeline[O] {
		return next(s(p))
	}
}
```

with:

```go
// Then chains two composables: the output of first becomes the input of
// second. Both [Stage] and [Segment] satisfy [Composable], so Then accepts
// either interchangeably:
//
//	validate := kitsune.Then(parse, enrich)              // Stage + Stage
//	pipeline := kitsune.Then(fetchSeg, kitsune.Then(enrichSeg, publishSeg))
//	mixed    := kitsune.Then(parse, enrichSeg)           // Stage + Segment
//
// The returned [Stage] runs first.Apply followed by second.Apply.
func Then[I, M, O any](first Composable[I, M], second Composable[M, O]) Stage[I, O] {
	return func(p *Pipeline[I]) *Pipeline[O] {
		return second.Apply(first.Apply(p))
	}
}
```

- [ ] **Step 4: Run the test plus the existing `TestThen` regression**

Run: `go test -run "TestComposable_ThenAcceptsComposable|TestThen" .`
Expected: PASS for both — existing `TestThen` (`operators2_test.go:680`) uses typed `Stage` values which satisfy `Composable`.

- [ ] **Step 5: Run the whole package**

Run: `task test`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add stage.go segment_test.go
git commit -m "$(cat <<'EOF'
feat(stage): widen Then to accept Composable[I,M] and Composable[M,O]

Stage continues to satisfy Composable via its Apply method, so existing
Then(stage1, stage2) call sites compile unchanged. Segment values added
in a later commit will compose with Stage values via the same Then.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Widen `Pipeline.Through` to accept `Composable`

**Files:**
- Modify: `stage.go:46`
- Modify: `segment_test.go`

- [ ] **Step 1: Write the failing test**

Append to `segment_test.go`:

```go
// TestComposable_ThroughAcceptsComposable verifies Pipeline.Through accepts
// any value implementing Composable[T,T], not only Stage[T,T].
func TestComposable_ThroughAcceptsComposable(t *testing.T) {
	upcase := composableFunc[string, string](func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, s string) (string, error) {
			return strings.ToUpper(s), nil
		})
	})

	out, err := kitsune.Collect(context.Background(),
		kitsune.FromSlice([]string{"hello", "world"}).Through(upcase),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"HELLO", "WORLD"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("got %v, want %v", out, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestComposable_ThroughAcceptsComposable .`
Expected: FAIL — `Through` accepts only `Stage[T, T]`.

- [ ] **Step 3: Update `Through` signature**

Edit `stage.go`. Replace:

```go
func (p *Pipeline[T]) Through(s Stage[T, T]) *Pipeline[T] {
	return s(p)
}
```

with:

```go
// Through applies a same-type composable to the pipeline and returns the
// result. Both [Stage] and [Segment] satisfy [Composable]:
//
//	p.Through(normalize).Through(enrich).ForEach(store).Run(ctx)
func (p *Pipeline[T]) Through(s Composable[T, T]) *Pipeline[T] {
	return s.Apply(p)
}
```

- [ ] **Step 4: Run all tests**

Run: `task test`
Expected: PASS — `examples/stages/main.go:84` and any other call sites use typed `Stage` values which satisfy `Composable`.

- [ ] **Step 5: Commit**

```bash
git add stage.go segment_test.go
git commit -m "$(cat <<'EOF'
feat(stage): widen Pipeline.Through to accept Composable[T,T]

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Add `segmentName` field to `stageMeta`

**Files:**
- Modify: `pipeline.go:36`

- [ ] **Step 1: Add the field**

Edit `pipeline.go`. In the `stageMeta` struct (line 36-71), insert immediately before `getChanLen func() int`:

```go
	// segmentName is the name of the enclosing Segment as set by Segment.Apply.
	// Empty when the stage is not inside a segment. When segments nest, the
	// innermost segment wins (deepest enclosing Segment owns each stage).
	segmentName string
```

- [ ] **Step 2: Verify the package builds**

Run: `go build ./...`
Expected: PASS — the field is unused for now and zero-valued by default.

- [ ] **Step 3: Run the test suite to ensure no regression**

Run: `task test`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pipeline.go
git commit -m "$(cat <<'EOF'
feat(pipeline): add segmentName to stageMeta

Populated at run/Describe time from runCtx.segmentByID (added next).
Empty for non-segment stages.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Add `segmentByID` to `runCtx`; stamp on `runCtx.add`

**Files:**
- Modify: `pipeline.go:110` (`runCtx` struct)
- Modify: `pipeline.go:163` (`newRunCtx`)
- Modify: `pipeline.go:175` (`runCtx.add`)

- [ ] **Step 1: Add `segmentByID` field to `runCtx`**

Edit `pipeline.go`. In the `runCtx` struct (line 110-133), add a new field at the end of the struct (after `drainNotify map[int64]*drainEntry` at line 132):

```go
	// segmentByID maps stage ID → segment name. Populated by Segment.Apply's
	// build wrapper at run/Describe time; consulted by runCtx.add to stamp
	// stageMeta.segmentName before appending to rc.metas.
	segmentByID map[int64]string
```

- [ ] **Step 2: Initialise the map in `newRunCtx`**

Edit `pipeline.go:163-173`. Add `segmentByID: make(map[int64]string),` to the `runCtx` literal:

```go
func newRunCtx() *runCtx {
	done := make(chan struct{})
	var once sync.Once
	return &runCtx{
		chans:       make(map[int64]any),
		drainNotify: make(map[int64]*drainEntry),
		segmentByID: make(map[int64]string),
		refs:        newRefRegistry(),
		done:        done,
		signalDone:  func() { once.Do(func() { close(done) }) },
	}
}
```

- [ ] **Step 3: Stamp `segmentName` in `runCtx.add`**

Edit `pipeline.go:175-178`. Replace:

```go
func (rc *runCtx) add(fn stageFunc, meta stageMeta) {
	rc.stages = append(rc.stages, fn)
	rc.metas = append(rc.metas, meta)
}
```

with:

```go
func (rc *runCtx) add(fn stageFunc, meta stageMeta) {
	if name, ok := rc.segmentByID[meta.id]; ok {
		meta.segmentName = name
	}
	rc.stages = append(rc.stages, fn)
	rc.metas = append(rc.metas, meta)
}
```

- [ ] **Step 4: Run the test suite**

Run: `task test`
Expected: PASS — `segmentByID` is empty for every existing test, so `meta.segmentName` stays empty and behaviour is identical.

- [ ] **Step 5: Commit**

```bash
git add pipeline.go
git commit -m "$(cat <<'EOF'
feat(pipeline): wire runCtx.segmentByID and stamp on rc.add

The map is populated by Segment.Apply (added next) and consulted by
runCtx.add to stamp stageMeta.segmentName for each registered stage.
For non-segment runs the map stays empty and behaviour is unchanged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Add `SegmentName` to public `GraphNode` and propagate

**Files:**
- Modify: `hooks/hooks.go:76`
- Modify: `kitsune.go:383`

- [ ] **Step 1: Add field to public `GraphNode`**

Edit `hooks/hooks.go`. In the `GraphNode` struct (line 76-88), append after `HasSupervision`:

```go
	SegmentName    string        `json:"segment_name,omitempty"`
```

The full struct should read:

```go
type GraphNode struct {
	ID             int64         `json:"id"`
	Name           string        `json:"name"`
	Kind           string        `json:"kind"`
	Inputs         []int64       `json:"inputs"`
	Concurrency    int           `json:"concurrency,omitempty"`
	Buffer         int           `json:"buffer,omitempty"`
	Overflow       int           `json:"overflow,omitempty"`
	BatchSize      int           `json:"batch_size,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty"`
	HasRetry       bool          `json:"has_retry,omitempty"`
	HasSupervision bool          `json:"has_supervision,omitempty"`
	SegmentName    string        `json:"segment_name,omitempty"`
}
```

- [ ] **Step 2: Propagate in `metasToGraphNodes`**

Edit `kitsune.go:383-401`. In the `internal.GraphNode{...}` literal, append after `HasSupervision: m.hasSuperv,`:

```go
			SegmentName:    m.segmentName,
```

- [ ] **Step 3: Run the test suite**

Run: `task test`
Expected: PASS — the new field is empty for existing tests.

- [ ] **Step 4: Commit**

```bash
git add hooks/hooks.go kitsune.go
git commit -m "$(cat <<'EOF'
feat(graph): expose SegmentName on the public GraphNode

Propagated from stageMeta.segmentName via metasToGraphNodes. Empty for
non-segment stages and omitted from JSON output via omitempty.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Implement `Segment[I, O]`, `NewSegment`, and `SegmentOption`

**Files:**
- Create: `segment.go`

- [ ] **Step 1: Write the failing test**

Append to `segment_test.go`:

```go
// TestSegment_GraphNodePropagation verifies SegmentName flows from stageMeta
// to the public GraphNode via Describe.
func TestSegment_GraphNodePropagation(t *testing.T) {
	inner := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v * 2, nil })
	})
	out := kitsune.NewSegment("doubler", inner).Apply(kitsune.FromSlice([]int{1, 2, 3}))

	nodes := out.Describe()
	found := false
	for _, n := range nodes {
		if n.SegmentName == "doubler" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected at least one GraphNode with SegmentName=\"doubler\", got %+v", nodes)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestSegment_GraphNodePropagation .`
Expected: FAIL — `kitsune.NewSegment` undefined.

- [ ] **Step 3: Create `segment.go`**

Create `segment.go`:

```go
package kitsune

import "sync/atomic"

// Segment wraps a [Stage] with a business name. When applied to a pipeline,
// every stage created by the inner Stage is stamped with SegmentName=name in
// its [GraphNode], making the group visible in [Pipeline.Describe] and the
// inspector dashboard.
//
// Segment satisfies [Composable], so it composes with [Then] and
// [Pipeline.Through] interchangeably with [Stage]:
//
//	fetch   := kitsune.NewSegment("fetch-pages",  fetchStage)
//	enrich  := kitsune.NewSegment("enrich-pages", enrichStage)
//	publish := kitsune.NewSegment("publish",      publishStage)
//
//	pipeline := kitsune.Then(kitsune.Then(fetch, enrich), publish)
//
// Nested segments resolve to the innermost name: the deepest enclosing
// Segment owns each stage in the graph. Segments are pure metadata and do
// not affect runtime behaviour.
type Segment[I, O any] struct {
	name  string
	stage Stage[I, O]
	opts  []SegmentOption
}

// SegmentOption is reserved for future per-segment metadata such as
// description, tags, owner, or metric labels. No options are defined in v1.
type SegmentOption interface {
	applySegment(*segmentConfig)
}

type segmentConfig struct{}

// NewSegment returns a Segment named name that delegates Apply to stage.
// opts is reserved; pass nothing in v1.
func NewSegment[I, O any](name string, stage Stage[I, O], opts ...SegmentOption) Segment[I, O] {
	return Segment[I, O]{name: name, stage: stage, opts: opts}
}

// Apply runs the inner stage against p, then arranges for every newly-created
// stage ID to be registered under the segment name in runCtx.segmentByID at
// run/Describe time. runCtx.add stamps stageMeta.segmentName from that map.
func (s Segment[I, O]) Apply(p *Pipeline[I]) *Pipeline[O] {
	before := atomic.LoadInt64(&globalIDSeq)
	output := s.stage.Apply(p)
	after := atomic.LoadInt64(&globalIDSeq)

	if after <= before {
		return output
	}
	ids := make([]int64, 0, after-before)
	for id := before + 1; id <= after; id++ {
		ids = append(ids, id)
	}

	name := s.name
	originalBuild := output.build
	output.build = func(rc *runCtx) chan O {
		// Always overwrite. Build-wrapper composition fires outer first
		// (outer.build runs, then calls originalBuild which is the inner
		// wrapper), so an inner Segment's writes correctly take precedence
		// for IDs in its narrower range. This yields "innermost wins"
		// without explicit guards.
		for _, id := range ids {
			rc.segmentByID[id] = name
		}
		return originalBuild(rc)
	}
	return output
}
```

- [ ] **Step 4: Run the propagation test**

Run: `go test -run TestSegment_GraphNodePropagation .`
Expected: PASS.

- [ ] **Step 5: Verify Segment satisfies Composable**

Append to `segment_test.go`:

```go
// TestSegment_SatisfiesComposable asserts at compile time that Segment[I,O]
// satisfies Composable[I,O].
func TestSegment_SatisfiesComposable(t *testing.T) {
	inner := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v + 1, nil })
	})
	var c kitsune.Composable[int, int] = kitsune.NewSegment("seg", inner)
	got, err := kitsune.Collect(context.Background(), c.Apply(kitsune.FromSlice([]int{1, 2, 3})))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int{2, 3, 4}) {
		t.Errorf("got %v, want [2 3 4]", got)
	}
}
```

- [ ] **Step 6: Run the test**

Run: `go test -run TestSegment_SatisfiesComposable .`
Expected: PASS.

- [ ] **Step 7: Write a single-segment correctness test**

Append to `segment_test.go`:

```go
// TestSegment_SingleSegmentStampsAllInnerStages verifies that every stage
// constructed by the inner Stage receives the segment name, and stages
// outside the segment (the source) do not.
func TestSegment_SingleSegmentStampsAllInnerStages(t *testing.T) {
	inner := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		a := kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v + 1, nil })
		b := kitsune.Filter(a, func(_ context.Context, v int) (bool, error) { return v > 0, nil })
		return kitsune.Map(b, func(_ context.Context, v int) (int, error) { return v * 2, nil })
	})
	src := kitsune.FromSlice([]int{1, 2, 3})
	out := kitsune.NewSegment("transform", inner).Apply(src)

	var inSeg, outSeg int
	for _, n := range out.Describe() {
		switch {
		case n.SegmentName == "transform":
			inSeg++
		case n.SegmentName == "":
			outSeg++
		}
	}
	if inSeg != 3 {
		t.Errorf("expected 3 stages stamped \"transform\" (Map, Filter, Map), got %d", inSeg)
	}
	if outSeg < 1 {
		t.Errorf("expected the FromSlice source to remain unstamped, got %d unstamped", outSeg)
	}
}
```

- [ ] **Step 8: Run the test**

Run: `go test -run TestSegment_SingleSegmentStampsAllInnerStages .`
Expected: PASS.

- [ ] **Step 9: Write a nested-segment innermost-wins test**

Append to `segment_test.go`:

```go
// TestSegment_NestedInnermostWins verifies that when a Segment wraps another
// Segment, the inner segment owns the stages it creates, and the outer
// segment owns only the stages it creates outside the inner.
func TestSegment_NestedInnermostWins(t *testing.T) {
	innermost := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v * 2, nil })
	})
	// "outer" runs an extra Map outside the inner Segment so we can verify
	// that stage gets "outer" while the inner Map gets "inner".
	outerStage := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		via := kitsune.NewSegment("inner", innermost).Apply(p)
		return kitsune.Map(via, func(_ context.Context, v int) (int, error) { return v + 100, nil })
	})

	out := kitsune.NewSegment("outer", outerStage).Apply(kitsune.FromSlice([]int{1, 2, 3}))

	var innerCount, outerCount int
	for _, n := range out.Describe() {
		switch n.SegmentName {
		case "inner":
			innerCount++
		case "outer":
			outerCount++
		}
	}
	if innerCount == 0 {
		t.Error("expected at least one stage stamped \"inner\"")
	}
	if outerCount == 0 {
		t.Error("expected at least one stage stamped \"outer\" (the post-inner Map)")
	}
}
```

- [ ] **Step 10: Run the test**

Run: `go test -run TestSegment_NestedInnermostWins .`
Expected: PASS.

- [ ] **Step 11: Run race detector across the package**

Run: `task test:race`
Expected: PASS, no races.

- [ ] **Step 12: Commit**

```bash
git add segment.go segment_test.go
git commit -m "$(cat <<'EOF'
feat(segment): add Segment[I,O] type and NewSegment

Segment wraps a Stage with a business name. Apply snapshots the global
stage-ID counter before and after delegating to the inner Stage, then
registers the new IDs in runCtx.segmentByID via a build-time wrapper.
runCtx.add stamps stageMeta.segmentName from that map so it propagates
to GraphNode.

Nested segments resolve to the innermost name (deepest enclosing
Segment wins per stage). Segments are pure metadata and do not affect
runtime behaviour.

Closes the "First-class composable segment type" Long-term roadmap
item; ships M1 of the Higher-Level Authoring Layer design
(docs/superpowers/specs/2026-04-15-higher-level-authoring-design.md).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: Add example

**Files:**
- Create: `examples/segment/main.go`
- Modify: `examples_test.go:23`

- [ ] **Step 1: Create the example**

Create `examples/segment/main.go`:

```go
// Example: Segment groups operators into named, graph-visible business units.
//
//	go run ./examples/segment
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/zenbaku/go-kitsune"
)

type Order struct {
	ID    int
	Item  string
	Price int // cents
}

func main() {
	ctx := context.Background()
	src := kitsune.FromSlice([]Order{
		{ID: 1, Item: "  apple ", Price: 150},
		{ID: 2, Item: "BANANA", Price: 75},
		{ID: 3, Item: " carrot", Price: 200},
	})

	// "normalize" segment: trim and uppercase the item name.
	normalize := kitsune.Stage[Order, Order](func(p *kitsune.Pipeline[Order]) *kitsune.Pipeline[Order] {
		return kitsune.Map(p, func(_ context.Context, o Order) (Order, error) {
			o.Item = strings.ToUpper(strings.TrimSpace(o.Item))
			return o, nil
		})
	})

	// "tax" segment: 10 percent sales tax.
	addTax := kitsune.Stage[Order, Order](func(p *kitsune.Pipeline[Order]) *kitsune.Pipeline[Order] {
		return kitsune.Map(p, func(_ context.Context, o Order) (Order, error) {
			o.Price = o.Price + o.Price/10
			return o, nil
		})
	})

	pipeline := kitsune.Then(
		kitsune.NewSegment("normalize", normalize),
		kitsune.NewSegment("tax", addTax),
	)

	out, err := kitsune.Collect(ctx, pipeline.Apply(src))
	if err != nil {
		panic(err)
	}

	fmt.Println("--- Output ---")
	for _, o := range out {
		fmt.Printf("#%d %-10s %d\n", o.ID, o.Item, o.Price)
	}

	fmt.Println("\n--- Graph ---")
	for _, n := range pipeline.Apply(src).Describe() {
		seg := "(no segment)"
		if n.SegmentName != "" {
			seg = "[segment: " + n.SegmentName + "]"
		}
		fmt.Printf("  %-12s %-30s %s\n", n.Kind, n.Name, seg)
	}
}
```

- [ ] **Step 2: Register the example**

Edit `examples_test.go`. The `examples` slice is at line 23. Insert `"segment"` in alphabetical order between `"runningtotal"` (line 48) and `"single"` (line 49):

```go
		"runningtotal",
		"segment",
		"single",
```

- [ ] **Step 3: Run the example directly**

Run: `go run ./examples/segment`
Expected: PASS — output prints transformed orders followed by a Graph section listing each stage's `Kind`, `Name`, and either `[segment: normalize]`, `[segment: tax]`, or `(no segment)` for the source.

- [ ] **Step 4: Run the example smoke test**

Run: `task test:examples`
Expected: PASS — `TestExamples/segment` passes.

- [ ] **Step 5: Commit**

```bash
git add examples/segment/main.go examples_test.go
git commit -m "$(cat <<'EOF'
test(segment): add segment example

Demonstrates two named segments composed via Then. Prints the
transformed orders and the graph annotated with SegmentName.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Document `Segment` in `doc/operators.md`

**Files:**
- Modify: `doc/operators.md`

- [ ] **Step 1: Locate the Stage Composition section**

The section heading is at line 2792 (`## :material-puzzle-outline: Stage Composition { #stage-composition }`). Read from there to find the existing `Stage` and `Then` references.

Run: `grep -n "^### " doc/operators.md | sed -n '/#stage-composition/,$p' | head -10`

(If the grep does not give the right slice because of how the file is structured, just open `doc/operators.md` from line 2792 onward.)

- [ ] **Step 2: Add a `Segment` subsection**

Edit `doc/operators.md`. Inside the Stage Composition section, append a new subsection after the existing `Then` / `Stage` material. Use the same heading style as the surrounding subsections in that section (typically `### Name`). Add:

```markdown
### Segment

```go
type Segment[I, O any] struct{ /* ... */ }

func NewSegment[I, O any](name string, stage Stage[I, O], opts ...SegmentOption) Segment[I, O]
func (s Segment[I, O]) Apply(p *Pipeline[I]) *Pipeline[O]
```

Wraps a [Stage](#stage-composition) with a business name. Every stage created by the inner Stage carries `SegmentName: name` in its [`GraphNode`], making the group visible in [`Pipeline.Describe`] and the inspector dashboard.

**Semantics**

- `Segment` satisfies [`Composable`], so it composes with [`Then`] and [`Pipeline.Through`] interchangeably with [`Stage`].
- Nested segments resolve to the innermost name; the deepest enclosing `Segment` owns each stage.
- Segments are pure metadata; they do not affect runtime behaviour.

**When to use**

Reach for `Segment` when you want to give a business name to a multi-step transformation that you want to read, debug, or attribute as one unit. Typical uses: an *enrichment* group, a *validation* group, a *publish* group at the end of a pipeline.

**Options**

`SegmentOption` is reserved for future per-segment metadata (description, tags, owner, metric labels). No options are defined in v1.

**Example**

```go
fetch   := kitsune.NewSegment("fetch-pages",  fetchStage)
enrich  := kitsune.NewSegment("enrich-pages", enrichStage)
publish := kitsune.NewSegment("publish",      publishStage)

pipeline := kitsune.Then(kitsune.Then(fetch, enrich), publish)

for _, n := range pipeline.Apply(src).Describe() {
    fmt.Println(n.SegmentName, n.Kind, n.Name)
}
```
```

- [ ] **Step 3: Sanity-check no em dashes**

Run: `grep -n "—" doc/operators.md | tail -5`
Expected: no em dashes in the new content (project style: never use em dashes; use a colon or semicolon). If grep finds any in the new content, replace with `:` or `;`.

- [ ] **Step 4: Commit**

```bash
git add doc/operators.md
git commit -m "$(cat <<'EOF'
docs(segment): add Segment reference to operators.md

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: Add `Segment` row to `doc/api-matrix.md`

**Files:**
- Modify: `doc/api-matrix.md`

- [ ] **Step 1: Locate the Stage Composition section**

Run: `grep -n "Stage Composition\|^| Operator\|^|---" doc/api-matrix.md | head -20`
Expected: identify the table that lists `Stage` and `Then`.

- [ ] **Step 2: Add a `Segment` row**

Edit `doc/api-matrix.md`. In the Stage Composition table, add a row for `Segment` immediately after the `Stage` row. Match the column count and use `-` for every option column (Segment accepts no `StageOption`s). Example shape (adjust to actual column headers in the file):

```markdown
| Segment    | -      | -      | -      | -          | -        | -          | -          | -      | -      | -          |
```

Add a footnote/note row beneath the table if the file uses inline notes for non-obvious operators:

> `Segment` is a metadata wrapper; it accepts no StageOptions. See [operators.md#segment](operators.md#segment).

- [ ] **Step 3: Verify the table renders**

Run: `head -50 doc/api-matrix.md` (or open the file) to confirm column alignment is intact.

- [ ] **Step 4: Commit**

```bash
git add doc/api-matrix.md
git commit -m "$(cat <<'EOF'
docs(segment): add Segment row to api-matrix

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: Mark roadmap item complete

**Files:**
- Modify: `doc/roadmap.md:91`

- [ ] **Step 1: Update the checkbox**

Edit `doc/roadmap.md`. Line 91 currently reads:

```markdown
- [ ] **First-class composable segment type**: There is no way to define and name a reusable pipeline segment ...
```

Replace `- [ ]` with `- [x]` and append the implementation note at the end of the line (after the existing description):

```markdown
- [x] **First-class composable segment type**: ... [existing text unchanged] ... Implemented as `Segment[I,O]` in `segment.go` with a `Composable[I,O]` interface widening for `Then` and `Pipeline.Through` (M1 of the higher-level authoring layer; spec at `docs/superpowers/specs/2026-04-15-higher-level-authoring-design.md`).
```

- [ ] **Step 2: Sanity-check no em dashes**

Run: `grep -n "—" doc/roadmap.md`
Expected: no new em dashes added.

- [ ] **Step 3: Commit**

```bash
git add doc/roadmap.md
git commit -m "$(cat <<'EOF'
docs(roadmap): mark composable segment type complete

Implemented as Segment[I,O] in segment.go (M1 of the higher-level
authoring layer).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: Final verification

- [ ] **Step 1: Run the full test suite with race detector**

Run: `task test:race`
Expected: PASS, no races, no test failures.

- [ ] **Step 2: Run property tests**

Run: `task test:property`
Expected: PASS — none of the existing property tests should be affected by Segment additions.

- [ ] **Step 3: Run example smoke tests**

Run: `task test:examples`
Expected: PASS for `TestExamples/segment` and every other example.

- [ ] **Step 4: Run the full PR-readiness suite**

Run: `task test:all`
Expected: PASS.

- [ ] **Step 5: Verify the example output by hand**

Run: `go run ./examples/segment`
Expected: prints three normalized + taxed orders, followed by a Graph section showing `[segment: normalize]` on the first Map, `[segment: tax]` on the second Map, and `(no segment)` on the source.

---

## Self-review

**Spec coverage (Section 1 of the higher-level authoring spec):**
- `Segment[I,O]` struct with `name`, inner stage, `SegmentOption` slice — **Task 7**.
- `NewSegment[I,O](name, stage, opts...)` constructor — **Task 7**.
- `Segment.Apply(p)` implements `Composable` — **Task 7** (verified by `TestSegment_SatisfiesComposable`).
- `Composable[I,O]` interface — **Task 1**.
- `Then` accepts `Composable` — **Task 2**.
- `Pipeline.Through` accepts `Composable` (not in spec but added for symmetry; called out in the file structure header) — **Task 3**.
- `stageMeta.segmentName` field — **Task 4**.
- `runCtx`-driven stamping — **Task 5**.
- `GraphNode.SegmentName` propagation — **Task 6**.
- `SegmentOption` placeholder interface — **Task 7**.
- Inspector / `Describe()` segment grouping (data path) — **Task 6** + verified in **Task 8** example.
- Hooks (`HookEvent.SegmentName`): the spec mentions this; the codebase has no `HookEvent` struct (hooks are method-param based). `GraphHook.OnGraph` already receives the updated `[]GraphNode` so segment data is available there. Other hook payloads (`BufferStatus`) are **deferred to a follow-up commit** as noted in the M1 plan preamble.

**Out-of-scope items (deferred to later milestones):**
- `Effect[I,R]`, `EffectOutcome`, `EffectPolicy`, `EffectOption`, `TryEffect`, `DryRun` — M2.
- `RunSummary`, `RunOutcome`, `Runner.Run` return-type upgrade, `WithFinalizer` — M3.
- `DevStore`, `FileDevStore`, `WithDevStore`, `ReplayThrough`, `FromCheckpoint` — M4.
- `RetryPolicy` → `RetryStrategy` rename — recorded in the M2-prerequisites preamble; not done in M1.

**Placeholder scan:** No "TBD", "implement later", or "add error handling" placeholders. Every code block is complete and runnable.

**Type consistency:** `Composable[I, O]` parameter naming matches across `Then` and `Through`. `stageMeta.segmentName` (lowercase, internal) and `GraphNode.SegmentName` (exported) are consistent with the rest of the file's casing conventions. `Segment[I, O]` field `name` matches the use in `Apply`. `runCtx.segmentByID` is consistent across `pipeline.go` and `segment.go`.

**Nesting semantics — caught during self-review.** An earlier draft used `if !ok { rc.segmentByID[id] = name }` to "let the innermost win." That actually gives *outermost*-wins, because outer's build wrapper fires before inner's (outer's wrapper calls `originalBuild`, which is the inner wrapper). Fixed in Task 7 step 3 to always overwrite: outer writes first, inner overwrites for its narrower ID range, yielding innermost-wins. `TestSegment_NestedInnermostWins` (Task 7 step 9) verifies this end-to-end.

---

**End of plan.**
