// Example: state — sharing typed state across pipeline stages.
//
// Demonstrates: NewKey, Ref, MapWith, FlatMapWith, Update, Get, UpdateAndGet
package main

import (
	"context"
	"fmt"

	kitsune "github.com/jonathan/go-kitsune"
)

// Simulate: items → generate queries → correlate results back to items.
type Item struct {
	ID   string
	Name string
}

type Query struct {
	QueryID string
	ItemID  string
}

type Result struct {
	QueryID  string
	ItemName string
}

// queryOrigin tracks which item each query came from.
var queryOrigin = kitsune.NewKey[map[string]string]("query-origin", make(map[string]string))

func main() {
	items := []Item{
		{"i1", "Widget"},
		{"i2", "Gadget"},
	}

	input := kitsune.FromSlice(items)

	// Stage 1: expand each item into queries, recording the origin.
	queries := kitsune.FlatMapWith(input, queryOrigin,
		func(ctx context.Context, ref *kitsune.Ref[map[string]string], item Item, yield func(Query) error) error {
			err := ref.Update(ctx, func(m map[string]string) (map[string]string, error) {
				m[item.ID+"-q1"] = item.Name
				m[item.ID+"-q2"] = item.Name
				return m, nil
			})
			if err != nil {
				return err
			}
			for _, q := range []Query{
				{QueryID: item.ID + "-q1", ItemID: item.ID},
				{QueryID: item.ID + "-q2", ItemID: item.ID},
			} {
				if err := yield(q); err != nil {
					return err
				}
			}
			return nil
		},
	)

	// Stage 2: correlate each query back using the origin map.
	results := kitsune.MapWith(queries, queryOrigin,
		func(ctx context.Context, ref *kitsune.Ref[map[string]string], q Query) (Result, error) {
			origins, _ := ref.Get(ctx)
			return Result{
				QueryID:  q.QueryID,
				ItemName: origins[q.QueryID],
			}, nil
		},
	)

	out, err := results.Collect(context.Background())
	if err != nil {
		panic(err)
	}

	for _, r := range out {
		fmt.Printf("Query %s → originated from %q\n", r.QueryID, r.ItemName)
	}

	// --- UpdateAndGet: atomic read-modify-write that returns the new value ---
	//
	// Unlike Update (fire and forget), UpdateAndGet returns the updated state
	// so you can use it directly in the output without a second Get call.
	fmt.Println("\n=== UpdateAndGet: running totals ===")

	var totalKey = kitsune.NewKey("total", 0)
	amounts := kitsune.FromSlice([]int{10, 25, 5, 40})

	runningTotals, err := kitsune.MapWith(amounts, totalKey,
		func(ctx context.Context, ref *kitsune.Ref[int], amount int) (string, error) {
			newTotal, err := ref.UpdateAndGet(ctx, func(v int) (int, error) {
				return v + amount, nil
			})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("+%d → total=%d", amount, newTotal), nil
		},
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	for _, line := range runningTotals {
		fmt.Println(line)
	}
}
