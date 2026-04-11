// Example: deadletter — retry with dead-letter fallback using DeadLetter.
//
// DeadLetter routes items by outcome: successful results (including items that
// succeed after retry) go to the ok pipeline; items that exhaust all retries
// go to the dlq pipeline as ErrItem values carrying the original input and the
// final error.
//
// Both branches must be consumed before calling Run — the same rule as
// Partition. MergeRunners is the idiomatic way to drive both together.
//
// Demonstrates:
//   - DeadLetter with OnError(Retry(...)) for transient-failure handling
//   - Two-branch consumption pattern: ForEach.Build() + MergeRunners
//   - ErrItem.Item for fallback logic on permanently-failed inputs
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

// User is the result of a successful profile fetch.
type User struct {
	ID   int
	Name string
}

// profiles simulates a remote store: id → name (missing ids always fail).
var profiles = map[int]string{
	1: "Alice",
	2: "Bob",
	3: "Carol",
	4: "Dave",
	5: "Eve",
}

// attempts tracks how many times each id has been tried (thread-safe).
var attempts sync.Map

// attemptsNeeded controls how many tries an id requires before succeeding.
// Ids not listed succeed on the first try; ids mapped to 0 always fail.
var attemptsNeeded = map[int]int{
	3: 2, // Carol needs 2 tries
	4: 3, // Dave needs 3 tries
	7: 0, // unknown — always fails
	8: 0, // unknown — always fails
}

var errTransient = errors.New("transient error")
var errNotFound = errors.New("user not found")

func fetchProfile(_ context.Context, id int) (User, error) {
	needed, limited := attemptsNeeded[id]

	// Load-and-increment atomically.
	var count int64
	v, _ := attempts.LoadOrStore(id, new(atomic.Int64))
	count = v.(*atomic.Int64).Add(1)

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

	// IDs to look up. 7 and 8 are not in the profiles store and will be
	// dead-lettered after exhausting retries.
	ids := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 7, 8})

	ok, dlq := kitsune.DeadLetter(
		ids,
		fetchProfile,
		kitsune.OnError(kitsune.RetryMax(3, kitsune.FixedBackoff(1*time.Millisecond))),
	)

	// Both branches must be consumed together via MergeRunners.
	var mu sync.Mutex
	var succeeded []User
	var fallbacks []User

	r1 := ok.ForEach(func(_ context.Context, u User) error {
		mu.Lock()
		succeeded = append(succeeded, u)
		mu.Unlock()
		return nil
	}).Build()

	r2 := dlq.ForEach(func(_ context.Context, ei kitsune.ErrItem[int]) error {
		// Fallback: synthesize a default user for the dead-lettered id.
		fallback := User{ID: ei.Item, Name: fmt.Sprintf("unknown-%d", ei.Item)}
		mu.Lock()
		fallbacks = append(fallbacks, fallback)
		mu.Unlock()
		fmt.Printf("  [dlq] id=%d err=%v  → using fallback %q\n", ei.Item, ei.Err, fallback.Name)
		return nil
	}).Build()

	runner, _ := kitsune.MergeRunners(r1, r2)
	if err := runner.Run(ctx); err != nil {
		panic(err)
	}

	fmt.Printf("\n=== Results ===\n")
	fmt.Printf("  succeeded : %d items\n", len(succeeded))
	for _, u := range succeeded {
		n, _ := attempts.Load(u.ID)
		tries := n.(*atomic.Int64).Load()
		fmt.Printf("    id=%-2d name=%-6s  tries=%d\n", u.ID, u.Name, tries)
	}
	fmt.Printf("  dead-lettered : %d items\n", len(fallbacks))
	for _, u := range fallbacks {
		fmt.Printf("    id=%-2d fallback=%q\n", u.ID, u.Name)
	}
}
