package testkit

import (
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// RecordingHook assertions
// ---------------------------------------------------------------------------

// AssertNoErrors fails the test if the hook recorded any item errors.
func AssertNoErrors(t testing.TB, hook *RecordingHook) {
	t.Helper()
	if errs := hook.Errors(); len(errs) != 0 {
		t.Errorf("testkit.AssertNoErrors: got %d error(s), want 0", len(errs))
	}
}

// AssertErrorCount fails the test if the total number of item errors does not
// equal want.
func AssertErrorCount(t testing.TB, hook *RecordingHook, want int) {
	t.Helper()
	got := len(hook.Errors())
	if got != want {
		t.Errorf("testkit.AssertErrorCount: got %d, want %d", got, want)
	}
}

// AssertDropCount fails the test if the total number of overflow drops does not
// equal want.
func AssertDropCount(t testing.TB, hook *RecordingHook, want int) {
	t.Helper()
	got := len(hook.Drops())
	if got != want {
		t.Errorf("testkit.AssertDropCount: got %d, want %d", got, want)
	}
}

// AssertRestartCount fails the test if the total number of supervision restarts
// does not equal want.
func AssertRestartCount(t testing.TB, hook *RecordingHook, want int) {
	t.Helper()
	got := len(hook.Restarts())
	if got != want {
		t.Errorf("testkit.AssertRestartCount: got %d, want %d", got, want)
	}
}

// AssertStageProcessed fails the test if the named stage's processed count
// (from its OnStageDone event) does not equal want.
// It also fails if no done event is found for the stage.
func AssertStageProcessed(t testing.TB, hook *RecordingHook, stage string, want int64) {
	t.Helper()
	for _, d := range hook.Dones() {
		if d.Stage == stage {
			if d.Processed != want {
				t.Errorf("testkit.AssertStageProcessed(%q): got %d, want %d", stage, d.Processed, want)
			}
			return
		}
	}
	t.Errorf("testkit.AssertStageProcessed: no done event for stage %q", stage)
}

// AssertStageErrors fails the test if the named stage's error count
// (from its OnStageDone event) does not equal want.
// It also fails if no done event is found for the stage.
func AssertStageErrors(t testing.TB, hook *RecordingHook, stage string, want int64) {
	t.Helper()
	for _, d := range hook.Dones() {
		if d.Stage == stage {
			if d.Errors != want {
				t.Errorf("testkit.AssertStageErrors(%q): got %d, want %d", stage, d.Errors, want)
			}
			return
		}
	}
	t.Errorf("testkit.AssertStageErrors: no done event for stage %q", stage)
}

// ---------------------------------------------------------------------------
// MetricsHook performance assertions
// ---------------------------------------------------------------------------

// AssertThroughputAbove fails the test if the named stage's throughput is
// below minPerSec. Elapsed time is taken from the hook's snapshot.
func AssertThroughputAbove(t testing.TB, hook *kitsune.MetricsHook, stage string, minPerSec float64) {
	t.Helper()
	snap := hook.Snapshot()
	m, ok := snap.Stages[stage]
	if !ok {
		t.Errorf("testkit.AssertThroughputAbove: unknown stage %q", stage)
		return
	}
	got := m.Throughput(snap.Elapsed)
	if got < minPerSec {
		t.Errorf("testkit.AssertThroughputAbove(%q): got %.2f/s, want >= %.2f/s", stage, got, minPerSec)
	}
}

// AssertMeanLatencyUnder fails the test if the named stage's mean per-item
// latency exceeds maxLatency.
func AssertMeanLatencyUnder(t testing.TB, hook *kitsune.MetricsHook, stage string, maxLatency time.Duration) {
	t.Helper()
	got := hook.Stage(stage).MeanLatency()
	if got > maxLatency {
		t.Errorf("testkit.AssertMeanLatencyUnder(%q): got %v, want <= %v", stage, got, maxLatency)
	}
}

// AssertPercentileUnder fails the test if the q-th percentile latency for the
// named stage exceeds maxLatency. q must be in [0, 1].
func AssertPercentileUnder(t testing.TB, hook *kitsune.MetricsHook, stage string, q float64, maxLatency time.Duration) {
	t.Helper()
	got := hook.Stage(stage).Percentile(q)
	if got > maxLatency {
		t.Errorf("testkit.AssertPercentileUnder(%q, p%.0f): got %v, want <= %v", stage, q*100, got, maxLatency)
	}
}

// AssertNoDropsMetrics fails the test if any stage in the hook recorded a
// non-zero drop count.
func AssertNoDropsMetrics(t testing.TB, hook *kitsune.MetricsHook) {
	t.Helper()
	snap := hook.Snapshot()
	for name, m := range snap.Stages {
		if m.Dropped > 0 {
			t.Errorf("testkit.AssertNoDropsMetrics: stage %q dropped %d items", name, m.Dropped)
		}
	}
}
