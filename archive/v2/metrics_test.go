package kitsune_test

import (
	"context"
	"errors"
	"testing"

	kitsune "github.com/zenbaku/go-kitsune/v2"
)

func TestMetricsHookCounts(t *testing.T) {
	ctx := context.Background()
	m := kitsune.NewMetricsHook()

	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	p2 := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v * 2, nil
	}, kitsune.WithName("double"))

	_, err := kitsune.Collect(ctx, p2, kitsune.WithHook(m))
	if err != nil {
		t.Fatal(err)
	}

	s := m.Stage("double")
	if s.Processed != 5 {
		t.Fatalf("processed=%d, want 5", s.Processed)
	}
	if s.Errors != 0 {
		t.Fatalf("errors=%d, want 0", s.Errors)
	}
}

func TestMetricsHookCountsErrors(t *testing.T) {
	ctx := context.Background()
	m := kitsune.NewMetricsHook()
	boom := errors.New("boom")

	p := kitsune.FromSlice([]int{1, 2, 3})
	p2 := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		if v == 2 {
			return 0, boom
		}
		return v, nil
	}, kitsune.WithName("stage"),
		kitsune.OnError(kitsune.Skip()),
	)

	_, err := kitsune.Collect(ctx, p2, kitsune.WithHook(m))
	if err != nil {
		t.Fatal(err)
	}

	s := m.Stage("stage")
	if s.Processed != 2 {
		t.Fatalf("processed=%d, want 2", s.Processed)
	}
	if s.Errors != 1 {
		t.Fatalf("errors=%d, want 1", s.Errors)
	}
}

func TestMetricsHookSnapshot(t *testing.T) {
	ctx := context.Background()
	m := kitsune.NewMetricsHook()

	p := kitsune.FromSlice([]int{1, 2, 3})
	_, err := kitsune.Collect(ctx,
		kitsune.Map(p, func(_ context.Context, v int) (int, error) {
			return v, nil
		}, kitsune.WithName("s1")),
		kitsune.WithHook(m),
	)
	if err != nil {
		t.Fatal(err)
	}

	snap := m.Snapshot()
	found := false
	for _, s := range snap.Stages {
		if s.Stage == "s1" {
			found = true
			if s.Processed != 3 {
				t.Fatalf("s1 processed=%d, want 3", s.Processed)
			}
		}
	}
	if !found {
		t.Fatal("stage s1 not in snapshot")
	}

	_, err = snap.JSON()
	if err != nil {
		t.Fatalf("Snapshot.JSON() error: %v", err)
	}
}

func TestMetricsHookAvgLatency(t *testing.T) {
	ctx := context.Background()
	m := kitsune.NewMetricsHook()

	p := kitsune.FromSlice([]int{1, 2, 3})
	_, err := kitsune.Collect(ctx,
		kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v, nil },
			kitsune.WithName("stage")),
		kitsune.WithHook(m),
	)
	if err != nil {
		t.Fatal(err)
	}

	s := m.Stage("stage")
	_ = s.AvgLatency() // must not panic
}

func TestMetricsHookDrops(t *testing.T) {
	ctx := context.Background()
	m := kitsune.NewMetricsHook()

	// Use overflow drop to trigger OnDrop.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8})
	_, err := kitsune.Collect(ctx,
		kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v, nil },
			kitsune.WithName("dropper"),
			kitsune.Buffer(1),
			kitsune.Overflow(kitsune.DropNewest),
		),
		kitsune.WithHook(m),
	)
	if err != nil {
		t.Fatal(err)
	}

	s := m.Stage("dropper")
	// Some items may be dropped, some not — just verify the counter is accessible.
	_ = s.Dropped
}

func TestMetricsHookReset(t *testing.T) {
	ctx := context.Background()
	m := kitsune.NewMetricsHook()

	p := kitsune.FromSlice([]int{1, 2})
	_, err := kitsune.Collect(ctx,
		kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v, nil },
			kitsune.WithName("stage")),
		kitsune.WithHook(m),
	)
	if err != nil {
		t.Fatal(err)
	}
	if m.Stage("stage").Processed == 0 {
		t.Fatal("expected non-zero processed before reset")
	}

	m.Reset()
	if m.Stage("stage").Processed != 0 {
		t.Fatal("expected zero processed after reset")
	}
}
