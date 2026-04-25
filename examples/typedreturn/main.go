// Example: typedreturn: compile-time-safe error fallback with TypedReturn.
//
// This example contrasts Return and TypedReturn side-by-side:
//
//   - Return(val): ErrorHandler. The fallback type T is inferred from val but is
//     NOT checked against the stage's output type at compile time. A mismatch
//     silently falls back to Halt at runtime; no error, no fallback emitted.
//
//   - TypedReturn[O](val): StageOption. O is the stage's output type and is
//     checked at the call site. A mismatch is a compile-time error.
//
// Demonstrates:
//   - Return with a correct type: works as expected
//   - Return with a mismatched type: compiles, but silently halts at runtime
//   - TypedReturn: guaranteed correct at compile time
package main

import (
	"context"
	"errors"
	"fmt"

	kitsune "github.com/zenbaku/go-kitsune"
)

var errLookupFailed = errors.New("lookup failed")

// lookup simulates a remote call: even IDs succeed, odd IDs fail.
func lookup(_ context.Context, id int) (string, error) {
	if id%2 != 0 {
		return "", fmt.Errorf("id %d: %w", id, errLookupFailed)
	}
	return fmt.Sprintf("user-%d", id), nil
}

func main() {
	ids := []int{1, 2, 3, 4, 5}

	// -------------------------------------------------------------------------
	// 1. Return with a correct type: works as expected.
	//    Return("unknown") infers T=string. The stage output is string. Match.
	// -------------------------------------------------------------------------
	fmt.Println("=== Return with correct type ===")
	p1 := kitsune.Map(kitsune.FromSlice(ids), lookup,
		kitsune.OnError(kitsune.Return("unknown")),
	)
	results1, err := collect(p1)
	fmt.Printf("  items: %v\n  err:   %v\n\n", results1, err)
	// Output: [unknown user-2 unknown user-4 unknown]

	// -------------------------------------------------------------------------
	// 2. Return with a mismatched type: compiles fine, but silently halts.
	//    Return(0) infers T=int. The stage output is string. Mismatch.
	//    The val.(string) assertion in ProcessItem fails; the original error
	//    propagates as though Halt had been used.
	// -------------------------------------------------------------------------
	fmt.Println("=== Return with MISMATCHED type (silent Halt) ===")
	p2 := kitsune.Map(kitsune.FromSlice(ids), lookup,
		kitsune.OnError(kitsune.Return(0)), // int != string; compiles! but fails at runtime
	)
	results2, err := collect(p2)
	fmt.Printf("  items: %v\n  err:   %v\n\n", results2, err)
	// Output: items=[] err=<original error>

	// -------------------------------------------------------------------------
	// 3. TypedReturn: O=string is checked at the call site.
	//    TypedReturn[string]("unknown"): the type is locked at compile time.
	//    Trying TypedReturn[int](0) on a Map[int, string] would not compile.
	// -------------------------------------------------------------------------
	fmt.Println("=== TypedReturn[string] (compile-time safe) ===")
	p3 := kitsune.Map(kitsune.FromSlice(ids), lookup,
		kitsune.TypedReturn[string]("unknown"),
	)
	results3, err := collect(p3)
	fmt.Printf("  items: %v\n  err:   %v\n\n", results3, err)
	// Output: [unknown user-2 unknown user-4 unknown]

	// -------------------------------------------------------------------------
	// Uncomment to see the compile-time error TypedReturn provides:
	//
	//   kitsune.Map(kitsune.FromSlice(ids), lookup,
	//       kitsune.TypedReturn[int](0), // cannot use int as string
	//   )
	// -------------------------------------------------------------------------
}

func collect(p *kitsune.Pipeline[string]) ([]string, error) {
	ctx := context.Background()
	var out []string
	_, err := p.ForEach(func(_ context.Context, s string) error {
		out = append(out, s)
		return nil
	}).Run(ctx)
	return out, err
}
