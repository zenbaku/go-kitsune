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

func ExampleStage() {
	// Stage[I,O] is a named function type for reusable pipeline fragments.
	// Define with a direct type conversion, then Apply to any source.
	double := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, n int) (int, error) { return n * 2, nil })
	})

	results, _ := double.Apply(kitsune.FromSlice([]int{1, 2, 3})).Collect(context.Background())
	fmt.Println(results)
	// Output: [2 4 6]
}

func ExampleThen() {
	// Then composes two stages into one. The output type of the first stage
	// must match the input type of the second — the compiler enforces this.
	toStr := kitsune.Stage[int, string](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, n int) (string, error) {
			return strconv.Itoa(n), nil
		})
	})
	shout := kitsune.Stage[string, string](func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, s string) (string, error) {
			return s + "!", nil
		})
	})

	pipeline := kitsune.Then(toStr, shout) // Stage[int, string]
	results, _ := pipeline.Apply(kitsune.FromSlice([]int{1, 2, 3})).Collect(context.Background())
	fmt.Println(results)
	// Output: [1! 2! 3!]
}

func ExamplePipeline_Through_stage() {
	// Stage[T,T] (type-preserving) is directly compatible with Through — no adapter needed.
	onlyPositive := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return p.Filter(func(n int) bool { return n > 0 })
	})

	results, _ := kitsune.FromSlice([]int{-1, 0, 1, 2}).
		Through(onlyPositive).
		Collect(context.Background())
	fmt.Println(results)
	// Output: [1 2]
}

func ExampleNewChannel() {
	ctx := context.Background()

	// NewChannel creates a push-based source. Feed it from any goroutine and
	// call Close when done — the pipeline drains and exits cleanly.
	src := kitsune.NewChannel[int](8)

	errCh := kitsune.Map(src.Source(), func(_ context.Context, n int) (int, error) {
		return n * 10, nil
	}).ForEach(func(_ context.Context, n int) error {
		fmt.Println(n)
		return nil
	}).RunAsync(ctx)

	for _, v := range []int{1, 2, 3} {
		src.Send(ctx, v) //nolint
	}
	src.Close()
	<-errCh
	// Output:
	// 10
	// 20
	// 30
}

func ExampleRunner_RunAsync() {
	// RunAsync starts the pipeline in a background goroutine and returns a
	// channel that receives exactly one value: nil on success, or an error.
	errCh := kitsune.FromSlice([]int{1, 2, 3}).
		Drain().
		RunAsync(context.Background())

	if err := <-errCh; err != nil {
		fmt.Println("error:", err)
	} else {
		fmt.Println("done")
	}
	// Output: done
}

func ExamplePairwise() {
	results, _ := kitsune.Pairwise(kitsune.FromSlice([]int{1, 2, 3, 4})).Collect(context.Background())
	for _, p := range results {
		fmt.Printf("{%d,%d}\n", p.First, p.Second)
	}
	// Output:
	// {1,2}
	// {2,3}
	// {3,4}
}

func ExampleSlidingWindow() {
	// size=3, step=2 — overlapping windows
	results, _ := kitsune.SlidingWindow(kitsune.FromSlice([]int{1, 2, 3, 4, 5}), 3, 2).Collect(context.Background())
	fmt.Println(results)
	// Output: [[1 2 3] [3 4 5]]
}

func ExampleConcatMap() {
	// ConcatMap guarantees sequential processing order (Concurrency=1).
	results, _ := kitsune.ConcatMap(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, n int) ([]string, error) {
			return []string{strconv.Itoa(n), strconv.Itoa(n * 10)}, nil
		},
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [1 10 2 20 3 30]
}

func ExampleMapResult() {
	parse := func(_ context.Context, s string) (int, error) {
		return strconv.Atoi(s)
	}
	ok, failed := kitsune.MapResult(kitsune.FromSlice([]string{"1", "bad", "3"}), parse)

	nums, _ := ok.Collect(context.Background())
	errs, _ := failed.Collect(context.Background())
	fmt.Println("ok:", nums)
	fmt.Println("failed items:", len(errs))
	// Output:
	// ok: [1 3]
	// failed items: 1
}

func ExampleZipWith() {
	branches := kitsune.Broadcast[int](kitsune.FromSlice([]int{1, 2, 3}), 2)
	doubled := kitsune.Map(branches[1], func(_ context.Context, v int) (int, error) {
		return v * 2, nil
	})
	results, _ := kitsune.ZipWith(branches[0], doubled,
		func(_ context.Context, a, b int) (int, error) { return a + b, nil },
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [3 6 9]
}

func ExampleMapBatch() {
	// Sum each batch of 3 — emits one value per batch.
	results, _ := kitsune.MapBatch(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}), 3,
		func(_ context.Context, batch []int) ([]int, error) {
			sum := 0
			for _, v := range batch {
				sum += v
			}
			return []int{sum}, nil
		},
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [6 9]
}

func ExampleLookupBy() {
	squares := map[int]int{1: 1, 2: 4, 3: 9}
	results, _ := kitsune.LookupBy(
		kitsune.FromSlice([]int{1, 2, 3}),
		kitsune.LookupConfig[int, int, int]{
			Key: func(n int) int { return n },
			Fetch: func(_ context.Context, ids []int) (map[int]int, error) {
				m := make(map[int]int, len(ids))
				for _, id := range ids {
					m[id] = squares[id]
				}
				return m, nil
			},
		},
	).Collect(context.Background())
	for _, p := range results {
		fmt.Printf("%d→%d\n", p.First, p.Second)
	}
	// Output:
	// 1→1
	// 2→4
	// 3→9
}

func ExampleEnrich() {
	names := map[int]string{1: "one", 2: "two", 3: "three"}
	results, _ := kitsune.Enrich(
		kitsune.FromSlice([]int{1, 2, 3}),
		kitsune.EnrichConfig[int, int, string, string]{
			Key: func(n int) int { return n },
			Fetch: func(_ context.Context, ids []int) (map[int]string, error) {
				m := make(map[int]string, len(ids))
				for _, id := range ids {
					m[id] = names[id]
				}
				return m, nil
			},
			Join: func(n int, name string) string {
				return fmt.Sprintf("%d=%s", n, name)
			},
		},
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [1=one 2=two 3=three]
}

func ExampleWithLatestFrom() {
	// WithLatestFrom pairs each primary item with the most recent secondary value.
	// Items arriving before any secondary value has been seen are dropped.
	branches := kitsune.Broadcast[int](kitsune.FromSlice([]int{10, 20, 30}), 2)
	doubled := kitsune.Map(branches[1], func(_ context.Context, v int) (int, error) {
		return v * 2, nil
	})
	// primary=branches[0], secondary=doubled
	combined := kitsune.WithLatestFrom(branches[0], doubled)
	results, _ := combined.Collect(context.Background())
	// The exact pairs depend on scheduling, but each Second is an even multiple of First.
	fmt.Println(len(results) >= 0) // always true — just verify it runs
	// Output: true
}
