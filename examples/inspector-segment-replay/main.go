// Example: inspector-segment-replay: showcase the REPLAY badge on the dashboard.
//
// Excluded from TestExamples (interactive/manual). Run with:
//
//	go run ./examples/inspector-segment-replay
//
// The demo opens the inspector dashboard once, then runs the same pipeline
// twice through it: the first run captures a snapshot (segment runs live),
// the second run replays from the snapshot (segment is bypassed and renders
// as a single REPLAY-badged node inside the hull).
//
// Demonstrates: Segment, WithDevStore capture/replay, kind="segment-replay".
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/inspector"
)

func main() {
	storeDir := filepath.Join(os.TempDir(), "kitsune-inspector-demo")
	if err := os.RemoveAll(storeDir); err != nil {
		fmt.Fprintln(os.Stderr, "wipe snapshot dir:", err)
		os.Exit(1)
	}
	store := kitsune.NewFileDevStore(storeDir)

	insp := inspector.New()
	defer insp.Close()
	fmt.Println()
	fmt.Println("Inspector dashboard:", insp.URL())
	fmt.Println("Open the URL in a browser, then come back and follow the prompts.")
	fmt.Println()

	stdin := bufio.NewReader(os.Stdin)
	pause := func(prompt string) {
		fmt.Print(prompt)
		_, _ = stdin.ReadString('\n')
	}

	build := func() *kitsune.Pipeline[int] {
		src := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
		seg := kitsune.NewSegment("enrich", kitsune.Stage[int, int](
			func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
				added := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
					time.Sleep(500 * time.Millisecond) // visible latency on the dashboard
					return v + 100, nil
				}, kitsune.WithName("inner-add"))
				return kitsune.Map(added, func(_ context.Context, v int) (int, error) {
					return v * 10, nil
				}, kitsune.WithName("inner-mul"))
			}))
		return seg.Apply(src)
	}

	run := func(label string) {
		var got []int
		summary, err := build().ForEach(func(_ context.Context, v int) error {
			got = append(got, v)
			return nil
		}, kitsune.WithName("collect")).Run(context.Background(),
			kitsune.WithDevStore(store), kitsune.WithHook(insp))
		fmt.Printf("[%s] outcome=%s items=%v err=%v duration=%s\n",
			label, summary.Outcome, got, err, summary.Duration)
	}

	pause("Press ENTER once the dashboard is open in your browser. ")

	fmt.Println()
	fmt.Println("--- Run 1: CAPTURE (no snapshot; segment runs live) ---")
	fmt.Println("Watch the dashboard: the segment hull contains two stages (inner-add, inner-mul),")
	fmt.Println("each item takes ~500ms, items flow live, and there is no REPLAY badge.")
	fmt.Println()
	run("capture")
	pause("\nPress ENTER to run again with the snapshot now present (replay mode)... ")

	fmt.Println()
	fmt.Println("--- Run 2: REPLAY (snapshot exists; segment is bypassed) ---")
	fmt.Println("On the dashboard, the segment hull now contains a single node with a blue")
	fmt.Println("REPLAY badge in the corner; the Avg Latency cell shows an em-dash.")
	fmt.Println()
	run("replay")
	pause("\nPress ENTER to exit. ")
}
