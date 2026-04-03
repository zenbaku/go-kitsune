# Contributing to Kitsune

Thank you for your interest in contributing. This document covers the development workflow, how to run tests and examples, the PR process, and the commit message convention.

---

## Prerequisites

- **Go 1.23 or later** (CI tests against 1.23, 1.24, and the latest stable release)
- **[Task](https://taskfile.dev)** — the project uses `task` as a build tool; install it with `brew install go-task` or see the Task docs

Verify your setup:

```bash
go version   # should be 1.23+
task --version
```

---

## Getting the code

```bash
git clone https://github.com/zenbaku/go-kitsune
cd go-kitsune
```

The repository is a multi-module workspace. The root module (`github.com/zenbaku/go-kitsune`) contains the core library, engine, testkit, and examples. Each tail under `tails/` is its own module with its own `go.mod`.

---

## Development workflow

The default task runs format, vet, and tests in one shot:

```bash
task          # fmt + vet + test (short mode, fast)
```

Individual tasks:

| Command | What it does |
|---|---|
| `task fmt` | `go fmt ./...` |
| `task vet` | `go vet ./...` |
| `task test` | Core tests, `-short` flag, 30s timeout |
| `task test:verbose` | Same with `-v` |
| `task test:race` | Race detector enabled, 60s timeout |
| `task test:cover` | Coverage report in your browser |
| `task test:examples` | Build and run all example programs |
| `task test:all` | Core + tail + example tests (full CI suite) |
| `task bench` | Core operator benchmarks |
| `task bench:compare` | Kitsune vs raw goroutines, conc, go-streams |

Run `task test:race` before opening a PR. The CI gate is `task test:all` — if you are touching tail code, run it locally first since some tails need external services (Redis, SQLite) and the CI matrix covers them but can take a while.

---

## Running examples

Examples live in `examples/`. Each is a self-contained `main` package:

```bash
go run ./examples/basic
go run ./examples/batch
go run ./examples/circuitbreaker
# ... etc.
```

`task test:examples` builds and runs all of them as a smoke test. If you add a new example, add its directory name to the `TestExamples` list in `examples_test.go`.

Prefer `go run` over `go build` for examples. If you do build manually, use `-o main` so the output is covered by the `.gitignore` pattern:

```bash
go build -o main ./examples/basic
```

---

## Adding a new operator

1. **Implement** it in `kitsune.go` (or a logically grouped file at the root).
2. **Add tests** to an existing or new `*_test.go` file at the root.
3. **Add an example** under `examples/<operator-name>/main.go` and register it in `examples_test.go`.
4. **Check allocations** — if the operator sits on a hot path, verify its per-item allocation count stays at or near 2 allocs/item (the `any`-boxing cost). Run `task test` and check that `TestAllocBounds` still passes.
5. **Document it** — add a godoc comment with at least one sentence describing the operator's behaviour and a `// Example:` block if the signature is non-obvious.

---

## Adding a new tail

Tails live under `tails/<name>/` and are separate Go modules so users only pull dependencies they need.

```
tails/
  kmytail/
    go.mod        # module github.com/zenbaku/go-kitsune/tails/kmytail
    kmytail.go
    kmytail_test.go
```

Steps:
1. Create the directory and `go.mod` (`go mod init github.com/zenbaku/go-kitsune/tails/kmytail`).
2. Add the tail to the `test:ext` task in `Taskfile.yml`.
3. If the tail needs an external service in CI, add a job to `.github/workflows/ci.yml` modelled after the existing `kredis` or `ksqlite` jobs.

---

## Code style

- Run `go fmt ./...` before committing — CI rejects unformatted code.
- Run `go vet ./...` — CI rejects vet failures.
- No other linter is required; `fmt` and `vet` are the only gates.
- Keep public API surface minimal. Prefer adding an option (`StageOption` or `RunOption`) over a new top-level function when the new behaviour is a variant of something existing.
- Avoid adding error handling, fallbacks, or validation for scenarios that cannot happen. Internal code trusts engine guarantees; validate only at public API boundaries.

---

## Tests

- **Short mode** (`-short`) skips timing-sensitive tests. The default `task test` uses `-short`; use `task test:verbose` or `task test:race` when you want the full suite.
- **Allocation regression** — `TestAllocBounds` in `bench/bench_allocs_test.go` enforces hard ceilings on per-pipeline alloc counts for the hottest operators. It runs in CI on every PR. If you hit a ceiling, investigate before raising it.
- **Race detector** — run `task test:race` for any change that touches concurrency (channels, goroutines, shared state). CI runs with `-race` on every PR.
- **Latency percentiles** — these are informational, not gated:
  ```bash
  go test -v -run TestLatencyPercentiles ./...
  ```

---

## Commit messages

Use the imperative mood, capitalized, no trailing period. The first line should complete the sentence "This commit will…":

```
Add SlidingWindow operator with configurable step
Fix MapBatch opts double-application
Update benchmarks after drain protocol
```

A short description on the first line is enough for most changes. For non-obvious decisions, add a body paragraph explaining the *why* — what problem the change solves or what alternative was rejected.

There is no strict conventional-commits prefix requirement, but `feat:`, `fix:`, and `doc:` prefixes are welcome and used in some recent commits.

---

## Pull requests

- **Branch from `main`**, keep branches focused on one change.
- **Run `task test:race`** (and `task test:all` if touching tails) before opening the PR.
- The PR description should explain what changed and why. A brief "before/after" is helpful for performance changes.
- CI runs fmt, vet, race tests, allocation regression tests, example smoke tests, and tail-specific tests. All must pass.
- For larger changes (new operators, new tails, API changes), open an issue or discussion first to align on the design before writing code.

---

## Benchmarks

See [`doc/benchmarks.md`](doc/benchmarks.md) for the current baseline numbers and how to reproduce them. If your change affects a hot path, run the relevant benchmark before and after and include the numbers in the PR description:

```bash
task bench           # core operators
task bench:compare   # vs raw goroutines, conc, go-streams
```

---

## Questions and design discussions

For "how do I…?" questions or design proposals, open a GitHub Discussion (once the Discussions tab is enabled) or file an issue with the `question` label. Keep GitHub Issues focused on confirmed bugs and actionable tasks.
