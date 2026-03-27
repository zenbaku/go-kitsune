// Example: state — sharing typed state across pipeline stages.
//
// Demonstrates: NewKey, Ref, MapWith, FlatMapWith, Update, Get
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
		func(ctx context.Context, ref *kitsune.Ref[map[string]string], item Item) ([]Query, error) {
			err := ref.Update(ctx, func(m map[string]string) (map[string]string, error) {
				m[item.ID+"-q1"] = item.Name
				m[item.ID+"-q2"] = item.Name
				return m, nil
			})
			if err != nil {
				return nil, err
			}
			return []Query{
				{QueryID: item.ID + "-q1", ItemID: item.ID},
				{QueryID: item.ID + "-q2", ItemID: item.ID},
			}, nil
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
}
