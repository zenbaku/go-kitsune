// Example: basic — a minimal linear pipeline.
//
// Demonstrates: FromSlice, Map, Lift, ForEach, Run
package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	raw := []string{"  hello  ", "  world  ", "  kitsune  "}

	input := kitsune.FromSlice(raw)
	trimmed := kitsune.Map(input, kitsune.Lift(trim))
	upper := kitsune.Map(trimmed, kitsune.Lift(toUpper))

	err := upper.ForEach(func(_ context.Context, s string) error {
		fmt.Println(s)
		return nil
	}).Run(context.Background())

	if err != nil {
		panic(err)
	}

	// Collect variant — materialize results into a slice.
	numbers := kitsune.FromSlice([]string{"1", "2", "3", "4", "5"})
	parsed := kitsune.Map(numbers, kitsune.Lift(strconv.Atoi))
	results, err := parsed.Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println("Parsed:", results)
}

func trim(s string) (string, error)    { return strings.TrimSpace(s), nil }
func toUpper(s string) (string, error) { return strings.ToUpper(s), nil }
