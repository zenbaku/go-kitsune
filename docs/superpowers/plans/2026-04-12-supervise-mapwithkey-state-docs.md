# Supervise + MapWithKey State Contract Documentation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Document the true `Supervise` + `MapWithKey` state contract (in-process Refs survive supervised restarts within a single `Run`; cross-process durability requires an external Store) and codify it with a panic-triggered test.

**Architecture:** The existing code already preserves per-key `Ref` state across supervised restarts because `refRegistry.init()` runs once per `Run()` and the `keyedRefMap` is captured by the `inner` closure that `internal.Supervise` invokes on each restart. The roadmap's premise (silent state loss on restart) is incorrect for within-`Run` supervision; state is only lost across separate `Run` calls. This plan corrects the docs to describe actual behaviour and adds a panic-based regression test alongside the existing error-based one.

**Tech Stack:** Go, standard `testing` package.

---

### Task 1: Add panic-based supervised restart test covering MemoryStore-backed Refs

**Files:**
- Modify: `state_test.go` (append after line 1057)

- [ ] **Step 1: Read `state_test.go:1024-1057` to confirm the existing `TestMapWithKey_Supervise_KeyedRefPreserved` shape and imports.**

- [ ] **Step 2: Append a new test function `TestMapWithKey_Supervise_KeyedRefPreservedOnPanic` directly after line 1057. Exact code to add:**

```go
func TestMapWithKey_Supervise_KeyedRefPreservedOnPanic(t *testing.T) {
	// Contract: per-key Ref state is preserved across supervised restarts
	// within a single Run() call, regardless of whether the restart is
	// triggered by an error or a panic. The keyedRefMap is constructed once
	// in refRegistry.init() before any stage starts and is captured by the
	// stage's inner loop closure; internal.Supervise re-invokes that closure
	// on restart, so the map (and every Ref it holds) survives.
	//
	// State is NOT preserved across separate Run() calls with the default
	// in-memory MemoryStore: a new Run() builds a fresh refRegistry. For
	// cross-Run durability the caller must configure an external Store.
	ctx := context.Background()
	key := kitsune.NewKey[int]("mwk_ref_panic_preserved", 0)
	calls := 0
	p := kitsune.FromSlice([]string{"a", "a"}) // same key twice
	got, err := kitsune.MapWithKey(p,
		func(s string) string { return s },
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], s string) (int, error) {
			calls++
			if calls == 1 {
				// Set "a"'s ref to 77, then panic: triggers supervised restart.
				_ = ref.Set(ctx, 77)
				panic("trigger restart via panic")
			}
			// After restart: the second "a" reads back the preserved ref.
			return ref.Get(ctx)
		},
		kitsune.Supervise(kitsune.RestartAlways(1, nil)),
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %v (len %d), want 1 item", got, len(got))
	}
	if got[0] != 77 {
		t.Errorf("ref value after panic restart: got %d, want 77", got[0])
	}
}
```

- [ ] **Step 3: Run the test: `go test -run TestMapWithKey_Supervise_KeyedRefPreservedOnPanic -race ./...`. It must pass (the code already supports this behaviour).**

- [ ] **Step 4: Run the group: `go test -run TestMapWithKey_Supervise -race ./...` to confirm no regressions.**

- [ ] **Step 5: Commit.**

```bash
git add state_test.go
git commit -m "test(state): verify MapWithKey Ref state survives supervised panic restart"
```

---

### Task 2: Update `Supervise` godoc with stateful-stage note

**Files:**
- Modify: `config.go:320-334`

- [ ] **Step 1: Read `config.go:318-340` to confirm the exact current godoc text.**

- [ ] **Step 2: Replace the `Supervise` godoc block. Old text:**

```go
// Supervise sets the supervision policy for a stage.
// See [RestartOnError], [RestartOnPanic], [RestartAlways] for convenience constructors.
// When combined with [OnError], the error handler runs first per item; Supervise
// only triggers a restart when the error handler's final decision is Halt.
func Supervise(policy SupervisionPolicy) StageOption {
```

**New text:**

```go
// Supervise sets the supervision policy for a stage.
// See [RestartOnError], [RestartOnPanic], [RestartAlways] for convenience constructors.
// When combined with [OnError], the error handler runs first per item; Supervise
// only triggers a restart when the error handler's final decision is Halt.
//
// Stateful stages and restarts: for [MapWith], [MapWithKey], [FlatMapWith], and
// [FlatMapWithKey], per-key [Ref] state is preserved across supervised restarts
// within the same [Run] call; the key map is allocated once per Run and captured
// by the restarted loop. State is NOT preserved when the process itself restarts
// (a new Run call) unless an external Store is configured via [WithStore]; the
// default in-memory store is scoped to a single Run.
func Supervise(policy SupervisionPolicy) StageOption {
```

- [ ] **Step 3: Run `go build ./...` and `go vet ./...` to confirm the change compiles cleanly.**

- [ ] **Step 4: Commit.**

```bash
git add config.go
git commit -m "docs(config): clarify Supervise state semantics for stateful stages"
```

---

### Task 3: Update `doc/operators.md` MapWithKey section with Supervise note

**Files:**
- Modify: `doc/operators.md` (after the `WithDefaultKeyTTL` paragraph, before the `---` divider)

- [ ] **Step 1: Read `doc/operators.md:1600-1610` to confirm the exact insertion point (the line ending with `... explicitly disables eviction even when a run-level default is set.`).**

- [ ] **Step 2: After that sentence (line 1603), append a blank line and this new paragraph before the `---` divider:**

```markdown
**Supervise and state lifetime:** When combined with `Supervise`, per-key `Ref` state IS preserved across supervised restarts within a single `Run` call: the keyed map is allocated once per run and captured by the stage's restarted loop, so a panic or error that triggers a restart does not zero the accumulated state. State is NOT preserved across separate `Run` calls with the default in-process store; callers that need cross-run durability must configure an external [Store](state.md) via `WithStore`.
```

- [ ] **Step 3: Verify by reading the surrounding 20 lines after the edit.**

- [ ] **Step 4: Commit.**

```bash
git add doc/operators.md
git commit -m "docs(operators): document MapWithKey state lifetime under Supervise"
```

---

### Task 4: Update "Combining OnError and Supervise" section

**Files:**
- Modify: `doc/operators.md:2826-2830`

- [ ] **Step 1: Read `doc/operators.md:2825-2835` to confirm the current text.**

- [ ] **Step 2: Insert a new paragraph between the first paragraph and the "See the Error Handling guide" sentence. Old text:**

```markdown
### Combining `OnError` and `Supervise`

`OnError` and `Supervise` operate at different levels and can be used together on the same stage. `OnError` is evaluated per item; `Supervise` is evaluated when the stage loop itself crashes. The evaluation order is: `OnError` runs first; only when its final decision is `Halt` (including after retry exhaustion) does `Supervise` see the error and decide whether to restart the stage.

See the [Error Handling guide](error-handling.md) for the full evaluation model, common combination patterns (retry-then-restart, skip-unless-fatal-then-restart), and observability.
```

**New text:**

```markdown
### Combining `OnError` and `Supervise`

`OnError` and `Supervise` operate at different levels and can be used together on the same stage. `OnError` is evaluated per item; `Supervise` is evaluated when the stage loop itself crashes. The evaluation order is: `OnError` runs first; only when its final decision is `Halt` (including after retry exhaustion) does `Supervise` see the error and decide whether to restart the stage.

**Stateful stages under Supervise:** For stateful stages (`MapWith`, `MapWithKey`, `FlatMapWith`, `FlatMapWithKey`), per-key `Ref` state is preserved across supervised restarts within a single `Run` call: the key map is allocated once per run and captured by the restarted loop. State is only lost when the surrounding process terminates and a new `Run` starts; for cross-run durability, configure an external Store via `WithStore`.

See the [Error Handling guide](error-handling.md) for the full evaluation model, common combination patterns (retry-then-restart, skip-unless-fatal-then-restart), and observability.
```

- [ ] **Step 3: Commit.**

```bash
git add doc/operators.md
git commit -m "docs(operators): note stateful-stage restart semantics in OnError+Supervise section"
```

---

### Task 5: Mark both roadmap items complete

**Files:**
- Modify: `doc/roadmap.md`

- [ ] **Step 1: Read `doc/roadmap.md:15-20` to confirm the exact text of the correctness item.**

- [ ] **Step 2: Change `- [ ]` to `- [x]` on the `Supervise + MapWithKey silent state loss on restart` line.**

- [ ] **Step 3: Read `doc/roadmap.md:51-55` to confirm the exact text of the testing item.**

- [ ] **Step 4: Change `- [ ]` to `- [x]` on the `Test Supervise + MapWithKey state contract on restart` line.**

- [ ] **Step 5: Commit.**

```bash
git add doc/roadmap.md
git commit -m "chore: mark Supervise+MapWithKey state docs and test items done in roadmap"
```

---

### Task 6: Final verification

- [ ] **Step 1: Run `task test:race` and confirm all tests pass.**

- [ ] **Step 2: Run `go vet ./...` and confirm no warnings.**

- [ ] **Step 3: Confirm the two new roadmap `[x]` entries by reading `doc/roadmap.md:17` and the testing section.**
