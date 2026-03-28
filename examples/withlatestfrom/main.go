// Example: withlatestfrom — combine a primary stream with the latest value from a secondary.
//
// Demonstrates: WithLatestFrom, Partition, Map, MergeRunners, NewChannel.
//
// Both pipelines passed to WithLatestFrom must share the same pipeline graph.
// The idiomatic pattern is to use a single source (Channel or Generate), then
// split it with Partition so that config/state updates and primary events each
// travel their own branch of the same graph.
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
)

// --- Scenario 1: tag requests with the active config version ---
//
// Config updates and user requests share a single channel. Partition routes
// them to separate branches on the same graph. WithLatestFrom pairs every
// request with whatever config was most recently seen. Requests that arrive
// before the first config update are dropped (no secondary value yet).

type EventKind int

const (
	KindConfig  EventKind = iota
	KindRequest           // 1
)

type Event struct {
	Kind          EventKind
	ConfigVersion int    // set when Kind == KindConfig
	RequestID     string // set when Kind == KindRequest
}

func main() {
	fmt.Println("=== WithLatestFrom: tag requests with the latest config version ===")

	src := kitsune.NewChannel[Event](32)

	// Split the shared source into two branches on the same graph.
	cfgEvents, reqEvents := kitsune.Partition(src.Source(), func(e Event) bool {
		return e.Kind == KindConfig
	})

	// Secondary: config versions (slow-changing state).
	configs := kitsune.Map(cfgEvents, func(_ context.Context, e Event) (int, error) {
		return e.ConfigVersion, nil
	})

	// Primary: request IDs (frequent events).
	requests := kitsune.Map(reqEvents, func(_ context.Context, e Event) (string, error) {
		return e.RequestID, nil
	})

	type Tagged struct {
		Request string
		Config  int
	}

	combined := kitsune.Map(
		kitsune.WithLatestFrom(requests, configs),
		func(_ context.Context, p kitsune.Pair[string, int]) (Tagged, error) {
			return Tagged{Request: p.First, Config: p.Second}, nil
		},
	)

	var results []Tagged
	errCh := combined.ForEach(func(_ context.Context, t Tagged) error {
		results = append(results, t)
		return nil
	}).RunAsync(context.Background())

	ctx := context.Background()

	// Config v1 lands first — subsequent requests will use it.
	_ = src.Send(ctx, Event{Kind: KindConfig, ConfigVersion: 1})
	time.Sleep(20 * time.Millisecond) // let secondary goroutine latch v1

	_ = src.Send(ctx, Event{Kind: KindRequest, RequestID: "req-A"})
	_ = src.Send(ctx, Event{Kind: KindRequest, RequestID: "req-B"})
	time.Sleep(20 * time.Millisecond) // let primary process req-A and req-B before v2 arrives

	// Config upgrades to v2 mid-stream.
	_ = src.Send(ctx, Event{Kind: KindConfig, ConfigVersion: 2})
	time.Sleep(20 * time.Millisecond) // let secondary latch v2

	_ = src.Send(ctx, Event{Kind: KindRequest, RequestID: "req-C"})

	src.Close()
	if err := <-errCh; err != nil {
		panic(err)
	}

	for _, r := range results {
		fmt.Printf("  %-7s  →  config v%d\n", r.Request, r.Config)
	}

	// --- Scenario 2: annotate measurements with the latest calibration offset ---
	//
	// A calibration stream and a measurement stream share one source.
	// Each measurement is adjusted by the most recent calibration reading.
	fmt.Println("\n=== WithLatestFrom: apply latest calibration to measurements ===")

	type Reading struct {
		IsCalibration bool
		Value         float64
	}

	src2 := kitsune.NewChannel[Reading](32)
	calBranch, measBranch := kitsune.Partition(src2.Source(), func(r Reading) bool {
		return r.IsCalibration
	})

	calibrations := kitsune.Map(calBranch, func(_ context.Context, r Reading) (float64, error) {
		return r.Value, nil
	})
	measurements := kitsune.Map(measBranch, func(_ context.Context, r Reading) (float64, error) {
		return r.Value, nil
	})

	type Adjusted struct {
		Raw    float64
		Offset float64
		Final  float64
	}
	adjusted := kitsune.Map(
		kitsune.WithLatestFrom(measurements, calibrations),
		func(_ context.Context, p kitsune.Pair[float64, float64]) (Adjusted, error) {
			return Adjusted{Raw: p.First, Offset: p.Second, Final: p.First - p.Second}, nil
		},
	)

	var measurements2 []Adjusted
	errCh2 := adjusted.ForEach(func(_ context.Context, a Adjusted) error {
		measurements2 = append(measurements2, a)
		return nil
	}).RunAsync(context.Background())

	ctx2 := context.Background()

	// Initial calibration offset.
	_ = src2.Send(ctx2, Reading{IsCalibration: true, Value: 0.5})
	time.Sleep(20 * time.Millisecond) // latch offset=0.5

	_ = src2.Send(ctx2, Reading{Value: 10.3})
	_ = src2.Send(ctx2, Reading{Value: 10.7})
	time.Sleep(20 * time.Millisecond) // let primary process these two before recalibrating

	// Recalibrate (sensor drifted).
	_ = src2.Send(ctx2, Reading{IsCalibration: true, Value: 0.8})
	time.Sleep(20 * time.Millisecond) // latch offset=0.8

	_ = src2.Send(ctx2, Reading{Value: 11.2})

	src2.Close()
	if err := <-errCh2; err != nil {
		panic(err)
	}

	fmt.Printf("%-6s  %-6s  %-6s\n", "raw", "offset", "adjusted")
	for _, a := range measurements2 {
		fmt.Printf("%-6.1f  %-6.1f  %.1f\n", a.Raw, a.Offset, a.Final)
	}
}
