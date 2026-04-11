# CLAUDE.md — go-kitsune

This file guides Claude Code when working on this repository. Read `CONTRIBUTING.md` for the full development workflow; this document captures the standards, quality gates, and lessons that matter most.

---

## Project orientation

go-kitsune is a reactive pipeline library for Go. The core abstraction is `Pipeline[T]` — a lazy, composable, channel-backed stage graph. Key files:

- `kitsune.go`, `pipeline.go` — core types and runner
- `batch.go`, `fan_combine.go`, `advanced.go`, `source.go` — operator implementations
- `config.go` — `StageOption` system (`Buffer`, `WithName`, `OnError`, etc.)
- `properties_test.go` — property-based tests (pgregory.net/rapid)
- `doc/operators.md` — full operator reference
- `doc/api-matrix.md` — operator option compatibility table
- `doc/roadmap.md` — planned work (completed items archived in `doc/roadmap-archive.md`)

---

## Adding or modifying an operator

Work through this checklist in order. Do not consider the work done until all items are checked.

### Implementation
- [ ] Place the function in the logically correct file (`batch.go` for batching, `fan_combine.go` for multi-input combiners, etc.)
- [ ] Call `track(p)` for every input pipeline
- [ ] Include `inputs: []int{p.id, ...}` in `stageMeta`
- [ ] Use `make + copy` when emitting slices (never send a slice whose backing array is reused)
- [ ] Drain all input channels on return: `defer func() { go internal.DrainChan(inCh) }()`
- [ ] Respect context cancellation; return `ctx.Err()` on `<-ctx.Done()`
- [ ] Wire `getChanLen` / `getChanCap` closures for inspector metrics

### Tests (`*_test.go`)
- [ ] **Basic correctness** — at least one test covering the happy path
- [ ] **Edge cases** — empty input, zero signals, source-closes-first, selector-closes-first, nil guard (if applicable)
- [ ] **Option tests** — one test each for `WithName` and `Buffer(n)` verifying they don't break output
- [ ] **Property test** in `properties_test.go` — cover at least one algebraic law (partition, subsequence, idempotency, ordering). Property tests have found real bugs that example tests missed.
- [ ] Run `task test:race` and confirm no races

### Documentation
- [ ] **`doc/operators.md`** — full section: signature, description, semantics, "When to use", "Options", code example. Place it in the correct category section.
- [ ] **`doc/api-matrix.md`** — add a row in the correct section table; add a note if the operator has non-obvious option behaviour
- [ ] **`doc/options.md`** — if the operator introduces a new `StageOption` or `RunOption`, add a full section here (signature, applies-to, description, code example). RunOptions also go in the `api-matrix.md` section 16 table.

### Example
- [ ] **`examples/<operator>/main.go`** — self-contained `main` package demonstrating a realistic use case
- [ ] Register the example name in the `examples` slice in `examples_test.go`
- [ ] Verify it runs: `go run ./examples/<operator>`

---

## Testing strategy

| Command | When to use |
|---|---|
| `task test` | Every change — fast, `-short`, skips examples |
| `task test:race` | Before any commit touching concurrency |
| `task test:property` | After adding/changing operators |
| `task test:examples` | After adding/changing examples |
| `task test:all` | Before opening a PR |

Property tests (`properties_test.go`) run in the default `task test:property` at 100 rapid checks each. They are **not** gated behind a build tag — they run with the normal suite.

When writing property tests:
- Test invariants that hold regardless of scheduling (ordering, partition properties, subsequence relationships)
- Do NOT assert "all items appear" when a multi-input operator can exit early (e.g. when the closing selector exhausts before the source)
- Use `rapid.IntRange`, `rapid.SliceOf` for inputs; keep max sizes small (≤ 20) to keep runtime fast

---

## Concurrency and correctness

- **Single-goroutine select loops** are preferred for operators that accumulate state (like `Batch`, `BufferWith`, `SessionWindow`). No mutex needed; ordering is implicit.
- **Mutex + background goroutine** is appropriate when the operator needs to track "latest value" independently of the main loop (like `SampleWith`, `CombineLatest`).
- When using `sync.Mutex`: acquire the lock, read state, release the lock, then send outside the lock to prevent deadlocks.
- Always write timing-sensitive tests with explicit channel sequencing rather than relying on `time.Sleep` alone. Use `NewChannel` sources with explicit `Send`/`Close` calls to control ordering. When sleeps are necessary, use them to ensure a goroutine has had time to process, not to enforce ordering.

---

## Code style

- **No em dashes** in any prose (godoc, comments, docs, commit messages). Use a colon or semicolon instead.
- Keep operator godoc concise: one sentence on what it does, one on when to use it, and a minimal `// Example:` block if the signature is non-obvious.
- Naming: if a new operator name would collide with an existing `StageOption` function, append `With` (e.g. `BufferWith` avoids colliding with `Buffer(n int) StageOption`).
- Follow existing patterns precisely — new operators should look identical in structure to their nearest neighbour in the same file.

---

## Documentation files

- `doc/operators.md` — the authoritative operator reference. Sections follow the same order as `doc/api-matrix.md`.
- `doc/api-matrix.md` — one row per operator, showing which `StageOption`s it accepts. Keep the column headers consistent with the legend at the top of the file.
- `doc/roadmap.md` — mark items `[x]` when complete. Do not delete completed items; they are archived in `doc/roadmap-archive.md` periodically.
- `doc/options.md` — describes each `StageOption`. Update it if a new option is added.

---

## Roadmap

The next unimplemented items in `doc/roadmap.md` (as of 2026-04-11):

**Operators:** *(all complete in Active section)*

**State:** *(all complete in Active section)*

**Developer experience:**
- "Choosing a concurrency model" guide
- `WithInspectorStore(store)` option
- `benchstat` performance regression baseline
- Unified tail integration test matrix
- Supervision + error handler interaction docs

Pick the next item by impact: operator work before dev-experience docs, correctness work before ergonomics.
