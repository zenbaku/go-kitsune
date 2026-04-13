# Named Result Types — Design Spec

**Date:** 2026-04-12
**Status:** Approved

## Problem

Three operators return `Pair[T, T]` or `Pair[T, V]`, forcing consumers to read `.First` and `.Second` everywhere with no type context:

| Operator | Current return type | Problem |
|---|---|---|
| `LookupBy` | `Pair[T, V]` | `.First` = item, `.Second` = looked-up value — semantically reversed to what most users expect |
| `Pairwise` | `Pair[T, T]` | `.First` = previous item, `.Second` = current item — temporal order is invisible |
| `MinMax` | `Pair[T, T]` | `.First` = min, `.Second` = max — min/max relationship is invisible |

`Timestamped[T]` and `Indexed[T]` were already added to fix the same problem for `Timestamp` and `WithIndex`.

## Solution

Add three named types and hard-swap the operator return types. `Pair` remains as a utility for generic positional pairing (`Zip`, `CombineLatest`, `WithLatestFrom`, `Unzip` are unaffected).

## New Types

```go
// enrich.go
// Enriched pairs an item with its looked-up value.
type Enriched[T any, V any] struct {
    Item  T
    Value V
}

// operator_transform.go
// Consecutive holds two adjacent items from a stream in arrival order.
type Consecutive[T any] struct {
    Prev T
    Curr T
}

// collect.go
// MinMaxResult holds the minimum and maximum values from a finite stream.
type MinMaxResult[T any] struct {
    Min T
    Max T
}
```

## Operator Signature Changes

```go
// Before:
func LookupBy[T any, K comparable, V any](...) *Pipeline[Pair[T, V]]
func Pairwise[T any](...) *Pipeline[Pair[T, T]]
func MinMax[T any](ctx, p, less, opts) (Pair[T, T], bool, error)

// After:
func LookupBy[T any, K comparable, V any](...) *Pipeline[Enriched[T, V]]
func Pairwise[T any](...) *Pipeline[Consecutive[T]]
func MinMax[T any](ctx, p, less, opts) (MinMaxResult[T], bool, error)
```

## Unchanged

- `Pair[A, B]` type remains exported
- `Zip`, `CombineLatest`, `WithLatestFrom`, `Pairwise` (... wait, Pairwise is changing), `Unzip` — `Zip`, `CombineLatest`, `WithLatestFrom`, `Unzip` all keep `Pair[A, B]`
- No new stage options, no behavioral changes

## Files to Update

| File | Change |
|---|---|
| `enrich.go` | Add `Enriched[T, V]`; update `LookupBy` return type and internal construction |
| `operator_transform.go` | Add `Consecutive[T]`; update `Pairwise` return type and internal construction |
| `collect.go` | Add `MinMaxResult[T]`; update `MinMax` return type and internal construction |
| `operators2_test.go` | Update `Pairwise` and `LookupBy` test assertions |
| `regression_test.go` | Update any `Pair` references in regression tests |
| `operator_test.go` | Update `LookupBy` test assertions |
| `state_test.go` | Update any `Pair` references |
| `examples/lookupby/main.go` | Update to use `Enriched`, `.Item`, `.Value` |
| `doc/operators.md` | Update signatures and field references for all three operators |
| `doc/api-matrix.md` | Update return type annotations for `LookupBy`, `Pairwise`, `MinMax` |

## Testing

- No new property tests required (behavioral invariants unchanged)
- Existing tests must be updated to use new field names
- `task test:race` must pass after changes

## Non-Goals

- No deprecation shims or aliases for `Pair`
- No changes to `Zip`, `CombineLatest`, `WithLatestFrom`, `Unzip`
- No behavioral changes to any operator
