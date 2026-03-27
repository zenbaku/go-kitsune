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
