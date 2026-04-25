package kitsune

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
)

// Compile-time interface checks.
var (
	_ Hook            = (*MetricsHook)(nil)
	_ OverflowHook    = (*MetricsHook)(nil)
	_ SupervisionHook = (*MetricsHook)(nil)
	_ GraphHook       = (*MetricsHook)(nil)
	_ BufferHook      = (*MetricsHook)(nil)
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

const maxInt64sentinel = int64(^uint64(0) >> 1)

// ---------------------------------------------------------------------------
// StageMetrics
// ---------------------------------------------------------------------------

// StageMetrics holds a point-in-time snapshot of one stage's counters.
type StageMetrics struct {
	Stage     string            `json:"stage"`
	Processed int64             `json:"processed"`
	Errors    int64             `json:"errors"`
	Skipped   int64             `json:"skipped"`  // items dropped via OnError(Skip())
	Dropped   int64             `json:"dropped"`  // items dropped due to buffer overflow
	Restarts  int64             `json:"restarts"` // stage restarts via supervision policy
	TotalNs   int64             `json:"total_ns"` // cumulative item processing time (ns)
	MinNs     int64             `json:"min_ns"`   // fastest single-item duration (ns)
	MaxNs     int64             `json:"max_ns"`   // slowest single-item duration (ns)
	Buckets   [numBuckets]int64 `json:"buckets"`  // latency histogram counts per bucket
}

// AvgLatency returns the mean per-item processing time for successfully processed
// items. Returns 0 if no items have been processed yet.
func (s StageMetrics) AvgLatency() time.Duration {
	if s.Processed == 0 {
		return 0
	}
	return time.Duration(s.TotalNs / s.Processed)
}

// MeanLatency returns the mean per-item processing time across all items
// (successful and errored). Returns 0 if no items recorded.
// Alias for cross-version compatibility; prefer [AvgLatency] for success-only mean.
func (s StageMetrics) MeanLatency() time.Duration {
	total := s.Processed + s.Errors
	if total == 0 || s.TotalNs == 0 {
		return 0
	}
	return time.Duration(s.TotalNs / total)
}

// Throughput returns the number of successfully processed items per second
// over the given elapsed duration. Returns 0 if elapsed ≤ 0 or no items processed.
func (s StageMetrics) Throughput(elapsed time.Duration) float64 {
	if elapsed <= 0 || s.Processed == 0 {
		return 0
	}
	return float64(s.Processed) / elapsed.Seconds()
}

// Percentile returns an estimate of the q-th percentile latency (0 ≤ q ≤ 1).
// It uses histogram bucket interpolation and assumes a uniform distribution
// within each bucket. Returns 0 if no items have been recorded.
func (s StageMetrics) Percentile(q float64) time.Duration {
	if q < 0 {
		q = 0
	}
	if q > 1 {
		q = 1
	}
	total := int64(0)
	for _, c := range s.Buckets {
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
		cumulative += s.Buckets[i]
		if cumulative >= target {
			lo := bucketBounds[i]
			hi := bucketBounds[i+1]
			if hi == math.MaxInt64 {
				return time.Duration(lo)
			}
			prev := cumulative - s.Buckets[i]
			fraction := float64(target-prev) / float64(s.Buckets[i])
			ns := float64(lo) + fraction*float64(hi-lo)
			return time.Duration(int64(ns))
		}
	}
	return time.Duration(bucketBounds[numBuckets-1])
}

// ErrorRate returns the fraction of items that resulted in an error, in [0, 1].
func (s StageMetrics) ErrorRate() float64 {
	total := s.Processed + s.Errors
	if total == 0 {
		return 0
	}
	return float64(s.Errors) / float64(total)
}

// ---------------------------------------------------------------------------
// stageState: atomic counters per stage
// ---------------------------------------------------------------------------

type stageState struct {
	processed atomic.Int64
	errors    atomic.Int64
	skipped   atomic.Int64
	dropped   atomic.Int64
	restarts  atomic.Int64
	totalNs   atomic.Int64
	minNs     atomic.Int64 // initialised to maxInt64sentinel; 0 means "no items yet"
	maxNs     atomic.Int64
	buckets   [numBuckets]atomic.Int64
}

func (s *stageState) snapshot(name string) StageMetrics {
	minNs := s.minNs.Load()
	if minNs == maxInt64sentinel {
		minNs = 0
	}
	var buckets [numBuckets]int64
	for i := range buckets {
		buckets[i] = s.buckets[i].Load()
	}
	return StageMetrics{
		Stage:     name,
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

// ---------------------------------------------------------------------------
// MetricsSnapshot
// ---------------------------------------------------------------------------

// MetricsSnapshot is a point-in-time capture of all stage metrics.
type MetricsSnapshot struct {
	Timestamp time.Time               `json:"timestamp"`
	Elapsed   time.Duration           `json:"elapsed_ns"`
	Stages    map[string]StageMetrics `json:"stages"`
	Graph     []GraphNode             `json:"graph,omitempty"`
	Buffers   []BufferStatus          `json:"buffers,omitempty"`
}

// JSON returns the snapshot serialised as indented JSON.
func (s MetricsSnapshot) JSON() ([]byte, error) {
	return json.MarshalIndent(s, "", "  ")
}

// ---------------------------------------------------------------------------
// MetricsHook
// ---------------------------------------------------------------------------

// MetricsHook collects per-stage pipeline metrics accessible programmatically.
// All hot-path operations (OnItem, OnDrop) use lock-free atomic operations.
//
// MetricsHook implements [Hook], [OverflowHook], [SupervisionHook],
// [GraphHook], and [BufferHook]. Pass it to [WithHook] and call [Snapshot]
// or [Stage] at any time during or after execution to read metrics.
//
//	m := kitsune.NewMetricsHook()
//	runner.Run(ctx, kitsune.WithHook(m))
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

// NewMetricsHook returns a ready-to-use [MetricsHook].
func NewMetricsHook() *MetricsHook {
	return &MetricsHook{
		stages:    make(map[string]*stageState),
		startedAt: time.Now(),
	}
}

// getOrCreate returns the stageState for the given stage, creating it if needed.
// After first creation the pointer is stable and subsequent reads are lock-free.
func (m *MetricsHook) getOrCreate(stage string) *stageState {
	m.mu.RLock()
	s, ok := m.stages[stage]
	m.mu.RUnlock()
	if ok {
		return s
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok = m.stages[stage]; ok {
		return s
	}
	s = &stageState{}
	s.minNs.Store(maxInt64sentinel)
	m.stages[stage] = s
	return s
}

// Stage returns the live counters for a single stage by name.
// Returns a zero-value snapshot if the stage has not been seen yet.
func (m *MetricsHook) Stage(name string) StageMetrics {
	m.mu.RLock()
	s, ok := m.stages[name]
	m.mu.RUnlock()
	if !ok {
		return StageMetrics{Stage: name}
	}
	return s.snapshot(name)
}

// Snapshot returns a point-in-time capture of all stage metrics.
// Safe to call concurrently with an active pipeline.
func (m *MetricsHook) Snapshot() MetricsSnapshot {
	now := time.Now()

	m.mu.RLock()
	names := make([]string, 0, len(m.stages))
	for name := range m.stages {
		names = append(names, name)
	}
	graph := m.graph
	bufferQuery := m.bufferQuery
	m.mu.RUnlock()

	stages := make(map[string]StageMetrics, len(names))
	for _, name := range names {
		stages[name] = m.Stage(name)
	}
	snap := MetricsSnapshot{
		Timestamp: now,
		Elapsed:   now.Sub(m.startedAt),
		Stages:    stages,
		Graph:     graph,
	}
	if bufferQuery != nil {
		snap.Buffers = bufferQuery()
	}
	return snap
}

// Reset clears all counters and resets the elapsed timer.
func (m *MetricsHook) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stages = make(map[string]*stageState)
	m.startedAt = time.Now()
}

// ---------------------------------------------------------------------------
// Hook interface implementation
// ---------------------------------------------------------------------------

func (m *MetricsHook) OnStageStart(_ context.Context, stage string) {
	m.getOrCreate(stage)
}

func (m *MetricsHook) OnItem(_ context.Context, stage string, dur time.Duration, err error) {
	s := m.getOrCreate(stage)

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
		for i := 0; i < numBuckets; i++ {
			if ns < bucketBounds[i+1] {
				s.buckets[i].Add(1)
				break
			}
		}
	}

	if err == nil {
		s.processed.Add(1)
	} else if err == internal.ErrSkipped {
		s.skipped.Add(1)
	} else {
		s.errors.Add(1)
	}
}

func (m *MetricsHook) OnStageDone(_ context.Context, _ string, _, _ int64) {}

// ---------------------------------------------------------------------------
// OverflowHook
// ---------------------------------------------------------------------------

func (m *MetricsHook) OnDrop(_ context.Context, stage string, _ any) {
	m.getOrCreate(stage).dropped.Add(1)
}

// ---------------------------------------------------------------------------
// SupervisionHook
// ---------------------------------------------------------------------------

func (m *MetricsHook) OnStageRestart(_ context.Context, stage string, _ int, _ error) {
	m.getOrCreate(stage).restarts.Add(1)
}

// ---------------------------------------------------------------------------
// GraphHook
// ---------------------------------------------------------------------------

func (m *MetricsHook) OnGraph(nodes []GraphNode) {
	m.mu.Lock()
	m.graph = nodes
	m.mu.Unlock()
}

// ---------------------------------------------------------------------------
// BufferHook
// ---------------------------------------------------------------------------

func (m *MetricsHook) OnBuffers(query func() []BufferStatus) {
	m.mu.Lock()
	m.bufferQuery = query
	m.mu.Unlock()
}
