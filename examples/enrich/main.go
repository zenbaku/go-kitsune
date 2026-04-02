// Example: enrich — bulk-lookup enrichment with MapBatch, LookupBy, and Enrich.
//
// Demonstrates: MapBatch (batched transforms), LookupBy (keyed bulk lookup returning Pair),
// Enrich (keyed bulk lookup with join), NewLookupConfig/NewEnrichConfig constructors,
// key deduplication across a batch.
package main

import (
	"context"
	"fmt"
	"strings"

	kitsune "github.com/zenbaku/go-kitsune"
)

type Event struct {
	ID     int
	UserID int
	Action string
}

type User struct {
	ID   int
	Name string
	Role string
}

type EnrichedEvent struct {
	EventID  int
	Action   string
	UserName string
	UserRole string
}

// userDB simulates a database of users.
var userDB = map[int]User{
	1: {1, "Alice", "admin"},
	2: {2, "Bob", "member"},
	3: {3, "Carol", "member"},
	4: {4, "Dave", "guest"},
}

// fetchUsers simulates a bulk DB query. It prints the IDs it was called with to
// show that LookupBy and Enrich deduplicate keys before calling Fetch.
func fetchUsers(_ context.Context, ids []int) (map[int]User, error) {
	fmt.Printf("  [DB] fetch users for ids %v\n", ids)
	m := make(map[int]User, len(ids))
	for _, id := range ids {
		if u, ok := userDB[id]; ok {
			m[id] = u
		}
	}
	return m, nil
}

var events = []Event{
	{1, 1, "login"},
	{2, 2, "view"},
	{3, 1, "purchase"}, // same UserID as event 1 — deduplication in action
	{4, 3, "logout"},
	{5, 4, "view"},
}

func main() {
	ctx := context.Background()

	// -------------------------------------------------------------------------
	// MapBatch: collect items into batches, process each batch, emit individual results.
	// Useful when your transform is most efficient operating on a slice at once.
	// -------------------------------------------------------------------------
	fmt.Println("=== MapBatch: normalise actions in batches ===")
	normalized, err := kitsune.MapBatch(
		kitsune.FromSlice(events), 3,
		func(_ context.Context, batch []Event) ([]string, error) {
			fmt.Printf("  processing batch of %d\n", len(batch))
			out := make([]string, len(batch))
			for i, e := range batch {
				out[i] = fmt.Sprintf("event-%d:%s", e.ID, strings.ToUpper(e.Action))
			}
			return out, nil
		},
	).Collect(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println("Result:", normalized)

	// -------------------------------------------------------------------------
	// LookupBy: bulk-fetch a value for each item, emit Pair[item, value].
	// Keys are deduplicated per batch — event 1 and event 3 share UserID 1,
	// so fetchUsers receives only 4 unique IDs despite 5 events.
	// Use LookupBy when you want to keep both the original item and its looked-up
	// value as separate fields, or when combining multiple lookups via ZipWith.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== LookupBy: attach User to each Event as Pair ===")
	pairs, err := kitsune.LookupBy(
		kitsune.FromSlice(events),
		kitsune.LookupConfig[Event, int, User]{
			Key:       func(e Event) int { return e.UserID },
			Fetch:     fetchUsers,
			BatchSize: 10,
		},
	).Collect(ctx)
	if err != nil {
		panic(err)
	}
	for _, p := range pairs {
		fmt.Printf("  event-%d (%s) → %s (%s)\n",
			p.First.ID, p.First.Action, p.Second.Name, p.Second.Role)
	}

	// -------------------------------------------------------------------------
	// Enrich: same bulk-fetch mechanic as LookupBy, but Join combines item and
	// value into a new output type directly — no intermediate Pair.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== Enrich: build EnrichedEvent directly ===")
	enriched, err := kitsune.Enrich(
		kitsune.FromSlice(events),
		kitsune.EnrichConfig[Event, int, User, EnrichedEvent]{
			Key:   func(e Event) int { return e.UserID },
			Fetch: fetchUsers,
			Join: func(e Event, u User) EnrichedEvent {
				return EnrichedEvent{
					EventID:  e.ID,
					Action:   e.Action,
					UserName: u.Name,
					UserRole: u.Role,
				}
			},
			BatchSize: 10,
		},
	).Collect(ctx)
	if err != nil {
		panic(err)
	}
	for _, e := range enriched {
		fmt.Printf("  event-%d: %s by %s (%s)\n", e.EventID, e.Action, e.UserName, e.UserRole)
	}

	// -------------------------------------------------------------------------
	// NewLookupConfig / NewEnrichConfig: constructor helpers that set the
	// default BatchSize (100) automatically.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== NewLookupConfig / NewEnrichConfig constructors ===")

	lookupCfg := kitsune.NewLookupConfig(
		func(e Event) int { return e.UserID },
		fetchUsers,
	)
	// Override BatchSize if needed:
	lookupCfg.BatchSize = 3

	pairs2, err := kitsune.LookupBy(kitsune.FromSlice(events), lookupCfg).Collect(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Printf("LookupBy via constructor: %d pairs\n", len(pairs2))

	enrichCfg := kitsune.NewEnrichConfig(
		func(e Event) int { return e.UserID },
		fetchUsers,
		func(e Event, u User) EnrichedEvent {
			return EnrichedEvent{EventID: e.ID, Action: e.Action, UserName: u.Name, UserRole: u.Role}
		},
	)
	enriched2, err := kitsune.Enrich(kitsune.FromSlice(events), enrichCfg).Collect(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Enrich via constructor: %d enriched events\n", len(enriched2))
}
