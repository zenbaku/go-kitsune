// Example: stages — composable, reusable pipeline transformers.
//
// Demonstrates:
//   - Stage[I,O] — a named pipeline transformer type
//   - Then — compose two stages into one
//   - Through — apply a Stage[T,T] to a pipeline
//   - Or — try a primary stage; fall back to a secondary on failure
package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// Named stage variables — define once, reuse anywhere
// ---------------------------------------------------------------------------

// ParseInt converts a decimal string to int.
var ParseInt kitsune.Stage[string, int] = func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[int] {
	return kitsune.Map(p, func(_ context.Context, s string) (int, error) {
		return strconv.Atoi(strings.TrimSpace(s))
	})
}

// Double multiplies each integer by 2.
var Double kitsune.Stage[int, int] = func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
	return kitsune.Map(p, func(_ context.Context, n int) (int, error) { return n * 2, nil })
}

// Stringify formats each int as "result=N".
var Stringify kitsune.Stage[int, string] = func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[string] {
	return kitsune.Map(p, func(_ context.Context, n int) (string, error) {
		return fmt.Sprintf("result=%d", n), nil
	})
}

// Uppercase is a Stage[T,T] — directly usable with Through.
var Uppercase kitsune.Stage[string, string] = func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[string] {
	return kitsune.Map(p, func(_ context.Context, s string) (string, error) {
		return strings.ToUpper(s), nil
	})
}

// ---------------------------------------------------------------------------
// Generic middleware — works at any type
// ---------------------------------------------------------------------------

func Logged[T any](label string) kitsune.Stage[T, T] {
	return func(p *kitsune.Pipeline[T]) *kitsune.Pipeline[T] {
		return kitsune.Tap(p, func(_ context.Context, v T) error {
			fmt.Printf("  [%s] %v\n", label, v)
			return nil
		})
	}
}

func main() {
	ctx := context.Background()
	input := kitsune.FromSlice([]string{"1", "2", "3", "4", "5"})

	// --- 1. Apply a Stage directly ---

	fmt.Println("=== Stage applied directly ===")
	parsed, _ := kitsune.Collect(ctx, ParseInt(kitsune.FromSlice([]string{"10", "20", "30"})))
	fmt.Println("parsed:", parsed)

	// --- 2. Compose stages with Then ---

	fmt.Println("\n=== Then: ParseInt → Double → Stringify ===")
	pipeline := kitsune.Then(kitsune.Then(ParseInt, Double), Stringify)
	results, _ := kitsune.Collect(ctx, pipeline(input))
	fmt.Println(strings.Join(results, "  "))

	// --- 3. Through: apply a Stage[T,T] as a pipeline method ---

	fmt.Println("\n=== Through: uppercase then log ===")
	out, _ := kitsune.Collect(ctx,
		kitsune.FromSlice([]string{"hello", "world"}).
			Through(Uppercase).
			Through(Logged[string]("out")),
	)
	fmt.Println("result:", out)

	// --- 4. Apply a stage to an intermediate pipeline ---

	fmt.Println("\n=== Isolated stage test ===")
	doubled, _ := kitsune.Collect(ctx, Double(kitsune.FromSlice([]int{1, 2, 3})))
	fmt.Println("doubled:", doubled)

	// --- 5. Or: primary with fallback ---
	//
	// Stage.Or(fallback) returns a new Stage that runs each item through the
	// primary first. If the primary returns an error (or emits nothing), the
	// same item is passed to the fallback. Or never propagates the primary's
	// error — fallback is always called on failure.
	//
	// Note: Or spawns a sub-pipeline per item (via First), so it is not a
	// high-throughput primitive. Use it for low-volume control paths or
	// read-fallback patterns (DB → cache, primary API → secondary API).

	fmt.Println("\n=== Or: primary with fallback ===")

	// primary succeeds only for odd numbers.
	var primaryStage kitsune.Stage[int, string] = func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, n int) (string, error) {
			if n%2 == 0 {
				return "", fmt.Errorf("primary failed for %d", n)
			}
			return fmt.Sprintf("primary:%d", n), nil
		})
	}

	// fallback always succeeds.
	var fallbackStage kitsune.Stage[int, string] = func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, n int) (string, error) {
			return fmt.Sprintf("fallback:%d", n), nil
		})
	}

	withFallback := primaryStage.Or(fallbackStage)
	orResults, _ := kitsune.Collect(ctx, withFallback(kitsune.FromSlice([]int{1, 2, 3, 4, 5})))
	// Expected: primary:1  fallback:2  primary:3  fallback:4  primary:5
	fmt.Println(strings.Join(orResults, "  "))
}
