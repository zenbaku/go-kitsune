# Examples

All examples use only the core `go-kitsune` package and run in the Go Playground. Run any locally with:

```
go run ./examples/<name>
```

## At a glance

<div class="grid cards" markdown>

- :material-pipe: **[Getting started](#getting-started)** — basic, concurrent, pause
- :material-broadcast: **[Fan-out & Fan-in](#fan-out-fan-in)** — fanout, broadcast, share
- :material-puzzle-outline: **[Composition](#composition)** — stages
- :material-shield-check-outline: **[Error handling](#error-handling)** — deadletter, circuitbreaker, timeout
- :material-memory: **[Stateful processing](#stateful-processing)** — runningtotal, keyedstate, caching
- :material-speedometer: **[Rate limiting](#rate-limiting)** — ratelimit, perkeyratelimit
- :material-message-arrow-right-outline: **[Push sources](#push-sources)** — channel
- :material-clock-outline: **[Time-based](#time-based)** — ticker, switchmap
- :material-filter-check-outline: **[Deduplication](#deduplication)** — bloomdedup
- :material-chart-line: **[Observability](#observability)** — hooks, inspector

</div>

---

## :material-pipe: Getting started { #getting-started }

### `basic` { #basic }

A minimal linear pipeline. Trims and uppercases strings, then collects squared numbers.

**Demonstrates:** `FromSlice`, `Map`, `Filter`, `ForEach`, `Collect`

[:material-play: Run in Playground](https://go.dev/play/p/7Mx5b3ppXE8){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/basic/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: basic — a minimal linear pipeline.
    //
    // Demonstrates: FromSlice, Map, Filter, ForEach, Collect
    package main

    import (
        "context"
        "fmt"
        "strings"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    func main() {
        ctx := context.Background()

        // --- ForEach: emit lines as we go ---

        words := []string{"  hello  ", "  world  ", "  kitsune  ", "  go  "}

        trimmed := kitsune.Map(kitsune.FromSlice(words),
            func(_ context.Context, s string) (string, error) {
                return strings.TrimSpace(s), nil
            })

        long := kitsune.Filter(trimmed,
            func(_ context.Context, s string) (bool, error) {
                return len(s) > 3, nil
            })

        upper := kitsune.Map(long,
            func(_ context.Context, s string) (string, error) {
                return strings.ToUpper(s), nil
            })

        err := upper.ForEach(func(_ context.Context, s string) error {
            fmt.Println(s)
            return nil
        }).Run(ctx)
        if err != nil {
            panic(err)
        }

        // --- Collect: materialise results into a slice ---

        nums := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8}),
            func(_ context.Context, n int) (int, error) { return n * n, nil })

        evens := kitsune.Filter(nums,
            func(_ context.Context, n int) (bool, error) { return n%2 == 0, nil })

        squares, err := kitsune.Collect(ctx, evens)
        if err != nil {
            panic(err)
        }
        fmt.Println("even squares:", squares)
    }
    ```

---

### `concurrent` { #concurrent }

Parallel processing with and without ordering guarantees.

**Demonstrates:** `Concurrency`, `Ordered`, `Buffer`, `WithName`

[:material-play: Run in Playground](https://go.dev/play/p/PC1hob96mkC){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/concurrent/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: concurrent — parallel processing with and without ordering guarantees.
    //
    // Demonstrates: Concurrency, Ordered, Buffer, WithName
    package main

    import (
        "context"
        "fmt"
        "time"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    func main() {
        ctx := context.Background()
        items := make([]int, 20)
        for i := range items {
            items[i] = i
        }

        simulate := func(_ context.Context, n int) (string, error) {
            time.Sleep(10 * time.Millisecond)
            return fmt.Sprintf("item-%02d", n), nil
        }

        // --- Unordered: fastest, output arrives in completion order ---

        fmt.Println("=== Unordered (completion order) ===")
        start := time.Now()
        unordered, err := kitsune.Collect(ctx,
            kitsune.Map(kitsune.FromSlice(items), simulate,
                kitsune.Concurrency(5),
                kitsune.Buffer(32),
                kitsune.WithName("parallel"),
            ))
        if err != nil {
            panic(err)
        }
        fmt.Printf("  processed %d items in %v\n\n", len(unordered), time.Since(start).Round(time.Millisecond))

        // --- Ordered: same concurrency, output in original input order ---

        fmt.Println("=== Ordered (input order preserved) ===")
        start = time.Now()
        ordered, err := kitsune.Collect(ctx,
            kitsune.Map(kitsune.FromSlice(items), simulate,
                kitsune.Concurrency(5),
                kitsune.Ordered(),
                kitsune.WithName("parallel-ordered"),
            ))
        if err != nil {
            panic(err)
        }
        fmt.Printf("  processed %d items in %v\n", len(ordered), time.Since(start).Round(time.Millisecond))
        fmt.Printf("  first=%s last=%s\n", ordered[0], ordered[len(ordered)-1])
    }
    ```

---

### `pause` { #pause }

Temporarily stop a running pipeline without cancelling it. Sources block; in-flight items drain normally. Resume restarts emission with no data loss.

**Demonstrates:** `RunAsync`, `RunHandle.Pause`, `RunHandle.Resume`, `RunHandle.Paused`, `NewGate`, `WithPauseGate`

[:material-play: Run in Playground](https://go.dev/play/p/AS4aGhBTeZS){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/pause/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: pause — temporarily stop a running pipeline without cancelling it.
    //
    // Demonstrates:
    //   - RunAsync + RunHandle.Pause / Resume / Paused
    //   - NewGate + WithPauseGate for use with blocking Runner.Run
    //   - Behaviour during pause: sources block, in-flight items drain normally
    package main

    import (
        "context"
        "fmt"
        "time"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    func main() {
        ctx := context.Background()

        // --- RunAsync: pause/resume via RunHandle ---

        fmt.Println("=== RunAsync: pause and resume via RunHandle ===")

        src := kitsune.NewChannel[int](4)
        processed := kitsune.Map(src.Source(), func(_ context.Context, n int) (string, error) {
            time.Sleep(10 * time.Millisecond)
            return fmt.Sprintf("item-%02d", n), nil
        }, kitsune.WithName("process"))

        var received []string
        handle := processed.ForEach(func(_ context.Context, s string) error {
            received = append(received, s)
            return nil
        }).Build().RunAsync(ctx)

        for i := 1; i <= 5; i++ {
            src.Send(ctx, i) //nolint
        }
        time.Sleep(100 * time.Millisecond)

        handle.Pause()
        fmt.Printf("paused: %v\n", handle.Paused())

        go func() {
            for i := 6; i <= 8; i++ {
                src.Send(ctx, i) //nolint
            }
        }()

        time.Sleep(80 * time.Millisecond)
        fmt.Printf("processed while paused: %d items total\n", len(received))

        handle.Resume()
        fmt.Printf("resumed: %v\n", handle.Paused())
        time.Sleep(100 * time.Millisecond)

        src.Close()
        if err := handle.Wait(); err != nil {
            panic(err)
        }
        fmt.Printf("total processed: %d items\n", len(received))

        // --- Gate + WithPauseGate: pause a blocking Runner.Run ---

        fmt.Println("\n=== Gate + WithPauseGate: pause a blocking Run ===")

        gate := kitsune.NewGate()
        nums := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
        pipeline := kitsune.Map(nums, func(_ context.Context, n int) (string, error) {
            time.Sleep(5 * time.Millisecond)
            return fmt.Sprintf("n=%d", n), nil
        }, kitsune.WithName("map"))

        var out []string
        runner := pipeline.ForEach(func(_ context.Context, s string) error {
            out = append(out, s)
            return nil
        }).Build()

        go func() {
            time.Sleep(30 * time.Millisecond)
            gate.Pause()
            fmt.Println("gate paused")
            time.Sleep(50 * time.Millisecond)
            gate.Resume()
            fmt.Println("gate resumed")
        }()

        if err := runner.Run(ctx, kitsune.WithPauseGate(gate)); err != nil {
            panic(err)
        }
        fmt.Printf("total: %d items\n", len(out))
    }
    ```

---

## :material-broadcast: Fan-out & Fan-in { #fan-out-fan-in }

### `fanout` { #fanout }

Split a stream into two typed branches and run them concurrently.

**Demonstrates:** `Partition`, `ForEachRunner.Build`, `MergeRunners`

[:material-play: Run in Playground](https://go.dev/play/p/vSSkaKIyre3){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/fanout/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: fanout — split a stream and run each branch concurrently.
    //
    // Demonstrates: Partition, ForEachRunner.Build, MergeRunners
    package main

    import (
        "context"
        "fmt"
        "sync"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    func main() {
        ctx := context.Background()

        nums := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})

        // Partition splits the stream into two typed pipelines based on a predicate.
        evens, odds := kitsune.Partition(nums, func(n int) bool { return n%2 == 0 })

        var mu sync.Mutex
        var evenResults, oddResults []int

        evenRunner := evens.ForEach(func(_ context.Context, n int) error {
            mu.Lock()
            evenResults = append(evenResults, n)
            mu.Unlock()
            return nil
        }).Build()

        oddRunner := odds.ForEach(func(_ context.Context, n int) error {
            mu.Lock()
            oddResults = append(oddResults, n)
            mu.Unlock()
            return nil
        }).Build()

        // MergeRunners starts both branches from the same shared source and waits
        // for both to finish. All branches must complete before Run returns.
        merged, err := kitsune.MergeRunners(evenRunner, oddRunner)
        if err != nil {
            panic(err)
        }
        if err := merged.Run(ctx); err != nil {
            panic(err)
        }

        fmt.Println("evens:", evenResults)
        fmt.Println("odds: ", oddResults)
    }
    ```

---

### `broadcast` { #broadcast }

Fan a single stream out to N independent consumers; each sees every item.

**Demonstrates:** `BroadcastN`, `MergeRunners`

[:material-play: Run in Playground](https://go.dev/play/p/z2rybKDooom){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/broadcast/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: broadcast — fan-out a single stream to N independent consumers.
    //
    // Demonstrates: BroadcastN, MergeRunners
    package main

    import (
        "context"
        "fmt"
        "sync"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    func main() {
        ctx := context.Background()

        events := kitsune.FromSlice([]string{"login", "purchase", "logout", "search", "purchase"})

        // BroadcastN fans the stream out to 3 independent channels. Each consumer
        // sees every item. The source is consumed at the speed of the slowest consumer.
        branches := kitsune.BroadcastN(events, 3)

        var mu sync.Mutex
        counts := make([]int, 3)

        runners := make([]*kitsune.Runner, 3)
        for i, branch := range branches {
            i, branch := i, branch
            runners[i] = branch.ForEach(func(_ context.Context, s string) error {
                mu.Lock()
                counts[i]++
                mu.Unlock()
                return nil
            }).Build()
        }

        merged, err := kitsune.MergeRunners(runners...)
        if err != nil {
            panic(err)
        }
        if err := merged.Run(ctx); err != nil {
            panic(err)
        }

        for i, c := range counts {
            fmt.Printf("consumer %d received %d items\n", i, c)
        }
    }
    ```

---

### `share` { #share }

Multicast a stream to a dynamically-built subscriber list — consumers registered one at a time with independent options.

**Demonstrates:** `Share`, per-branch `Buffer` and `WithName`, `MergeRunners`

[:material-play: Run in Playground](https://go.dev/play/p/IVv5IoO6n9h){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/share/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: share — multicast a stream to a dynamically-built subscriber list.
    //
    // Share lets you register consumers one at a time, each with its own options.
    // Unlike Broadcast, the number of consumers doesn't need to be fixed upfront.
    package main

    import (
        "context"
        "fmt"
        "sync"
        "sync/atomic"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    type OrderEvent struct {
        ID     int
        Amount float64
    }

    func main() {
        ctx := context.Background()

        orders := kitsune.FromSlice([]OrderEvent{
            {ID: 1, Amount: 49.99},
            {ID: 2, Amount: 1250.00},
            {ID: 3, Amount: 7.50},
            {ID: 4, Amount: 3400.00},
            {ID: 5, Amount: 22.00},
        })

        subscribe := kitsune.Share(orders)

        audit   := subscribe(kitsune.WithName("audit"), kitsune.Buffer(256))
        metrics := subscribe(kitsune.WithName("metrics"))
        fraud   := subscribe(kitsune.WithName("fraud-detection"))

        var auditLog []string
        var mu sync.Mutex
        auditRunner := audit.ForEach(func(_ context.Context, o OrderEvent) error {
            mu.Lock()
            auditLog = append(auditLog, fmt.Sprintf("order #%d: $%.2f", o.ID, o.Amount))
            mu.Unlock()
            return nil
        }).Build()

        var totalRevenue atomic.Value
        totalRevenue.Store(0.0)
        var eventCount atomic.Int64
        metricsRunner := metrics.ForEach(func(_ context.Context, o OrderEvent) error {
            eventCount.Add(1)
            for {
                old := totalRevenue.Load().(float64)
                if totalRevenue.CompareAndSwap(old, old+o.Amount) {
                    break
                }
            }
            return nil
        }).Build()

        var flagged atomic.Int64
        fraudRunner := fraud.ForEach(func(_ context.Context, o OrderEvent) error {
            if o.Amount > 1000 {
                flagged.Add(1)
                fmt.Printf("fraud alert: order #%d amount $%.2f\n", o.ID, o.Amount)
            }
            return nil
        }).Build()

        merged, _ := kitsune.MergeRunners(auditRunner, metricsRunner, fraudRunner)
        merged.Run(ctx)

        fmt.Println("\n--- audit log ---")
        for _, entry := range auditLog {
            fmt.Println(" ", entry)
        }
        fmt.Printf("\nevents: %d  revenue: $%.2f  flagged: %d\n",
            eventCount.Load(), totalRevenue.Load().(float64), flagged.Load())
    }
    ```

---

## :material-puzzle-outline: Composition { #composition }

### `stages` { #stages }

Define reusable, composable pipeline fragments with `Stage[I,O]`.

**Demonstrates:** `Stage[I,O]`, `Then`, `Through`, `Stage.Or`

[:material-play: Run in Playground](https://go.dev/play/p/xrflC9maV5d){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/stages/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: stages — composable, reusable pipeline transformers.
    //
    // Demonstrates: Stage[I,O], Then, Through, Stage.Or
    package main

    import (
        "context"
        "fmt"
        "strconv"
        "strings"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    var ParseInt kitsune.Stage[string, int] = func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[int] {
        return kitsune.Map(p, func(_ context.Context, s string) (int, error) {
            return strconv.Atoi(strings.TrimSpace(s))
        })
    }

    var Double kitsune.Stage[int, int] = func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
        return kitsune.Map(p, func(_ context.Context, n int) (int, error) { return n * 2, nil })
    }

    var Stringify kitsune.Stage[int, string] = func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[string] {
        return kitsune.Map(p, func(_ context.Context, n int) (string, error) {
            return fmt.Sprintf("result=%d", n), nil
        })
    }

    var Uppercase kitsune.Stage[string, string] = func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[string] {
        return kitsune.Map(p, func(_ context.Context, s string) (string, error) {
            return strings.ToUpper(s), nil
        })
    }

    func main() {
        ctx := context.Background()
        input := kitsune.FromSlice([]string{"1", "2", "3", "4", "5"})

        // Then: compose two stages into one
        pipeline := kitsune.Then(kitsune.Then(ParseInt, Double), Stringify)
        results, _ := kitsune.Collect(ctx, pipeline(input))
        fmt.Println("ParseInt → Double → Stringify:", strings.Join(results, "  "))

        // Through: apply a Stage[T,T] as a method
        out, _ := kitsune.Collect(ctx,
            kitsune.FromSlice([]string{"hello", "world"}).Through(Uppercase))
        fmt.Println("Uppercase:", out)

        // Or: primary with fallback
        var primary kitsune.Stage[int, string] = func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[string] {
            return kitsune.Map(p, func(_ context.Context, n int) (string, error) {
                if n%2 == 0 {
                    return "", fmt.Errorf("primary failed for %d", n)
                }
                return fmt.Sprintf("primary:%d", n), nil
            })
        }
        var fallback kitsune.Stage[int, string] = func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[string] {
            return kitsune.Map(p, func(_ context.Context, n int) (string, error) {
                return fmt.Sprintf("fallback:%d", n), nil
            })
        }
        orResults, _ := kitsune.Collect(ctx, primary.Or(fallback)(kitsune.FromSlice([]int{1, 2, 3, 4, 5})))
        fmt.Println("Or:", strings.Join(orResults, "  "))
    }
    ```

---

## :material-shield-check-outline: Error handling { #error-handling }

### `deadletter` { #deadletter }

Retry with a dead-letter fallback. Successful items (including those that succeed after retries) go to `ok`; permanently failed items go to `dlq` as `ErrItem` values.

**Demonstrates:** `DeadLetter`, `OnError(Retry(...))`, `ErrItem`, `MergeRunners`

[:material-play: Run in Playground](https://go.dev/play/p/r0hbEatH2pD){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/deadletter/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: deadletter — retry with dead-letter fallback using DeadLetter.
    package main

    import (
        "context"
        "errors"
        "fmt"
        "sync"
        "sync/atomic"
        "time"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    type User struct {
        ID   int
        Name string
    }

    var profiles = map[int]string{1: "Alice", 2: "Bob", 3: "Carol", 4: "Dave", 5: "Eve"}
    var attempts sync.Map
    var attemptsNeeded = map[int]int{3: 2, 4: 3, 7: 0, 8: 0}

    var errTransient = errors.New("transient error")
    var errNotFound  = errors.New("user not found")

    func fetchProfile(_ context.Context, id int) (User, error) {
        needed, limited := attemptsNeeded[id]
        v, _ := attempts.LoadOrStore(id, new(atomic.Int64))
        count := v.(*atomic.Int64).Add(1)
        if limited {
            if needed == 0 {
                return User{}, fmt.Errorf("id %d: %w", id, errNotFound)
            }
            if int(count) < needed {
                return User{}, fmt.Errorf("id %d attempt %d: %w", id, count, errTransient)
            }
        }
        name, ok := profiles[id]
        if !ok {
            return User{}, fmt.Errorf("id %d: %w", id, errNotFound)
        }
        return User{ID: id, Name: name}, nil
    }

    func main() {
        ctx := context.Background()

        ids := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 7, 8})
        ok, dlq := kitsune.DeadLetter(
            ids, fetchProfile,
            kitsune.OnError(kitsune.Retry(3, kitsune.FixedBackoff(1*time.Millisecond))),
        )

        var mu sync.Mutex
        var succeeded, fallbacks []User

        r1 := ok.ForEach(func(_ context.Context, u User) error {
            mu.Lock(); succeeded = append(succeeded, u); mu.Unlock()
            return nil
        }).Build()

        r2 := dlq.ForEach(func(_ context.Context, ei kitsune.ErrItem[int]) error {
            fallback := User{ID: ei.Item, Name: fmt.Sprintf("unknown-%d", ei.Item)}
            mu.Lock(); fallbacks = append(fallbacks, fallback); mu.Unlock()
            fmt.Printf("  [dlq] id=%d err=%v\n", ei.Item, ei.Err)
            return nil
        }).Build()

        runner, _ := kitsune.MergeRunners(r1, r2)
        runner.Run(ctx)

        fmt.Printf("succeeded: %d  dead-lettered: %d\n", len(succeeded), len(fallbacks))
    }
    ```

---

### `circuitbreaker` { #circuitbreaker }

Protect a flaky dependency: after N consecutive failures the circuit opens, fast-failing subsequent items without calling the backend.

**Demonstrates:** `CircuitBreaker`, `FailureThreshold`, `CooldownDuration`, `HalfOpenProbes`, `ErrCircuitOpen`

[:material-play: Run in Playground](https://go.dev/play/p/bJviwYCZPwx){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/circuitbreaker/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: circuitbreaker — protect a flaky dependency with a circuit breaker.
    package main

    import (
        "context"
        "errors"
        "fmt"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    func main() {
        ctx := context.Background()

        callCount := 0
        backend := func(_ context.Context, n int) (string, error) {
            callCount++
            if n >= 3 && n <= 7 {
                return "", fmt.Errorf("backend error on item %d", n)
            }
            return fmt.Sprintf("ok-%d", n), nil
        }

        items := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
        out := kitsune.CircuitBreaker(items, backend,
            []kitsune.CircuitBreakerOpt{
                kitsune.FailureThreshold(3),
                kitsune.CooldownDuration(0),
                kitsune.HalfOpenProbes(1),
            },
            kitsune.OnError(kitsune.Skip()),
            kitsune.WithName("backend"),
        )

        results, err := kitsune.Collect(ctx, out)
        if err != nil {
            panic(err)
        }

        fmt.Println("results:", results)
        fmt.Printf("backend called %d times (circuit blocked some calls)\n", callCount)

        errBackend := func(_ context.Context, n int) (string, error) {
            return "", errors.New("always fails")
        }
        err = kitsune.CircuitBreaker(kitsune.FromSlice([]int{1, 2, 3}), errBackend,
            []kitsune.CircuitBreakerOpt{kitsune.FailureThreshold(2)},
        ).Drain().Run(ctx)

        if errors.Is(err, kitsune.ErrCircuitOpen) {
            fmt.Println("pipeline stopped with ErrCircuitOpen")
        }
    }
    ```

---

### `timeout` { #timeout }

Enforce per-item deadlines on slow stages. Timed-out items can be skipped or replaced with a default value.

**Demonstrates:** `Timeout`, `OnError(Skip())`, `OnError(Return(...))`

[:material-play: Run in Playground](https://go.dev/play/p/amZn04Je2qj){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/timeout/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: timeout — enforce per-item deadlines on slow stages.
    package main

    import (
        "context"
        "fmt"
        "time"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    func main() {
        ctx := context.Background()

        delays := []time.Duration{
            5 * time.Millisecond, 50 * time.Millisecond,
            5 * time.Millisecond, 80 * time.Millisecond,
            5 * time.Millisecond, 30 * time.Millisecond,
            5 * time.Millisecond,
        }

        slow := func(ctx context.Context, d time.Duration) (string, error) {
            select {
            case <-time.After(d):
                return fmt.Sprintf("done in %v", d), nil
            case <-ctx.Done():
                return "", ctx.Err()
            }
        }

        // Skip timed-out items
        results, _ := kitsune.Collect(ctx,
            kitsune.Map(kitsune.FromSlice(delays), slow,
                kitsune.Timeout(20*time.Millisecond),
                kitsune.OnError(kitsune.Skip()),
            ))
        fmt.Printf("Skip: received %d of %d items\n", len(results), len(delays))

        // Replace timed-out items with a default value
        results2, _ := kitsune.Collect(ctx,
            kitsune.Map(kitsune.FromSlice(delays), slow,
                kitsune.Timeout(20*time.Millisecond),
                kitsune.OnError(kitsune.Return("timed out")),
            ))
        fmt.Printf("Return: received %d items\n", len(results2))
        for _, r := range results2 {
            fmt.Println(" ", r)
        }
    }
    ```

---

## :material-memory: Stateful processing { #stateful-processing }

### `runningtotal` { #runningtotal }

Accumulate a running total across a stream using `MapWith` — one shared `Ref` for the lifetime of the run.

**Demonstrates:** `NewKey`, `MapWith`, `Ref.UpdateAndGet`

[:material-play: Run in Playground](https://go.dev/play/p/0H_P6mnH9Bz){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/runningtotal/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: runningtotal — shared mutable state with MapWith.
    package main

    import (
        "context"
        "fmt"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    type Tx struct {
        ID     int
        Amount int
    }

    type Summary struct {
        ID, Amount, Total, Max, Count int
    }

    type totals struct{ sum, max, count int }

    var runningTotalsKey = kitsune.NewKey[totals]("totals", totals{})

    func buildPipeline(txns []Tx) *kitsune.Pipeline[Summary] {
        return kitsune.MapWith(
            kitsune.FromSlice(txns),
            runningTotalsKey,
            func(ctx context.Context, ref *kitsune.Ref[totals], tx Tx) (Summary, error) {
                s, _ := ref.UpdateAndGet(ctx, func(t totals) (totals, error) {
                    t.sum += tx.Amount
                    t.count++
                    if tx.Amount > t.max {
                        t.max = tx.Amount
                    }
                    return t, nil
                })
                return Summary{tx.ID, tx.Amount, s.sum, s.max, s.count}, nil
            },
        )
    }

    func main() {
        ctx := context.Background()
        txns := []Tx{{1, 120}, {2, 45}, {3, 300}, {4, 80}, {5, 210}}

        results, _ := kitsune.Collect(ctx, buildPipeline(txns))
        for _, s := range results {
            fmt.Printf("tx#%d amount=%-4d  total=%-5d max=%-4d count=%d\n",
                s.ID, s.Amount, s.Total, s.Max, s.Count)
        }
    }
    ```

---

### `keyedstate` { #keyedstate }

Per-entity stateful processing. Items are routed by key hash so each entity's state never crosses goroutine boundaries — the in-process actor model.

**Demonstrates:** `MapWithKey`, `Concurrency` with key-sharded routing, `Ref.UpdateAndGet`

[:material-play: Run in Playground](https://go.dev/play/p/ozm5Gn8kdVT){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/keyedstate/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: keyedstate — per-entity stateful processing with key-sharded concurrency.
    package main

    import (
        "context"
        "fmt"
        "time"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    type Event struct {
        UserID string
        Amount int
    }

    var totalKey = kitsune.NewKey[int]("user_total", 0)

    func main() {
        ctx := context.Background()
        events := []Event{
            {"alice", 100}, {"bob", 200}, {"carol", 50},
            {"dave", 300}, {"alice", 150}, {"bob", 100},
            {"carol", 200}, {"alice", 75}, {"dave", 50},
        }

        keyFn := func(e Event) string { return e.UserID }
        mapFn := func(ctx context.Context, ref *kitsune.Ref[int], e Event) (string, error) {
            total, _ := ref.UpdateAndGet(ctx, func(t int) (int, error) {
                return t + e.Amount, nil
            })
            return fmt.Sprintf("%-8s total=%d", e.UserID, total), nil
        }

        // Serial: one goroutine
        start := time.Now()
        results, _ := kitsune.Collect(ctx, kitsune.MapWithKey(
            kitsune.FromSlice(events), keyFn, totalKey, mapFn))
        fmt.Printf("Serial: %d events in %v\n", len(results), time.Since(start).Round(time.Microsecond))
        for _, r := range results {
            fmt.Println(" ", r)
        }

        // Concurrent(4): 4 workers, items routed by hash(userID) % 4
        start = time.Now()
        results2, _ := kitsune.Collect(ctx, kitsune.MapWithKey(
            kitsune.FromSlice(events), keyFn, totalKey, mapFn,
            kitsune.Concurrency(4)))
        fmt.Printf("\nConcurrent(4): %d events in %v\n", len(results2), time.Since(start).Round(time.Microsecond))
    }
    ```

---

### `caching` { #caching }

Skip redundant work with `CacheBy`. Cache misses call the function; hits return the stored result immediately.

**Demonstrates:** `CacheBy`, `CacheTTL`, `MemoryCache`, `WithCache`

[:material-play: Run in Playground](https://go.dev/play/p/EorfMbyo1tK){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/caching/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: caching — skip redundant work with CacheBy + MemoryCache.
    package main

    import (
        "context"
        "fmt"
        "time"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    func main() {
        ctx := context.Background()

        calls := 0
        expensive := func(_ context.Context, id string) (string, error) {
            calls++
            time.Sleep(5 * time.Millisecond)
            return "data-for-" + id, nil
        }

        items := kitsune.FromSlice([]string{"a", "b", "a", "c", "b", "a"})
        results := kitsune.Map(items, expensive,
            kitsune.CacheBy(func(s string) string { return s },
                kitsune.CacheTTL(5*time.Minute),
                kitsune.CacheBackend(kitsune.MemoryCache(128)),
            ),
            kitsune.WithName("fetch"),
        )

        out, _ := kitsune.Collect(ctx, results)
        fmt.Println("results:", out)
        fmt.Printf("fn called %d times for %d items (%d cache hits)\n",
            calls, len(out), len(out)-calls)
    }
    ```

---

## :material-speedometer: Rate limiting { #rate-limiting }

### `ratelimit` { #ratelimit }

Throttle pipeline throughput with a token bucket. `RateLimitWait` applies backpressure; `RateLimitDrop` silently discards excess items.

**Demonstrates:** `RateLimit`, `RateLimitWait`, `RateLimitDrop`, `Burst`

[:material-play: Run in Playground](https://go.dev/play/p/xYD0wBk2rCG){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/ratelimit/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: ratelimit — throttle pipeline throughput with a token bucket.
    package main

    import (
        "context"
        "fmt"
        "time"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    func main() {
        ctx := context.Background()
        items := make([]int, 20)
        for i := range items { items[i] = i }

        // RateLimitWait: backpressure — blocks until a token is available
        start := time.Now()
        results, _ := kitsune.Collect(ctx,
            kitsune.RateLimit(kitsune.FromSlice(items[:10]), 200,
                []kitsune.RateLimitOpt{kitsune.Burst(5)},
            ))
        fmt.Printf("Wait: processed %d items in %v\n", len(results), time.Since(start).Round(time.Millisecond))

        // RateLimitDrop: excess items are silently discarded
        fast := kitsune.FromSlice(items)
        dropped, _ := kitsune.Collect(ctx, kitsune.RateLimit(fast, 5,
            []kitsune.RateLimitOpt{kitsune.RateMode(kitsune.RateLimitDrop), kitsune.Burst(3)},
        ))
        fmt.Printf("Drop: received %d of %d items (%d dropped)\n",
            len(dropped), len(items), len(items)-len(dropped))
    }
    ```

---

### `perkeyratelimit` { #perkeyratelimit }

Per-entity rate limiting: each user gets an independent token budget. Key-sharded routing ensures per-user state never crosses goroutine boundaries — no mutex required.

**Demonstrates:** `MapWithKey`, `Ref.UpdateAndGet`, `Concurrency` with key sharding

[:material-play: Run in Playground](https://go.dev/play/p/DQ2d7DvHhBR){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/perkeyratelimit/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: perkeyratelimit — per-entity rate limiting with MapWithKey.
    //
    // Each user is allowed at most 3 requests per window (every 5 ticks).
    package main

    import (
        "context"
        "fmt"
        "sort"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    type Request struct{ UserID string; Tick int }
    type Result  struct{ UserID string; Tick, Window int; Status string }
    type bucket  struct{ windowStart, count int; lastAccepted bool }

    const (windowSize = 5; rateLimit = 3)

    var rateLimitKey = kitsune.NewKey[bucket]("rate_limit", bucket{})

    func main() {
        ctx := context.Background()

        requests := []Request{
            {"alice", 0}, {"alice", 1}, {"alice", 2}, {"alice", 3},
            {"bob", 1}, {"bob", 2},
            {"carol", 0},
            {"alice", 5}, {"alice", 6},
            {"bob", 5}, {"bob", 6}, {"bob", 7}, {"bob", 8}, {"bob", 9},
        }

        results, _ := kitsune.Collect(ctx, kitsune.MapWithKey(
            kitsune.FromSlice(requests),
            func(r Request) string { return r.UserID },
            rateLimitKey,
            func(ctx context.Context, ref *kitsune.Ref[bucket], r Request) (Result, error) {
                window := r.Tick / windowSize
                b, _ := ref.UpdateAndGet(ctx, func(b bucket) (bucket, error) {
                    if window != b.windowStart {
                        b = bucket{windowStart: window}
                    }
                    if b.count < rateLimit {
                        b.count++; b.lastAccepted = true
                    } else {
                        b.lastAccepted = false
                    }
                    return b, nil
                })
                st := "rejected"
                if b.lastAccepted { st = "accepted" }
                return Result{r.UserID, r.Tick, window, st}, nil
            },
            kitsune.Concurrency(4),
        ))

        sort.Slice(results, func(i, j int) bool {
            if results[i].UserID != results[j].UserID {
                return results[i].UserID < results[j].UserID
            }
            return results[i].Tick < results[j].Tick
        })

        for _, r := range results {
            fmt.Printf("  %-6s tick=%-2d window=%d  %s\n", r.UserID, r.Tick, r.Window, r.Status)
        }
    }
    ```

---

## :material-message-arrow-right-outline: Push sources { #push-sources }

### `channel` { #channel }

Create a push-based source with `NewChannel`. External producers call `Send` while the pipeline runs in the background.

**Demonstrates:** `NewChannel`, `Channel.Send`, `Channel.Close`, `RunAsync`, `RunHandle`

[:material-play: Run in Playground](https://go.dev/play/p/IwkGU20Hh6c){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/channel/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: channel — push-based source with external producers.
    package main

    import (
        "context"
        "fmt"
        "sync"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    func main() {
        ctx := context.Background()

        src := kitsune.NewChannel[int](16)
        doubled := kitsune.Map(src.Source(),
            func(_ context.Context, n int) (int, error) { return n * 2, nil })

        var mu sync.Mutex
        var results []int

        handle := doubled.ForEach(func(_ context.Context, n int) error {
            mu.Lock(); results = append(results, n); mu.Unlock()
            return nil
        }).Build().RunAsync(ctx)

        for i := 1; i <= 10; i++ {
            if err := src.Send(ctx, i); err != nil {
                panic(err)
            }
        }
        src.Close()

        if err := handle.Wait(); err != nil {
            panic(err)
        }
        fmt.Println("results:", results)
    }
    ```

---

## :material-clock-outline: Time-based { #time-based }

### `ticker` { #ticker }

Time-based sources: periodic ticks, monotonic counters, and one-shot timers.

**Demonstrates:** `Ticker`, `Interval`, `Timer`, `Take`

[:material-play: Run in Playground](https://go.dev/play/p/hn7zBJl78Fh){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/ticker/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: ticker — time-based sources and limiting.
    package main

    import (
        "context"
        "fmt"
        "time"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    func main() {
        ctx := context.Background()

        // Ticker: emits time.Time, take first 5
        ticks, _ := kitsune.Collect(ctx,
            kitsune.Map(
                kitsune.Take(kitsune.Ticker(20*time.Millisecond), 5),
                func(_ context.Context, t time.Time) (string, error) {
                    return t.Format("15:04:05.000"), nil
                }))
        fmt.Println("ticks:", ticks)

        // Interval: monotonically increasing counter
        counts, _ := kitsune.Collect(ctx, kitsune.Take(kitsune.Interval(20*time.Millisecond), 4))
        fmt.Println("counts:", counts)

        // Timer: one-shot after a delay
        msg, _ := kitsune.Collect(ctx,
            kitsune.Timer(20*time.Millisecond, func() string { return "fired!" }))
        fmt.Println("timer:", msg)
    }
    ```

---

### `switchmap` { #switchmap }

Cancel in-progress work when a newer item arrives. Models type-ahead search or live-reload: only the last item's result is emitted.

**Demonstrates:** `SwitchMap` cancellation semantics vs `FlatMap`

[:material-play: Run in Playground](https://go.dev/play/p/Sk1NcNlMTgT){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/switchmap/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: switchmap — cancel in-progress work when a newer item arrives.
    package main

    import (
        "context"
        "fmt"
        "time"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    func main() {
        ctx := context.Background()
        queries := kitsune.FromSlice([]string{"g", "go", "gol", "golang"})

        search := func(ctx context.Context, query string, emit func(string) error) error {
            select {
            case <-time.After(30 * time.Millisecond):
                return emit(fmt.Sprintf("results for %q", query))
            case <-ctx.Done():
                return nil // cancelled by a newer query
            }
        }

        // SwitchMap: only the last query completes
        results, _ := kitsune.Collect(ctx, kitsune.SwitchMap(queries, search))
        fmt.Println("SwitchMap:", results)

        // FlatMap: all queries complete (no cancellation)
        allResults, _ := kitsune.Collect(ctx,
            kitsune.FlatMap(kitsune.FromSlice([]string{"g", "go", "gol", "golang"}),
                func(_ context.Context, query string, emit func(string) error) error {
                    time.Sleep(5 * time.Millisecond)
                    return emit(fmt.Sprintf("results for %q", query))
                }))
        fmt.Println("FlatMap:", allResults)
    }
    ```

---

## :material-filter-check-outline: Deduplication { #deduplication }

### `bloomdedup` { #bloomdedup }

Probabilistic global deduplication with a Bloom filter: bounded memory regardless of key-space size, with a configurable false-positive rate.

**Demonstrates:** `BloomDedupSet`, `WithDedupSet`, `DistinctBy`, `DedupeBy`

[:material-play: Run in Playground](https://go.dev/play/p/89pbET77igp){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/bloomdedup/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: bloomdedup — probabilistic deduplication with a Bloom filter.
    package main

    import (
        "context"
        "fmt"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    func main() {
        ctx := context.Background()

        // DistinctBy with a Bloom filter (1% FP rate, 1000 expected keys)
        events := kitsune.FromSlice([]string{
            "login:alice", "purchase:bob", "login:alice",
            "logout:carol", "purchase:bob",
            "login:dave",
        })

        set := kitsune.BloomDedupSet(1_000, 0.01)
        unique := kitsune.DistinctBy(events, func(e string) string { return e },
            kitsune.WithDedupSet(set),
        )

        results, _ := kitsune.Collect(ctx, unique)
        fmt.Println("distinct events:", results)

        // Shared filter: keys from run1 are remembered in run2
        sharedSet := kitsune.BloomDedupSet(10_000, 0.01)
        kitsune.Collect(ctx, kitsune.DistinctBy(kitsune.FromSlice([]int{1, 2, 3}),
            func(n int) int { return n }, kitsune.WithDedupSet(sharedSet)))

        newOnly, _ := kitsune.Collect(ctx, kitsune.DistinctBy(
            kitsune.FromSlice([]int{2, 3, 4, 5}),
            func(n int) int { return n },
            kitsune.WithDedupSet(sharedSet),
        ))
        fmt.Println("new items in run 2:", newOnly)
    }
    ```

---

## :material-chart-line: Observability { #observability }

### `hooks` { #hooks }

Attach a `MetricsHook` and `LogHook` to a pipeline to collect per-stage throughput, error counts, and average latency.

**Demonstrates:** `WithHook`, `LogHook`, `MetricsHook`, `MultiHook`, `MetricsSnapshot`

[:material-play: Run in Playground](https://go.dev/play/p/JarzFLgu3UV){ .md-button .md-button--primary }
[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/hooks/main.go){ .md-button }

??? example "Full source"

    ```go
    // Example: hooks — observability with LogHook and MetricsHook.
    package main

    import (
        "context"
        "fmt"
        "log/slog"
        "os"
        "time"

        kitsune "github.com/zenbaku/go-kitsune"
    )

    func main() {
        ctx := context.Background()

        metrics := kitsune.NewMetricsHook()
        logger  := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

        slow := func(_ context.Context, n int) (int, error) {
            time.Sleep(2 * time.Millisecond)
            return n * n, nil
        }

        items    := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8})
        squared  := kitsune.Map(items, slow, kitsune.WithName("square"))
        filtered := kitsune.Filter(squared,
            func(_ context.Context, n int) (bool, error) { return n > 10, nil },
            kitsune.WithName("filter"),
        )

        results, _ := kitsune.Collect(ctx, filtered,
            kitsune.WithHook(kitsune.MultiHook(metrics, kitsune.LogHook(logger))),
        )
        fmt.Println("\nresults:", results)

        fmt.Println("\n=== Stage Metrics ===")
        for _, s := range metrics.Snapshot().Stages {
            fmt.Printf("  %-12s  processed=%d  errors=%d  avgLatency=%v\n",
                s.Stage, s.Processed, s.Errors, s.AvgLatency().Round(time.Microsecond))
        }
    }
    ```

---

### `inspector` { #inspector }

A live web dashboard showing the pipeline DAG, per-stage metrics, and buffer fill levels. Add one line to any pipeline.

!!! note "Interactive — run locally"
    The inspector starts an HTTP server and runs indefinitely. It is excluded from automated tests.

    ```
    go run ./examples/inspector
    ```

[:material-github: View source](https://github.com/zenbaku/go-kitsune/blob/main/examples/inspector/main.go){ .md-button }
