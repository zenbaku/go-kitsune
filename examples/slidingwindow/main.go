// Example: slidingwindow — overlapping and tumbling count-based windows.
//
// Demonstrates: SlidingWindow, Map, Collect.
package main

import (
	"context"
	"fmt"
	"math"

	kitsune "github.com/zenbaku/go-kitsune"
)

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return math.Round(sum/float64(len(xs))*100) / 100
}

func main() {
	// --- Overlapping windows: moving average ---
	//
	// SlidingWindow(p, size=3, step=1) produces one window per item after
	// the buffer fills. Each window overlaps the previous by size-step items.
	// Classic use: smooth a noisy signal with a rolling average.
	fmt.Println("=== SlidingWindow(size=3, step=1): rolling average ===")

	readings := []float64{10, 12, 11, 15, 14, 16, 13, 17}
	avgs, err := kitsune.Map(
		kitsune.SlidingWindow(kitsune.FromSlice(readings), 3, 1),
		func(_ context.Context, w []float64) (float64, error) {
			return mean(w), nil
		},
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}

	fmt.Printf("Raw:     %v\n", readings)
	fmt.Printf("Avg(3):  %v\n", avgs)

	// --- Tumbling windows: non-overlapping batches ---
	//
	// SlidingWindow(p, size=3, step=3) is equivalent to Batch(p, 3) but
	// expressed as a sliding window. Each item appears in exactly one window.
	fmt.Println("\n=== SlidingWindow(size=3, step=3): tumbling batches ===")

	items := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9})
	batches, err := kitsune.SlidingWindow(items, 3, 3).Collect(context.Background())
	if err != nil {
		panic(err)
	}

	fmt.Printf("Input:   [1..9]\n")
	for i, b := range batches {
		fmt.Printf("Batch %d: %v\n", i, b)
	}

	// --- Larger step: advance by 2, window size 3 ---
	//
	// step=2 means consecutive windows share exactly one overlapping item.
	// Good for detecting trends with minimal redundancy.
	fmt.Println("\n=== SlidingWindow(size=3, step=2): every other window ===")

	stream := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9})
	sampled, err := kitsune.SlidingWindow(stream, 3, 2).Collect(context.Background())
	if err != nil {
		panic(err)
	}

	for i, w := range sampled {
		fmt.Printf("  window %d: %v\n", i, w)
	}

	// --- Short stream: stream shorter than window size → no output ---
	fmt.Println("\n=== Short stream (length < size): zero windows emitted ===")

	empty, err := kitsune.SlidingWindow(kitsune.FromSlice([]int{1, 2}), 5, 1).
		Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("Windows from a 2-item stream with size=5: %d\n", len(empty))
}
