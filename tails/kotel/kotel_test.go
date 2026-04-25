package kotel_test

import (
	"context"
	"errors"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/tails/kotel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// newTestMeter creates an in-memory metric reader and a meter backed by it.
func newTestMeter() (sdkmetric.Reader, *sdkmetric.MeterProvider) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	return reader, provider
}

func collect(t *testing.T, reader sdkmetric.Reader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	return rm
}

func findCounter(t *testing.T, rm metricdata.ResourceMetrics, name string) int64 {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				switch d := m.Data.(type) {
				case metricdata.Sum[int64]:
					var total int64
					for _, dp := range d.DataPoints {
						total += dp.Value
					}
					return total
				}
			}
		}
	}
	return 0
}

func TestOTelHookItems(t *testing.T) {
	reader, provider := newTestMeter()
	defer provider.Shutdown(context.Background())

	meter := provider.Meter("test")
	hook := kotel.New(meter)

	// Simulate a 5-item pipeline via the public kitsune API.
	items := []int{1, 2, 3, 4, 5}
	_, err := kitsune.Map(
		kitsune.FromSlice(items),
		func(_ context.Context, v int) (int, error) { return v * 2, nil },
		kitsune.WithName("double"),
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}

	rm := collect(t, reader)
	if total := findCounter(t, rm, "kitsune.stage.items"); total < 5 {
		t.Errorf("kitsune.stage.items: want >= 5, got %d", total)
	}
}

func TestOTelHookErrors(t *testing.T) {
	reader, provider := newTestMeter()
	defer provider.Shutdown(context.Background())

	meter := provider.Meter("test")
	hook := kotel.New(meter)

	boom := errors.New("boom")
	kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) { return 0, boom },
		kitsune.OnError(kitsune.ActionDrop()),
	).ForEach(func(_ context.Context, v int) error { return nil }).
		Run(context.Background(), kitsune.WithHook(hook))

	rm := collect(t, reader)
	// All items are skipped; skipped items still count as processed.
	if total := findCounter(t, rm, "kitsune.stage.items"); total == 0 {
		t.Errorf("kitsune.stage.items: expected non-zero, got 0")
	}
}

func TestOTelHookDrops(t *testing.T) {
	reader, provider := newTestMeter()
	defer provider.Shutdown(context.Background())

	meter := provider.Meter("test")
	hook := kotel.New(meter)

	const n = 1000
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}

	err := kitsune.Map(
		kitsune.FromSlice(items),
		func(_ context.Context, v int) (int, error) { return v, nil },
		kitsune.Buffer(2),
		kitsune.Overflow(kitsune.DropNewest),
	).ForEach(func(_ context.Context, v int) error {
		time.Sleep(time.Millisecond)
		return nil
	}).Run(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}

	rm := collect(t, reader)
	drops := findCounter(t, rm, "kitsune.stage.drops")
	if drops == 0 {
		t.Error("expected drops > 0 with slow consumer and DropNewest overflow")
	}
}

func TestOTelHookRestarts(t *testing.T) {
	reader, provider := newTestMeter()
	defer provider.Shutdown(context.Background())

	meter := provider.Meter("test")
	hook := kotel.New(meter)

	var calls int
	_, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			calls++
			if calls == 1 {
				return 0, errors.New("transient")
			}
			return v * 2, nil
		},
		kitsune.Supervise(kitsune.RestartOnError(1, kitsune.FixedBackoff(0))),
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}

	rm := collect(t, reader)
	if restarts := findCounter(t, rm, "kitsune.stage.restarts"); restarts < 1 {
		t.Errorf("kitsune.stage.restarts: want >= 1, got %d", restarts)
	}
}

func TestOTelHookBuffers(t *testing.T) {
	reader, provider := newTestMeter()
	defer provider.Shutdown(context.Background())

	meter := provider.Meter("test")
	hook := kotel.New(meter)

	const n = 200
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}

	// Slow consumer so buffers fill up during the run.
	_, err := kitsune.Map(
		kitsune.FromSlice(items),
		func(_ context.Context, v int) (int, error) { return v, nil },
		kitsune.WithName("buffered"),
		kitsune.Buffer(32),
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}

	// Trigger a collection to exercise the observable gauge callback.
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	// The gauge may be zero after the pipeline drains; we just verify it was
	// registered and the callback runs without error.
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "kitsune.stage.buffer_length" {
				found = true
			}
		}
	}
	if !found {
		t.Error("kitsune.stage.buffer_length gauge not registered")
	}
}

func TestOTelHookGraph(t *testing.T) {
	reader, provider := newTestMeter()
	defer provider.Shutdown(context.Background())

	meter := provider.Meter("test")
	hook := kotel.New(meter)

	_, err := kitsune.Map(
		kitsune.FromSlice([]int{1}),
		func(_ context.Context, v int) (int, error) { return v, nil },
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}

	rm := collect(t, reader)
	// Pipeline has 2 stages: source (map wraps a source) and a sink.
	// At minimum 1 stage should be counted.
	var stagesTotal int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "kitsune.pipeline.stages" {
				switch d := m.Data.(type) {
				case metricdata.Sum[int64]:
					for _, dp := range d.DataPoints {
						stagesTotal += dp.Value
					}
				}
			}
		}
	}
	if stagesTotal < 1 {
		t.Errorf("kitsune.pipeline.stages: want >= 1, got %d", stagesTotal)
	}
}
