# LookupBy / Enrich BatchTimeout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `BatchTimeout time.Duration` field to `LookupConfig` and `EnrichConfig` so partial batches flush after the configured duration under low throughput.

**Architecture:** The field is wired through as a prepended `BatchTimeout(d)` StageOption when calling `MapBatch`, so the existing `batchCollectOpts` mechanism picks it up naturally. Explicit StageOption `BatchTimeout` takes precedence (last-write-wins semantics).

**Tech Stack:** Go generics, pgregory.net/rapid for property tests, kitsune's internal testkit for clock control.

---

## Task 1: `LookupConfig.BatchTimeout` field and wiring

### 1.1 Write a failing test

- [ ] Open `/Users/jonathan/projects/go-kitsune/state_test.go` and append the following test at the end of the `LookupBy / Enrich` section (immediately after `TestLookupByDeduplicatesKeys`, before the `Lift alias` separator comment).

```go
func TestLookupByBatchTimeoutFlushesPartialBatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var fetchMu sync.Mutex
	var fetchCalls [][]int

	ch := kitsune.NewChannel[int](10)
	cfg := kitsune.LookupConfig[int, int, string]{
		Key: func(id int) int { return id },
		Fetch: func(_ context.Context, ids []int) (map[int]string, error) {
			fetchMu.Lock()
			cp := make([]int, len(ids))
			copy(cp, ids)
			fetchCalls = append(fetchCalls, cp)
			fetchMu.Unlock()
			result := make(map[int]string, len(ids))
			for _, id := range ids {
				result[id] = fmt.Sprintf("v%d", id)
			}
			return result, nil
		},
		BatchSize:    100,
		BatchTimeout: 20 * time.Millisecond,
	}

	pairs := make(chan kitsune.Pair[int, string], 16)
	done := make(chan error, 1)
	go func() {
		done <- kitsune.LookupBy(ch.Source(), cfg).
			ForEach(func(_ context.Context, p kitsune.Pair[int, string]) error {
				pairs <- p
				return nil
			}).Run(ctx)
	}()

	// Send two items; far below BatchSize. Without BatchTimeout these would
	// sit in the Batch buffer indefinitely until the source closes.
	if err := ch.Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(ctx, 2); err != nil {
		t.Fatal(err)
	}

	// Wait past the timeout so the ticker fires and flushes the partial batch.
	time.Sleep(50 * time.Millisecond)

	// We must see the two pairs emitted *before* we close the channel, proving
	// the flush came from the timeout rather than the source-closed path.
	seen := make(map[int]string)
	for i := 0; i < 2; i++ {
		select {
		case p := <-pairs:
			seen[p.First] = p.Second
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("timeout waiting for pair %d (seen so far: %v)", i, seen)
		}
	}
	if seen[1] != "v1" || seen[2] != "v2" {
		t.Fatalf("got %v, want map[1:v1 2:v2]", seen)
	}

	fetchMu.Lock()
	if len(fetchCalls) != 1 {
		fetchMu.Unlock()
		t.Fatalf("expected exactly 1 fetch call before close, got %d: %v", len(fetchCalls), fetchCalls)
	}
	if len(fetchCalls[0]) != 2 {
		fetchMu.Unlock()
		t.Fatalf("expected fetch batch of size 2, got %d: %v", len(fetchCalls[0]), fetchCalls[0])
	}
	fetchMu.Unlock()

	ch.Close()
	if err := <-done; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
}
```

- [ ] Add `"sync"` to the import block of `state_test.go` if it is not already present. (`state_test.go` currently imports `context`, `fmt`, `sort`, `testing`, `time`, and kitsune; `sync` is new.)

- [ ] Run the test and confirm it fails on the `BatchTimeout` struct field (not yet defined).

```
task test -- -run TestLookupByBatchTimeoutFlushesPartialBatch
```

Expected output contains a compile error similar to:

```
./state_test.go: unknown field BatchTimeout in struct literal of type kitsune.LookupConfig[...]
```

### 1.2 Make the test pass

- [ ] Open `/Users/jonathan/projects/go-kitsune/enrich.go`.

- [ ] Add the `"time"` import to the import block so it reads:

```go
import (
	"context"
	"time"
)
```

- [ ] Replace the `LookupConfig` struct definition with:

```go
// LookupConfig configures a [LookupBy] stage.
//
//   - Key extracts the lookup key from each item.
//   - Fetch receives a deduplicated slice of keys and returns a map from key
//     to looked-up value. Items whose key is absent from the map receive the
//     zero value for V.
//   - BatchSize controls how many items are collected before a single Fetch
//     call is made. Defaults to 100 when zero.
//   - BatchTimeout, when non-zero, flushes a partial batch after the duration
//     elapses with no new item. Without it, items sit in the internal buffer
//     until BatchSize is reached or the source closes, which can introduce
//     unbounded latency under low throughput.
type LookupConfig[T any, K comparable, V any] struct {
	Key          func(T) K
	Fetch        func(context.Context, []K) (map[K]V, error)
	BatchSize    int
	BatchTimeout time.Duration
}
```

- [ ] Replace the body of `LookupBy` with the version that prepends `BatchTimeout(cfg.BatchTimeout)` to `opts` when the field is non-zero, so the existing `batchCollectOpts` plumbing picks it up. Explicit caller-supplied options still win via last-write-wins semantics:

```go
func LookupBy[T any, K comparable, V any](p *Pipeline[T], cfg LookupConfig[T, K, V], opts ...StageOption) *Pipeline[Pair[T, V]] {
	size := cfg.BatchSize
	if size <= 0 {
		size = defaultLookupBatchSize
	}
	if cfg.BatchTimeout > 0 {
		opts = append([]StageOption{BatchTimeout(cfg.BatchTimeout)}, opts...)
	}
	return MapBatch(p, size, func(ctx context.Context, batch []T) ([]Pair[T, V], error) {
		keys := uniqueKeys(batch, cfg.Key)
		m, err := cfg.Fetch(ctx, keys)
		if err != nil {
			return nil, err
		}
		result := make([]Pair[T, V], len(batch))
		for i, item := range batch {
			result[i] = Pair[T, V]{First: item, Second: m[cfg.Key(item)]}
		}
		return result, nil
	}, opts...)
}
```

- [ ] Run the targeted test and confirm it passes.

```
task test -- -run TestLookupByBatchTimeoutFlushesPartialBatch
```

Expected output:

```
ok  	github.com/zenbaku/go-kitsune	<time>s
```

- [ ] Run the full short suite and the race suite; both must stay green.

```
task test
task test:race
```

Expected output (both): `ok  github.com/zenbaku/go-kitsune`, no FAIL, no DATA RACE.

---

## Task 2: `EnrichConfig.BatchTimeout` field and wiring

### 2.1 Write a failing test

- [ ] Append the following test to `/Users/jonathan/projects/go-kitsune/state_test.go`, immediately after `TestLookupByBatchTimeoutFlushesPartialBatch`.

```go
func TestEnrichBatchTimeoutFlushesPartialBatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var fetchMu sync.Mutex
	fetchCalls := 0

	ch := kitsune.NewChannel[string](10)
	cfg := kitsune.EnrichConfig[string, string, int, string]{
		Key: func(s string) string { return s },
		Fetch: func(_ context.Context, keys []string) (map[string]int, error) {
			fetchMu.Lock()
			fetchCalls++
			fetchMu.Unlock()
			result := make(map[string]int, len(keys))
			for i, k := range keys {
				result[k] = i + 1
			}
			return result, nil
		},
		Join:         func(s string, n int) string { return fmt.Sprintf("%s=%d", s, n) },
		BatchSize:    100,
		BatchTimeout: 20 * time.Millisecond,
	}

	out := make(chan string, 16)
	done := make(chan error, 1)
	go func() {
		done <- kitsune.Enrich(ch.Source(), cfg).
			ForEach(func(_ context.Context, s string) error {
				out <- s
				return nil
			}).Run(ctx)
	}()

	if err := ch.Send(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(ctx, "b"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	got := make(map[string]bool)
	for i := 0; i < 2; i++ {
		select {
		case s := <-out:
			got[s] = true
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("timeout waiting for enrich output %d (got %v)", i, got)
		}
	}
	if !got["a=1"] || !got["b=2"] {
		t.Fatalf("got %v, want a=1 and b=2", got)
	}

	fetchMu.Lock()
	if fetchCalls != 1 {
		fetchMu.Unlock()
		t.Fatalf("expected 1 fetch call before close, got %d", fetchCalls)
	}
	fetchMu.Unlock()

	ch.Close()
	if err := <-done; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
}
```

- [ ] Run it and confirm it fails on the unknown field.

```
task test -- -run TestEnrichBatchTimeoutFlushesPartialBatch
```

Expected output contains:

```
./state_test.go: unknown field BatchTimeout in struct literal of type kitsune.EnrichConfig[...]
```

### 2.2 Make the test pass

- [ ] In `/Users/jonathan/projects/go-kitsune/enrich.go`, replace the `EnrichConfig` struct definition with:

```go
// EnrichConfig configures an [Enrich] stage.
//
//   - Key extracts the lookup key from each item.
//   - Fetch receives a deduplicated slice of keys and returns a map from key
//     to looked-up value.
//   - Join combines the original item with its fetched value into the output type.
//   - BatchSize controls how many items are collected before a single Fetch
//     call is made. Defaults to 100 when zero.
//   - BatchTimeout, when non-zero, flushes a partial batch after the duration
//     elapses with no new item. Without it, items sit in the internal buffer
//     until BatchSize is reached or the source closes, which can introduce
//     unbounded latency under low throughput.
type EnrichConfig[T any, K comparable, V, O any] struct {
	Key          func(T) K
	Fetch        func(context.Context, []K) (map[K]V, error)
	Join         func(T, V) O
	BatchSize    int
	BatchTimeout time.Duration
}
```

- [ ] Replace the body of `Enrich` with the version that prepends the timeout option:

```go
func Enrich[T any, K comparable, V, O any](p *Pipeline[T], cfg EnrichConfig[T, K, V, O], opts ...StageOption) *Pipeline[O] {
	size := cfg.BatchSize
	if size <= 0 {
		size = defaultLookupBatchSize
	}
	if cfg.BatchTimeout > 0 {
		opts = append([]StageOption{BatchTimeout(cfg.BatchTimeout)}, opts...)
	}
	return MapBatch(p, size, func(ctx context.Context, batch []T) ([]O, error) {
		keys := uniqueKeys(batch, cfg.Key)
		m, err := cfg.Fetch(ctx, keys)
		if err != nil {
			return nil, err
		}
		result := make([]O, len(batch))
		for i, item := range batch {
			result[i] = cfg.Join(item, m[cfg.Key(item)])
		}
		return result, nil
	}, opts...)
}
```

- [ ] Run the targeted test and confirm it passes.

```
task test -- -run TestEnrichBatchTimeoutFlushesPartialBatch
```

Expected output: `ok  github.com/zenbaku/go-kitsune`.

- [ ] Run the full suites.

```
task test
task test:race
```

Both should report `ok` with no failures or races.

---

## Task 3: Update `doc/operators.md`

- [ ] Open `/Users/jonathan/projects/go-kitsune/doc/operators.md`.

- [ ] In the `LookupBy` section, replace the `LookupConfig` field bullet list (around line 2108) with:

```markdown
- `Key func(T) K`: extracts the lookup key from each item
- `Fetch func(context.Context, []K) (map[K]V, error)`: bulk fetcher
- `BatchSize int`: how many items to collect before calling `Fetch` (default: 100)
- `BatchTimeout time.Duration`: when non-zero, flushes a partial batch after the duration elapses with no new item. Without this, items sit in the internal buffer until `BatchSize` is reached or the source closes, which can introduce unbounded latency under low throughput.
```

- [ ] In the `Enrich` section, replace the `EnrichConfig` field bullet list (around line 2139) with:

```markdown
- `Key func(T) K`
- `Fetch func(context.Context, []K) (map[K]V, error)`
- `Join func(T, V) O`
- `BatchSize int`: default 100
- `BatchTimeout time.Duration`: when non-zero, flushes a partial batch after the duration elapses with no new item.
```

- [ ] Verify the "Options" line for both operators already reads `**Options:** Buffer, WithName, BatchTimeout.` and leave it unchanged.

- [ ] Sanity check that the file still renders (no broken headings):

```
task test
```

Expected output: `ok`.

---

## Task 4: Update `doc/api-matrix.md`

- [ ] Open `/Users/jonathan/projects/go-kitsune/doc/api-matrix.md`.

- [ ] Replace the `Enrich` and `LookupBy` rows (lines 177 and 178) with:

```markdown
| `Enrich` | `Enrich[T,K,V,O](p, keyFn, fetch, join, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | ✓ | – |
| `LookupBy` | `LookupBy[T,K,V](p, keyFn, fetch, opts...)` → `*Pipeline[Pair[T,V]]` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | ✓ | – |
```

- [ ] In the **Notes** block immediately below the table, append this bullet:

```markdown
- `LookupBy` and `Enrich` also accept `BatchTimeout` as a first-class field on `LookupConfig` / `EnrichConfig`; the field is equivalent to passing the `BatchTimeout(d)` `StageOption` and is forwarded to the internal `Batch` stage.
```

- [ ] Run:

```
task test
```

Expected output: `ok`.

---

## Task 5: `examples/lookupby/main.go`

- [ ] Create the directory and file `/Users/jonathan/projects/go-kitsune/examples/lookupby/main.go` with the following content:

```go
// Example: lookupby — bulk-fetch user records with BatchTimeout for low
// throughput.
//
// LookupBy batches items internally and issues one Fetch call per batch with
// deduplicated keys. Under low throughput a pure size-based batch can stall
// waiting for items that never arrive. Setting BatchTimeout on LookupConfig
// flushes the partial batch after the configured duration, bounding latency
// at the cost of smaller batches.
//
// Demonstrates:
//   - LookupConfig with BatchSize and BatchTimeout
//   - A simulated user database fetched in bulk
//   - Pair[T,V] output attaching the fetched value to the original item
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// userDB stands in for a remote user store. A real example would call out to
// a database or HTTP service; here we just sleep briefly to simulate latency.
var userDB = map[int]string{
	1: "alice",
	2: "bob",
	3: "carol",
	4: "dave",
	5: "eve",
}

func fetchUsers(_ context.Context, ids []int) (map[int]string, error) {
	// Simulate a bulk lookup round-trip.
	time.Sleep(2 * time.Millisecond)
	out := make(map[int]string, len(ids))
	for _, id := range ids {
		if name, ok := userDB[id]; ok {
			out[id] = name
		}
	}
	return out, nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Low-throughput producer: a handful of ids trickle in with gaps between
	// them. BatchSize is large (50) so without BatchTimeout the pipeline would
	// wait for the source to close before ever calling fetchUsers.
	ch := kitsune.NewChannel[int](8)

	cfg := kitsune.LookupConfig[int, int, string]{
		Key:          func(id int) int { return id },
		Fetch:        fetchUsers,
		BatchSize:    50,
		BatchTimeout: 25 * time.Millisecond,
	}

	done := make(chan error, 1)
	go func() {
		done <- kitsune.LookupBy(ch.Source(), cfg).
			ForEach(func(_ context.Context, p kitsune.Pair[int, string]) error {
				fmt.Printf("  id=%d  name=%q\n", p.First, p.Second)
				return nil
			}).Run(ctx)
	}()

	fmt.Println("=== lookupby with BatchTimeout ===")
	ids := []int{1, 2, 3, 4, 5}
	for _, id := range ids {
		if err := ch.Send(ctx, id); err != nil {
			panic(err)
		}
		// Trickle: each gap is longer than BatchTimeout, so each item flushes
		// as its own partial batch rather than waiting to fill up.
		time.Sleep(40 * time.Millisecond)
	}

	ch.Close()
	if err := <-done; err != nil {
		panic(err)
	}
}
```

- [ ] Verify it runs clean:

```
go run ./examples/lookupby
```

Expected output (order of the five lines is deterministic because each item flushes its own batch):

```
=== lookupby with BatchTimeout ===
  id=1  name="alice"
  id=2  name="bob"
  id=3  name="carol"
  id=4  name="dave"
  id=5  name="eve"
```

### 5.1 Register the example

- [ ] Open `/Users/jonathan/projects/go-kitsune/examples_test.go`.

- [ ] In the `examples` slice, insert `"lookupby",` immediately after `"isoptimized",` (keep the existing alphabetical grouping).

- [ ] Run:

```
go test -run TestExamples/lookupby -timeout 120s .
```

Expected output: `PASS` for the `TestExamples/lookupby` subtest.

---

## Task 6: `examples/enrich/main.go`

- [ ] Create `/Users/jonathan/projects/go-kitsune/examples/enrich/main.go` with the following content:

```go
// Example: enrich — combine a stream of events with user records fetched in
// bulk, using BatchTimeout to bound latency under low throughput.
//
// Enrich is like LookupBy but calls a Join function to produce the output type
// directly, avoiding an intermediate Pair. Setting BatchTimeout on EnrichConfig
// flushes partial batches after a duration so the pipeline does not stall when
// traffic is slow.
//
// Demonstrates:
//   - EnrichConfig with BatchSize, BatchTimeout, and a Join function
//   - A simulated user database fetched in bulk
//   - Join producing a typed EnrichedEvent
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// Event is the incoming record; UserID drives the lookup.
type Event struct {
	ID     int
	UserID int
	Action string
}

// User is the fetched record.
type User struct {
	ID   int
	Name string
}

// EnrichedEvent is the output: an Event annotated with its User's name.
type EnrichedEvent struct {
	EventID int
	UserID  int
	Action  string
	Name    string
}

var users = map[int]User{
	10: {ID: 10, Name: "alice"},
	11: {ID: 11, Name: "bob"},
	12: {ID: 12, Name: "carol"},
}

func fetchUsers(_ context.Context, ids []int) (map[int]User, error) {
	// Simulate a bulk lookup round-trip.
	time.Sleep(2 * time.Millisecond)
	out := make(map[int]User, len(ids))
	for _, id := range ids {
		if u, ok := users[id]; ok {
			out[id] = u
		}
	}
	return out, nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := kitsune.NewChannel[Event](8)

	cfg := kitsune.EnrichConfig[Event, int, User, EnrichedEvent]{
		Key:   func(e Event) int { return e.UserID },
		Fetch: fetchUsers,
		Join: func(e Event, u User) EnrichedEvent {
			return EnrichedEvent{
				EventID: e.ID,
				UserID:  e.UserID,
				Action:  e.Action,
				Name:    u.Name,
			}
		},
		BatchSize:    50,
		BatchTimeout: 25 * time.Millisecond,
	}

	done := make(chan error, 1)
	go func() {
		done <- kitsune.Enrich(ch.Source(), cfg).
			ForEach(func(_ context.Context, e EnrichedEvent) error {
				fmt.Printf("  event=%d user=%d (%s) action=%s\n", e.EventID, e.UserID, e.Name, e.Action)
				return nil
			}).Run(ctx)
	}()

	fmt.Println("=== enrich with BatchTimeout ===")
	events := []Event{
		{ID: 1, UserID: 10, Action: "login"},
		{ID: 2, UserID: 11, Action: "click"},
		{ID: 3, UserID: 12, Action: "purchase"},
	}
	for _, e := range events {
		if err := ch.Send(ctx, e); err != nil {
			panic(err)
		}
		// Trickle: gap longer than BatchTimeout forces a partial flush per item.
		time.Sleep(40 * time.Millisecond)
	}

	ch.Close()
	if err := <-done; err != nil {
		panic(err)
	}
}
```

- [ ] Verify it runs clean:

```
go run ./examples/enrich
```

Expected output:

```
=== enrich with BatchTimeout ===
  event=1 user=10 (alice) action=login
  event=2 user=11 (bob) action=click
  event=3 user=12 (carol) action=purchase
```

### 6.1 Register the example

- [ ] Open `/Users/jonathan/projects/go-kitsune/examples_test.go`.

- [ ] Insert `"enrich",` into the `examples` slice immediately after `"deadletter",` (alphabetical order).

- [ ] Run:

```
go test -run TestExamples/enrich -timeout 120s .
```

Expected output: `PASS` for the `TestExamples/enrich` subtest.

- [ ] Run the whole example matrix to confirm nothing else broke:

```
task test:examples
```

Expected output: all subtests pass.

---

## Task 7: Mark the roadmap item done

- [ ] Open `/Users/jonathan/projects/go-kitsune/doc/roadmap.md`.

- [ ] Change line 25 from:

```markdown
- [ ] **`LookupBy` / `Enrich` batch timeout**: Under low throughput, items accumulate in the internal `MapBatch` buffer until the full batch size is reached, introducing unbounded latency before `Fetch` is called. Add a `BatchTimeout time.Duration` field to `LookupConfig` and `EnrichConfig`, using the same semantics as `Batch`'s `BatchTimeout` option: flush a partial batch when the duration elapses with no new item.
```

to:

```markdown
- [x] **`LookupBy` / `Enrich` batch timeout**: Under low throughput, items accumulate in the internal `MapBatch` buffer until the full batch size is reached, introducing unbounded latency before `Fetch` is called. Add a `BatchTimeout time.Duration` field to `LookupConfig` and `EnrichConfig`, using the same semantics as `Batch`'s `BatchTimeout` option: flush a partial batch when the duration elapses with no new item.
```

- [ ] Final verification: run the full pre-PR matrix.

```
task test:all
```

Expected output: every stage passes (`test`, `test:race`, `test:property`, `test:examples`).

---

## Done criteria

- `LookupConfig` and `EnrichConfig` both carry a `BatchTimeout time.Duration` field.
- `TestLookupByBatchTimeoutFlushesPartialBatch` and `TestEnrichBatchTimeoutFlushesPartialBatch` both pass under `task test:race`.
- `doc/operators.md` documents the new field on both configs.
- `doc/api-matrix.md` shows `✓` in the BT column for both rows, with a note explaining the config-field shortcut.
- `examples/lookupby` and `examples/enrich` exist, run cleanly, and are registered in `examples_test.go`.
- `doc/roadmap.md` marks the item `[x]`.
- `task test:all` is green.
