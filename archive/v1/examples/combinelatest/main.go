// Example: combinelatest — sensor fusion with CombineLatest.
//
// Demonstrates: CombineLatest, Broadcast, Map, NewChannel.
//
// CombineLatest is the symmetric counterpart to WithLatestFrom: whenever
// either stream emits, the output receives a pair of the latest values from
// both sides. No output is produced until both streams have emitted at least
// once. Use it when both streams are equally authoritative and any update from
// either side should trigger a recalculation.
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	// --- Scenario 1: Fuse temperature + humidity into a comfort index ---
	//
	// Both sensors emit independently. Whenever either sensor updates, the
	// latest pair is combined into a comfort score.
	fmt.Println("=== CombineLatest: sensor fusion (temperature + humidity) ===")

	type Reading struct {
		Kind  string
		Value float64
	}

	tempCh := kitsune.NewChannel[float64](8)
	humCh := kitsune.NewChannel[float64](8)

	ctx := context.Background()

	type Comfort struct {
		Temp     float64
		Humidity float64
		Score    float64
	}

	comfort := kitsune.Map(
		kitsune.CombineLatest(tempCh.Source(), humCh.Source()),
		func(_ context.Context, p kitsune.Pair[float64, float64]) (Comfort, error) {
			// Simplified comfort index: penalise high humidity.
			score := p.First - (p.Second-40)*0.1
			return Comfort{Temp: p.First, Humidity: p.Second, Score: score}, nil
		},
	)

	var readings []Comfort
	h := comfort.ForEach(func(_ context.Context, c Comfort) error {
		readings = append(readings, c)
		return nil
	}).RunAsync(ctx)

	// Temperature sensor updates.
	_ = tempCh.Send(ctx, 22.0)
	time.Sleep(20 * time.Millisecond) // let CombineLatest latch temp

	// First humidity reading — triggers output (both sides now have values).
	_ = humCh.Send(ctx, 45.0)
	time.Sleep(20 * time.Millisecond)

	// Temperature update — triggers another output.
	_ = tempCh.Send(ctx, 24.0)
	time.Sleep(20 * time.Millisecond)

	// Humidity update — triggers another output.
	_ = humCh.Send(ctx, 60.0)
	time.Sleep(20 * time.Millisecond)

	tempCh.Close()
	humCh.Close()

	if err := h.Wait(); err != nil {
		panic(err)
	}

	fmt.Printf("%-8s  %-10s  %s\n", "temp °C", "humidity %", "comfort")
	for _, r := range readings {
		fmt.Printf("%-8.1f  %-10.1f  %.2f\n", r.Temp, r.Humidity, r.Score)
	}

	// --- Scenario 2: Same-graph — Broadcast + CombineLatest ---
	//
	// When both pipelines share the same graph, CombineLatest uses the
	// engine-native node for zero-overhead wiring.
	fmt.Println("\n=== CombineLatest: same-graph (Broadcast) ===")

	prices := kitsune.FromSlice([]float64{100.0, 101.5, 99.8, 103.2})
	branches := kitsune.Broadcast(prices, 2)

	// One branch: raw price; other branch: smoothed (×0.9).
	smoothed := kitsune.Map(branches[1], func(_ context.Context, p float64) (float64, error) {
		return p * 0.9, nil
	})

	pairs, err := kitsune.CombineLatest(branches[0], smoothed).Collect(context.Background())
	if err != nil {
		panic(err)
	}

	fmt.Printf("%-8s  %s\n", "raw", "smoothed")
	for _, p := range pairs {
		fmt.Printf("%-8.2f  %.2f\n", p.First, p.Second)
	}
}
