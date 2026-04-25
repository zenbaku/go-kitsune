package kitsune_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
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
		kitsune.OnError(kitsune.ActionDrop()),
	)

	_, err := kitsune.Collect(ctx, p2, kitsune.WithHook(m))
	if err != nil {
		t.Fatal(err)
	}

	s := m.Stage("stage")
	if s.Processed != 2 {
		t.Fatalf("processed=%d, want 2", s.Processed)
	}
	if s.Skipped != 1 {
		t.Fatalf("skipped=%d, want 1", s.Skipped)
	}
	if s.Errors != 0 {
		t.Fatalf("errors=%d, want 0", s.Errors)
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
	s, ok := snap.Stages["s1"]
	if !ok {
		t.Fatal("stage s1 not in snapshot")
	}
	if s.Processed != 3 {
		t.Fatalf("s1 processed=%d, want 3", s.Processed)
	}
	if snap.Timestamp.IsZero() {
		t.Fatal("snapshot Timestamp is zero")
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

// ---------------------------------------------------------------------------
// New tests: Skipped, Latency, ErrorRate, Throughput, GraphHook,
// SnapshotJSON, StageUnknown, ConcurrentAccess, Histogram, Percentile
// ---------------------------------------------------------------------------

func TestMetricsHookSkipped(t *testing.T) {
	m := kitsune.NewMetricsHook()
	boom := errors.New("skip-me")
	// OnError(Skip()) converts errors to ErrSkipped in the engine, which the
	// hook receives as Skipped (not Errors).
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	mapped := kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		if n%2 == 0 {
			return 0, boom // even items are skipped
		}
		return n, nil
	}, kitsune.WithName("skipstage"), kitsune.OnError(kitsune.ActionDrop()))

	_, err := mapped.Collect(context.Background(), kitsune.WithHook(m))
	if err != nil {
		t.Fatal(err)
	}

	s := m.Stage("skipstage")
	if s.Processed != 3 { // 1, 3, 5
		t.Errorf("Processed = %d, want 3", s.Processed)
	}
	if s.Skipped != 2 { // 2, 4
		t.Errorf("Skipped = %d, want 2", s.Skipped)
	}
	if s.Errors != 0 {
		t.Errorf("Errors = %d, want 0", s.Errors)
	}
}

func TestMetricsHookLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping latency test in short mode")
	}
	m := kitsune.NewMetricsHook()
	delay := 5 * time.Millisecond

	p := kitsune.FromSlice([]int{1, 2, 3})
	_, err := kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		time.Sleep(delay)
		return n, nil
	}, kitsune.WithName("slow")).Collect(context.Background(), kitsune.WithHook(m))
	if err != nil {
		t.Fatal(err)
	}

	s := m.Stage("slow")
	if s.TotalNs == 0 {
		t.Error("TotalNs should be > 0")
	}
	if s.MinNs == 0 {
		t.Error("MinNs should be > 0")
	}
	if s.MaxNs < s.MinNs {
		t.Errorf("MaxNs %d < MinNs %d", s.MaxNs, s.MinNs)
	}
	if mean := s.MeanLatency(); mean < delay/2 {
		t.Errorf("MeanLatency %v seems too low (want >= %v)", mean, delay/2)
	}
}

func TestMetricsHookErrorRate(t *testing.T) {
	m := kitsune.NewMetricsHook()
	boom := errors.New("boom")

	// Pipeline halts when n==3. Items 1 and 2 succeed, then the error fires.
	_, runErr := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, n int) (int, error) {
			if n == 3 {
				return 0, boom
			}
			return n, nil
		}, kitsune.WithName("rate-stage"),
	).Collect(context.Background(), kitsune.WithHook(m))

	if runErr == nil {
		t.Fatal("expected pipeline to halt with an error")
	}

	s := m.Stage("rate-stage")
	if s.Errors == 0 {
		t.Error("Errors = 0, expected at least 1")
	}
	rate := s.ErrorRate()
	if rate <= 0 || rate > 1 {
		t.Errorf("ErrorRate = %v, want in (0, 1]", rate)
	}
}

func TestMetricsHookThroughput(t *testing.T) {
	m := kitsune.NewMetricsHook()

	p := kitsune.FromSlice(make([]int, 100))
	_, _ = kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		return n, nil
	}, kitsune.WithName("tp")).Drain().Run(context.Background(), kitsune.WithHook(m))

	snap := m.Snapshot()
	s := snap.Stages["tp"]
	if tp := s.Throughput(snap.Elapsed); tp <= 0 {
		t.Errorf("Throughput = %v, want > 0", tp)
	}
}

func TestMetricsHookGraphHook(t *testing.T) {
	m := kitsune.NewMetricsHook()

	p := kitsune.FromSlice([]int{1})
	_, _ = kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		return n, nil
	}, kitsune.WithName("g")).Drain().Run(context.Background(), kitsune.WithHook(m))

	snap := m.Snapshot()
	if len(snap.Graph) == 0 {
		t.Error("expected Graph to be populated via GraphHook")
	}
}

func TestMetricsHookSnapshotJSON(t *testing.T) {
	m := kitsune.NewMetricsHook()

	p := kitsune.FromSlice([]int{1, 2})
	_, _ = kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		return n, nil
	}, kitsune.WithName("json-stage")).Drain().Run(context.Background(), kitsune.WithHook(m))

	data, err := m.Snapshot().JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("JSON roundtrip failed: %v", err)
	}
	stages, ok := decoded["stages"].(map[string]any)
	if !ok {
		t.Fatal("expected stages to be a JSON object (map), got something else")
	}
	if _, ok := stages["json-stage"]; !ok {
		t.Error("expected 'json-stage' key in JSON stages object")
	}
}

func TestMetricsHookStageUnknown(t *testing.T) {
	m := kitsune.NewMetricsHook()
	s := m.Stage("nonexistent")
	if s.Processed != 0 || s.Errors != 0 || s.Skipped != 0 {
		t.Errorf("unknown stage should return zero metrics, got %+v", s)
	}
}

func TestMetricsHookConcurrentAccess(t *testing.T) {
	m := kitsune.NewMetricsHook()
	items := make([]int, 500)
	for i := range items {
		items[i] = i
	}
	_, _ = kitsune.Map(kitsune.FromSlice(items), func(_ context.Context, n int) (int, error) {
		return n * 2, nil
	}, kitsune.WithName("concurrent"), kitsune.Concurrency(8)).
		Drain().Run(context.Background(), kitsune.WithHook(m))

	if s := m.Stage("concurrent"); s.Processed != int64(len(items)) {
		t.Errorf("Processed = %d, want %d", s.Processed, len(items))
	}
}

func TestMetricsHookHistogramBuckets(t *testing.T) {
	m := kitsune.NewMetricsHook()
	p := kitsune.FromSlice([]int{1, 2, 3})
	// A short sleep ensures each item records a non-zero duration in the histogram.
	_, err := kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		time.Sleep(time.Microsecond)
		return n, nil
	}, kitsune.WithName("histo")).Collect(context.Background(), kitsune.WithHook(m))
	if err != nil {
		t.Fatal(err)
	}
	s := m.Stage("histo")
	total := int64(0)
	for _, c := range s.Buckets {
		total += c
	}
	if total != 3 {
		t.Errorf("histogram bucket total = %d, want 3", total)
	}
}

func TestStageMetricsPercentileBasic(t *testing.T) {
	// 10 items in bucket 3 (1ms–5ms boundary).
	var s kitsune.StageMetrics
	s.Buckets[3] = 10
	p50 := s.Percentile(0.5)
	if p50 < time.Millisecond || p50 > 5*time.Millisecond {
		t.Errorf("p50 = %v, expected in [1ms, 5ms]", p50)
	}
}

func TestStageMetricsPercentileEmpty(t *testing.T) {
	var s kitsune.StageMetrics
	if got := s.Percentile(0.99); got != 0 {
		t.Errorf("Percentile on empty = %v, want 0", got)
	}
}

func TestStageMetricsPercentileBounds(t *testing.T) {
	var s kitsune.StageMetrics
	s.Buckets[0] = 5
	if got := s.Percentile(0); got < 0 {
		t.Errorf("p0 = %v, want >= 0", got)
	}
	if got := s.Percentile(1); got < 0 {
		t.Errorf("p100 = %v, want >= 0", got)
	}
}
