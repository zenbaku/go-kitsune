// Example: collectors — terminal operators that return a single result.
//
// Demonstrates: First, Last, Count, Any, All, Skip, Map, Filter.
package main

import (
	"context"
	"fmt"
	"strings"

	kitsune "github.com/jonathan/go-kitsune"
)

type Record struct {
	ID     int
	Name   string
	Score  int
	Active bool
}

func main() {
	records := []Record{
		{ID: 1, Name: "alice", Score: 82, Active: true},
		{ID: 2, Name: "bob", Score: 45, Active: false},
		{ID: 3, Name: "carol", Score: 91, Active: true},
		{ID: 4, Name: "dave", Score: 67, Active: true},
		{ID: 5, Name: "eve", Score: 55, Active: false},
	}

	// --- First: find the first high scorer ---
	fmt.Println("=== First: first record with score ≥ 90 ===")
	topScorer, found, err := kitsune.Map(
		kitsune.FromSlice(records).Filter(func(r Record) bool { return r.Score >= 90 }),
		func(_ context.Context, r Record) (string, error) {
			return fmt.Sprintf("%s (score %d)", r.Name, r.Score), nil
		},
	).First(context.Background())
	if err != nil {
		panic(err)
	}
	if found {
		fmt.Println("Top scorer:", topScorer)
	} else {
		fmt.Println("No top scorer found")
	}

	// --- First on empty (no match): safe zero value ---
	fmt.Println("\n=== First: no match returns (zero, false) ===")
	_, found, err = kitsune.FromSlice(records).
		Filter(func(r Record) bool { return r.Score > 200 }).
		First(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("Found: %v\n", found) // false

	// --- Last: most recently processed active record ---
	fmt.Println("\n=== Last: last active record in stream ===")
	last, found, err := kitsune.FromSlice(records).
		Filter(func(r Record) bool { return r.Active }).
		Last(context.Background())
	if err != nil {
		panic(err)
	}
	if found {
		fmt.Printf("Last active: %s (ID %d)\n", last.Name, last.ID)
	}

	// --- Count: how many inactive records ---
	fmt.Println("\n=== Count: inactive records ===")
	inactive, err := kitsune.FromSlice(records).
		Filter(func(r Record) bool { return !r.Active }).
		Count(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("Inactive records: %d\n", inactive)

	// --- Count after Map: count enriched outputs ---
	fmt.Println("\n=== Count after Map ===")
	n, err := kitsune.Map(
		kitsune.FromSlice(records),
		func(_ context.Context, r Record) (string, error) {
			return strings.ToUpper(r.Name), nil
		},
	).Count(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("Mapped %d records\n", n)

	// --- Any: does any record fail validation? ---
	fmt.Println("\n=== Any: any score below passing threshold (60)? ===")
	hasFailures, err := kitsune.FromSlice(records).
		Any(context.Background(), func(r Record) bool { return r.Score < 60 })
	if err != nil {
		panic(err)
	}
	fmt.Printf("Has failures: %v\n", hasFailures) // true (bob=45, eve=55)

	// --- All: do all active records have a passing score? ---
	fmt.Println("\n=== All: every active record scored ≥ 60? ===")
	allPassed, err := kitsune.FromSlice(records).
		Filter(func(r Record) bool { return r.Active }).
		All(context.Background(), func(r Record) bool { return r.Score >= 60 })
	if err != nil {
		panic(err)
	}
	fmt.Printf("All active passed: %v\n", allPassed) // true (alice=82, carol=91, dave=67)

	// --- All: vacuous truth on empty stream ---
	fmt.Println("\n=== All: empty stream → true (vacuous truth) ===")
	vacuous, err := kitsune.FromSlice([]Record{}).
		All(context.Background(), func(r Record) bool { return r.Score > 9000 })
	if err != nil {
		panic(err)
	}
	fmt.Printf("All (empty): %v\n", vacuous) // true

	// --- Skip + collectors: skip the header/first element ---
	fmt.Println("\n=== Skip + Count: ignore first record, count the rest ===")
	rest, err := kitsune.FromSlice(records).Skip(1).Count(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("Records after skipping first: %d\n", rest) // 4

	// --- Composition: Skip → Filter → Map → First ---
	// Skip 1, keep active, get first active name — all in one pipeline.
	fmt.Println("\n=== Skip → Filter → Map → First (full composition) ===")
	name, found, err := kitsune.Map(
		kitsune.FromSlice(records).
			Skip(1).
			Filter(func(r Record) bool { return r.Active }),
		func(_ context.Context, r Record) (string, error) { return r.Name, nil },
	).First(context.Background())
	if err != nil {
		panic(err)
	}
	if found {
		fmt.Println("First active after skip:", name) // carol
	}
}
