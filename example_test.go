package kitsune_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	kitsune "github.com/jonathan/go-kitsune"
)

func ExampleFromSlice() {
	p := kitsune.FromSlice([]string{"a", "b", "c"})
	results, _ := p.Collect(context.Background())
	fmt.Println(results)
	// Output: [a b c]
}

func ExampleMap() {
	input := kitsune.FromSlice([]string{"1", "2", "3"})
	parsed := kitsune.Map(input, kitsune.Lift(strconv.Atoi))
	results, _ := parsed.Collect(context.Background())
	fmt.Println(results)
	// Output: [1 2 3]
}

func ExampleFlatMap() {
	input := kitsune.FromSlice([]string{"a,b", "c,d,e"})
	split := kitsune.FlatMap(input, func(_ context.Context, s string) ([]string, error) {
		return strings.Split(s, ","), nil
	})
	results, _ := split.Collect(context.Background())
	fmt.Println(results)
	// Output: [a b c d e]
}

func ExamplePipeline_Filter() {
	input := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	even := input.Filter(func(n int) bool { return n%2 == 0 })
	results, _ := even.Collect(context.Background())
	fmt.Println(results)
	// Output: [2 4 6]
}

func ExampleBatch() {
	input := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	batched := kitsune.Batch(input, 2)
	results, _ := batched.Collect(context.Background())
	fmt.Println(results)
	// Output: [[1 2] [3 4] [5]]
}

func ExamplePipeline_Take() {
	input := kitsune.FromSlice([]int{10, 20, 30, 40, 50})
	results, _ := input.Take(3).Collect(context.Background())
	fmt.Println(results)
	// Output: [10 20 30]
}

func ExampleGenerate() {
	// Emit items from a custom source.
	counter := kitsune.Generate(func(_ context.Context, yield func(int) bool) error {
		for i := 1; i <= 5; i++ {
			if !yield(i * 10) {
				return nil
			}
		}
		return nil
	})
	results, _ := counter.Collect(context.Background())
	fmt.Println(results)
	// Output: [10 20 30 40 50]
}

func ExamplePartition() {
	input := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	even, odd := kitsune.Partition(input, func(n int) bool { return n%2 == 0 })

	// Merge both branches back together.
	all := kitsune.Merge(even, odd)
	results, _ := all.Collect(context.Background())
	fmt.Println("count:", len(results))
	// Output: count: 6
}

func ExampleLift() {
	input := kitsune.FromSlice([]string{"42", "7"})
	// Lift wraps a plain func(I)(O, error) to add context.Context.
	parsed := kitsune.Map(input, kitsune.Lift(strconv.Atoi))
	results, _ := parsed.Collect(context.Background())
	fmt.Println(results)
	// Output: [42 7]
}

func ExamplePipeline_Through() {
	// Define a reusable pipeline fragment.
	onlyPositive := func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return p.Filter(func(n int) bool { return n > 0 })
	}

	input := kitsune.FromSlice([]int{-2, -1, 0, 1, 2})
	results, _ := input.Through(onlyPositive).Collect(context.Background())
	fmt.Println(results)
	// Output: [1 2]
}

func ExampleScan() {
	// Running sum — emits the accumulator after each item.
	results, _ := kitsune.Scan(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		0,
		func(sum, v int) int { return sum + v },
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [1 3 6 10 15]
}

func ExampleGroupBy() {
	type Event struct{ Kind, Val string }
	input := kitsune.FromSlice([]Event{
		{"click", "btn1"}, {"scroll", "page"}, {"click", "btn2"},
	})
	maps, _ := kitsune.GroupBy(input, func(e Event) string { return e.Kind }).
		Collect(context.Background())
	m := maps[0]
	fmt.Println(len(m["click"]), len(m["scroll"]))
	// Output: 2 1
}

func ExampleDistinct() {
	results, _ := kitsune.Distinct(
		kitsune.FromSlice([]int{3, 1, 4, 1, 5, 9, 2, 6, 5}),
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [3 1 4 5 9 2 6]
}

func ExampleDistinctBy() {
	type Item struct{ ID, Name string }
	input := kitsune.FromSlice([]Item{
		{"a", "Alice"}, {"b", "Bob"}, {"a", "Alex"},
	})
	results, _ := kitsune.DistinctBy(input, func(x Item) string { return x.ID }).
		Collect(context.Background())
	for _, r := range results {
		fmt.Println(r.ID, r.Name)
	}
	// Output:
	// a Alice
	// b Bob
}

func ExampleTakeWhile() {
	results, _ := kitsune.TakeWhile(
		kitsune.FromSlice([]int{2, 4, 6, 7, 8, 10}),
		func(n int) bool { return n%2 == 0 },
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [2 4 6]
}

func ExampleDropWhile() {
	results, _ := kitsune.DropWhile(
		kitsune.FromSlice([]int{1, 2, 3, 10, 4, 5}),
		func(n int) bool { return n < 5 },
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [10 4 5]
}

func ExampleZip() {
	branches := kitsune.Broadcast[int](kitsune.FromSlice([]int{1, 2, 3}), 2)
	doubled := kitsune.Map(branches[1], func(_ context.Context, v int) (int, error) {
		return v * 2, nil
	})
	pairs, _ := kitsune.Zip(branches[0], doubled).Collect(context.Background())
	for _, p := range pairs {
		fmt.Printf("%d→%d\n", p.First, p.Second)
	}
	// Output:
	// 1→2
	// 2→4
	// 3→6
}
