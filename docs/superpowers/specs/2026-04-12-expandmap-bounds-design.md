# ExpandMap Depth and Fanout Bounds

**Date:** 2026-04-12  
**Status:** Approved

## Problem

`ExpandMap` performs BFS on an arbitrary graph with no limit on expansion depth or total items. A graph with high branching factor produces `fan^depth` items, exhausting memory silently. Users have no way to bound expansion without wrapping the entire output in `Take(n)` downstream, which does not stop the BFS queue from growing in memory before items are consumed.

## Solution

Add two new `StageOption`s:

- `MaxDepth(n int)` — stop enqueuing children once the BFS frontier has reached depth `n`. Depth 0 = roots only (no expansion at all). Depth 1 = roots plus their immediate children. Unlimited by default (current behaviour).
- `MaxItems(n int)` — stop emitting and stop enqueueing children after `n` total items have been emitted. Unlimited by default (current behaviour).

When a limit is hit, expansion stops silently: no error is returned, the output channel closes normally. This is consistent with how `Take(n)` terminates a pipeline. If both limits are set, whichever fires first wins.

## Architecture

### stageConfig fields

Add two integer fields to `stageConfig` in `config.go`:

```go
expandMaxDepth int // 0 = unlimited
expandMaxItems int // 0 = unlimited
```

`0` means "no limit" for both (zero value = current unlimited behaviour).

### Option constructors (config.go)

```go
// MaxDepth limits BFS expansion to at most n levels below the root items.
// Depth 0 emits only the root items with no expansion.
// Only meaningful on ExpandMap; silently ignored on all other operators.
func MaxDepth(n int) StageOption

// MaxItems limits total items emitted by ExpandMap to n.
// When the limit is reached, expansion stops and the stage closes normally.
// Only meaningful on ExpandMap; silently ignored on all other operators.
func MaxItems(n int) StageOption
```

### ExpandMap BFS loop changes (operator_filter.go)

The existing BFS queue stores `*Pipeline[T]`. To support `MaxDepth`, the queue must also carry a depth value per entry. Change the queue element from `*Pipeline[T]` to a struct:

```go
type expandEntry[T any] struct {
    p     *Pipeline[T]
    depth int
}
```

At item emission time:
1. If `expandMaxItems > 0 && emitted >= expandMaxItems` — stop (return nil, close channel).
2. When enqueueing a child: if `expandMaxDepth > 0 && childDepth > expandMaxDepth` — skip enqueueing (do not expand further).

Both checks are O(1) per item and add no goroutines.

### godoc warning on ExpandMap

Add a prominent warning to the `ExpandMap` godoc:

```
// WARNING: ExpandMap performs unbounded BFS by default. A graph with a high
// branching factor can produce fan^depth items, exhausting memory silently.
// Use MaxDepth(n) or MaxItems(n) to bound expansion, or pair with Take(n)
// downstream to limit total output.
```

## Tests

- `TestExpandMap_MaxDepth_Zero` — MaxDepth(0): only root items emitted, no expansion.
- `TestExpandMap_MaxDepth_One` — MaxDepth(1): roots + immediate children only.
- `TestExpandMap_MaxDepth_ExceedsTree` — MaxDepth larger than tree height: full tree emitted.
- `TestExpandMap_MaxItems` — MaxItems(3) on a deep tree: exactly 3 items emitted.
- `TestExpandMap_MaxItems_ExceedsTotal` — MaxItems larger than total: all items emitted.
- `TestExpandMap_MaxDepth_And_MaxItems` — both set: whichever fires first wins.
- `TestExpandMap_Buffer` already exists; no changes needed.

## Documentation updates

- `doc/operators.md` — add `MaxDepth`, `MaxItems` to the Options list; add godoc warning prose; update code example or add a second example showing bounded traversal.
- `doc/options.md` — add sections for `MaxDepth` and `MaxItems`.
- `doc/api-matrix.md` — add `MaxDepth` and `MaxItems` rows (ExpandMap column only).
- `doc/roadmap.md` — mark item `[x]`.

## Example

Add `examples/expandmap/main.go` demonstrating bounded directory tree traversal with `MaxDepth`.
