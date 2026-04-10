// Example: runningtotal — shared mutable state with MapWith.
//
// MapWith gives every item access to a single shared Ref that persists for the
// lifetime of one Run. Because Concurrency(1) is implied, items are processed
// in order and the Ref is never accessed concurrently — no mutex needed.
//
// Contrast with MapWithKey (examples/keyedstate): one Ref per entity key.
// Here there is one Ref for the entire stream.
//
// Demonstrates:
//   - NewKey / MapWith for single shared pipeline state
//   - Ref.UpdateAndGet for atomic read-modify-write
//   - State reset between independent Run calls
package main

import (
	"context"
	"fmt"

	kitsune "github.com/zenbaku/go-kitsune"
)

// Tx is a financial transaction.
type Tx struct {
	ID     int
	Amount int
}

// Summary holds the running aggregate after each transaction.
type Summary struct {
	ID     int
	Amount int
	Total  int
	Max    int
	Count  int
}

// totals is the shared state accumulated across the stream.
type totals struct {
	sum, max, count int
}

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
			return Summary{
				ID:     tx.ID,
				Amount: tx.Amount,
				Total:  s.sum,
				Max:    s.max,
				Count:  s.count,
			}, nil
		},
	)
}

func main() {
	ctx := context.Background()

	txns := []Tx{
		{1, 120}, {2, 45}, {3, 300}, {4, 80}, {5, 210},
	}

	// --- First run ---
	fmt.Println("=== Run 1 ===")
	results, _ := kitsune.Collect(ctx, buildPipeline(txns))
	for _, s := range results {
		fmt.Printf("  tx#%d amount=%-4d  running_total=%-5d max=%-4d count=%d\n",
			s.ID, s.Amount, s.Total, s.Max, s.Count)
	}

	// --- Second run on same pipeline factory — state resets ---
	//
	// Each Run creates a fresh runCtx, so the Ref starts from the zero value
	// declared in NewKey. Build a new pipeline from the same factory to confirm.
	fmt.Println("\n=== Run 2 (state resets between runs) ===")
	results2, _ := kitsune.Collect(ctx, buildPipeline(txns))
	if results2[0].Total == results[0].Total {
		fmt.Println("  Run 2 matches Run 1 — state was correctly reset.")
	}
	lastRun1 := results[len(results)-1]
	lastRun2 := results2[len(results2)-1]
	fmt.Printf("  Final total: run1=%d  run2=%d  match=%v\n",
		lastRun1.Total, lastRun2.Total, lastRun1.Total == lastRun2.Total)
}
