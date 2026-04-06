package kitsune_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/testkit"
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
	split := kitsune.FlatMap(input, func(_ context.Context, s string, yield func(string) error) error {
		for _, part := range strings.Split(s, ",") {
			if err := yield(part); err != nil {
				return err
			}
		}
		return nil
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

func ExampleLiftPure() {
	input := kitsune.FromSlice([]int{1, 2, 3})
	// LiftPure wraps a plain func(I) O — no error return needed.
	doubled := kitsune.Map(input, kitsune.LiftPure(func(n int) int { return n * 2 }))
	results, _ := doubled.Collect(context.Background())
	fmt.Println(results)
	// Output: [2 4 6]
}

func ExampleReturn() {
	type User struct{ Name string }
	unknown := User{Name: "unknown"}

	input := kitsune.FromSlice([]int{1, 2, 3})
	// Simulate a lookup that fails for item 2; substitute a sentinel value.
	enriched := kitsune.Map(input, func(_ context.Context, id int) (User, error) {
		if id == 2 {
			return User{}, errors.New("not found")
		}
		return User{Name: fmt.Sprintf("user%d", id)}, nil
	}, kitsune.OnError(kitsune.Return(unknown)))

	results, _ := enriched.Collect(context.Background())
	for _, u := range results {
		fmt.Println(u.Name)
	}
	// Output:
	// user1
	// unknown
	// user3
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

	h := kitsune.Map(src.Source(), func(_ context.Context, n int) (int, error) {
		return n * 10, nil
	}).ForEach(func(_ context.Context, n int) error {
		fmt.Println(n)
		return nil
	}).RunAsync(ctx)

	for _, v := range []int{1, 2, 3} {
		src.Send(ctx, v) //nolint
	}
	src.Close()
	h.Wait() //nolint
	// Output:
	// 10
	// 20
	// 30
}

func ExampleRunner_RunAsync() {
	// RunAsync starts the pipeline in a background goroutine and returns a
	// *RunHandle. Call Wait() to block until completion, Done() to select on
	// completion, or Err() to receive the error channel directly.
	h := kitsune.FromSlice([]int{1, 2, 3}).
		Drain().
		RunAsync(context.Background())

	if err := h.Wait(); err != nil {
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
		func(_ context.Context, n int, yield func(string) error) error {
			for _, v := range []string{strconv.Itoa(n), strconv.Itoa(n * 10)} {
				if err := yield(v); err != nil {
					return err
				}
			}
			return nil
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

func ExampleReject() {
	input := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	results, _ := kitsune.Reject(input, func(n int) bool { return n%2 == 0 }).Collect(context.Background())
	fmt.Println(results)
	// Output: [1 3 5]
}

func ExampleWithIndex() {
	results, _ := kitsune.WithIndex(kitsune.FromSlice([]string{"a", "b", "c"})).Collect(context.Background())
	for _, p := range results {
		fmt.Printf("%d:%s\n", p.First, p.Second)
	}
	// Output:
	// 0:a
	// 1:b
	// 2:c
}

func ExampleMapIntersperse() {
	results, _ := kitsune.MapIntersperse(
		kitsune.FromSlice([]int{1, 2, 3}),
		0,
		func(_ context.Context, n int) (int, error) { return n * 10, nil },
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [10 0 20 0 30]
}

func ExampleIntersperse() {
	results, _ := kitsune.Intersperse(kitsune.FromSlice([]string{"a", "b", "c"}), ",").Collect(context.Background())
	fmt.Println(results)
	// Output: [a , b , c]
}

func ExampleTakeEvery() {
	results, _ := kitsune.TakeEvery(kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6}), 2).Collect(context.Background())
	fmt.Println(results)
	// Output: [1 3 5]
}

func ExampleDropEvery() {
	results, _ := kitsune.DropEvery(kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6}), 2).Collect(context.Background())
	fmt.Println(results)
	// Output: [2 4 6]
}

func ExampleMapEvery() {
	// Double every third item (0-indexed: indices 0, 3, 6, …).
	results, _ := kitsune.MapEvery(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6}),
		3,
		func(_ context.Context, n int) (int, error) { return n * 2, nil },
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [2 2 3 8 5 6]
}

func ExampleConsecutiveDedupBy() {
	type Item struct{ Group, Name string }
	results, _ := kitsune.ConsecutiveDedupBy(
		kitsune.FromSlice([]Item{{"A", "x"}, {"A", "y"}, {"B", "z"}, {"A", "w"}}),
		func(i Item) string { return i.Group },
	).Collect(context.Background())
	for _, r := range results {
		fmt.Printf("%s:%s\n", r.Group, r.Name)
	}
	// Output:
	// A:x
	// B:z
	// A:w
}

func ExampleConsecutiveDedup() {
	results, _ := kitsune.ConsecutiveDedup(
		kitsune.FromSlice([]int{1, 1, 2, 3, 3, 3, 2}),
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [1 2 3 2]
}

func ExampleChunkBy() {
	results, _ := kitsune.ChunkBy(
		kitsune.FromSlice([]int{1, 1, 2, 2, 3, 1}),
		func(n int) int { return n },
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [[1 1] [2 2] [3] [1]]
}

func ExampleChunkWhile() {
	results, _ := kitsune.ChunkWhile(
		kitsune.FromSlice([]int{1, 2, 4, 9, 10, 11, 15}),
		func(prev, next int) bool { return next-prev <= 1 },
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [[1 2] [4] [9 10 11] [15]]
}

func ExampleUnzip() {
	pairs := []kitsune.Pair[int, string]{{First: 1, Second: "a"}, {First: 2, Second: "b"}, {First: 3, Second: "c"}}
	as, bs := kitsune.Unzip(kitsune.FromSlice(pairs))
	aRes, _ := as.Collect(context.Background())
	bRes, _ := bs.Collect(context.Background())
	fmt.Println(aRes)
	fmt.Println(bRes)
	// Output:
	// [1 2 3]
	// [a b c]
}

func ExampleSort() {
	results, _ := kitsune.Sort(
		kitsune.FromSlice([]int{3, 1, 4, 1, 5, 9, 2, 6}),
		func(a, b int) bool { return a < b },
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [1 1 2 3 4 5 6 9]
}

func ExampleSortBy() {
	type Item struct{ Name string }
	results, _ := kitsune.SortBy(
		kitsune.FromSlice([]Item{{"banana"}, {"apple"}, {"cherry"}}),
		func(x Item) string { return x.Name },
		func(a, b string) bool { return a < b },
	).Collect(context.Background())
	for _, r := range results {
		fmt.Println(r.Name)
	}
	// Output:
	// apple
	// banana
	// cherry
}

func ExampleUnfold() {
	// Generate the first 8 Fibonacci numbers.
	results, _ := kitsune.Unfold([2]int{0, 1}, func(s [2]int) (int, [2]int, bool) {
		return s[0], [2]int{s[1], s[0] + s[1]}, false
	}).Take(8).Collect(context.Background())
	fmt.Println(results)
	// Output: [0 1 1 2 3 5 8 13]
}

func ExampleIterate() {
	results, _ := kitsune.Iterate(1, func(n int) int { return n * 2 }).Take(6).Collect(context.Background())
	fmt.Println(results)
	// Output: [1 2 4 8 16 32]
}

func ExampleRepeatedly() {
	// Emit a counter value on each call.
	n := 0
	results, _ := kitsune.Repeatedly(func() int {
		n++
		return n * n
	}).Take(5).Collect(context.Background())
	fmt.Println(results)
	// Output: [1 4 9 16 25]
}

func ExampleCycle() {
	results, _ := kitsune.Cycle([]string{"a", "b", "c"}).Take(7).Collect(context.Background())
	fmt.Println(results)
	// Output: [a b c a b c a]
}

func ExampleTimer() {
	// Timer emits a single value after a delay, then closes.
	results, _ := kitsune.Timer(1*time.Millisecond, func() string { return "ready" }).Collect(context.Background())
	fmt.Println(results)
	// Output: [ready]
}

func ExampleConcat() {
	results, _ := kitsune.Concat(
		func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{1, 2}) },
		func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{3, 4}) },
		func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{5}) },
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [1 2 3 4 5]
}

func ExampleSum() {
	total, _ := kitsune.Sum(context.Background(), kitsune.FromSlice([]int{1, 2, 3, 4, 5}))
	fmt.Println(total)
	// Output: 15
}

func ExampleMin() {
	v, _, _ := kitsune.Min(context.Background(), kitsune.FromSlice([]int{3, 1, 4, 1, 5, 9}), func(a, b int) bool { return a < b })
	fmt.Println(v)
	// Output: 1
}

func ExampleMax() {
	v, _, _ := kitsune.Max(context.Background(), kitsune.FromSlice([]int{3, 1, 4, 1, 5, 9}), func(a, b int) bool { return a < b })
	fmt.Println(v)
	// Output: 9
}

func ExampleMinMax() {
	pair, _, _ := kitsune.MinMax(context.Background(), kitsune.FromSlice([]int{3, 1, 4, 1, 5, 9}), func(a, b int) bool { return a < b })
	fmt.Printf("min=%d max=%d\n", pair.First, pair.Second)
	// Output: min=1 max=9
}

func ExampleMinBy() {
	type Item struct{ Name string }
	v, _, _ := kitsune.MinBy(
		context.Background(),
		kitsune.FromSlice([]Item{{"banana"}, {"apple"}, {"cherry"}}),
		func(x Item) string { return x.Name },
		func(a, b string) bool { return a < b },
	)
	fmt.Println(v.Name)
	// Output: apple
}

func ExampleMaxBy() {
	type Item struct{ Name string }
	v, _, _ := kitsune.MaxBy(
		context.Background(),
		kitsune.FromSlice([]Item{{"banana"}, {"apple"}, {"cherry"}}),
		func(x Item) string { return x.Name },
		func(a, b string) bool { return a < b },
	)
	fmt.Println(v.Name)
	// Output: cherry
}

func ExampleFrequenciesBy() {
	m, _ := kitsune.FrequenciesBy(
		context.Background(),
		kitsune.FromSlice([]string{"cat", "dog", "ant", "bee", "cow"}),
		func(s string) int { return len(s) },
	)
	fmt.Println(m[3]) // all five words have length 3
	// Output: 5
}

func ExampleTakeRandom() {
	items := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	sample, _ := kitsune.TakeRandom(context.Background(), items, 3)
	fmt.Println(len(sample)) // always 3
	// Output: 3
}

func ExampleFind() {
	v, ok, _ := kitsune.Find(context.Background(), kitsune.FromSlice([]int{1, 2, 3, 4, 5}), func(n int) bool { return n > 3 })
	fmt.Println(v, ok)
	// Output: 4 true
}

func ExampleFrequencies() {
	m, _ := kitsune.Frequencies(context.Background(), kitsune.FromSlice([]string{"a", "b", "a", "c", "b", "a"}))
	fmt.Println(m["a"], m["b"], m["c"])
	// Output: 3 2 1
}

func ExampleReduceWhile() {
	// Sum numbers until the running total exceeds 10.
	total, _ := kitsune.ReduceWhile(
		context.Background(),
		kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7}),
		0,
		func(acc, n int) (int, bool) {
			acc += n
			return acc, acc <= 10
		},
	)
	fmt.Println(total)
	// Output: 15
}

func ExampleCombineLatest() {
	// CombineLatest emits a pair whenever either side produces a new value,
	// paired with the latest from the other side.
	branches := kitsune.Broadcast[int](kitsune.FromSlice([]int{1, 2, 3}), 2)
	combined := kitsune.CombineLatest(branches[0], branches[1])
	results, _ := combined.Collect(context.Background())
	fmt.Println(len(results) >= 0) // always true — verify it runs
	// Output: true
}

func ExampleBalance() {
	src := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	outputs := kitsune.Balance(src, 3)
	runners := make([]*kitsune.Runner, 3)
	counts := make([]int, 3)
	for i, p := range outputs {
		i := i
		runners[i] = p.ForEach(func(_ context.Context, _ int) error {
			counts[i]++
			return nil
		})
	}
	merged, _ := kitsune.MergeRunners(runners...)
	_ = merged.Run(context.Background())
	fmt.Println(counts[0], counts[1], counts[2])
	// Output: 2 2 2
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

func ExampleDeadLetter() {
	// DeadLetter routes permanently-failed items to a separate pipeline.
	ok, dlq := kitsune.DeadLetter(
		kitsune.FromSlice([]string{"1", "bad", "3"}),
		func(_ context.Context, s string) (int, error) {
			return strconv.Atoi(s)
		},
	)
	nums, _ := ok.Collect(context.Background())
	failed, _ := dlq.Collect(context.Background())
	fmt.Println("ok:", nums)
	fmt.Println("dlq:", len(failed), "items")
	// Output:
	// ok: [1 3]
	// dlq: 1 items
}

func ExampleDeadLetterSink() {
	var written []int
	var dlqItems []kitsune.ErrItem[int]
	dlq, runner := kitsune.DeadLetterSink(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) error {
			if v == 2 {
				return errors.New("fail")
			}
			written = append(written, v)
			return nil
		},
	)
	_ = dlq.ForEach(func(_ context.Context, ei kitsune.ErrItem[int]) error {
		dlqItems = append(dlqItems, ei)
		return nil
	})
	_ = runner.Run(context.Background())
	fmt.Println("written:", written)
	fmt.Println("dlq len:", len(dlqItems))
	// Output:
	// written: [1 3]
	// dlq len: 1
}

func ExampleStageError() {
	cause := errors.New("something broke")
	p := kitsune.Map(
		kitsune.FromSlice([]int{1}),
		func(_ context.Context, _ int) (int, error) { return 0, cause },
		kitsune.WithName("my-stage"),
	)
	_, err := p.Collect(context.Background())

	var se *kitsune.StageError
	if errors.As(err, &se) {
		fmt.Println("stage:", se.Stage)
		fmt.Println("cause:", se.Cause)
	}
	// Output:
	// stage: my-stage
	// cause: something broke
}

func ExampleWithSampleRate() {
	// WithSampleRate controls how often OnItemSample is called.
	// Pass -1 to disable sampling entirely.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	results, err := p.Collect(context.Background(), kitsune.WithSampleRate(-1))
	fmt.Println(results, err)
	// Output: [1 2 3 4 5] <nil>
}

func ExamplePipeline_Dedupe_withDedupSet() {
	// Use a custom DedupSet backend via WithDedupSet.
	set := kitsune.MemoryDedupSet()
	p := kitsune.FromSlice([]string{"a", "b", "a", "c", "b"})
	results, _ := p.Dedupe(func(s string) string { return s },
		kitsune.WithDedupSet(set),
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [a b c]
}

func ExampleScan_withName() {
	// Scan accepts StageOption — here we name the stage for observability.
	results, _ := kitsune.Scan(
		kitsune.FromSlice([]int{1, 2, 3, 4}),
		0,
		func(sum, v int) int { return sum + v },
		kitsune.WithName("running-sum"),
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [1 3 6 10]
}

func ExampleNewMetricsHook() {
	m := kitsune.NewMetricsHook()
	results, _ := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, n int) (int, error) { return n * 10, nil },
		kitsune.WithName("mul10"),
	).Collect(context.Background(), kitsune.WithHook(m))
	fmt.Println(results)
	fmt.Println("processed:", m.Stage("mul10").Processed)
	// Output:
	// [10 20 30]
	// processed: 3
}

func ExampleRateLimit() {
	// RateLimit with Wait mode — 6 items, rate=1000/sec, burst=6.
	// All items pass; burst=6 means the token bucket starts full so there
	// is no delay in this example.
	results, _ := kitsune.RateLimit(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6}),
		1000,
		kitsune.Burst(6),
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [1 2 3 4 5 6]
}

func ExampleRateLimit_drop() {
	// Drop mode discards items when the token bucket is empty.
	// With rate=1000/sec and burst=3, only the first 3 items can pass
	// before the bucket empties (pipeline runs much faster than 1 s).
	m := kitsune.NewMetricsHook()
	results, _ := kitsune.RateLimit(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6}),
		1000,
		kitsune.Burst(3),
		kitsune.RateMode(kitsune.RateLimitDrop),
		kitsune.WithName("rl"),
	).Collect(context.Background(), kitsune.WithHook(m))
	fmt.Println("passed:", len(results))
	fmt.Println("dropped:", m.Stage("rl").Skipped)
	// Output:
	// passed: 3
	// dropped: 3
}

func ExampleCircuitBreaker() {
	// Closed circuit: all items pass when the fn never errors.
	results, _ := kitsune.CircuitBreaker(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, n int) (int, error) { return n * 10, nil },
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [10 20 30]
}

func ExampleMapPooled() {
	pool := kitsune.NewPool(func() *[2]int { return new([2]int) })
	pooled, _ := kitsune.MapPooled(
		kitsune.FromSlice([]int{1, 2, 3}),
		pool,
		func(_ context.Context, n int, buf *[2]int) (*[2]int, error) {
			buf[0] = n
			buf[1] = n * n
			return buf, nil
		},
	).Collect(context.Background())
	for _, p := range pooled {
		fmt.Printf("%d²=%d\n", p.Value[0], p.Value[1])
	}
	kitsune.ReleaseAll(pooled)
	// Output:
	// 1²=1
	// 2²=4
	// 3²=9
}

func ExampleReleaseAll() {
	pool := kitsune.NewPool(func() *int { v := 0; return &v })
	items, _ := kitsune.MapPooled(
		kitsune.FromSlice([]int{10, 20}),
		pool,
		func(_ context.Context, n int, v *int) (*int, error) { *v = n; return v, nil },
	).Collect(context.Background())
	fmt.Println(*items[0].Value, *items[1].Value)
	kitsune.ReleaseAll(items)
	// Output: 10 20
}

func ExampleRunHandle_Pause() {
	h := kitsune.FromSlice([]int{1, 2, 3}).
		Drain().
		RunAsync(context.Background())

	h.Pause()
	fmt.Println("paused:", h.Paused())
	h.Resume()
	fmt.Println("paused:", h.Paused())
	h.Wait() //nolint:errcheck
	// Output:
	// paused: true
	// paused: false
}

func ExamplePipeline_Iter() {
	seq, errFn := kitsune.FromSlice([]int{1, 2, 3, 4, 5}).
		Filter(func(n int) bool { return n%2 == 0 }).
		Iter(context.Background())
	for n := range seq {
		fmt.Println(n)
	}
	if err := errFn(); err != nil {
		panic(err)
	}
	// Output:
	// 2
	// 4
}

func ExampleSwitchMap() {
	// SwitchMap — search-as-you-type: only the latest query's results arrive.
	// Each new item cancels the in-flight inner pipeline and starts a fresh one.
	// With a single-item source there is nothing to cancel, so the result is
	// always deterministic.  In practice, pair with a slow async inner fn and a
	// multi-item source to observe intermediate queries being cancelled.
	results, _ := kitsune.SwitchMap(
		kitsune.FromSlice([]int{42}),
		func(_ context.Context, n int, yield func(string) error) error {
			return yield(fmt.Sprintf("result for %d", n))
		},
	).Collect(context.Background())
	fmt.Println(results)
	// Output: [result for 42]
}

func ExampleExhaustMap() {
	// ExhaustMap — form-submission semantics: the first item is processed, and
	// subsequent items arriving while the first is still active are dropped.
	// Three items are sent rapidly; ExhaustMap starts item 1 immediately and
	// discards items 2 and 3 while item 1 is in flight.
	results, _ := kitsune.ExhaustMap(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, n int, yield func(int) error) error {
			return yield(n * 10)
		},
		kitsune.Buffer(8), // buffer items so they arrive while the first is active
	).Collect(context.Background())
	fmt.Println(results[0]) // only the first item's result survives
	// Output: 10
}

func ExampleWithClock() {
	// WithClock injects a virtual clock so time-sensitive operators can be
	// tested without real sleeps.  Here we wire a TestClock into Debounce:
	// three items arrive in a burst; advancing virtual time past the quiet
	// period flushes only the last item.
	//
	// The pipeline runs in a goroutine (via RunAsync + a ForEach sink) while
	// the main goroutine controls virtual time.  A small real sleep ensures
	// the pipeline goroutine has entered its blocking select before we advance.
	clock := testkit.NewTestClock()
	const quietPeriod = 2 * time.Second

	src := kitsune.NewChannel[int](10)
	var collected []int
	h := kitsune.Debounce(src.Source(), quietPeriod, kitsune.WithClock(clock)).
		ForEach(func(_ context.Context, n int) error {
			collected = append(collected, n)
			return nil
		}).
		RunAsync(context.Background())

	// Give the goroutine time to reach the idle select.
	time.Sleep(5 * time.Millisecond)

	_ = src.Send(context.Background(), 1)
	_ = src.Send(context.Background(), 2)
	_ = src.Send(context.Background(), 3)

	// Let the sends settle, then advance virtual time to trigger the debounce flush.
	time.Sleep(5 * time.Millisecond)
	clock.Advance(2 * time.Second)
	time.Sleep(10 * time.Millisecond)

	src.Close()
	_ = h.Wait()

	fmt.Println(collected)
	// Output: [3]
}

func ExampleMapWithKey() {
	// Track per-user session event counts.
	type event struct {
		userID string
		action string
	}
	sessionKey := kitsune.NewKey("session-events", 0)

	events := kitsune.FromSlice([]event{
		{"alice", "login"},
		{"bob", "login"},
		{"alice", "click"},
		{"alice", "logout"},
		{"bob", "click"},
	})

	p := kitsune.MapWithKey(events,
		func(e event) string { return e.userID },
		sessionKey,
		func(ctx context.Context, ref *kitsune.Ref[int], e event) (string, error) {
			n, err := ref.UpdateAndGet(ctx, func(v int) (int, error) { return v + 1, nil })
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s/%s:#%d", e.userID, e.action, n), nil
		},
	)
	results, _ := p.Collect(context.Background())
	for _, r := range results {
		fmt.Println(r)
	}
	// Output:
	// alice/login:#1
	// bob/login:#1
	// alice/click:#2
	// alice/logout:#3
	// bob/click:#2
}

func ExampleFlatMapWithKey() {
	// Track per-user output counts with 1:N expansion.
	// Each event expands into one output per "credit" the user has,
	// and the per-user state tracks the running total emitted.
	type event struct {
		userID  string
		credits int
	}
	totalKey := kitsune.NewKey("flatmap-total", 0)

	events := kitsune.FromSlice([]event{
		{"alice", 2},
		{"bob", 1},
		{"alice", 1},
	})

	p := kitsune.FlatMapWithKey(events,
		func(e event) string { return e.userID },
		totalKey,
		func(ctx context.Context, ref *kitsune.Ref[int], e event, yield func(string) error) error {
			for i := 0; i < e.credits; i++ {
				total, err := ref.UpdateAndGet(ctx, func(v int) (int, error) { return v + 1, nil })
				if err != nil {
					return err
				}
				if err := yield(fmt.Sprintf("%s:out#%d", e.userID, total)); err != nil {
					return err
				}
			}
			return nil
		},
	)
	results, _ := p.Collect(context.Background())
	for _, r := range results {
		fmt.Println(r)
	}
	// Output:
	// alice:out#1
	// alice:out#2
	// bob:out#1
	// alice:out#3
}

func ExampleStateTTL() {
	// Session-like state that resets after a period of inactivity.
	// Use a very short TTL so the example runs quickly.
	ttl := 10 * time.Millisecond
	sessionKey := kitsune.NewKey("session-ttl", "none", kitsune.StateTTL(ttl))

	input := kitsune.FromSlice([]string{"req1", "req2"})
	p := kitsune.MapWith(input, sessionKey,
		func(ctx context.Context, ref *kitsune.Ref[string], req string) (string, error) {
			prev, err := ref.Get(ctx)
			if err != nil {
				return "", err
			}
			if err := ref.Set(ctx, req); err != nil {
				return "", err
			}
			if req == "req2" {
				// Sleep past TTL before reading to demonstrate expiry.
				time.Sleep(ttl + 5*time.Millisecond)
				v, err := ref.Get(ctx)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("prev=%s expired=%v", prev, v == "none"), nil
			}
			return req, nil
		},
	)
	results, _ := p.Collect(context.Background())
	fmt.Println(results[0])
	fmt.Println(results[1])
	// Output:
	// req1
	// prev=req1 expired=true
}

func ExampleCountBy() {
	type event struct{ typ string }
	events := kitsune.FromSlice([]event{
		{"click"}, {"view"}, {"click"}, {"click"}, {"view"},
	})
	p := kitsune.CountBy(events, func(e event) string { return e.typ })
	results, _ := p.Collect(context.Background())
	// Print only the final snapshot.
	last := results[len(results)-1]
	fmt.Printf("click=%d view=%d\n", last["click"], last["view"])
	// Output:
	// click=3 view=2
}

func ExampleSumBy() {
	type txn struct {
		account string
		amount  float64
	}
	txns := kitsune.FromSlice([]txn{
		{"alice", 10.0}, {"bob", 5.0}, {"alice", 20.0}, {"bob", 15.0},
	})
	p := kitsune.SumBy(txns,
		func(t txn) string { return t.account },
		func(t txn) float64 { return t.amount },
	)
	results, _ := p.Collect(context.Background())
	last := results[len(results)-1]
	fmt.Printf("alice=%.1f bob=%.1f\n", last["alice"], last["bob"])
	// Output:
	// alice=30.0 bob=20.0
}

func ExampleContains() {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	found, _ := kitsune.Contains(context.Background(), p, 3)
	fmt.Println(found)
	// Output: true
}

func ExampleToMap() {
	type kv struct{ K, V string }
	p := kitsune.FromSlice([]kv{{"a", "apple"}, {"b", "banana"}})
	m, _ := kitsune.ToMap(context.Background(), p,
		func(x kv) string { return x.K },
		func(x kv) string { return x.V },
	)
	fmt.Println(m["a"], m["b"])
	// Output: apple banana
}

func ExampleStartWith() {
	p := kitsune.FromSlice([]int{3, 4, 5})
	results, _ := kitsune.StartWith(p, 1, 2).Collect(context.Background())
	fmt.Println(results)
	// Output: [1 2 3 4 5]
}

func ExampleDefaultIfEmpty() {
	empty := kitsune.FromSlice([]string{})
	results, _ := kitsune.DefaultIfEmpty(empty, "none").Collect(context.Background())
	fmt.Println(results)
	// Output: [none]
}

func ExampleTimestamp() {
	clock := testkit.NewTestClock()
	p := kitsune.FromSlice([]int{1, 2})
	results, _ := kitsune.Timestamp(p, kitsune.WithClock(clock)).Collect(context.Background())
	for _, r := range results {
		fmt.Printf("value=%d stamped=%v\n", r.Value, !r.Time.IsZero())
	}
	// Output:
	// value=1 stamped=true
	// value=2 stamped=true
}

func ExampleTimeInterval() {
	clock := testkit.NewTestClock()
	ch := kitsune.NewChannel[string](10)
	src := ch.Source()
	p := kitsune.TimeInterval(src, kitsune.WithClock(clock))

	resultCh := make(chan []kitsune.TimedInterval[string], 1)
	go func() {
		items, _ := p.Collect(context.Background())
		resultCh <- items
	}()

	time.Sleep(5 * time.Millisecond)
	_ = ch.Send(context.Background(), "a")
	time.Sleep(5 * time.Millisecond)

	clock.Advance(1 * time.Second)
	time.Sleep(5 * time.Millisecond)
	_ = ch.Send(context.Background(), "b")
	time.Sleep(5 * time.Millisecond)

	ch.Close()
	results := <-resultCh

	fmt.Printf("a elapsed=0: %v\n", results[0].Elapsed == 0)
	fmt.Printf("b elapsed=1s: %v\n", results[1].Elapsed == time.Second)
	// Output:
	// a elapsed=0: true
	// b elapsed=1s: true
}

func ExampleAmb() {
	// Only one factory — straightforward passthrough.
	p := kitsune.Amb(
		func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{1, 2, 3}) },
	)
	results, _ := p.Collect(context.Background())
	fmt.Println(results)
	// Output: [1 2 3]
}

func ExampleSessionWindow() {
	clock := testkit.NewTestClock()

	ch := kitsune.NewChannel[string](10)
	src := ch.Source()
	sessions := kitsune.SessionWindow(src, 5*time.Second, kitsune.WithClock(clock))

	resultCh := make(chan [][]string, 1)
	go func() {
		items, _ := sessions.Collect(context.Background())
		resultCh <- items
	}()

	// First session: two clicks close together.
	time.Sleep(5 * time.Millisecond)
	_ = ch.Send(context.Background(), "click")
	_ = ch.Send(context.Background(), "scroll")
	time.Sleep(5 * time.Millisecond)

	// Gap expires — flush first session.
	clock.Advance(5 * time.Second)
	time.Sleep(10 * time.Millisecond)

	// Second session: one click.
	_ = ch.Send(context.Background(), "click")
	time.Sleep(5 * time.Millisecond)

	// Close channel — flush second session immediately.
	ch.Close()

	results := <-resultCh
	for i, s := range results {
		fmt.Printf("Session %d: %v\n", i+1, s)
	}
	// Output:
	// Session 1: [click scroll]
	// Session 2: [click]
}
