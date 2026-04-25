# Inspector — RunSummary surfacing implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface `RunSummary` in the Inspector dashboard. After every `Run`, the dashboard shows the final outcome (RunSuccess / RunPartialSuccess / RunFailure), duration, completion timestamp, the fatal error if any, and any finalizer errors. Inspector receives the summary via a new optional hook callback the runner fires at end-of-run.

**Architecture:** Add a `RunSummaryHook` optional interface in `runsummary.go` next to `RunSummary`. `Runner.Run` already builds the summary and runs finalizers; after finalizers complete, type-assert the configured hook against `RunSummaryHook` and call `OnRunComplete(ctx, summary)`. The Inspector implements that callback, stores the latest summary under its existing mutex, and broadcasts a new `summary` SSE event. The UI listens for that event and renders a dedicated "Last Run" card with the outcome, duration, completion time, and a collapsed-by-default Finalizer Errors section.

**Tech Stack:** Go generics, SSE, the existing inspector hook plumbing.

**Module path:** `github.com/zenbaku/go-kitsune`

---

## Background and design notes

### Why a new hook interface

`Runner.Run` already returns `(RunSummary, error)`. Synchronous callers see the summary directly. But the inspector observes runs through the hook interface; it never holds a `*Runner`. A hook callback at end-of-run is the natural place to give the inspector access to the summary without the caller having to manually pass it in.

### Why not a finalizer

`WithFinalizer` is the user-facing way to register post-run callbacks. It would be awkward to ask users who attach the inspector via `WithHook(insp)` to also wire `WithFinalizer(insp.AsFinalizer())` — the inspector should already know about everything it needs from the hook.

### Where the type lives

`RunSummary` is in package `kitsune` (`runsummary.go`). The `Hook` interface and its existing optional extensions (`GraphHook`, `BufferHook`, `OverflowHook`, `SupervisionHook`, `SampleHook`) are in package `hooks/`. To avoid an import cycle (the hooks package would need to import kitsune for `RunSummary`), define `RunSummaryHook` in `runsummary.go` alongside the data type. The inspector adds an import of the root `kitsune` package — the same pattern `testkit/` already uses.

### Ordering inside `Runner.Run`

Finalizers must run before `OnRunComplete` is fired so the summary's `FinalizerErrs` is populated when the inspector receives it. The order:

1. Stage graph completes (`pipelineErr` is set).
2. Derive `Outcome`, populate `Duration`, `CompletedAt`, `Err`, `Metrics`.
3. Run each finalizer in order; capture `FinalizerErrs[i]`.
4. Type-assert `hook` against `RunSummaryHook`; if it satisfies, call `OnRunComplete(ctx, summary)`.
5. Return `summary, pipelineErr`.

### Concurrent runs

If a single `Inspector` is wired to multiple `Runner`s and they overlap in time, `OnRunComplete` may be called concurrently from different goroutines. The inspector wraps writes to its `lastRunSummary` field in the existing `i.mu` mutex.

### Visual placement

The inspector layout currently has:

```
┌───────────────────────────────────────────┐
│ KPI bar (uptime, items, throughput, …)    │
├───────────────────────────────────────────┤
│ Pipeline Graph │ Stage Metrics            │
│                │                          │
├────────────────┴──────────────────────────┤
│ Event Log                                 │
└───────────────────────────────────────────┘
```

Add a "Last Run" card at the top, between the KPI bar and the graph. It shows:

```
┌────────────────────────────────────────────┐
│ Last Run    [● RunSuccess]   1.4s          │
│ Completed: 14:23:05.123                    │
│ ▸ 1 finalizer error (click to expand)      │
└────────────────────────────────────────────┘
```

The card is hidden until the first `OnRunComplete` arrives. After that, every subsequent run replaces its content.

---

## Spec deltas

These are pragmatic scoping decisions captured during plan-writing.

1. **`RunSummaryHook` is an optional extension, not a required Hook method.** Existing user-supplied `kitsune.Hook` implementations do not need to be updated. The runner uses a type-assertion (the same pattern as `GraphHook` and `BufferHook`).

2. **Persistence of the last summary in `InspectorStore`.** The existing store already persists graph, per-stage state, and recent samples. Including the last summary lets a refreshed browser session immediately see the outcome of the previous run. Single field; minimal cost.

3. **Errors serialize as `string`.** `error` is not JSON-friendly. The SSE payload converts `summary.Err` and each `summary.FinalizerErrs[i]` to its `Error()` string. The store does the same.

4. **`MetricsSnapshot` is NOT included in the SSE payload.** The inspector already streams stage stats every 250ms. Bundling the snapshot into the summary event would duplicate that data. The summary event carries `Outcome`, `Err`, `Duration`, `CompletedAt`, and `FinalizerErrs` only.

5. **No "currently running" state in the card.** The card stays hidden until the first run completes, and shows the latest run thereafter. Per-stage status already tells the user what's running; the summary card is end-of-run information.

---

## File structure

| File | Change |
|---|---|
| `runsummary.go` | Add `RunSummaryHook` interface |
| `kitsune.go` | In `Runner.Run`, type-assert and call `OnRunComplete` after finalizers run |
| `inspector/inspector.go` | Add import of root `kitsune` package; add `lastRunSummary` field; implement `OnRunComplete`; broadcast `summary` SSE event; persist via store |
| `inspector/store.go` | Add `LastSummary *summarySnapshot` field to the persisted state shape |
| `inspector/ui.html` | Add SSE listener for the `summary` event; render the "Last Run" card |
| `inspector/inspector_test.go` | Test that `OnRunComplete` is called and the summary appears in `/state` |
| `doc/inspector.md` | Document the new card |
| Memory: `project_higher_level_authoring.md` | Note the inspector now surfaces RunSummary |

---

## Tasks

### Task 1: Define `RunSummaryHook` interface

Pure additive: a new optional interface in `runsummary.go`. No callers yet.

**Files:**
- Modify: `runsummary.go`

- [ ] **Step 1: Append the interface to `runsummary.go`**

After `RunSummary` and before any `func deriveRunOutcome` (or wherever the bottom of the file is), add:

```go
// RunSummaryHook is an optional extension of [Hook]. If the hook passed to
// [WithHook] implements RunSummaryHook, [Runner.Run] calls OnRunComplete once
// at the end of the run, after the [RunSummary] is computed and after any
// finalizers attached via [Runner.WithFinalizer] have run. The summary
// passed to OnRunComplete is the same value Run returns to the caller.
//
// Hooks that observe per-item events implement [Hook]; hooks that observe
// the run as a whole implement RunSummaryHook. A single hook may implement
// both.
//
// Checked via type assertion; existing Hook implementations need not
// implement this.
type RunSummaryHook interface {
    OnRunComplete(ctx context.Context, summary RunSummary)
}
```

Add `"context"` to the import block if not already present.

- [ ] **Step 2: Build to verify**

Run: `go build ./...`
Expected: clean. (No callers yet.)

- [ ] **Step 3: Commit**

```bash
git add runsummary.go
git commit -m "feat(runsummary): add optional RunSummaryHook interface"
```

---

### Task 2: Fire `OnRunComplete` from `Runner.Run`

Wire the hook callback after finalizers run.

**Files:**
- Modify: `kitsune.go`

- [ ] **Step 1: Locate the end of `Runner.Run`**

In `kitsune.go`, find the section near the end of `Runner.Run` where `summary` is constructed and finalizers are run. The current shape is:

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

return summary, pipelineErr
```

- [ ] **Step 2: Add the OnRunComplete dispatch**

Insert immediately before the `return`:

```go
if rsh, ok := hook.(RunSummaryHook); ok {
    rsh.OnRunComplete(ctx, summary)
}

return summary, pipelineErr
```

The dispatch happens AFTER finalizers run so `FinalizerErrs` is populated when the hook receives the summary.

- [ ] **Step 3: Build to verify**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 4: Run the test suite**

Run: `task test`
Expected: PASS. No existing test asserts on this behaviour; it's purely additive.

- [ ] **Step 5: Commit**

```bash
git add kitsune.go
git commit -m "feat(runsummary): fire OnRunComplete after finalizers run"
```

---

### Task 3: Implement `OnRunComplete` in the inspector

The inspector receives the summary, stores it, and broadcasts a SSE `summary` event. JSON shape carries `outcome`, `outcomeName` (string form), `err` (string), `duration_ns`, `completedAt`, `finalizerErrs` (array of strings or nulls).

**Files:**
- Modify: `inspector/inspector.go`

- [ ] **Step 1: Add the import**

In `inspector/inspector.go`, add the kitsune root import:

```go
import (
    // existing imports...
    kitsune "github.com/zenbaku/go-kitsune"
    kithooks "github.com/zenbaku/go-kitsune/hooks"
)
```

- [ ] **Step 2: Add the `lastRunSummary` field to `Inspector`**

Locate the `Inspector` struct (around line 65). Add a field next to the existing `graph` field:

```go
type Inspector struct {
    // ... existing fields ...
    graph          []kithooks.GraphNode
    lastRunSummary *summarySnapshot // populated by OnRunComplete; nil before first run
    // ... rest unchanged ...
}
```

- [ ] **Step 3: Define the `summarySnapshot` JSON shape**

Add near the existing `stageSnapshot` (around line 114):

```go
// summarySnapshot is the JSON shape sent over SSE for the "summary" event
// and persisted via the InspectorStore. It is the inspector-facing
// projection of [kitsune.RunSummary]; errors are converted to their string
// form and the MetricsSnapshot is intentionally excluded (live stage
// metrics are streamed every tick).
type summarySnapshot struct {
    Outcome       int      `json:"outcome"`
    OutcomeName   string   `json:"outcomeName"`
    Err           string   `json:"err,omitempty"`
    DurationNs    int64    `json:"duration_ns"`
    CompletedAt   int64    `json:"completedAt"` // unix milliseconds
    FinalizerErrs []string `json:"finalizerErrs,omitempty"`
}

func toSummarySnapshot(s kitsune.RunSummary) summarySnapshot {
    snap := summarySnapshot{
        Outcome:     int(s.Outcome),
        OutcomeName: s.Outcome.String(),
        DurationNs:  s.Duration.Nanoseconds(),
        CompletedAt: s.CompletedAt.UnixMilli(),
    }
    if s.Err != nil {
        snap.Err = s.Err.Error()
    }
    if len(s.FinalizerErrs) > 0 {
        snap.FinalizerErrs = make([]string, len(s.FinalizerErrs))
        for i, e := range s.FinalizerErrs {
            if e != nil {
                snap.FinalizerErrs[i] = e.Error()
            }
        }
    }
    return snap
}
```

- [ ] **Step 4: Implement `OnRunComplete` on the Inspector**

Add a method near the other hook-method implementations (after `OnGraph`, around line 372):

```go
// OnRunComplete implements [kitsune.RunSummaryHook]. It is called once per
// Run after finalizers have completed, with the run's final summary. The
// inspector stores the summary, broadcasts a "summary" SSE event so the
// dashboard can render it, and persists the snapshot if a store is attached.
func (i *Inspector) OnRunComplete(_ context.Context, s kitsune.RunSummary) {
    snap := toSummarySnapshot(s)

    i.mu.Lock()
    i.lastRunSummary = &snap
    i.mu.Unlock()

    data, _ := json.Marshal(snap)
    i.broadcast(sseMsg{"summary", data})

    if i.store != nil {
        i.persistAfterEvent(context.Background())
    }
}
```

If `persistAfterEvent` does not exist as a helper, the existing pattern is to call `i.store.Save(...)` from `loadFromStore`'s reverse helper or to schedule a save on the next tick. Inspect the existing code for the canonical save pattern; the simplest change is to fire a tick of the existing save loop. Look at how the existing `OnGraph` or `OnItem` triggers store saves; mirror that pattern.

- [ ] **Step 5: Include the summary in the `/state` response**

Find `handleState` in `inspector/inspector.go` (search for `handleState`). The current handler builds a JSON response containing graph, stages, and log entries. Extend the response struct to include the latest summary:

```go
i.mu.RLock()
summary := i.lastRunSummary
i.mu.RUnlock()

// Include in the response JSON:
// "summary": summary  (omits when nil)
```

Use the same locking pattern the existing handler uses; if it uses a single read-lock to capture all the fields at once, add `summary` to the same critical section.

- [ ] **Step 6: Build and run inspector tests**

Run: `go test ./inspector/`
Expected: PASS. Existing tests should be unaffected (the new field is unread by them).

- [ ] **Step 7: Commit**

```bash
git add inspector/inspector.go
git commit -m "feat(inspector): implement OnRunComplete and broadcast summary SSE event"
```

---

### Task 4: Persist the summary in `InspectorStore`

Saves the summary alongside graph and stage state so a refreshed browser sees the latest outcome.

**Files:**
- Modify: `inspector/store.go`

- [ ] **Step 1: Inspect the existing `InspectorState` shape**

Open `inspector/store.go`. Find the `InspectorState` struct (or whatever the persisted shape is named). It currently includes graph, stages, log, etc.

- [ ] **Step 2: Add the `LastSummary` field**

Add a field for the last summary:

```go
type InspectorState struct {
    // ... existing fields ...
    LastSummary *summarySnapshot `json:"lastSummary,omitempty"`
}
```

(The exact name of the type is whatever the existing code uses. The field is `LastSummary` and points to the same `*summarySnapshot` defined in inspector.go.)

- [ ] **Step 3: Wire the field through save and load**

Find the helper that builds the `InspectorState` for save (likely a `snapshot` method or similar). Add `LastSummary: i.lastRunSummary` to its struct literal.

Find `loadFromStore` and the corresponding save call. After the existing fields are restored, restore `i.lastRunSummary` from the loaded state:

```go
i.lastRunSummary = state.LastSummary
```

- [ ] **Step 4: Run inspector tests**

Run: `go test ./inspector/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add inspector/store.go inspector/inspector.go
git commit -m "feat(inspector): persist last RunSummary via InspectorStore"
```

(The Inspector save/load wiring may have changed both files; if so, single combined commit is fine.)

---

### Task 5: UI summary card

Adds a "Last Run" card to the dashboard. Hidden until first summary arrives.

**Files:**
- Modify: `inspector/ui.html`

- [ ] **Step 1: Add the card markup**

Find the existing top-of-dashboard section (KPI bar). Insert a new card AFTER the KPI bar and BEFORE the DAG/Stages row. Use the existing card styling for visual consistency.

```html
<div id="run-summary-card" style="display:none">
  <div class="sec-title">Last Run</div>
  <div id="rs-card" class="card">
    <div id="rs-row" class="rs-row">
      <span id="rs-outcome" class="rs-outcome"></span>
      <span id="rs-duration" class="rs-duration"></span>
      <span id="rs-completed" class="rs-completed"></span>
    </div>
    <div id="rs-err" class="rs-err" style="display:none"></div>
    <details id="rs-finalizer-errs" class="rs-fin" style="display:none">
      <summary id="rs-finalizer-summary"></summary>
      <ul id="rs-finalizer-list"></ul>
    </details>
  </div>
</div>
```

- [ ] **Step 2: Add the CSS**

In the `<style>` block, add styles consistent with the existing aesthetic. Approximate values:

```css
#run-summary-card { margin: 12px 0; }
#rs-card { background: var(--card); border: 1px solid var(--border); border-radius: 8px; padding: 12px 16px; }
.rs-row { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
.rs-outcome { font-size: 14px; font-weight: 700; padding: 3px 10px; border-radius: 12px; }
.rs-outcome.success { background: rgba(76, 175, 80, .14); color: var(--green); }
.rs-outcome.partial { background: rgba(255, 193,   7, .14); color: var(--yellow); }
.rs-outcome.failure { background: rgba(244,  67,  54, .14); color: var(--red); }
.rs-duration { color: var(--muted); font-variant-numeric: tabular-nums; }
.rs-completed { color: var(--muted); font-size: 11px; }
.rs-err { margin-top: 8px; font-size: 12px; color: var(--red); white-space: pre-wrap; }
.rs-fin { margin-top: 8px; font-size: 12px; }
.rs-fin summary { cursor: pointer; color: var(--yellow); }
.rs-fin ul { margin: 6px 0 0 18px; color: var(--muted); }
```

- [ ] **Step 3: Add the SSE listener and render function**

Find where the existing SSE event listeners are registered (look for `es.addEventListener`). Add:

```javascript
es.addEventListener('summary', e => { renderSummary(JSON.parse(e.data)); });
```

Add the render function near the existing `renderDAG` / `renderTable`:

```javascript
function renderSummary(s) {
  document.getElementById('run-summary-card').style.display = '';
  const out = document.getElementById('rs-outcome');
  const cls = s.outcome === 0 ? 'success' : s.outcome === 1 ? 'partial' : 'failure';
  out.className = 'rs-outcome ' + cls;
  out.textContent = s.outcomeName;

  document.getElementById('rs-duration').textContent = fmtDuration(s.duration_ns);
  document.getElementById('rs-completed').textContent = 'Completed ' + fmtCompletedAt(s.completedAt);

  const errEl = document.getElementById('rs-err');
  if (s.err) {
    errEl.style.display = '';
    errEl.textContent = s.err;
  } else {
    errEl.style.display = 'none';
  }

  const finEl = document.getElementById('rs-finalizer-errs');
  const finList = document.getElementById('rs-finalizer-list');
  const finSum = document.getElementById('rs-finalizer-summary');
  const errs = (s.finalizerErrs || []).filter(x => x);
  if (errs.length) {
    finEl.style.display = '';
    finSum.textContent = errs.length + ' finalizer error' + (errs.length === 1 ? '' : 's');
    finList.innerHTML = errs.map(e => `<li>${esc(e)}</li>`).join('');
  } else {
    finEl.style.display = 'none';
  }
}

function fmtDuration(ns) {
  if (ns < 1000)        return ns + 'ns';
  if (ns < 1e6)         return (ns/1e3).toFixed(1) + 'µs';
  if (ns < 1e9)         return (ns/1e6).toFixed(1) + 'ms';
  return (ns/1e9).toFixed(2) + 's';
}

function fmtCompletedAt(ms) {
  const d = new Date(ms);
  return d.toLocaleTimeString('en', {hour12: false, fractionalSecondDigits: 3});
}
```

- [ ] **Step 4: On initial load, render the persisted summary if any**

The `/state` endpoint now includes `summary`. Find the function that processes the initial `/state` fetch (look for `fetch('/state')` or the SSE `state` event handler). After the existing fields are processed, add:

```javascript
if (state.summary) renderSummary(state.summary);
```

- [ ] **Step 5: Spot-check by running the example**

Run: `go run ./examples/effect`
Expected: prints OK / FAIL lines, exits 0.

For a more visual check, modify the existing `examples/inspector` example (or temporarily edit a small program) to attach `inspector.New()` and run a pipeline, then open the URL in a browser. The "Last Run" card should appear after the run completes with the appropriate outcome dot. This step is optional; the test in Task 6 covers the wiring.

- [ ] **Step 6: Commit**

```bash
git add inspector/ui.html
git commit -m "feat(inspector): add Last Run summary card to UI"
```

---

### Task 6: Tests

Verify `OnRunComplete` is called and the summary surfaces through both SSE and `/state`.

**Files:**
- Modify: `inspector/inspector_test.go`

- [ ] **Step 1: Add a test that exercises the full path**

Append to `inspector/inspector_test.go`:

```go
// TestInspector_OnRunComplete verifies that the inspector receives the run
// summary when the runner completes, broadcasts a "summary" SSE event, and
// includes the summary in /state for browser refreshes.
func TestInspector_OnRunComplete(t *testing.T) {
    insp := inspector.New()
    defer insp.Close()

    src := kitsune.FromSlice([]int{1, 2, 3})
    runner := src.ForEach(func(_ context.Context, _ int) error { return nil })

    summary, err := runner.Run(context.Background(), kitsune.WithHook(insp))
    if err != nil {
        t.Fatal(err)
    }
    if summary.Outcome != kitsune.RunSuccess {
        t.Fatalf("Outcome=%v, want RunSuccess", summary.Outcome)
    }

    // Allow the broadcast goroutine to deliver the SSE event.
    time.Sleep(50 * time.Millisecond)

    // /state should now include the summary.
    res, err := http.Get(insp.URL() + "/state")
    if err != nil {
        t.Fatal(err)
    }
    defer res.Body.Close()
    body, _ := io.ReadAll(res.Body)
    var got struct {
        Summary *struct {
            Outcome     int    `json:"outcome"`
            OutcomeName string `json:"outcomeName"`
        } `json:"summary"`
    }
    if err := json.Unmarshal(body, &got); err != nil {
        t.Fatal(err)
    }
    if got.Summary == nil {
        t.Fatalf("expected summary in /state response; got body: %s", body)
    }
    if got.Summary.OutcomeName != "RunSuccess" {
        t.Errorf("OutcomeName=%q, want RunSuccess", got.Summary.OutcomeName)
    }
}

// TestInspector_OnRunComplete_FinalizerErrors verifies that finalizer
// errors flow through to the summary the inspector sees.
func TestInspector_OnRunComplete_FinalizerErrors(t *testing.T) {
    insp := inspector.New()
    defer insp.Close()

    sentinel := errors.New("finalizer-failed")
    src := kitsune.FromSlice([]int{1})
    runner := src.ForEach(func(_ context.Context, _ int) error { return nil }).
        WithFinalizer(func(_ context.Context, _ kitsune.RunSummary) error {
            return sentinel
        })

    summary, err := runner.Run(context.Background(), kitsune.WithHook(insp))
    if err != nil {
        t.Fatal(err)
    }
    if len(summary.FinalizerErrs) != 1 || !errors.Is(summary.FinalizerErrs[0], sentinel) {
        t.Fatalf("FinalizerErrs=%v, want [%v]", summary.FinalizerErrs, sentinel)
    }

    time.Sleep(50 * time.Millisecond)

    res, _ := http.Get(insp.URL() + "/state")
    defer res.Body.Close()
    body, _ := io.ReadAll(res.Body)
    var got struct {
        Summary *struct {
            FinalizerErrs []string `json:"finalizerErrs"`
        } `json:"summary"`
    }
    if err := json.Unmarshal(body, &got); err != nil {
        t.Fatal(err)
    }
    if got.Summary == nil || len(got.Summary.FinalizerErrs) != 1 ||
        got.Summary.FinalizerErrs[0] != "finalizer-failed" {
        t.Errorf("FinalizerErrs in /state = %+v, want [\"finalizer-failed\"]", got.Summary)
    }
}
```

Add the necessary imports: `errors`, `io`, `net/http`, `encoding/json`, `time`. Some may already be imported; check the existing test file header.

- [ ] **Step 2: Run the new tests**

Run: `go test -run "TestInspector_OnRunComplete" ./inspector/ -v`
Expected: 2 PASS.

If `TestInspector_OnRunComplete` fails because the `time.Sleep(50ms)` is racy under load, increase to 200ms. The sleep is needed because SSE broadcast happens in a goroutine; the `/state` handler reads the field synchronously, but the field is set before the broadcast, so reading `/state` immediately after `Run` returns should already see the summary. Verify with `task test:race` to confirm no actual race.

- [ ] **Step 3: Run the inspector test suite**

Run: `go test ./inspector/ -race`
Expected: PASS, no races.

- [ ] **Step 4: Run the full short suite**

Run: `task test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add inspector/inspector_test.go
git commit -m "test(inspector): add OnRunComplete + finalizer-errors tests"
```

---

### Task 7: Documentation

Update the inspector guide to describe the new card.

**Files:**
- Modify: `doc/inspector.md`

- [ ] **Step 1: Add the "Last Run" section**

Find the "Dashboard layout" section. After the KPI bar subsection, insert a new subsection (renumber subsequent ones if they're numbered):

```markdown
### 2. Last Run card

A card sits below the KPI bar and is hidden until the first run completes. After every run it shows:

- **Outcome badge**: green `RunSuccess`, yellow `RunPartialSuccess`, red `RunFailure`.
- **Duration**: wall-clock time of the run (formatted as ms/s).
- **Completed at**: the run's completion timestamp.
- **Fatal error** (if any): the error returned by `Run`, in red.
- **Finalizer errors** (if any): a collapsible list of errors returned by finalizers attached via [`WithFinalizer`](operators.md#withfinalizer).

The card replaces its content on every subsequent run. If you persist inspector state via [`WithStore`](#persistence-with-withstore) the latest summary is restored on browser reload.

The card is driven by the [`RunSummaryHook`](operators.md#run-summary) optional interface that the inspector implements. The runner fires `OnRunComplete` once per `Run`, after finalizers have run, with the same `RunSummary` that synchronous callers see as the first return value.
```

Renumber the subsequent subsections (Pipeline Graph, Stage Metrics, Event Log) accordingly.

- [ ] **Step 2: Cross-link from operators.md Run Summary section**

Find the "Run Summary" section in `doc/operators.md`. Add a brief mention near the top:

```markdown
The [Inspector dashboard](inspector.md#2-last-run-card) renders the latest `RunSummary` automatically when the inspector is attached as a hook.
```

- [ ] **Step 3: Run-summary feature guide cross-link**

In `doc/run-summary.md`, find the section about reading the summary. Add a sentence:

```markdown
Hooks that observe the run as a whole (rather than individual items) implement [`RunSummaryHook`](operators.md#run-summary). The inspector uses this to render the run's outcome on its dashboard; see [Inspector → Last Run card](inspector.md#2-last-run-card).
```

- [ ] **Step 4: Commit**

```bash
git add doc/inspector.md doc/operators.md doc/run-summary.md
git commit -m "docs(inspector): describe Last Run card; cross-link from RunSummary docs"
```

---

### Task 8: Memory + final verification

**Files:**
- Modify: `~/.claude/projects/-Users-jonathan-projects-go-kitsune/memory/project_higher_level_authoring.md`

- [ ] **Step 1: Update the memory note**

Add a sentence to the "M3" section indicating the inspector now surfaces the summary:

```markdown
The Inspector dashboard now renders the latest summary on a "Last Run" card via the optional [`RunSummaryHook`](operators.md#run-summary) interface; see `doc/inspector.md`.
```

- [ ] **Step 2: Final test pass**

```bash
task test
task test:race
task test:property
task test:examples
```

All four must PASS.

- [ ] **Step 3: Inform the user**

Concise report: how many commits, gates passed, and a one-line summary of what's now visible in the dashboard.

---

## Self-review notes (for the plan author, not the engineer)

- **Spec coverage:** every M3-Inspector surface gap from the original review is mapped to a task: `RunSummaryHook` (Task 1), Runner.Run dispatch (Task 2), inspector implementation (Task 3), persistence (Task 4), UI rendering (Task 5), tests (Task 6), docs (Task 7).
- **Type-name consistency:** `RunSummaryHook`, `OnRunComplete`, `summarySnapshot`, `toSummarySnapshot`, `lastRunSummary` are used consistently across tasks.
- **No placeholders:** Each step shows the actual content. Task 3 step 4 mentions a `persistAfterEvent` helper that may not exist yet; the instruction is to inspect the existing save pattern and mirror it. That is judgment work, not a placeholder.
- **Sequencing:** Tasks 1-4 are sequential (each depends on the previous compiling). Task 5 (UI) depends on Task 3's SSE event format. Task 6 (tests) depends on the full chain. Tasks 7 and 8 are tail items.
- **Risk:** Task 3's `persistAfterEvent` instruction is the most ambiguous step. If the existing save/load wiring is more complex than expected, the implementer may want to defer persistence to Task 4 only and mark Task 3 as not yet persisting (the SSE broadcast still works without persistence). Acceptable degradation.
