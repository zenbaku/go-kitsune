# Testing Kitsune Pipelines

A guide to writing fast, deterministic tests for Kitsune stages and pipelines.

---

## The core pattern

Every pipeline is assembled lazily: no goroutines start until you call a terminal function. This makes any fragment testable in isolation: give it a `FromSlice` source and collect the output.

```go
func TestParseStage(t *testing.T) {
    input  := kitsune.FromSlice([]string{"1", "2", "bad", "4"})
    parsed := kitsune.Map(input, kitsune.LiftFallible(strconv.Atoi),
        kitsune.OnError(kitsune.ActionDrop()))

    testkit.CollectAndExpect(t, parsed, []int{1, 2, 4})
}
```

!!! tip "Deterministic by construction"
    `FromSlice` + `Collect` never starts a timer, opens a socket, or shuffles goroutine order. The same input always produces the same output; no `t.Eventually` loops required.

`testkit.CollectAndExpect` runs the pipeline, collects all output, and fails the test if the result doesn't match. For unordered output (concurrent stages), use `CollectAndExpectUnordered`.

---

## testkit package

`github.com/zenbaku/go-kitsune/testkit` contains helpers for the most common testing patterns. Import it alongside the main package:

```go
import (
    kitsune "github.com/zenbaku/go-kitsune"
    "github.com/zenbaku/go-kitsune/testkit"
)
```

### Collecting output

| Function | What it does |
|----------|-------------|
| `MustCollect(t, p)` | Collect all items; `t.Fatal` on pipeline error |
| `CollectAndExpect(t, p, want)` | Collect and assert exact ordered equality |
| `CollectAndExpectUnordered(t, p, want)` | Collect and assert same elements, any order |
| `MustCollectWithHook(t, p)` | Collect items and return a `RecordingHook` for event inspection |

### Running sinks

| Function | What it does |
|----------|-------------|
| `MustRun(t, runner)` | Run a `ForEachRunner` or `DrainRunner`; `t.Fatal` on error |
| `MustRunWithHook(t, runner)` | Run with a `RecordingHook` wired in; return the hook |

### Injecting failures

!!! note "Failure injection preserves types"
    `FailAt[T]` and `FailEvery[T]` are typed helpers: they plug directly into `Map` without wrapping, so your test fragments mirror production code exactly.

| Function | What it does |
|----------|-------------|
| `FailAt[T](err, positions...)` | Return `err` for the items at the given 0-based positions |
| `FailEvery[T](err, n)` | Return `err` for every nth item (0, n, 2n, …) |

### Simulating slow I/O

!!! warning "Avoid `time.Sleep` in stage functions"
    Use `testkit.TestClock` with `WithClock` instead. Real sleeps inflate test runtimes and cause flakes on loaded CI runners.

| Function | What it does |
|----------|-------------|
| `SlowMap[T](d)` | Map function that sleeps `d` before passing the item through |
| `SlowSink[T](d)` | ForEach function that sleeps `d` per item |

### Controlling virtual time

| Function | What it does |
|----------|-------------|
| `NewTestClock()` | Virtual clock; use with `WithClock` on time-sensitive operators |
| `clock.Advance(d)` | Move virtual time forward; fires any due timers and tickers |

---

## Testing stages in isolation

Define reusable stages as constructor functions that return a `Stage[I,O]`. The client (or any dependency) is a parameter to the constructor, not a global:

```go
// parseStage is a pure transform — no external dependency.
func parseStage() kitsune.Stage[string, LogEntry] {
    return func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[LogEntry] {
        return kitsune.Map(p, parseLine, kitsune.OnError(kitsune.Skip()))
    }
}

// enrichStage depends on an external service; receives it as an interface.
type Enricher interface {
    Enrich(ctx context.Context, e LogEntry) (LogEntry, error)
}

func enrichStage(svc Enricher) kitsune.Stage[LogEntry, LogEntry] {
    return func(p *kitsune.Pipeline[LogEntry]) *kitsune.Pipeline[LogEntry] {
        return kitsune.Map(p, svc.Enrich, kitsune.Concurrency(10))
    }
}
```

Production wiring: the real client is injected once and flows into every stage that needs it:

```go
svc := enrichment.NewClient(cfg)

pipeline := kitsune.From(source).
    Through(parseStage()).
    Through(enrichStage(svc))
```

Testing each stage independently: swap the real client for a mock:

```go
func TestEnrichStage(t *testing.T) {
    mock := &mockEnricher{
        fn: func(_ context.Context, e LogEntry) (LogEntry, error) {
            e.Region = "us-east-1"
            return e, nil
        },
    }

    input  := kitsune.FromSlice([]LogEntry{{Level: "INFO"}, {Level: "WARN"}})
    result := enrichStage(mock)(input)

    got := testkit.MustCollect(t, result)
    for _, entry := range got {
        if entry.Region != "us-east-1" {
            t.Errorf("expected region to be set, got %q", entry.Region)
        }
    }
}

type mockEnricher struct {
    fn func(context.Context, LogEntry) (LogEntry, error)
}

func (m *mockEnricher) Enrich(ctx context.Context, e LogEntry) (LogEntry, error) {
    return m.fn(ctx, e)
}
```

**Rule of thumb**: define clients as narrow interfaces, only the methods each stage actually uses. A stage that only calls `Lookup` should depend on a `Lookuper`, not the full API client. This keeps mocks minimal and makes the stage's intent clear.

---

## Testing error paths

### Asserting items are skipped

```go
func TestBadItemsAreSkipped(t *testing.T) {
    boom := errors.New("parse error")
    input := kitsune.FromSlice([]string{"1", "bad", "3"})
    p := kitsune.Map(input, testkit.FailAt[string](boom, 1), // fail item at index 1
        kitsune.OnError(kitsune.Skip()))

    testkit.CollectAndExpect(t, p, []string{"1", "3"})
}
```

### Asserting errors are recorded

Use `RecordingHook` to inspect what errors reached the hook without asserting on the final output:

```go
func TestErrorsAreRecorded(t *testing.T) {
    boom := errors.New("transient")
    input := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
    p := kitsune.Map(input, testkit.FailEvery[int](boom, 2), // fail items 0, 2, 4
        kitsune.WithName("my_stage"),
        kitsune.OnError(kitsune.Skip()))

    _, hook := testkit.MustCollectWithHook(t, p)
    testkit.AssertErrorCount(t, hook, 3)
    testkit.AssertStageErrors(t, hook, "my_stage", 3)
}
```

### Testing that a stage halts on error

When a stage should propagate errors (the default), assert that `Run` returns an error and that it wraps the expected cause:

```go
func TestStageHaltsOnError(t *testing.T) {
    boom := errors.New("fatal")
    input := kitsune.FromSlice([]int{1, 2, 3})
    p := kitsune.Map(input, testkit.FailAt[int](boom, 1))

    _, err := p.Collect(context.Background())
    if !errors.Is(err, boom) {
        t.Fatalf("expected boom, got %v", err)
    }
}
```

### Testing dead-letter branches

Both branches of a `MapResult` pipeline must be consumed before calling `Run`. Test them independently by collecting each branch:

```go
func TestMapResultRouting(t *testing.T) {
    boom := errors.New("bad")
    input := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
    ok, dlq := kitsune.MapResult(input,
        testkit.FailAt[int](boom, 1, 3)) // items 2 and 4 fail

    var (
        goodItems []int
        deadItems []kitsune.ErrItem[int]
        wg        sync.WaitGroup
    )
    wg.Add(2)
    go func() { defer wg.Done(); goodItems, _ = ok.Collect(context.Background()) }()
    go func() { defer wg.Done(); deadItems, _ = dlq.Collect(context.Background()) }()
    wg.Wait()

    if len(goodItems) != 3 || len(deadItems) != 2 {
        t.Fatalf("good=%d dead=%d", len(goodItems), len(deadItems))
    }
}
```

---

## Testing concurrent stages

Concurrent stages (`Concurrency(n)`) produce output in arrival order, not input order. Use `CollectAndExpectUnordered` to assert on the set of results regardless of order:

```go
func TestConcurrentEnrich(t *testing.T) {
    input := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
    p := kitsune.Map(input,
        func(_ context.Context, v int) (int, error) { return v * 2, nil },
        kitsune.Concurrency(4))

    testkit.CollectAndExpectUnordered(t, p, []int{2, 4, 6, 8, 10})
}
```

Add `WithName` to stages so that `RecordingHook` assertions can target them by name rather than relying on internal defaults:

```go
p := kitsune.Map(input, enrich,
    kitsune.Concurrency(10),
    kitsune.WithName("enrich"))

_, hook := testkit.MustCollectWithHook(t, p)
testkit.AssertStageProcessed(t, hook, "enrich", 100)
testkit.AssertNoErrors(t, hook)
```

---

## Testing time-sensitive operators

Operators like `Debounce`, `Throttle`, `Batch` (with `BatchTimeout`), `Ticker`, and `SessionWindow` depend on wall-clock time. Use `TestClock` to control time deterministically: no sleeps, no flaky tests.

### Batch with a timeout flush

```go
func TestBatchFlushedByTimeout(t *testing.T) {
    clock  := testkit.NewTestClock()
    ch     := kitsune.NewChannel[int]()
    batched := kitsune.Batch(kitsune.From(ch.Out()),
        10,
        kitsune.BatchTimeout(5*time.Second),
        kitsune.WithClock(clock))

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    var got [][]int
    runner := batched.ForEach(func(_ context.Context, batch []int) error {
        got = append(got, batch)
        return nil
    })

    done := make(chan error, 1)
    go func() { done <- runner.Run(ctx) }()

    ch.Send(ctx, 1)
    ch.Send(ctx, 2)
    ch.Send(ctx, 3)
    clock.Advance(5 * time.Second) // fires the flush timer
    cancel()
    <-done

    if len(got) != 1 || len(got[0]) != 3 {
        t.Fatalf("got %v", got)
    }
}
```

### Debounce

```go
func TestDebounce(t *testing.T) {
    clock  := testkit.NewTestClock()
    ch     := kitsune.NewChannel[string]()
    result := kitsune.Debounce(kitsune.From(ch.Out()),
        200*time.Millisecond,
        kitsune.WithClock(clock))

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    var mu sync.Mutex
    var got []string
    go result.ForEach(func(_ context.Context, s string) error {
        mu.Lock(); got = append(got, s); mu.Unlock()
        return nil
    }).Run(ctx)

    ch.Send(ctx, "a")
    ch.Send(ctx, "b") // supersedes "a"
    clock.Advance(200 * time.Millisecond) // silence window elapses — "b" emitted

    cancel()
    mu.Lock(); defer mu.Unlock()
    if len(got) != 1 || got[0] != "b" {
        t.Fatalf("got %v", got)
    }
}
```

---

## Testing supervision restarts

`RecordingHook.Restarts()` captures every `OnRestart` event so you can assert that supervision behaved as expected:

```go
func TestSupervisionRestartsStage(t *testing.T) {
    var calls atomic.Int32
    boom := errors.New("transient")

    input := kitsune.Repeatedly(func() int { return 1 })
    p := kitsune.Map(input,
        func(_ context.Context, v int) (int, error) {
            if calls.Add(1) <= 2 {
                return 0, boom
            }
            return v, nil
        },
        kitsune.WithName("flaky"),
        kitsune.Supervise(kitsune.RestartOnError))

    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()

    runner := p.ForEach(func(_ context.Context, _ int) error { return nil })
    hook := testkit.MustRunWithHook(t, runner)

    testkit.AssertRestartCount(t, hook, 2)
}
```

---

## Performance assertions

For stages where throughput or latency matter, use `MetricsHook` together with the testkit assertion helpers:

```go
func TestEnrichThroughput(t *testing.T) {
    metrics := &kitsune.MetricsHook{}
    input := kitsune.FromSlice(make([]int, 1000))
    p := kitsune.Map(input,
        func(_ context.Context, v int) (int, error) { return v, nil },
        kitsune.WithName("enrich"),
        kitsune.Concurrency(8))

    testkit.MustRun(t, p.ForEach(func(_ context.Context, _ int) error { return nil }),
        kitsune.WithHook(metrics))

    testkit.AssertThroughputAbove(t, metrics, "enrich", 50_000)
    testkit.AssertMeanLatencyUnder(t, metrics, "enrich", time.Millisecond)
}
```

---

## Property-based testing

Kitsune ships property tests that verify algebraic invariants of its core operators. These use [`pgregory.net/rapid`](https://pkg.go.dev/pgregory.net/rapid) and are gated behind the `property` build tag so they don't slow down the regular test run.

```bash
# Run all property tests (100 random cases each, ~1 s total)
task test:property

# Run with more iterations for deeper coverage
go test -tags=property -rapid.checks=1000 -timeout 300s .

# Reproduce a specific failure from a rapid failure file
go test -tags=property -rapid.failfile=testdata/rapid/TestPropBalanceItemCount/... .
```

### Covered invariants

| Test | Invariant |
|------|-----------|
| `TestPropMergeMultiset` | `Merge` preserves the exact multiset union of all inputs |
| `TestPropMergeLength` | Total item count from `Merge` equals sum of input lengths |
| `TestPropMergeCommutativity` | `Merge` result is the same multiset regardless of argument order |
| `TestPropSortIsSorted` | `Sort` output is always in sorted order |
| `TestPropSortPreservesMultiset` | `Sort` never adds or drops items |
| `TestPropSortIdempotent` | `Sort(Sort(p)) == Sort(p)` |
| `TestPropTakeAfterSort` | `Take(n, Sort(p))` yields exactly the `n` smallest elements |
| `TestPropTakeBounded` | `Take(n)` always emits exactly `min(n, len(p))` items |
| `TestPropTakePreservesOrder` | `Take(n)` emits a prefix of the input in original order |
| `TestPropBroadcastCompleteness` | Every branch of `Broadcast(p, n)` receives all items in order |
| `TestPropBalanceItemCount` | `Balance` multiset union equals input; no items are lost or duplicated |
| `TestPropBalanceRoundRobin` | Per-branch counts differ by at most 1 (round-robin fairness) |

### Adding new property tests

When you add a new fan-in/fan-out operator or an order-preserving combinator, add a corresponding property to `properties_test.go`. Good candidates for property tests are:

- **Multiset invariants**: does the operator preserve, add, or remove items?
- **Order invariants**: when is order guaranteed to be preserved or changed?
- **Composition laws**: does `f ∘ g == g ∘ f`? Does `f ∘ f == f`?
- **Count invariants**: does a fan-out operator deliver the right total across all branches?

---

## Quick reference

| Goal | Tool |
|------|------|
| Assert exact ordered output | `testkit.CollectAndExpect` |
| Assert output regardless of order | `testkit.CollectAndExpectUnordered` |
| Collect output without assertion | `testkit.MustCollect` |
| Run a sink pipeline | `testkit.MustRun` |
| Inspect lifecycle events | `testkit.MustCollectWithHook` / `MustRunWithHook` |
| Inject failures at specific positions | `testkit.FailAt` |
| Inject periodic failures | `testkit.FailEvery` |
| Simulate slow I/O | `testkit.SlowMap` / `SlowSink` |
| Control time deterministically | `testkit.NewTestClock` + `WithClock` |
| Mock an external client | Interface + stage constructor |
| Test branching pipelines | Collect each branch in separate goroutines |
| Name a stage for hook assertions | `WithName("my_stage")` |
| Run property/algebra invariant tests | `task test:property` |
