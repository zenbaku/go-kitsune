// Example: reduce — fold a stream into a single value.
//
// Demonstrates: Reduce, Scan (for contrast), GroupBy, FlatMap, Map.
package main

import (
	"context"
	"fmt"
	"strings"

	kitsune "github.com/zenbaku/go-kitsune"
)

type Order struct {
	ID       string
	Customer string
	Amount   float64
}

func main() {
	orders := []Order{
		{ID: "A1", Customer: "alice", Amount: 120.00},
		{ID: "A2", Customer: "bob", Amount: 45.50},
		{ID: "A3", Customer: "alice", Amount: 88.75},
		{ID: "A4", Customer: "carol", Amount: 200.00},
		{ID: "A5", Customer: "bob", Amount: 15.25},
	}

	// --- Sum total revenue ---
	fmt.Println("=== Total revenue ===")
	totals, err := kitsune.Reduce(
		kitsune.FromSlice(orders),
		0.0,
		func(sum float64, o Order) float64 { return sum + o.Amount },
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("Total: $%.2f\n", totals[0]) // $469.50

	// --- Reduce vs Scan: spot the difference ---
	// Scan emits the running total after every order.
	// Reduce emits once at the end.
	fmt.Println("\n=== Running total (Scan) vs final total (Reduce) ===")
	running, _ := kitsune.Scan(
		kitsune.FromSlice(orders),
		0.0,
		func(sum float64, o Order) float64 { return sum + o.Amount },
	).Collect(context.Background())
	fmt.Println("Running totals:", running) // [120 165.5 254.25 454.25 469.5]

	final, _ := kitsune.Reduce(
		kitsune.FromSlice(orders),
		0.0,
		func(sum float64, o Order) float64 { return sum + o.Amount },
	).Collect(context.Background())
	fmt.Printf("Final total:   $%.2f\n", final[0]) // $469.50

	// --- Reduce to build an index ---
	// Fold a stream of orders into a map[customerID]totalSpend.
	fmt.Println("\n=== Revenue per customer (Reduce to map) ===")
	byCustomer, err := kitsune.Reduce(
		kitsune.FromSlice(orders),
		make(map[string]float64),
		func(m map[string]float64, o Order) map[string]float64 {
			m[o.Customer] += o.Amount
			return m
		},
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	for customer, total := range byCustomer[0] {
		fmt.Printf("  %-8s $%.2f\n", customer, total)
	}

	// --- Reduce after FlatMap: expand then aggregate ---
	// Each order line-item (comma-separated tags) is flattened, then
	// Reduce builds a deduplicated tag list.
	fmt.Println("\n=== Unique tags across all orders (FlatMap → Reduce) ===")
	taggedOrders := []string{
		"priority,domestic",
		"standard,domestic",
		"priority,international,expedited",
		"standard",
	}
	tags, err := kitsune.Reduce(
		kitsune.FlatMap(
			kitsune.FromSlice(taggedOrders),
			func(_ context.Context, s string, yield func(string) error) error {
				for _, part := range strings.Split(s, ",") {
					if err := yield(part); err != nil {
						return err
					}
				}
				return nil
			},
		),
		make(map[string]struct{}),
		func(seen map[string]struct{}, tag string) map[string]struct{} {
			seen[tag] = struct{}{}
			return seen
		},
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("Unique tags (%d):\n", len(tags[0]))
	for tag := range tags[0] {
		fmt.Printf("  %s\n", tag)
	}

	// --- Reduce on empty stream always emits the seed ---
	fmt.Println("\n=== Empty stream emits seed ===")
	empty, _ := kitsune.Reduce(
		kitsune.FromSlice([]Order{}),
		0.0,
		func(sum float64, o Order) float64 { return sum + o.Amount },
	).Collect(context.Background())
	fmt.Printf("Empty total: $%.2f (seed)\n", empty[0]) // $0.00
}
