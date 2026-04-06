// Example: groupby — collecting and grouping items.
//
// Demonstrates: GroupBy, Distinct, DistinctBy.
package main

import (
	"context"
	"fmt"
	"sort"

	kitsune "github.com/zenbaku/go-kitsune"
)

type Order struct {
	ID       int
	Customer string
	Amount   float64
}

func main() {
	orders := []Order{
		{1, "alice", 49.99},
		{2, "bob", 120.00},
		{3, "alice", 15.50},
		{4, "carol", 200.00},
		{5, "bob", 34.00},
		{6, "alice", 88.00},
	}

	// --- GroupBy: orders per customer ---
	fmt.Println("=== Orders per customer ===")
	maps, err := kitsune.GroupBy(
		kitsune.FromSlice(orders),
		func(o Order) string { return o.Customer },
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	byCustomer := maps[0]
	customers := make([]string, 0, len(byCustomer))
	for c := range byCustomer {
		customers = append(customers, c)
	}
	sort.Strings(customers)
	for _, c := range customers {
		total := 0.0
		for _, o := range byCustomer[c] {
			total += o.Amount
		}
		fmt.Printf("  %-8s  %d orders  $%.2f total\n", c, len(byCustomer[c]), total)
	}

	// --- Distinct: unique amounts (float64 is comparable) ---
	fmt.Println("\n=== Distinct status codes ===")
	codes := []int{200, 404, 200, 500, 404, 200, 301}
	unique, _ := kitsune.Distinct(kitsune.FromSlice(codes)).Collect(context.Background())
	fmt.Println("  Unique codes seen:", unique)

	// --- DistinctBy: first order per customer ---
	fmt.Println("\n=== First order per customer (DistinctBy) ===")
	first, _ := kitsune.DistinctBy(
		kitsune.FromSlice(orders),
		func(o Order) string { return o.Customer },
	).Collect(context.Background())
	for _, o := range first {
		fmt.Printf("  customer=%-8s  first_order_id=%d\n", o.Customer, o.ID)
	}
}
