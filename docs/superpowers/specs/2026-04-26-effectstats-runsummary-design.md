# Per-stage `EffectStats` in `RunSummary` — design

**Status:** ready for implementation
**Date:** 2026-04-26
**Roadmap item:** Long-term → "Per-stage `EffectStats` in `RunSummary`"

---

## Problem

`RunSummary.Outcome` derives from `runCtx.effectStats`, which tracks per-Effect-stage success and terminal-failure counts plus the required/best-effort flag. Today only the success and failure counters are exposed downstream, via `Metrics.Stages[name].Processed` and `Metrics.Stages[name].Errors`. The required/best-effort split is collapsed: a dashboard showing "required failures" vs "best-effort failures" must re-derive the distinction from stage names or hard-code it.

The roadmap proposes a structured `summary.EffectStats` field that preserves this split alongside the per-stage counts.

## Goal

Add a top-level `EffectStats` field to `RunSummary`, keyed by stage name, carrying the required flag and the success/failure counters. Dashboards and finalizers can read the structured data without re-deriving anything from stage metadata.

## Non-goals

- No new tracking. `runCtx.effectStats` already has every value the new field exposes.
- No changes to `Outcome` derivation. The existing `deriveRunOutcome` continues to read `rc.effectStats` directly.
- No retry-attempt counters. The retry loop in `effect.go` does not count attempts today; adding that is a separate instrumentation task and out of scope here.
- No changes to `Metrics.Stages[name]`. It continues to expose `Processed`/`Errors` for Effects exactly as today.

## Design

### Public type

A new exported type in `runsummary.go`:

```go
// EffectStats reports per-Effect-stage success and terminal-failure counts
// for a single run, together with the stage's required flag.
//
// The map RunSummary.EffectStats is keyed by stage name (the same key used
// in Metrics.Stages). Stages constructed by Effect or TryEffect appear in
// the map; non-Effect stages do not.
type EffectStats struct {
    Required bool  `json:"required"`
    Success  int64 `json:"success"`
    Failure  int64 `json:"failure"`
}
```

### `RunSummary` change

A new field on the `RunSummary` struct:

```go
type RunSummary struct {
    Outcome       RunOutcome              `json:"outcome"`
    Err           error                   `json:"-"`
    Metrics       MetricsSnapshot         `json:"metrics"`
    Duration      time.Duration           `json:"duration_ns"`
    CompletedAt   time.Time               `json:"completed_at"`
    FinalizerErrs []error                 `json:"-"`
    EffectStats   map[string]EffectStats  `json:"effect_stats"`
}
```

### Population

In `Runner.Run` (and the equivalent `ForEachRunner.Run` / `DrainRunner.Run` paths), after building `summary` and before running finalizers:

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

`summary.EffectStats` is always allocated, never nil. Empty when no Effect stages exist (matching `Metrics.Stages`'s convention).

### Stage-name collisions

`runCtx.effectStats` is keyed by stage ID, so two Effect stages with the same name would each get their own entry internally. When projected to a `map[string]EffectStats` keyed by name, two stages with the same name would clobber. We accept last-write-wins; building a pipeline with two Effect stages of the same name is already ambiguous in `Metrics.Stages` (which has the same shape). No new failure mode introduced.

### Compatibility

- Source-breaking? **No.** This is purely additive: a new field on `RunSummary` and a new type. No method signatures change. Existing callers that destructure `RunSummary` keep working.
- JSON shape change? **Yes** — `effect_stats` appears in serialized output. Inspector's `SummarySnapshot` (in `inspector/inspector.go`) currently does not project `EffectStats`; it can stay that way (the inspector renders `Outcome` and the existing per-stage Metrics; adding a panel for the required/best-effort split is a follow-up if anyone wants one).

## Testing

Three tests in `runsummary_test.go` (or appended to an existing test file):

1. **Empty map for non-Effect pipeline.** Build a `FromSlice → Map → ForEach` pipeline. Run. Assert `summary.EffectStats != nil && len(summary.EffectStats) == 0`.
2. **One required + one best-effort Effect.** Build a pipeline with two Effects: one `Required()` that succeeds 3 times and fails 1 time, one `BestEffort()` that succeeds 2 times. Assert the map has both entries with the expected `Required`/`Success`/`Failure` fields.
3. **JSON round-trip.** Marshal a `RunSummary` containing populated `EffectStats`, unmarshal, assert deep-equal of the `EffectStats` portion.

`task test:race` for the count-load race-safety check (the success/failure atomics are already race-safe; the projection just calls `.Load()`).

## File touch list

| File | Change |
|---|---|
| `runsummary.go` | Add `EffectStats` type and field |
| `kitsune.go` | Populate `summary.EffectStats` in `Runner.Run` after summary construction, before finalizers |
| `runsummary_test.go` (or new) | Three tests above |
| `doc/run-summary.md` | Add a paragraph documenting the new field |
| `doc/roadmap.md` | Mark the item `[x]` |
| Memory: `project_higher_level_authoring.md` | Note that the required/best-effort split is now in `RunSummary.EffectStats` |

## Spec self-review

- **Placeholders:** none.
- **Internal consistency:** the JSON tag `effect_stats` matches the snake_case used elsewhere in `RunSummary` (`duration_ns`, `completed_at`). Field name `EffectStats` matches roadmap proposal.
- **Scope:** single short plan. Map projection + three tests + docs.
- **Ambiguity:** stage-name collision behavior is documented (last-write-wins), matching `Metrics.Stages` convention.
