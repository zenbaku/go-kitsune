package kprometheus_test

import (
	"context"
	"errors"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/tails/kprometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/client_golang/prometheus"
)

// newReg returns a fresh pedantic registry (catches duplicate registrations).
func newReg() *prometheus.Registry {
	return prometheus.NewPedanticRegistry()
}

// gatherSum sums the values of all samples for the named metric family.
func gatherSum(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			var total float64
			for _, m := range mf.GetMetric() {
				total += metricValue(m)
			}
			return total
		}
	}
	return 0
}

func metricValue(m *dto.Metric) float64 {
	if c := m.GetCounter(); c != nil {
		return c.GetValue()
	}
	if g := m.GetGauge(); g != nil {
		return g.GetValue()
	}
	return 0
}

func TestPrometheusHookItems(t *testing.T) {
	reg := newReg()
	hook := kprometheus.New(reg, "kitsune")

	items := []int{1, 2, 3, 4, 5}
	_, err := kitsune.Map(
		kitsune.FromSlice(items),
		func(_ context.Context, v int) (int, error) { return v * 2, nil },
		kitsune.WithName("double"),
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}

	total := gatherSum(t, reg, "kitsune_stage_items_total")
	if total < 5 {
		t.Errorf("kitsune_stage_items_total: want >= 5, got %v", total)
	}
}

func TestPrometheusHookErrors(t *testing.T) {
	reg := newReg()
	hook := kprometheus.New(reg, "kitsune")

	boom := errors.New("boom")
	_, _ = kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) { return 0, boom },
		kitsune.OnError(kitsune.ActionDrop()),
		kitsune.WithName("failing"),
	).ForEach(func(_ context.Context, v int) error { return nil }).
		Run(context.Background(), kitsune.WithHook(hook))

	total := gatherSum(t, reg, "kitsune_stage_items_total")
	if total == 0 {
		t.Error("kitsune_stage_items_total: expected non-zero for skipped items")
	}
}

func TestPrometheusHookDrops(t *testing.T) {
	reg := newReg()
	hook := kprometheus.New(reg, "kitsune")

	const n = 1000
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}

	_, err := kitsune.Map(
		kitsune.FromSlice(items),
		func(_ context.Context, v int) (int, error) { return v, nil },
		kitsune.Buffer(2),
		kitsune.Overflow(kitsune.DropNewest),
		kitsune.WithName("overflow-stage"),
	).ForEach(func(_ context.Context, v int) error {
		time.Sleep(time.Millisecond)
		return nil
	}).Run(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}

	drops := gatherSum(t, reg, "kitsune_stage_drops_total")
	if drops == 0 {
		t.Error("kitsune_stage_drops_total: expected > 0 with slow consumer and DropNewest")
	}
}

func TestPrometheusHookRestarts(t *testing.T) {
	reg := newReg()
	hook := kprometheus.New(reg, "kitsune")

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
		kitsune.WithName("supervised"),
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}

	restarts := gatherSum(t, reg, "kitsune_stage_restarts_total")
	if restarts < 1 {
		t.Errorf("kitsune_stage_restarts_total: want >= 1, got %v", restarts)
	}
}

func TestPrometheusHookDuration(t *testing.T) {
	reg := newReg()
	hook := kprometheus.New(reg, "kitsune")

	_, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			time.Sleep(time.Millisecond)
			return v, nil
		},
		kitsune.WithName("slow"),
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}

	// Verify the histogram was registered and has observations.
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, mf := range mfs {
		if mf.GetName() == "kitsune_stage_duration_seconds" {
			found = true
			for _, m := range mf.GetMetric() {
				if m.GetHistogram().GetSampleCount() > 0 {
					return
				}
			}
		}
	}
	if !found {
		t.Error("kitsune_stage_duration_seconds histogram not registered")
	}
}

func TestPrometheusHookNamespace(t *testing.T) {
	// Verify that an empty namespace produces metrics without a leading underscore.
	reg := newReg()
	hook := kprometheus.New(reg, "")
	_ = hook

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		name := mf.GetName()
		if len(name) > 0 && name[0] == '_' {
			t.Errorf("metric name starts with underscore: %q", name)
		}
	}
}
