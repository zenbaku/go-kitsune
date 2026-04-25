// Example: Segment groups operators into named, graph-visible business units.
//
//	go run ./examples/segment
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/zenbaku/go-kitsune"
)

type Order struct {
	ID    int
	Item  string
	Price int // cents
}

func main() {
	ctx := context.Background()
	src := kitsune.FromSlice([]Order{
		{ID: 1, Item: "  apple ", Price: 150},
		{ID: 2, Item: "BANANA", Price: 75},
		{ID: 3, Item: " carrot", Price: 200},
	})

	// "normalize" segment: trim and uppercase the item name.
	normalize := kitsune.Stage[Order, Order](func(p *kitsune.Pipeline[Order]) *kitsune.Pipeline[Order] {
		return kitsune.Map(p, func(_ context.Context, o Order) (Order, error) {
			o.Item = strings.ToUpper(strings.TrimSpace(o.Item))
			return o, nil
		})
	})

	// "tax" segment: 10 percent sales tax.
	addTax := kitsune.Stage[Order, Order](func(p *kitsune.Pipeline[Order]) *kitsune.Pipeline[Order] {
		return kitsune.Map(p, func(_ context.Context, o Order) (Order, error) {
			o.Price = o.Price + o.Price/10
			return o, nil
		})
	})

	pipeline := kitsune.Then(
		kitsune.NewSegment("normalize", normalize),
		kitsune.NewSegment("tax", addTax),
	)

	out, err := kitsune.Collect(ctx, pipeline.Apply(src))
	if err != nil {
		panic(err)
	}

	fmt.Println("--- Output ---")
	for _, o := range out {
		fmt.Printf("#%d %-10s %d\n", o.ID, o.Item, o.Price)
	}

	fmt.Println("\n--- Graph ---")
	for _, n := range pipeline.Apply(src).Describe() {
		seg := "(no segment)"
		if n.SegmentName != "" {
			seg = "[segment: " + n.SegmentName + "]"
		}
		fmt.Printf("  %-12s %-30s %s\n", n.Kind, n.Name, seg)
	}
}
