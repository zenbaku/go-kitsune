// Example: stages — composable, reusable processing functions.
//
// Demonstrates:
//   - Stage[I,O] — a named function type compatible with Map
//   - Then — compose two stages into one
//   - Through — apply a Stage[T,T] to a pipeline
package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	kitsune "github.com/zenbaku/go-kitsune/v2"
)

// ---------------------------------------------------------------------------
// Named stage variables — define once, reuse anywhere, test in isolation
// ---------------------------------------------------------------------------

// ParseInt converts a decimal string to int.
var ParseInt kitsune.Stage[string, int] = func(_ context.Context, s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}

// Double multiplies an integer by 2.
var Double kitsune.Stage[int, int] = func(_ context.Context, n int) (int, error) {
	return n * 2, nil
}

// Stringify formats an int as "result=N".
var Stringify kitsune.Stage[int, string] = func(_ context.Context, n int) (string, error) {
	return fmt.Sprintf("result=%d", n), nil
}

// Uppercase is a Stage[T,T] — directly usable with Through.
var Uppercase kitsune.Stage[string, string] = func(_ context.Context, s string) (string, error) {
	return strings.ToUpper(s), nil
}

// ---------------------------------------------------------------------------
// Generic middleware — works at any type
// ---------------------------------------------------------------------------

func Logged[T any](label string) kitsune.Stage[T, T] {
	return func(_ context.Context, v T) (T, error) {
		fmt.Printf("  [%s] %v\n", label, v)
		return v, nil
	}
}

func main() {
	ctx := context.Background()
	input := kitsune.FromSlice([]string{"1", "2", "3", "4", "5"})

	// --- 1. Use a Stage directly as a Map function ---

	fmt.Println("=== Map with Stage ===")
	parsed, _ := kitsune.Collect(ctx, kitsune.Map(kitsune.FromSlice([]string{"10", "20", "30"}), ParseInt))
	fmt.Println("parsed:", parsed)

	// --- 2. Compose stages with Then ---

	fmt.Println("\n=== Then: ParseInt → Double → Stringify ===")
	pipeline := kitsune.Then(kitsune.Then(ParseInt, Double), Stringify)
	results, _ := kitsune.Collect(ctx, kitsune.Map(input, pipeline))
	fmt.Println(strings.Join(results, "  "))

	// --- 3. Through: apply a Stage[T,T] as a pipeline method ---

	fmt.Println("\n=== Through: uppercase then log ===")
	out, _ := kitsune.Collect(ctx,
		kitsune.FromSlice([]string{"hello", "world"}).
			Through(Uppercase).
			Through(Logged[string]("out")),
	)
	fmt.Println("result:", out)

	// --- 4. Test a stage in isolation with FromSlice ---

	fmt.Println("\n=== Isolated stage test ===")
	doubled, _ := kitsune.Collect(ctx, kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}), Double))
	fmt.Println("doubled:", doubled)
}
