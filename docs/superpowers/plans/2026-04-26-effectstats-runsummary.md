# Per-stage `EffectStats` in `RunSummary` — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `RunSummary.EffectStats map[string]EffectStats` field that exposes the required-vs-best-effort split alongside per-Effect-stage success and failure counts. Pure projection of the existing `runCtx.effectStats`; no new tracking required.

**Architecture:** A new exported `EffectStats{Required bool; Success, Failure int64}` type in `runsummary.go`. A new field on `RunSummary`. In `Runner.Run`, after `summary` is built and before finalizers run, project `rc.effectStats` into the new map keyed by stage name. Always allocate the map (never nil) so callers don't need a nil check. `ForEachRunner.Run` and `DrainRunner.Run` delegate to `*Runner.Run`, so they inherit the population for free.

**Tech Stack:** Go generics, the existing `runCtx.effectStats` atomic counters.

**Module path:** `github.com/zenbaku/go-kitsune`

---

## File structure

| File | Responsibility | Change |
|---|---|---|
| `runsummary.go` | RunSummary types | Add `EffectStats` type and `RunSummary.EffectStats` field |
| `kitsune.go` | Runner.Run summary construction | Populate `summary.EffectStats` after `summary` literal, before finalizers |
| `runsummary_external_test.go` | RunSummary integration tests | Append three tests (empty map, populated map, JSON round-trip) |
| `doc/run-summary.md` | RunSummary feature guide | Document the new field |
| `doc/roadmap.md` | Project roadmap | Mark item `[x]` |
| Memory: `project_higher_level_authoring.md` | User auto-memory | Note that the required/best-effort split is now in `RunSummary.EffectStats` |

---

## Tasks

### Task 1: Add `EffectStats` type, field, and population (TDD)

**Files:**
- Modify: `runsummary.go`
- Modify: `kitsune.go`
- Modify: `runsummary_external_test.go`

- [ ] **Step 1: Write the failing baseline test**

Append to `runsummary_external_test.go`:

```go
// TestRunSummary_EffectStats_EmptyForNonEffectPipeline verifies that a
// pipeline with no Effect stages still produces a non-nil EffectStats map
// with zero entries. Always-allocate avoids a nil check at every call site.
func TestRunSummary_EffectStats_EmptyForNonEffectPipeline(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1, 2, 3})
	runner := kitsune.Map(src, func(_ context.Context, v int) (int, error) { return v, nil }).
		ForEach(func(_ context.Context, _ int) error { return nil })

	summary, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if summary.EffectStats == nil {
		t.Fatalf("EffectStats is nil; want allocated empty map")
	}
	if len(summary.EffectStats) != 0 {
		t.Errorf("len(EffectStats)=%d, want 0; got %+v", len(summary.EffectStats), summary.EffectStats)
	}
}
```

- [ ] **Step 2: Run the test and verify it fails to compile**

Run: `go test -run TestRunSummary_EffectStats_EmptyForNonEffectPipeline ./... -count=1`
Expected: build failure: `summary.EffectStats undefined (type kitsune.RunSummary has no field or method EffectStats)`.

- [ ] **Step 3: Add the `EffectStats` type**

In `runsummary.go`, add the new type just below the existing `RunSummaryHook` interface (or wherever the bottom of the file is, before any closing comment):

```go
// EffectStats reports per-Effect-stage success and terminal-failure counts
// for a single run, together with the stage's required flag. The map
// [RunSummary.EffectStats] is keyed by stage name (the same key used in
// [MetricsSnapshot.Stages]). Stages constructed by [Effect] or [TryEffect]
// appear in the map; non-Effect stages do not.
//
// Required is true when the stage was constructed with the default
// required policy (or explicitly with [Required]); false when constructed
// with [BestEffort]. Success counts items that produced a non-error
// outcome; Failure counts items whose Effect call exhausted retries with
// a terminal error (a per-item failure attributed to this stage).
type EffectStats struct {
	Required bool  `json:"required"`
	Success  int64 `json:"success"`
	Failure  int64 `json:"failure"`
}
```

- [ ] **Step 4: Add the `EffectStats` field to `RunSummary`**

In `runsummary.go`, modify the `RunSummary` struct definition. Find the existing block:

```go
type RunSummary struct {
	Outcome       RunOutcome      `json:"outcome"`
	Err           error           `json:"-"`
	Metrics       MetricsSnapshot `json:"metrics"`
	Duration      time.Duration   `json:"duration_ns"`
	CompletedAt   time.Time       `json:"completed_at"`
	FinalizerErrs []error         `json:"-"`
}
```

Add the new field at the end:

```go
type RunSummary struct {
	Outcome       RunOutcome             `json:"outcome"`
	Err           error                  `json:"-"`
	Metrics       MetricsSnapshot        `json:"metrics"`
	Duration      time.Duration          `json:"duration_ns"`
	CompletedAt   time.Time              `json:"completed_at"`
	FinalizerErrs []error                `json:"-"`
	EffectStats   map[string]EffectStats `json:"effect_stats"`
}
```

Also update the godoc comment on the struct (around line 44-60) to document the new field. After the existing paragraph on `Metrics`, append a paragraph:

```go
// EffectStats is keyed by stage name and reports the required flag plus
// success and terminal-failure counts for each [Effect] / [TryEffect]
// stage that ran. The map is always allocated (never nil); empty when
// the pipeline contains no Effect stages.
```

- [ ] **Step 5: Populate `EffectStats` in `Runner.Run`**

In `kitsune.go`, find the section near the end of `Runner.Run` where `summary` is built (around line 353). The current shape is:

```go
summary := RunSummary{
	Outcome:     deriveRunOutcome(rc, pipelineErr),
	Err:         pipelineErr,
	Duration:    finishedAt.Sub(started),
	CompletedAt: finishedAt,
}
if mh, ok := hook.(*MetricsHook); ok {
	summary.Metrics = mh.Snapshot()
} else {
	summary.Metrics = MetricsSnapshot{Timestamp: finishedAt, Elapsed: summary.Duration}
}

if len(r.finalizers) > 0 {
	summary.FinalizerErrs = make([]error, len(r.finalizers))
	for i, fn := range r.finalizers {
		summary.FinalizerErrs[i] = fn(ctx, summary)
	}
}
```

Insert the EffectStats population AFTER the Metrics block and BEFORE the finalizers block (so finalizers receive a summary with the stats already populated):

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

- [ ] **Step 6: Build to verify**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 7: Run the test and verify it passes**

Run: `go test -run TestRunSummary_EffectStats_EmptyForNonEffectPipeline ./... -count=1`
Expected: PASS.

- [ ] **Step 8: Run the full RunSummary test suite for regressions**

Run: `go test -run TestRunSummary ./... -count=1 -v`
Expected: all PASS, including the existing `TestRunSummary_*` tests that don't reference `EffectStats`.

- [ ] **Step 9: Commit**

```bash
git add runsummary.go kitsune.go runsummary_external_test.go
git commit -m "$(cat <<'EOF'
feat(runsummary): add EffectStats map to RunSummary

Expose the required-vs-best-effort split alongside per-Effect-stage
success and failure counters, projected from runCtx.effectStats. The
map is always allocated (never nil); empty when the pipeline has no
Effect stages.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Test populated EffectStats with required + best-effort Effects

**Files:**
- Modify: `runsummary_external_test.go`

- [ ] **Step 1: Write the test**

Append to `runsummary_external_test.go`:

```go
// TestRunSummary_EffectStats_PopulatedWithSplit verifies that a pipeline
// with one required and one best-effort Effect produces an EffectStats
// map with both entries, the correct Required flag on each, and the
// correct success/failure counts.
func TestRunSummary_EffectStats_PopulatedWithSplit(t *testing.T) {
	ctx := context.Background()

	// Required effect: succeeds for items where v != 4 (3 successes), fails for v == 4 (1 failure).
	srcA := kitsune.FromSlice([]int{1, 2, 3, 4})
	requiredFn := func(_ context.Context, v int) (int, error) {
		if v == 4 {
			return 0, errors.New("boom")
		}
		return v * 10, nil
	}
	required := kitsune.Effect(srcA, requiredFn, kitsune.WithName("required-effect"))

	// Pipe outcomes through to a sink to actually run the effect; counts
	// land in rc.effectStats regardless of what the sink does.
	requiredSink := required.ForEach(func(_ context.Context, _ kitsune.EffectOutcome[int, int]) error { return nil })

	// Best-effort effect: succeeds for all 2 items, no failures.
	srcB := kitsune.FromSlice([]int{10, 20})
	bestEffortFn := func(_ context.Context, v int) (int, error) { return v + 1, nil }
	bestEffort := kitsune.Effect(srcB, bestEffortFn, kitsune.BestEffort(), kitsune.WithName("besteffort-effect"))
	bestEffortSink := bestEffort.ForEach(func(_ context.Context, _ kitsune.EffectOutcome[int, int]) error { return nil })

	merged, err := kitsune.MergeRunners(requiredSink, bestEffortSink)
	if err != nil {
		t.Fatalf("MergeRunners: %v", err)
	}

	summary, err := merged.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected pipeline err: %v", err)
	}

	got := summary.EffectStats
	if len(got) != 2 {
		t.Fatalf("len(EffectStats)=%d, want 2; got %+v", len(got), got)
	}

	r, ok := got["required-effect"]
	if !ok {
		t.Fatalf("missing required-effect entry; got keys: %v", mapKeys(got))
	}
	if !r.Required {
		t.Errorf("required-effect Required=false, want true")
	}
	if r.Success != 3 {
		t.Errorf("required-effect Success=%d, want 3", r.Success)
	}
	if r.Failure != 1 {
		t.Errorf("required-effect Failure=%d, want 1", r.Failure)
	}

	be, ok := got["besteffort-effect"]
	if !ok {
		t.Fatalf("missing besteffort-effect entry; got keys: %v", mapKeys(got))
	}
	if be.Required {
		t.Errorf("besteffort-effect Required=true, want false")
	}
	if be.Success != 2 {
		t.Errorf("besteffort-effect Success=%d, want 2", be.Success)
	}
	if be.Failure != 0 {
		t.Errorf("besteffort-effect Failure=%d, want 0", be.Failure)
	}
}

// mapKeys returns the keys of a map for diagnostic output in test failures.
func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

If `mapKeys` already exists in the test package (different test file), reuse it; do not duplicate.

- [ ] **Step 2: Run the test**

Run: `go test -run TestRunSummary_EffectStats_PopulatedWithSplit ./... -count=1 -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add runsummary_external_test.go
git commit -m "$(cat <<'EOF'
test(runsummary): verify EffectStats split for required + best-effort

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Test JSON round-trip for `EffectStats`

**Files:**
- Modify: `runsummary_external_test.go`

- [ ] **Step 1: Write the test**

Append to `runsummary_external_test.go`:

```go
// TestRunSummary_EffectStats_JSONRoundTrip verifies that EffectStats
// serializes to JSON with the documented field names and survives a
// round-trip without losing data.
func TestRunSummary_EffectStats_JSONRoundTrip(t *testing.T) {
	original := kitsune.RunSummary{
		EffectStats: map[string]kitsune.EffectStats{
			"required-effect":   {Required: true, Success: 3, Failure: 1},
			"besteffort-effect": {Required: false, Success: 2, Failure: 0},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got kitsune.RunSummary
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !reflect.DeepEqual(got.EffectStats, original.EffectStats) {
		t.Errorf("EffectStats mismatch after round-trip:\n  got  = %+v\n  want = %+v",
			got.EffectStats, original.EffectStats)
	}

	// Verify the documented JSON shape: lower-snake field names.
	var asMap map[string]interface{}
	if err := json.Unmarshal(data, &asMap); err != nil {
		t.Fatalf("Unmarshal as map: %v", err)
	}
	es, ok := asMap["effect_stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected effect_stats key in JSON; got: %s", data)
	}
	req, ok := es["required-effect"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected required-effect entry; got: %+v", es)
	}
	for _, want := range []string{"required", "success", "failure"} {
		if _, ok := req[want]; !ok {
			t.Errorf("missing JSON field %q in required-effect entry; got keys: %v", want, mapKeys(req))
		}
	}
}
```

Imports needed in the test file (add any that are not already present): `"encoding/json"`, `"reflect"`. The `mapKeys` helper from Task 2 is reused.

Note: `mapKeys` from Task 2 is generic (`mapKeys[V any]`). Using it here with `map[string]interface{}` works without explicit type parameter (Go infers `V = interface{}`).

- [ ] **Step 2: Run the test**

Run: `go test -run TestRunSummary_EffectStats_JSONRoundTrip ./... -count=1 -v`
Expected: PASS.

- [ ] **Step 3: Run the full short test suite to verify no regressions**

Run: `go test -short ./... -count=1`
Expected: all packages PASS.

- [ ] **Step 4: Run with race detector**

Run: `go test -race -run "TestRunSummary_EffectStats" ./... -count=1`
Expected: PASS, no races. (The atomic `Load` calls in the projection are race-safe; the test exercises this implicitly because the Runner's stage goroutines write the counters.)

- [ ] **Step 5: Commit**

```bash
git add runsummary_external_test.go
git commit -m "$(cat <<'EOF'
test(runsummary): verify EffectStats JSON round-trip

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Documentation, roadmap, and memory

**Files:**
- Modify: `doc/run-summary.md`
- Modify: `doc/roadmap.md`
- Modify: `~/.claude/projects/-Users-jonathan-projects-go-kitsune/memory/project_higher_level_authoring.md`

- [ ] **Step 1: Document the new field in `doc/run-summary.md`**

Open `doc/run-summary.md`. Find the section that describes the `RunSummary` struct shape (search for "Outcome" or "FinalizerErrs"). Add a new subsection after the existing fields documentation:

```markdown
### Per-Effect-stage statistics (`EffectStats`)

`summary.EffectStats` is a map keyed by stage name with one entry per `Effect` (or `TryEffect`) stage that ran. The value carries:

- `Required bool` — `true` for required effects (the default, or explicitly `Required()`); `false` for `BestEffort()`.
- `Success int64` — count of items that produced a non-error outcome.
- `Failure int64` — count of items whose effect exhausted retries with a terminal error.

The map is always allocated; it is empty (zero entries) when the pipeline contains no Effect stages. Use this when a dashboard or finalizer needs to render "required failures" vs "best-effort failures" separately without re-deriving the distinction from stage names.

`summary.Metrics.Stages[name]` continues to expose `Processed` and `Errors` for every stage (Effects included); `EffectStats` adds the required-vs-best-effort split that `Metrics` does not preserve.
```

The exact heading depth (`###` vs `##`) should match the surrounding sections of the file.

- [ ] **Step 2: Mark the roadmap item complete**

In `doc/roadmap.md`, find the line that begins:

```markdown
- [ ] **Per-stage `EffectStats` in `RunSummary`**: `runCtx.effectStats` (per-Effect-stage atomic success/failure counters)
```

Replace the entire bullet with:

```markdown
- [x] **Per-stage `EffectStats` in `RunSummary`** *(shipped 2026-04-26)*: `RunSummary.EffectStats map[string]EffectStats` is keyed by stage name and carries `Required bool`, `Success int64`, `Failure int64`. Always allocated (empty for pipelines with no Effects). Pure projection of `runCtx.effectStats`; no new tracking. Plan at `docs/superpowers/plans/2026-04-26-effectstats-runsummary.md`.
```

- [ ] **Step 3: Append the memory note**

Append to `~/.claude/projects/-Users-jonathan-projects-go-kitsune/memory/project_higher_level_authoring.md`:

```markdown
`RunSummary.EffectStats map[string]EffectStats{Required, Success, Failure}` exposes the required-vs-best-effort split per Effect stage; the map is always allocated, empty when no Effects ran (2026-04-26).
```

The memory file is outside the repo; do NOT commit it.

- [ ] **Step 4: Commit roadmap and docs**

```bash
git add doc/run-summary.md doc/roadmap.md
git commit -m "$(cat <<'EOF'
docs: document RunSummary.EffectStats and mark roadmap item complete

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Final verification

- [ ] **Step 1: Full test suite**

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

Concise report: number of commits, gates passed, one-line summary of the new field.

---

## Self-review

- **Spec coverage:** Public type → Task 1 step 3. Field on RunSummary → Task 1 step 4. Population in Runner.Run → Task 1 step 5. Empty-map test → Task 1 step 1. Populated-map test → Task 2. JSON round-trip test → Task 3. Documentation → Task 4. Roadmap → Task 4. Memory → Task 4.
- **Type-name consistency:** `EffectStats` (struct), `RunSummary.EffectStats` (field, same name) — Go allows this; the field shadows the type at the use site through the qualified `kitsune.RunSummary.EffectStats` access path. Unambiguous in practice. JSON tag `effect_stats` matches the existing snake-case convention (`duration_ns`, `completed_at`).
- **No placeholders:** every step has concrete code or commands.
- **Risk:** `ForEachRunner.Run` and `DrainRunner.Run` both delegate to `*Runner.Run` (terminal.go lines 390 and 442), so they inherit `EffectStats` population without separate code changes. `MergeRunners` also uses `*Runner.Run`. All paths covered by Task 1's single change.
- **Sequencing:** Task 1 must complete before Tasks 2/3 (they depend on the new type). Task 4 documents what was built. Task 5 is the final gate.
