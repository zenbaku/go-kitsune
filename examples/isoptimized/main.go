// Example: IsOptimized — inspect fast-path and fusion eligibility
//
// IsOptimized reports whether each stage in a pipeline would run on the
// fast path (drain-protocol + micro-batching loop) and whether adjacent
// stages would be fused into a single goroutine.
//
// This is useful for asserting in tests that a performance-critical pipeline
// stays optimised as code evolves, and for diagnosing why a pipeline is
// slower than expected.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	kitsune "github.com/zenbaku/go-kitsune"
)

func parse(_ context.Context, s string) (int, error) { return len(s), nil }
func isShort(_ context.Context, n int) (bool, error) { return n < 10, nil }

func main() {
	fmt.Println("=== Default pipeline (expect: fast path + fused) ===")
	{
		src := kitsune.FromSlice([]string{"hello", "world", "kitsune"})
		mapped := kitsune.Map(src, parse)
		filtered := kitsune.Filter(mapped, isShort)
		_ = filtered.ForEach(func(_ context.Context, n int) error {
			fmt.Println(" item:", n)
			return nil
		})

		for _, r := range filtered.IsOptimized() {
			fmt.Printf("  %-12s kind=%-8s fastPath=%v fused=%v reasons=%v\n",
				r.Name, r.Kind, r.FastPath, r.Fused, r.Reasons)
		}
		fmt.Println()
	}

	fmt.Println("=== With WithHook (expect: fast path disabled) ===")
	{
		src := kitsune.FromSlice([]string{"hello", "world"})
		mapped := kitsune.Map(src, parse)
		_ = mapped.ForEach(func(_ context.Context, _ int) error { return nil })

		hook := kitsune.LogHook(slog.New(slog.NewTextHandler(os.Stderr, nil)))
		for _, r := range mapped.IsOptimized(kitsune.WithHook(hook)) {
			fmt.Printf("  %-12s kind=%-8s fastPath=%v reasons=%v\n",
				r.Name, r.Kind, r.FastPath, r.Reasons)
		}
		fmt.Println()
	}

	fmt.Println("=== With Concurrency(4) on Map (expect: fast path disabled) ===")
	{
		src := kitsune.FromSlice([]string{"hello", "world"})
		mapped := kitsune.Map(src, parse, kitsune.Concurrency(4))
		_ = mapped.ForEach(func(_ context.Context, _ int) error { return nil })

		for _, r := range mapped.IsOptimized() {
			fmt.Printf("  %-12s kind=%-8s fastPath=%v reasons=%v\n",
				r.Name, r.Kind, r.FastPath, r.Reasons)
		}
	}
}
