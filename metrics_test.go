package kitsune_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func TestMetricsHook_Processed(t *testing.T) {
	m := kitsune.NewMetricsHook()
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	mapped := kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		return n * 2, nil
	}, kitsune.WithName("double"))

	_, err := mapped.Collect(context.Background(), kitsune.WithHook(m))
	if err != nil {
		t.Fatal(err)
	}

	s := m.Stage("double")
	if s.Processed != 5 {
		t.Errorf("Processed = %d, want 5", s.Processed)
	}
	if s.Errors != 0 {
		t.Errorf("Errors = %d, want 0", s.Errors)
	}
}

func TestMetricsHook_Errors(t *testing.T) {
	m := kitsune.NewMetricsHook()
	sentinel := errors.New("boom")

	var count atomic.Int32
	p := kitsune.FromSlice([]int{1, 2, 3})
	// OnError(Skip()) converts errors to ErrSkipped inside the engine, so
	// the hook sees them as Skipped, not Errors.
	mapped := kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		count.Add(1)
		if n == 2 {
			return 0, sentinel
		}
		return n, nil
	}, kitsune.WithName("failstage"), kitsune.OnError(kitsune.Skip()))

	_, err := mapped.Collect(context.Background(), kitsune.WithHook(m))
	if err != nil {
		t.Fatal(err)
	}

	s := m.Stage("failstage")
	if s.Processed != 2 {
		t.Errorf("Processed = %d, want 2", s.Processed)
	}
	if s.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (OnError(Skip()) reports as skipped)", s.Skipped)
	}
	if s.Errors != 0 {
		t.Errorf("Errors = %d, want 0", s.Errors)
	}
}

func TestMetricsHook_Skipped(t *testing.T) {
	m := kitsune.NewMetricsHook()

	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	// Filter uses ErrSkipped internally; Dedupe also uses it.
	// Use a Map with Skip handler to exercise the skipped counter.
	mapped := kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		if n%2 == 0 {
			return 0, nil // will be skipped by filter semantics via Dedupe
		}
		return n, nil
	}, kitsune.WithName("evens"))

	// Use Dedupe to force ErrSkipped events through the hook.
	deduped := mapped.Dedupe(func(n int) string {
		return "key" // all items have same key → only first passes
	}, kitsune.WithName("dedup"))

	_, err := deduped.Collect(context.Background(), kitsune.WithHook(m))
	if err != nil {
		t.Fatal(err)
	}

	s := m.Stage("dedup")
	// First item passes, remaining 4 are skipped.
	if s.Processed != 1 {
		t.Errorf("Processed = %d, want 1", s.Processed)
	}
	if s.Skipped != 4 {
		t.Errorf("Skipped = %d, want 4", s.Skipped)
	}
}

func TestMetricsHook_Latency(t *testing.T) {
	m := kitsune.NewMetricsHook()
	delay := 10 * time.Millisecond

	p := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		time.Sleep(delay)
		return n, nil
	}, kitsune.WithName("slow"))

	_, err := mapped.Collect(context.Background(), kitsune.WithHook(m))
	if err != nil {
		t.Fatal(err)
	}

	s := m.Stage("slow")
	if s.Processed != 3 {
		t.Fatalf("Processed = %d, want 3", s.Processed)
	}
	if s.TotalNs == 0 {
		t.Error("TotalNs should be > 0")
	}
	if s.MinNs == 0 {
		t.Error("MinNs should be > 0")
	}
	if s.MaxNs < s.MinNs {
		t.Errorf("MaxNs %d < MinNs %d", s.MaxNs, s.MinNs)
	}
	mean := s.MeanLatency()
	if mean < delay/2 {
		t.Errorf("MeanLatency %v seems too low (expected >= %v)", mean, delay/2)
	}
}

func TestMetricsHook_ErrorRate(t *testing.T) {
	m := kitsune.NewMetricsHook()
	boom := errors.New("boom")

	// Let the pipeline halt on n==3. Collect returns the error;
	// items 1 and 2 are processed, item 3 triggers a halt error.
	p := kitsune.FromSlice([]int{1, 2, 3})
	_, runErr := kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		if n == 3 {
			return 0, boom
		}
		return n, nil
	}, kitsune.WithName("rate-stage")).Collect(context.Background(), kitsune.WithHook(m))

	if runErr == nil {
		t.Fatal("expected pipeline to halt with an error")
	}

	s := m.Stage("rate-stage")
	// At least one error must have been recorded before halt.
	if s.Errors == 0 {
		t.Errorf("Errors = 0, expected at least 1 after halting pipeline")
	}
	rate := s.ErrorRate()
	if rate <= 0 || rate > 1 {
		t.Errorf("ErrorRate = %v, want in range (0,1]", rate)
	}
}

func TestMetricsHook_Throughput(t *testing.T) {
	m := kitsune.NewMetricsHook()

	p := kitsune.FromSlice(make([]int, 100))
	_ = kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		return n, nil
	}, kitsune.WithName("tp")).Drain().Run(context.Background(), kitsune.WithHook(m))

	snap := m.Snapshot()
	s := snap.Stages["tp"]
	tp := s.Throughput(snap.Elapsed)
	if tp <= 0 {
		t.Errorf("Throughput = %v, want > 0", tp)
	}
}

func TestMetricsHook_Snapshot(t *testing.T) {
	m := kitsune.NewMetricsHook()

	p := kitsune.FromSlice([]string{"a", "b", "c"})
	_ = kitsune.Map(p, func(_ context.Context, s string) (string, error) {
		return s + "!", nil
	}, kitsune.WithName("exclaim")).Drain().Run(context.Background(), kitsune.WithHook(m))

	snap := m.Snapshot()
	if snap.Timestamp.IsZero() {
		t.Error("Snapshot.Timestamp should not be zero")
	}
	if snap.Elapsed <= 0 {
		t.Error("Snapshot.Elapsed should be > 0")
	}
	if _, ok := snap.Stages["exclaim"]; !ok {
		t.Error("Snapshot.Stages should contain 'exclaim'")
	}
}

func TestMetricsHook_SnapshotJSON(t *testing.T) {
	m := kitsune.NewMetricsHook()

	p := kitsune.FromSlice([]int{1, 2})
	_ = kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		return n, nil
	}, kitsune.WithName("json-stage")).Drain().Run(context.Background(), kitsune.WithHook(m))

	snap := m.Snapshot()
	data, err := snap.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("JSON roundtrip failed: %v", err)
	}
	stages, ok := decoded["stages"].(map[string]any)
	if !ok {
		t.Fatal("expected stages key in JSON")
	}
	if _, ok := stages["json-stage"]; !ok {
		t.Error("expected 'json-stage' in JSON stages")
	}
}

func TestMetricsHook_StageUnknown(t *testing.T) {
	m := kitsune.NewMetricsHook()
	s := m.Stage("nonexistent")
	if s.Processed != 0 || s.Errors != 0 {
		t.Errorf("unknown stage should return zero metrics, got %+v", s)
	}
}

func TestMetricsHook_ConcurrentAccess(t *testing.T) {
	m := kitsune.NewMetricsHook()

	items := make([]int, 500)
	for i := range items {
		items[i] = i
	}

	p := kitsune.FromSlice(items)
	_ = kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		return n * 2, nil
	}, kitsune.WithName("concurrent"), kitsune.Concurrency(8)).
		Drain().Run(context.Background(), kitsune.WithHook(m))

	s := m.Stage("concurrent")
	if s.Processed != int64(len(items)) {
		t.Errorf("Processed = %d, want %d", s.Processed, len(items))
	}
}

func TestMetricsHook_GraphHook(t *testing.T) {
	m := kitsune.NewMetricsHook()

	p := kitsune.FromSlice([]int{1})
	_ = kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		return n, nil
	}).Drain().Run(context.Background(), kitsune.WithHook(m))

	snap := m.Snapshot()
	if len(snap.Graph) == 0 {
		t.Error("expected Graph to be populated via GraphHook")
	}
}

func TestMetricsHook_Histogram_BucketsPopulated(t *testing.T) {
	m := kitsune.NewMetricsHook()
	p := kitsune.FromSlice([]int{1, 2, 3})
	_, err := kitsune.Map(p, func(_ context.Context, n int) (int, error) {
		return n, nil
	}, kitsune.WithName("fast")).Collect(context.Background(), kitsune.WithHook(m))
	if err != nil {
		t.Fatal(err)
	}
	s := m.Stage("fast")
	total := int64(0)
	for _, c := range s.Buckets {
		total += c
	}
	if total != 3 {
		t.Errorf("Buckets total = %d, want 3", total)
	}
}

func TestStageMetrics_Percentile_Basic(t *testing.T) {
	// Construct known buckets: 10 items in bucket 3 (1ms–5ms).
	var m kitsune.StageMetrics
	m.Buckets[3] = 10
	p50 := m.Percentile(0.5)
	if p50 < time.Millisecond || p50 > 5*time.Millisecond {
		t.Errorf("p50 = %v, expected in [1ms, 5ms]", p50)
	}
}

func TestStageMetrics_Percentile_Empty(t *testing.T) {
	var m kitsune.StageMetrics
	if got := m.Percentile(0.99); got != 0 {
		t.Errorf("Percentile on empty = %v, want 0", got)
	}
}

func TestStageMetrics_Percentile_Bounds(t *testing.T) {
	var m kitsune.StageMetrics
	m.Buckets[0] = 5
	if got := m.Percentile(0); got < 0 {
		t.Errorf("p0 should be non-negative, got %v", got)
	}
	if got := m.Percentile(1); got < 0 {
		t.Errorf("p100 should be non-negative, got %v", got)
	}
}
