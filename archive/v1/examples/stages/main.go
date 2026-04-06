// Example: stages — composable, reusable, independently testable pipeline stages.
//
// Demonstrates:
//   - Stage[I,O] and Stage.Apply for type-changing transformations
//   - Then for composing stages into longer pipelines
//   - Stage[T,T] compatibility with Through (type-preserving middleware)
//   - Generic middleware: a Stage factory function parameterised over T
//   - Parameterised stages: struct with an AsStage() method
//   - Swappable sources: same Stage, FromSlice in tests / Channel in production
package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// 1. Named stage variables — define once, use anywhere
// ---------------------------------------------------------------------------

// ParseStage converts raw strings to integers.
var ParseStage kitsune.Stage[string, int] = func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[int] {
	return kitsune.Map(p, kitsune.Lift(strconv.Atoi))
}

// DoubleStage multiplies each integer by 2.
var DoubleStage kitsune.Stage[int, int] = func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
	return kitsune.Map(p, func(_ context.Context, n int) (int, error) { return n * 2, nil })
}

// FormatStage converts integers to labelled strings.
var FormatStage kitsune.Stage[int, string] = func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[string] {
	return kitsune.Map(p, func(_ context.Context, n int) (string, error) {
		return fmt.Sprintf("result=%d", n), nil
	})
}

// FilterEvenStage is type-preserving — Stage[T,T] is directly compatible with Through.
var FilterEvenStage kitsune.Stage[int, int] = func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
	return p.Filter(func(n int) bool { return n%2 == 0 })
}

// ---------------------------------------------------------------------------
// 2. Generic middleware — a Stage factory parameterised over any type T
//
// WithLabel[T] works for any pipeline item type. The type parameter is
// inferred at the call site, so usage is concise.
// ---------------------------------------------------------------------------

func WithLabel[T any](label string) kitsune.Stage[T, T] {
	return func(p *kitsune.Pipeline[T]) *kitsune.Pipeline[T] {
		return p.Tap(func(item T) {
			fmt.Printf("  [%s] %v\n", label, item)
		})
	}
}

// ---------------------------------------------------------------------------
// 3. Parameterised stage — configuration lives in a struct; AsStage() returns
// the Stage value. Useful when a stage needs several options.
// ---------------------------------------------------------------------------

type PrefixStage struct {
	Prefix      string
	Concurrency int
}

func (s *PrefixStage) AsStage() kitsune.Stage[string, string] {
	return func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, v string) (string, error) {
			return s.Prefix + v, nil
		}, kitsune.Concurrency(s.Concurrency))
	}
}

// ---------------------------------------------------------------------------
// 4. Pipeline factory — build an entire multi-step Stage from config
// ---------------------------------------------------------------------------

func newProcessingPipeline(prefix string) kitsune.Stage[string, string] {
	parse := kitsune.Then(ParseStage, DoubleStage) // Stage[string, int]
	format := kitsune.Then(parse, FormatStage)     // Stage[string, string]
	label := (&PrefixStage{Prefix: prefix, Concurrency: 1}).AsStage()
	return kitsune.Then(format, label) // Stage[string, string]
}

func main() {
	ctx := context.Background()
	input := []string{"1", "2", "3", "4", "5"}

	// --- 1. Test each stage independently with FromSlice (no goroutines needed) ---

	fmt.Println("=== Isolated stage tests ===")
	parsed, _ := ParseStage.Apply(kitsune.FromSlice(input)).Collect(ctx)
	fmt.Println("parsed:", parsed)

	doubled, _ := DoubleStage.Apply(kitsune.FromSlice(parsed)).Collect(ctx)
	fmt.Println("doubled:", doubled)

	// --- 2. Compose with Then ---

	fmt.Println("\n=== Composed: Parse → Double → Format ===")
	full := kitsune.Then(kitsune.Then(ParseStage, DoubleStage), FormatStage)
	results, _ := full.Apply(kitsune.FromSlice(input)).Collect(ctx)
	fmt.Println(strings.Join(results, ", "))

	// --- 3. Stage[T,T] works directly with Through ---

	fmt.Println("\n=== FilterEvenStage via Through ===")
	evens, _ := kitsune.FromSlice(parsed).Through(FilterEvenStage).Collect(ctx)
	fmt.Println("evens:", evens)

	// --- 4. Generic middleware applied at two different types ---

	fmt.Println("\n=== Generic WithLabel middleware ===")
	withRawLog := kitsune.Then(WithLabel[string]("raw"), ParseStage)
	withNumLog := kitsune.Then(withRawLog, WithLabel[int]("parsed"))
	withNumLog.Apply(kitsune.FromSlice([]string{"10", "20"})).Collect(ctx) //nolint

	// --- 5. Parameterised stage via struct ---

	fmt.Println("\n=== PrefixStage (struct-based) ===")
	prefixed, _ := (&PrefixStage{Prefix: ">> ", Concurrency: 2}).
		AsStage().
		Apply(kitsune.FromSlice([]string{"hello", "world"})).
		Collect(ctx)
	fmt.Println(prefixed)

	// --- 6. Swappable source: same Stage, FromSlice in tests / Channel in production ---
	//
	// The pipeline logic is identical — only the source differs.
	// In tests: FromSlice gives deterministic, goroutine-free execution.
	// In production: NewChannel accepts pushes from HTTP handlers, event loops, etc.

	fmt.Println("\n=== Swappable source ===")
	pipeline := newProcessingPipeline("→ ")

	// Test source (deterministic):
	testResults, _ := pipeline.Apply(kitsune.FromSlice(input)).Collect(ctx)
	fmt.Println("test results:", testResults)

	// Production source (push-based):
	src := kitsune.NewChannel[string](16)
	h := pipeline.Apply(src.Source()).ForEach(func(_ context.Context, s string) error {
		fmt.Println("live:", s)
		return nil
	}).RunAsync(ctx)
	for _, v := range input {
		src.Send(ctx, v) //nolint
	}
	src.Close()
	h.Wait() //nolint

	// --- 7. Or: try primary, fall back on error ---
	//
	// Or returns a Stage that tries primary and, on any error, calls fallback
	// with the same item. Useful for cache-aside, service fallbacks, etc.

	fmt.Println("\n=== Or: primary / fallback ===")

	cachedValues := map[string]int{"a": 1, "b": 2}
	fetchFromCache := func(_ context.Context, key string) (int, error) {
		if v, ok := cachedValues[key]; ok {
			return v, nil
		}
		return 0, fmt.Errorf("cache miss: %s", key)
	}
	fetchFromDB := func(_ context.Context, key string) (int, error) {
		// Simulated DB: compute value from key length.
		return len(key) * 10, nil
	}

	lookupStage := kitsune.Or(fetchFromCache, fetchFromDB, kitsune.WithName("lookup"))
	lookupResults, _ := lookupStage.Apply(kitsune.FromSlice([]string{"a", "b", "c", "dd"})).
		Collect(ctx)
	fmt.Println("lookup results:", lookupResults)
}
