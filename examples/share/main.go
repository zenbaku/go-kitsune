// Example: share — multicast a stream to a dynamically-built subscriber list.
//
// Demonstrates: Share, per-branch Buffer and WithName, MergeRunners
//
// # Share vs Broadcast
//
// Broadcast requires the number of consumers to be fixed at construction:
//
//	branches := kitsune.Broadcast(events, 3)
//	audit   := branches[0]
//	metrics := branches[1]
//	alerts  := branches[2]
//
// Share lets you register consumers one at a time, each with its own options,
// and build the list programmatically. This is useful when:
//
//   - The consumer list comes from config or a plugin registry.
//   - Different consumers need different buffer sizes or stage names.
//   - You want to add consumers conditionally (feature flags, A/B tests).
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

	// A stream of order events.
	orders := kitsune.FromSlice([]OrderEvent{
		{ID: 1, Amount: 49.99},
		{ID: 2, Amount: 1250.00},
		{ID: 3, Amount: 7.50},
		{ID: 4, Amount: 3400.00},
		{ID: 5, Amount: 22.00},
	})

	// Share returns a factory. Each call creates a new output branch.
	// Unlike Broadcast, the number of branches does not need to be known
	// upfront — and each branch can have its own options.
	subscribe := kitsune.Share(orders)

	// Audit log: large buffer so it never slows down the pipeline.
	audit := subscribe(kitsune.WithName("audit"), kitsune.Buffer(256))

	// Metrics: counts events and accumulates revenue.
	metrics := subscribe(kitsune.WithName("metrics"))

	// Fraud detection: only active above a threshold — added conditionally.
	fraudEnabled := true
	var fraud *kitsune.Pipeline[OrderEvent]
	if fraudEnabled {
		fraud = subscribe(kitsune.WithName("fraud-detection"))
	}

	// --- wire up consumers ---

	var auditLog []string
	var mu sync.Mutex

	auditRunner := audit.ForEach(func(_ context.Context, o OrderEvent) error {
		mu.Lock()
		auditLog = append(auditLog, fmt.Sprintf("order #%d: $%.2f", o.ID, o.Amount))
		mu.Unlock()
		return nil
	})

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
	})

	runners := []kitsune.Runnable{auditRunner, metricsRunner}

	var flagged atomic.Int64
	if fraud != nil {
		fraudRunner := fraud.ForEach(func(_ context.Context, o OrderEvent) error {
			if o.Amount > 1000 {
				flagged.Add(1)
				fmt.Printf("fraud alert: order #%d amount $%.2f exceeds threshold\n", o.ID, o.Amount)
			}
			return nil
		})
		runners = append(runners, fraudRunner)
	}

	merged, err := kitsune.MergeRunners(runners...)
	if err != nil {
		panic(err)
	}
	if _, err := merged.Run(ctx); err != nil {
		panic(err)
	}

	fmt.Println("\n--- audit log ---")
	for _, entry := range auditLog {
		fmt.Println(" ", entry)
	}

	fmt.Println("\n--- metrics ---")
	fmt.Printf("  events: %d\n", eventCount.Load())
	fmt.Printf("  revenue: $%.2f\n", totalRevenue.Load().(float64))

	fmt.Println("\n--- fraud ---")
	fmt.Printf("  flagged orders: %d\n", flagged.Load())
}
