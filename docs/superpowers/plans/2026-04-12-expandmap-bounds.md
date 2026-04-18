# ExpandMap Depth and Fanout Bounds Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add MaxDepth(n) and MaxItems(n) StageOptions to ExpandMap to bound BFS expansion and prevent memory exhaustion on deep or high-fanout graphs.

**Architecture:** Add two integer fields to stageConfig (expandMaxDepth, expandMaxItems), add option constructor functions, modify the ExpandMap BFS loop to track depth per queue entry and check limits at emission time, add godoc warning. 0 = unlimited (preserves current behaviour).

**Tech Stack:** Go 1.22+, pgregory.net/rapid (property tests)

---

## Spec reference

Full design: `docs/superpowers/specs/2026-04-12-expandmap-bounds-design.md`

Key semantics:

- `MaxDepth(n)`: stops enqueueing children once the BFS frontier reaches depth `n`.
  - Depth 0 = roots only (no expansion).
  - Depth 1 = roots plus their immediate children.
  - Default unlimited (zero value on stageConfig).
- `MaxItems(n)`: stops emitting and stops enqueueing children after `n` total items have been emitted.
  - Default unlimited.
- If both limits are set, whichever fires first wins.
- When a limit is hit, expansion stops silently (no error). The output channel closes normally.
- Both options are only meaningful on `ExpandMap`; silently ignored on all other operators (same pattern as `VisitedBy` and `WithDedupSet`).

---

## Task 1 — Add stageConfig fields and option constructors

**Files:**
- Modify: `config.go` (stageConfig struct + two new option functions)
- Modify: `operator_test.go` (new test functions at the end of the ExpandMap section)

- [ ] **Step 1: Write failing tests in `operator_test.go`**

Append after `TestExpandMap_VisitedBy_ExternalDedupSet`:

```go
// ---------------------------------------------------------------------------
// ExpandMap — MaxDepth / MaxItems bounds
// ---------------------------------------------------------------------------

// depthTree is an infinite binary tree generator: every node v expands to
// [2v, 2v+1].
func depthTree(_ context.Context, v int) *kitsune.Pipeline[int] {
	return kitsune.FromSlice([]int{2 * v, 2*v + 1})
}

func TestExpandMap_MaxDepth_Zero(t *testing.T) {
	// MaxDepth(0): emit roots only; do not expand.
	p := kitsune.ExpandMap(
		kitsune.FromSlice([]int{1}),
		depthTree,
		kitsune.MaxDepth(0),
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1}) {
		t.Fatalf("got %v, want [1]", got)
	}
}

func TestExpandMap_MaxDepth_One(t *testing.T) {
	// MaxDepth(1): roots + one level of children.
	// Root 1 expands to [2,3]. BFS order: 1, 2, 3.
	p := kitsune.ExpandMap(
		kitsune.FromSlice([]int{1}),
		depthTree,
		kitsune.MaxDepth(1),
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Fatalf("got %v, want [1 2 3]", got)
	}
}

func TestExpandMap_MaxDepth_ExceedsTree(t *testing.T) {
	// MaxDepth larger than actual tree height: full tree is emitted.
	// expandTree (defined earlier): 1->[2,3], 2->[4,5], others leaves.
	// Full BFS order: 1, 2, 3, 4, 5.
	p := kitsune.ExpandMap(
		kitsune.FromSlice([]int{1}),
		expandTree,
		kitsune.MaxDepth(10),
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("got %v, want [1 2 3 4 5]", got)
	}
}

func TestExpandMap_MaxItems(t *testing.T) {
	// Infinite binary tree bounded to exactly 5 items.
	// BFS order on binary tree rooted at 1: 1, 2, 3, 4, 5.
	p := kitsune.ExpandMap(
		kitsune.FromSlice([]int{1}),
		depthTree,
		kitsune.MaxItems(5),
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("got %v, want [1 2 3 4 5]", got)
	}
}

func TestExpandMap_MaxItems_ExceedsTotal(t *testing.T) {
	// MaxItems larger than total tree: all items emitted.
	p := kitsune.ExpandMap(
		kitsune.FromSlice([]int{1}),
		expandTree,
		kitsune.MaxItems(100),
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("got %v, want [1 2 3 4 5]", got)
	}
}

func TestExpandMap_MaxDepth_And_MaxItems(t *testing.T) {
	// Both set. Binary tree rooted at 1, MaxDepth(3) would allow up to
	// 1+2+4+8=15 items. MaxItems(4) fires first.
	// BFS: 1, 2, 3, 4 (stop after 4 items).
	p := kitsune.ExpandMap(
		kitsune.FromSlice([]int{1}),
		depthTree,
		kitsune.MaxDepth(3),
		kitsune.MaxItems(4),
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3, 4}) {
		t.Fatalf("got %v, want [1 2 3 4]", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail to compile**

```
task test
```

Expected: compilation error `undefined: kitsune.MaxDepth` and `undefined: kitsune.MaxItems`. This is the required failing state.

- [ ] **Step 3: Add fields to stageConfig in `config.go`**

In the `stageConfig` struct, append the two new fields at the end:

```go
type stageConfig struct {
	name            string
	concurrency     int
	ordered         bool
	buffer          int
	bufferExplicit  bool
	overflow        internal.Overflow
	errorHandler    internal.ErrorHandler
	batchTimeout    time.Duration
	supervision     internal.SupervisionPolicy
	cacheConfig     *stageCacheConfig
	timeout         time.Duration
	keyTTL          time.Duration
	keyTTLExplicit  bool
	dedupSet        DedupSet
	clock           internal.Clock
	visitedKeyFn    any
	contextMapperFn any
	expandMaxDepth  int // MaxDepth: BFS depth cap for ExpandMap; 0 = unlimited
	expandMaxItems  int // MaxItems: total emission cap for ExpandMap; 0 = unlimited
}
```

- [ ] **Step 4: Add option constructors in `config.go`**

Insert after the `VisitedBy` function (before `WithContextMapper`):

```go
// MaxDepth limits BFS expansion in [ExpandMap] to at most n levels below the
// roots. Depth 0 emits only the root items and performs no expansion.
// Depth 1 emits roots and their immediate children. Default is unlimited.
//
// When the limit is reached the stage stops enqueueing children and closes
// its output channel normally; no error is returned. If used together with
// [MaxItems], whichever limit fires first wins.
//
// MaxDepth is only meaningful on [ExpandMap]. Silently ignored on all other
// operators.
func MaxDepth(n int) StageOption {
	return func(c *stageConfig) {
		if n >= 0 {
			c.expandMaxDepth = n
		}
	}
}

// MaxItems limits the total number of items emitted by an [ExpandMap] stage
// to n. When n items have been emitted the stage stops emitting, stops
// enqueueing children, and closes its output channel normally; no error is
// returned. Default is unlimited.
//
// If used together with [MaxDepth], whichever limit fires first wins.
//
// MaxItems is only meaningful on [ExpandMap]. Silently ignored on all other
// operators.
func MaxItems(n int) StageOption {
	return func(c *stageConfig) {
		if n > 0 {
			c.expandMaxItems = n
		}
	}
}
```

Note: `MaxDepth` accepts `n == 0` (roots only); `MaxItems` rejects `n <= 0` because zero collides with the "zero = unlimited" sentinel on `stageConfig.expandMaxItems`.

- [ ] **Step 5: Run tests — should compile and some pass, bounds tests still fail**

```
task test
```

Expected: compiles; `TestExpandMap_MaxDepth_ExceedsTree` and `TestExpandMap_MaxItems_ExceedsTotal` may pass (full tree emitted either way); the four bounded tests fail because the BFS loop does not yet honour the options.

- [ ] **Step 6: Commit**

```
git add config.go operator_test.go
git commit -m "feat(config): add MaxDepth and MaxItems stage options

Wire two new StageOptions into stageConfig. ExpandMap BFS wiring lands in
the next commit. Bounds tests compile but currently fail by design."
```

---

## Task 2 — Modify ExpandMap BFS loop to honour the bounds

**Files:**
- Modify: `operator_filter.go` (stage closure inside ExpandMap)

- [ ] **Step 1: Replace the BFS stage closure in `operator_filter.go`**

Replace the `stage := func(ctx context.Context) error {` closure inside `ExpandMap` (currently lines 436-487) with:

```go
		stage := func(ctx context.Context) error {
			defer close(ch)
			outbox := internal.NewBlockingOutbox(ch)

			// expandEntry couples a pipeline with the BFS depth at which its
			// items will be emitted. Root pipelines start at depth 0.
			type expandEntry struct {
				p     *Pipeline[T]
				depth int
			}

			maxDepth := cfg.expandMaxDepth // 0 = unlimited
			maxItems := cfg.expandMaxItems // 0 = unlimited
			emitted := 0

			queue := []expandEntry{{p: p, depth: 0}}
			for len(queue) > 0 {
				current := queue[0]
				queue = queue[1:]

				innerCtx, cancel := context.WithCancel(ctx)
				var sendErr error
				var limitHit bool
				err := current.p.ForEach(func(_ context.Context, item T) error {
					// Dedup check: skip item and its subtree if already visited.
					if set != nil {
						key := keyFn(item)
						dup, err := set.Contains(innerCtx, key)
						if err != nil {
							return err
						}
						if dup {
							return nil
						}
						if err := set.Add(innerCtx, key); err != nil {
							return err
						}
					}
					// MaxItems: stop before emitting if we have hit the cap.
					if maxItems > 0 && emitted >= maxItems {
						limitHit = true
						cancel()
						return nil
					}
					// Emit item to downstream.
					if err := outbox.Send(ctx, item); err != nil {
						sendErr = err
						cancel()
						return nil
					}
					emitted++
					// MaxDepth: only enqueue children if the next BFS level is
					// within the cap (0 = unlimited).
					childDepth := current.depth + 1
					if maxDepth > 0 && childDepth > maxDepth {
						return nil
					}
					// Enqueue children for BFS expansion.
					if child := fn(ctx, item); child != nil {
						queue = append(queue, expandEntry{p: child, depth: childDepth})
					}
					return nil
				}).Run(innerCtx)
				cancel()

				if sendErr != nil {
					return sendErr
				}
				if limitHit {
					// MaxItems reached: stop BFS and close output normally.
					return nil
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err != nil {
					return err
				}
			}
			return nil
		}
```

Key points:
- `expandEntry` is declared inside the stage closure so the type parameter `T` is in scope without a package-level generic type.
- `emitted` increments only after a successful `outbox.Send` — `MaxItems` reflects actual downstream deliveries.
- `limitHit` causes the outer queue loop to `return nil` (normal close), matching `Take` semantics.
- `cancel()` inside `MaxItems` branch terminates the currently-running inner `ForEach` eagerly.
- `cancel()` is called a second time unconditionally after `ForEach` returns — this is safe; `CancelFunc` is idempotent.
- Existing callers with `expandMaxDepth == 0` and `expandMaxItems == 0` take the same code paths as before; `maxDepth > 0` and `maxItems > 0` guards are both false.

- [ ] **Step 2: Run all tests**

```
task test
```

Expected: all tests pass, including all six new bounds tests and all existing ExpandMap tests.

- [ ] **Step 3: Run race detector**

```
task test:race
```

Expected: clean, no races.

- [ ] **Step 4: Commit**

```
git add operator_filter.go
git commit -m "feat(operator): honour MaxDepth and MaxItems in ExpandMap BFS

Track depth per queue entry via inline expandEntry struct. MaxItems stops
the stage cleanly when the emission cap is hit; MaxDepth skips enqueueing
children whose depth would exceed the cap. Zero for either option preserves
the prior unlimited behaviour."
```

---

## Task 3 — Add godoc warning to ExpandMap

**Files:**
- Modify: `operator_filter.go` (godoc comment above `func ExpandMap`)

- [ ] **Step 1: Replace the ExpandMap godoc comment**

Replace the existing comment block above `func ExpandMap[T any]` (lines ~378-399) with:

```go
// ExpandMap performs a breadth-first expansion of a pipeline. For each item
// emitted by p, fn is called to produce a child [*Pipeline[T]]; the children
// are emitted and then themselves expanded, level by level, until no more
// children are produced.
//
// Emission order is BFS: all items at depth N are emitted before any item at
// depth N+1. fn may return nil to signal that an item has no children.
//
// WARNING: ExpandMap performs unbounded BFS by default. A graph with a high
// branching factor can produce fan^depth items, exhausting memory silently
// as the BFS queue grows. Use [MaxDepth] or [MaxItems] to bound expansion,
// or pair with [Take] downstream to limit total output. For cyclic graphs,
// use [VisitedBy] to break loops.
//
// Options:
//   - [WithName] labels the stage for metrics and traces.
//   - [Buffer] sets the output channel buffer size.
//   - [MaxDepth] caps BFS depth below the roots (0 = roots only, default unlimited).
//   - [MaxItems] caps total items emitted (default unlimited).
//   - [VisitedBy] prevents re-visiting items whose key was already seen,
//     breaking infinite loops in cyclic graphs. Defaults to [MemoryDedupSet];
//     override the backend with [WithDedupSet].
//
// Typical uses: tree traversal, recursive API pagination, graph walks where
// each node expands into its neighbours.
//
//	// Walk a directory tree, bounded to 4 levels and 10 000 entries.
//	kitsune.ExpandMap(kitsune.FromSlice(roots), func(ctx context.Context, dir Dir) *kitsune.Pipeline[Dir] {
//	    children, _ := dir.ReadChildren(ctx)
//	    return kitsune.FromSlice(children)
//	},
//	    kitsune.MaxDepth(4),
//	    kitsune.MaxItems(10_000),
//	)
```

- [ ] **Step 2: Run tests**

```
task test
```

Expected: all tests pass (godoc change only).

- [ ] **Step 3: Commit**

```
git add operator_filter.go
git commit -m "docs(expandmap): add unbounded-BFS warning and document bounds options

List MaxDepth/MaxItems/Buffer alongside the existing VisitedBy/WithDedupSet
options and add a prominent WARNING about fan^depth memory exhaustion."
```

---

## Task 4 — Update documentation

**Files:**
- Modify: `doc/operators.md`
- Modify: `doc/options.md`
- Modify: `doc/api-matrix.md`

- [ ] **Step 1: Update the ExpandMap section in `doc/operators.md`**

Replace the ExpandMap section (from `### ExpandMap` through the closing code fence, before `### Pairwise`) with:

````markdown
### ExpandMap

```go
func ExpandMap[T any](
    p *Pipeline[T],
    fn func(context.Context, T) *Pipeline[T],
    opts ...StageOption,
) *Pipeline[T]
```

BFS graph expansion. For each item, `fn` returns a child pipeline (or `nil` for leaf items). Items are emitted in BFS order. Use `VisitedBy` to detect and skip cycles.

**When to use:** Tree or DAG traversal where each node can produce more nodes of the same type: directory trees, dependency graphs, org charts.

**Options:** `Buffer`, `WithName`, `MaxDepth`, `MaxItems`, `VisitedBy` (for cycle detection), `WithDedupSet`.

> **Warning — unbounded by default.** Without `MaxDepth`, `MaxItems`, or a downstream `Take(n)`, `ExpandMap` will traverse the entire reachable graph. A graph with branching factor `fan` and depth `d` produces up to `fan^d` items, which can exhaust memory silently as the BFS queue grows. Always bound expansion on untrusted or potentially deep inputs.

```go
// Crawl a directory tree.
files := kitsune.ExpandMap(
    kitsune.FromSlice([]string{"/root"}),
    func(ctx context.Context, path string) *kitsune.Pipeline[string] {
        entries, err := os.ReadDir(path)
        if err != nil {
            return nil
        }
        var children []string
        for _, e := range entries {
            if e.IsDir() {
                children = append(children, filepath.Join(path, e.Name()))
            }
        }
        return kitsune.FromSlice(children)
    },
)
```

Bounded expansion — cap both depth and total entries:

```go
// Walk at most 4 levels deep and at most 10 000 entries total.
files := kitsune.ExpandMap(
    kitsune.FromSlice([]string{"/root"}),
    func(ctx context.Context, path string) *kitsune.Pipeline[string] {
        entries, err := os.ReadDir(path)
        if err != nil {
            return nil
        }
        var children []string
        for _, e := range entries {
            if e.IsDir() {
                children = append(children, filepath.Join(path, e.Name()))
            }
        }
        return kitsune.FromSlice(children)
    },
    kitsune.MaxDepth(4),
    kitsune.MaxItems(10_000),
)
```

When either bound is reached the stage stops enqueueing children and closes its output channel normally — no error is returned, matching the semantics of `Take(n)`. If both options are set, whichever limit fires first wins.
````

- [ ] **Step 2: Add MaxDepth and MaxItems sections to `doc/options.md`**

Insert immediately after the `VisitedBy` section (before `WithKeyTTL`):

```markdown
---

## `MaxDepth(n int)`

**Applies to:** `ExpandMap`

Limit BFS expansion to at most `n` levels below the root items.

- `MaxDepth(0)` emits only the root items and performs no expansion.
- `MaxDepth(1)` emits roots plus their immediate children.
- Default is unlimited.

When the depth cap is reached the stage stops enqueueing children but continues to drain items already in the BFS queue. The output channel closes normally — no error is returned.

```go
// Walk at most 3 levels below each root.
nodes := kitsune.ExpandMap(roots, fetchChildren,
    kitsune.MaxDepth(3),
)
```

Combine with [`MaxItems`](#maxitemsn-int) to cap both depth and total output. Whichever limit fires first wins.

Silently ignored on all operators other than `ExpandMap`.

---

## `MaxItems(n int)`

**Applies to:** `ExpandMap`

Limit total items emitted by an `ExpandMap` stage to `n`. When the stage has emitted `n` items it stops enqueueing children, cancels its currently-running inner pipeline, and closes its output channel normally — no error is returned, matching the semantics of `Take(n)` downstream.

Unlike a downstream `Take(n)`, `MaxItems` stops the BFS queue from growing in memory after the cap is reached. Prefer `MaxItems` over `Take` for graphs with high fan-out.

```go
// Emit at most 1 000 items, regardless of tree shape.
nodes := kitsune.ExpandMap(roots, fetchChildren,
    kitsune.MaxItems(1_000),
)
```

`MaxItems(0)` (or any non-positive value) is ignored; use `MaxDepth(0)` if you want roots-only behaviour.

Combine with [`MaxDepth`](#maxdepthn-int) to cap both depth and total output. Whichever limit fires first wins.

Silently ignored on all operators other than `ExpandMap`.

---
```

- [ ] **Step 3: Update `doc/api-matrix.md`**

Find the ExpandMap note in the notes section and extend it to mention `MaxDepth` and `MaxItems`. Find the existing text:

```
- `ExpandMap` performs BFS expansion: items at depth N are all emitted before any item at depth N+1. fn may return nil for leaf nodes. Accepts `WithName` and `Buffer`. Use `VisitedBy(keyFn)` to enable cycle detection (items whose key was already seen are skipped, along with their subtrees); combine with `WithDedupSet` to override the default `MemoryDedupSet` backend.
```

Replace with:

```
- `ExpandMap` performs BFS expansion: items at depth N are all emitted before any item at depth N+1. fn may return nil for leaf nodes. Accepts `WithName` and `Buffer`. Use `MaxDepth(n)` to cap BFS depth (0 = roots only) and `MaxItems(n)` to cap total emissions — the stage closes normally when either cap is reached. Use `VisitedBy(keyFn)` to enable cycle detection (items whose key was already seen are skipped, along with their subtrees); combine with `WithDedupSet` to override the default `MemoryDedupSet` backend. **WARNING:** without `MaxDepth`, `MaxItems`, or a downstream `Take`, `ExpandMap` traverses the entire reachable graph and can exhaust memory on high-fanout inputs.
```

Also add the two new options to the options table. Find the `VisitedBy` row in section 18 (or wherever it lives) and add rows after it:

```markdown
| `MaxDepth(n int)` | `StageOption` | `ExpandMap` | Cap BFS depth to `n` levels below roots. `0` = roots only; default unlimited. |
| `MaxItems(n int)` | `StageOption` | `ExpandMap` | Cap total items emitted to `n`. Stage closes normally when cap is hit. Default unlimited. |
```

- [ ] **Step 4: Run tests**

```
task test
```

Expected: all tests pass (docs-only changes).

- [ ] **Step 5: Commit**

```
git add doc/operators.md doc/options.md doc/api-matrix.md
git commit -m "docs: document ExpandMap MaxDepth and MaxItems bounds

Add bounded-traversal example to operators.md, new MaxDepth/MaxItems
sections in options.md, and matching rows in api-matrix.md. Add the
unbounded-BFS warning next to the ExpandMap operator notes."
```

---

## Task 5 — Add example

**Files:**
- Create: `examples/expandmap/main.go`
- Modify: `examples_test.go` (register `expandmap` in the examples slice)

- [ ] **Step 1: Create `examples/expandmap/main.go`**

```go
// Example: expandmap — bounded BFS traversal with MaxDepth / MaxItems.
//
// ExpandMap walks an arbitrary graph breadth-first. Without bounds it will
// traverse the entire reachable graph, which on a high-fanout input can
// exhaust memory silently. MaxDepth caps how deep the walk goes; MaxItems
// caps the total number of items emitted. Whichever limit fires first wins.
package main

import (
	"context"
	"fmt"

	kitsune "github.com/zenbaku/go-kitsune"
)

// node is a synthetic directory-tree entry.
type node struct {
	path     string
	children []string
}

// tree is a synthetic filesystem with branching factor 3 and depth 4.
// Total nodes: 1 + 3 + 9 + 27 + 81 = 121.
var tree = func() map[string]node {
	out := map[string]node{}
	var build func(path string, depth int)
	build = func(path string, depth int) {
		if depth == 4 {
			out[path] = node{path: path}
			return
		}
		n := node{path: path}
		for i := 0; i < 3; i++ {
			child := fmt.Sprintf("%s/%d", path, i)
			n.children = append(n.children, child)
			build(child, depth+1)
		}
		out[path] = n
	}
	build("/root", 0)
	return out
}()

func expand(_ context.Context, path string) *kitsune.Pipeline[string] {
	n, ok := tree[path]
	if !ok || len(n.children) == 0 {
		return nil
	}
	return kitsune.FromSlice(n.children)
}

func main() {
	ctx := context.Background()

	// Unbounded walk: visits all 121 nodes.
	all, err := kitsune.Collect(ctx, kitsune.ExpandMap(
		kitsune.FromSlice([]string{"/root"}),
		expand,
	))
	if err != nil {
		panic(err)
	}
	fmt.Printf("unbounded walk: %d nodes\n", len(all))

	// MaxDepth(2): root + 2 levels of children = 1 + 3 + 9 = 13 nodes.
	shallow, err := kitsune.Collect(ctx, kitsune.ExpandMap(
		kitsune.FromSlice([]string{"/root"}),
		expand,
		kitsune.MaxDepth(2),
	))
	if err != nil {
		panic(err)
	}
	fmt.Printf("MaxDepth(2): %d nodes\n", len(shallow))

	// MaxItems(10): stop after 10 emissions regardless of tree shape.
	capped, err := kitsune.Collect(ctx, kitsune.ExpandMap(
		kitsune.FromSlice([]string{"/root"}),
		expand,
		kitsune.MaxItems(10),
	))
	if err != nil {
		panic(err)
	}
	fmt.Printf("MaxItems(10): %d nodes\n", len(capped))

	// Both bounds: MaxDepth(3) allows up to 40 nodes; MaxItems(20) fires first.
	both, err := kitsune.Collect(ctx, kitsune.ExpandMap(
		kitsune.FromSlice([]string{"/root"}),
		expand,
		kitsune.MaxDepth(3),
		kitsune.MaxItems(20),
	))
	if err != nil {
		panic(err)
	}
	fmt.Printf("MaxDepth(3) + MaxItems(20): %d nodes\n", len(both))
}
```

- [ ] **Step 2: Register in `examples_test.go`**

Add `"expandmap"` to the `examples` slice in alphabetical order (between `"deadletter"` and `"fanout"`):

```go
"deadletter",
"expandmap",
"fanout",
```

- [ ] **Step 3: Smoke test the example**

```
go run ./examples/expandmap
```

Expected output:

```
unbounded walk: 121 nodes
MaxDepth(2): 13 nodes
MaxItems(10): 10 nodes
MaxDepth(3) + MaxItems(20): 20 nodes
```

- [ ] **Step 4: Run example tests**

```
task test:examples
```

Expected: all examples pass including `expandmap`.

- [ ] **Step 5: Commit**

```
git add examples/expandmap/main.go examples_test.go
git commit -m "examples: add bounded ExpandMap directory-tree walkthrough

Synthetic tree with branching factor 3 and depth 4 demonstrates unbounded,
MaxDepth, MaxItems, and combined-bounds behaviour."
```

---

## Task 6 — Mark roadmap item done

**Files:**
- Modify: `doc/roadmap.md`

- [ ] **Step 1: Flip the checkbox**

In `doc/roadmap.md` line 19, change:

```
- [ ] **`ExpandMap` has no depth or fanout bound**:
```

to:

```
- [x] **`ExpandMap` has no depth or fanout bound**:
```

- [ ] **Step 2: Run tests**

```
task test
```

Expected: all tests pass.

- [ ] **Step 3: Commit**

```
git add doc/roadmap.md
git commit -m "docs(roadmap): mark ExpandMap depth/fanout bounds as done"
```

---

## Verification checklist

- [ ] `task test` — all unit tests pass including all six new ExpandMap bounds tests.
- [ ] `task test:race` — race detector clean.
- [ ] `task test:property` — property tests pass.
- [ ] `go run ./examples/expandmap` — prints all four expected lines.
- [ ] `go vet ./...` — clean.
- [ ] `git log --oneline -n 6` — shows six expected commits.

---

## Risk notes

- **Backwards compatibility:** All existing callers have `expandMaxDepth == 0` and `expandMaxItems == 0`; both `maxDepth > 0` and `maxItems > 0` guards are false, so new code paths are inert.
- **Interaction with VisitedBy:** Deduplicated items are skipped before `emitted` is incremented — duplicates do not consume the MaxItems budget.
- **Interaction with Take(n) downstream:** `MaxItems(n)` closes the output channel normally; a downstream `Take(m)` with `m > n` sees `n` items then close, same as any short-running source.
- **Why check MaxItems before Send?** Allows the stage to close immediately at the n-th item without one extra ForEach iteration.
- **Why reject MaxItems(0)?** Zero is the "unlimited" sentinel for `stageConfig.expandMaxItems`. Accepting 0 would silently emit nothing, creating a hard-to-debug footgun. Use `MaxDepth(0)` for roots-only behaviour.
