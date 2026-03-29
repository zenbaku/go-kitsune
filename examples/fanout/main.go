// Example: fanout — splitting a pipeline by condition.
//
// Demonstrates: Partition, Merge, MergeRunners
package main

import (
	"context"
	"fmt"

	kitsune "github.com/jonathan/go-kitsune"
)

type Order struct {
	ID     string
	Amount float64
}

func main() {
	orders := []Order{
		{"A1", 25.00},
		{"A2", 150.00},
		{"A3", 10.00},
		{"A4", 500.00},
		{"A5", 75.00},
	}

	input := kitsune.FromSlice(orders)

	// Split orders into high-value (≥100) and regular.
	high, regular := kitsune.Partition(input, func(o Order) bool {
		return o.Amount >= 100
	})

	// Process each branch independently.
	var highResults, regularResults []string

	highRunner := kitsune.Map(high, func(_ context.Context, o Order) (string, error) {
		return fmt.Sprintf("[VIP] %s: $%.2f", o.ID, o.Amount), nil
	}).ForEach(func(_ context.Context, s string) error {
		highResults = append(highResults, s)
		return nil
	})

	regularRunner := kitsune.Map(regular, func(_ context.Context, o Order) (string, error) {
		return fmt.Sprintf("[STD] %s: $%.2f", o.ID, o.Amount), nil
	}).ForEach(func(_ context.Context, s string) error {
		regularResults = append(regularResults, s)
		return nil
	})

	// Run both branches.
	runner, err := kitsune.MergeRunners(highRunner, regularRunner)
	if err != nil {
		panic(err)
	}
	if err := runner.Run(context.Background()); err != nil {
		panic(err)
	}

	fmt.Println("High-value orders:")
	for _, s := range highResults {
		fmt.Println(" ", s)
	}
	fmt.Println("Regular orders:")
	for _, s := range regularResults {
		fmt.Println(" ", s)
	}
}
