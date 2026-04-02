package kitsune

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zenbaku/go-kitsune/engine"
)

// ---------------------------------------------------------------------------
// Latency histogram constants
// ---------------------------------------------------------------------------

const numBuckets = 11

// bucketBounds defines the upper bound (exclusive) of each histogram bucket
// in nanoseconds. The final bucket catches everything above 5s.
var bucketBounds = [numBuckets + 1]int64{
	0,
	100_000,       // 100µs
	500_000,       // 500µs
	1_000_000,     // 1ms
	5_000_000,     // 5ms
	10_000_000,    // 10ms
	50_000_000,    // 50ms
	100_000_000,   // 100ms
	500_000_000,   // 500ms
	1_000_000_000, // 1s
	5_000_000_000, // 5s
	math.MaxInt64, // ∞
}

// Compile-time interface checks.
var (
	_ Hook            = (*MetricsHook)(nil)
	_ OverflowHook    = (*MetricsHook)(nil)
	_ SupervisionHook = (*MetricsHook)(nil)
	_ GraphHook       = (*MetricsHook)(nil)
	_ BufferHook      = (*MetricsHook)(nil)
)

// ---------------------------------------------------------------------------
// StageMetrics and Snapshot
// ---------------------------------------------------------------------------

// StageMetrics holds a point-in-time view of metrics for one pipeline stage.
type StageMetrics struct {
	Processed int64             `json:"processed"` // items that exited successfully
	Errors    int64             `json:"errors"`    // items that resulted in a non-skip error
	Skipped   int64             `json:"skipped"`   // items dropped via OnError(Skip())
	Dropped   int64             `json:"dropped"`   // items dropped due to buffer overflow
	Restarts  int64             `json:"restarts"`  // stage restarts via supervision policy
	TotalNs   int64             `json:"total_ns"`  // cumulative item processing time (ns)
	MinNs     int64             `json:"min_ns"`    // fastest single-item duration (ns)
	MaxNs     int64             `json:"max_ns"`    // slowest single-item duration (ns)
	Buckets   [numBuckets]int64 `json:"buckets"`   // latency histogram counts per bucket
}

// Percentile returns an estimate of the q-th percentile latency (0 ≤ q ≤ 1).
// It uses the histogram buckets and assumes a uniform distribution within
// each bucket. Returns 0 if no items have been recorded.
func (m StageMetrics) Percentile(q float64) time.Duration {
	if q < 0 {
		q = 0
	}
	if q > 1 {
		q = 1
	}
	total := int64(0)
	for _, c := range m.Buckets {
		total += c
	}
	if total == 0 {
		return 0
	}
	target := int64(math.Ceil(float64(total) * q))
	if target <= 0 {
		target = 1
	}
	cumulative := int64(0)
	for i := 0; i < numBuckets; i++ {
		cumulative += m.Buckets[i]
		if cumulative >= target {
			// Interpolate within the bucket.
			lo := bucketBounds[i]
			hi := bucketBounds[i+1]
			if hi == math.MaxInt64 {
				return time.Duration(lo)
			}
			prev := cumulative - m.Buckets[i]
			fraction := float64(target-prev) / float64(m.Buckets[i])
			ns := float64(lo) + fraction*float64(hi-lo)
			return time.Duration(int64(ns))
		}
	}
	return time.Duration(bucketBounds[numBuckets-1])
}

// Throughput returns the number of successfully processed items per second
// over the given elapsed duration.
func (m StageMetrics) Throughput(elapsed time.Duration) float64 {
	if elapsed <= 0 || m.Processed == 0 {
		return 0
	}
	return float64(m.Processed) / elapsed.Seconds()
}

// MeanLatency returns the average per-item processing duration across all
// items (successful and errored).
func (m StageMetrics) MeanLatency() time.Duration {
	total := m.Processed + m.Errors
	if total == 0 || m.TotalNs == 0 {
		return 0
	}
	return time.Duration(m.TotalNs / total)
}

// ErrorRate returns the fraction of items that resulted in an error,
// in the range [0, 1].
func (m StageMetrics) ErrorRate() float64 {
	total := m.Processed + m.Errors
	if total == 0 {
		return 0
	}
	return float64(m.Errors) / float64(total)
}

// Snapshot is a point-in-time capture of all stage metrics for a pipeline run.
type Snapshot struct {
	Timestamp time.Time               `json:"timestamp"`
	Elapsed   time.Duration           `json:"elapsed_ns"`
	Stages    map[string]StageMetrics `json:"stages"`
	Graph     []GraphNode             `json:"graph,omitempty"`
	Buffers   []BufferStatus          `json:"buffers,omitempty"`
}

// JSON returns the snapshot serialized as indented JSON.
func (s Snapshot) JSON() ([]byte, error) {
	return json.MarshalIndent(s, "", "  ")
}

// ---------------------------------------------------------------------------
// MetricsHook
// ---------------------------------------------------------------------------

// stageState holds atomic counters for a single pipeline stage.
// The struct is allocated once and then accessed lock-free via atomics.
type stageState struct {
	processed atomic.Int64
	errors    atomic.Int64
	skipped   atomic.Int64
	dropped   atomic.Int64
	restarts  atomic.Int64
	totalNs   atomic.Int64
	minNs     atomic.Int64 // initialized to maxInt64; 0 means "no items yet"
	maxNs     atomic.Int64
	buckets   [numBuckets]atomic.Int64
}

const maxInt64 = int64(^uint64(0) >> 1)

// MetricsHook collects per-stage pipeline metrics accessible programmatically.
// All hot-path operations (OnItem, OnDrop) use lock-free atomic operations.
//
// MetricsHook implements [Hook], [OverflowHook], [SupervisionHook],
// [GraphHook], and [BufferHook]. Pass it to [WithHook] and call [Snapshot]
// or [Stage] at any time during or after execution to read metrics.
//
//	m := kitsune.NewMetricsHook()
//	err := runner.Run(ctx, kitsune.WithHook(m))
//	snap := m.Snapshot()
//	fmt.Printf("parse: %d items, mean latency %v\n",
//	    snap.Stages["parse"].Processed,
//	    snap.Stages["parse"].MeanLatency())
type MetricsHook struct {
	mu          sync.RWMutex
	stages      map[string]*stageState
	graph       []GraphNode
	bufferQuery func() []BufferStatus
	startedAt   time.Time
}

// NewMetricsHook creates a MetricsHook ready for use with [WithHook].
func NewMetricsHook() *MetricsHook {
	return &MetricsHook{
		stages:    make(map[string]*stageState),
		startedAt: time.Now(),
	}
}

// getOrCreate returns the stageState for the given stage name, creating it if
// it does not yet exist. After first creation the pointer is stable and all
// subsequent reads are lock-free.
func (h *MetricsHook) getOrCreate(stage string) *stageState {
	h.mu.RLock()
	s, ok := h.stages[stage]
	h.mu.RUnlock()
	if ok {
		return s
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok = h.stages[stage]; ok {
		return s
	}
	s = &stageState{}
	s.minNs.Store(maxInt64) // sentinel: no items seen yet
	h.stages[stage] = s
	return s
}

// OnStageStart implements [Hook]. Ensures the stage is registered.
func (h *MetricsHook) OnStageStart(_ context.Context, stage string) {
	h.getOrCreate(stage)
}

// OnItem implements [Hook]. All updates are lock-free atomic operations.
func (h *MetricsHook) OnItem(_ context.Context, stage string, dur time.Duration, err error) {
	s := h.getOrCreate(stage)

	if ns := dur.Nanoseconds(); ns > 0 {
		s.totalNs.Add(ns)
		for { // CAS min
			old := s.minNs.Load()
			if ns >= old {
				break
			}
			if s.minNs.CompareAndSwap(old, ns) {
				break
			}
		}
		for { // CAS max
			old := s.maxNs.Load()
			if ns <= old {
				break
			}
			if s.maxNs.CompareAndSwap(old, ns) {
				break
			}
		}
		// Update histogram bucket.
		for i := 0; i < numBuckets; i++ {
			if ns < bucketBounds[i+1] {
				s.buckets[i].Add(1)
				break
			}
		}
	}

	if err == nil {
		s.processed.Add(1)
	} else if err == engine.ErrSkipped {
		s.skipped.Add(1)
	} else {
		s.errors.Add(1)
	}
}

// OnStageDone implements [Hook].
func (h *MetricsHook) OnStageDone(_ context.Context, _ string, _, _ int64) {}

// OnDrop implements [OverflowHook].
func (h *MetricsHook) OnDrop(_ context.Context, stage string, _ any) {
	h.getOrCreate(stage).dropped.Add(1)
}

// OnStageRestart implements [SupervisionHook].
func (h *MetricsHook) OnStageRestart(_ context.Context, stage string, _ int, _ error) {
	h.getOrCreate(stage).restarts.Add(1)
}

// OnGraph implements [GraphHook].
func (h *MetricsHook) OnGraph(nodes []GraphNode) {
	h.mu.Lock()
	h.graph = nodes
	h.mu.Unlock()
}

// OnBuffers implements [BufferHook].
func (h *MetricsHook) OnBuffers(query func() []BufferStatus) {
	h.mu.Lock()
	h.bufferQuery = query
	h.mu.Unlock()
}

// Stage returns a snapshot of the current metrics for the named stage.
// Returns zero [StageMetrics] if the stage is unknown.
func (h *MetricsHook) Stage(name string) StageMetrics {
	h.mu.RLock()
	s, ok := h.stages[name]
	h.mu.RUnlock()
	if !ok {
		return StageMetrics{}
	}
	minNs := s.minNs.Load()
	if minNs == maxInt64 {
		minNs = 0 // no items processed yet
	}
	var buckets [numBuckets]int64
	for i := range buckets {
		buckets[i] = s.buckets[i].Load()
	}
	return StageMetrics{
		Processed: s.processed.Load(),
		Errors:    s.errors.Load(),
		Skipped:   s.skipped.Load(),
		Dropped:   s.dropped.Load(),
		Restarts:  s.restarts.Load(),
		TotalNs:   s.totalNs.Load(),
		MinNs:     minNs,
		MaxNs:     s.maxNs.Load(),
		Buckets:   buckets,
	}
}

// Snapshot returns a point-in-time capture of all stage metrics.
// Safe to call concurrently with an active pipeline.
func (h *MetricsHook) Snapshot() Snapshot {
	now := time.Now()

	h.mu.RLock()
	names := make([]string, 0, len(h.stages))
	for name := range h.stages {
		names = append(names, name)
	}
	graph := h.graph
	bufferQuery := h.bufferQuery
	h.mu.RUnlock()

	stages := make(map[string]StageMetrics, len(names))
	for _, name := range names {
		stages[name] = h.Stage(name)
	}

	snap := Snapshot{
		Timestamp: now,
		Elapsed:   now.Sub(h.startedAt),
		Stages:    stages,
		Graph:     graph,
	}
	if bufferQuery != nil {
		snap.Buffers = bufferQuery()
	}
	return snap
}
