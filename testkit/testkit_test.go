package testkit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
	"github.com/jonathan/go-kitsune/testkit"
)

func TestRecordingHookDones(t *testing.T) {
	hook := &testkit.RecordingHook{}
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) { return v, nil },
		kitsune.WithName("stage-done-test"),
	)
	if err := p.Drain().Run(context.Background(), kitsune.WithHook(hook)); err != nil {
		t.Fatal(err)
	}
	dones := hook.Dones()
	if len(dones) == 0 {
		t.Fatal("expected at least one StageDoneEvent")
	}
	var found bool
	for _, d := range dones {
		if d.Stage == "stage-done-test" {
			found = true
			if d.Processed != 3 {
				t.Errorf("expected Processed=3, got %d", d.Processed)
			}
			if d.Errors != 0 {
				t.Errorf("expected Errors=0, got %d", d.Errors)
			}
		}
	}
	if !found {
		t.Errorf("no done event for 'stage-done-test'; got %v", dones)
	}
}

func TestRecordingHookItemsFor(t *testing.T) {
	hook := &testkit.RecordingHook{}
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) { return v, nil },
		kitsune.WithName("items-for-stage"),
	)
	if err := p.Drain().Run(context.Background(), kitsune.WithHook(hook), kitsune.WithSampleRate(1)); err != nil {
		t.Fatal(err)
	}
	all := hook.Items()
	forStage := hook.ItemsFor("items-for-stage")
	if len(forStage) == 0 {
		t.Fatal("expected items for 'items-for-stage'")
	}
	// ItemsFor must be a subset of Items
	if len(forStage) > len(all) {
		t.Errorf("ItemsFor returned more items (%d) than Items (%d)", len(forStage), len(all))
	}
	for _, e := range forStage {
		if e.Stage != "items-for-stage" {
			t.Errorf("ItemsFor returned event for wrong stage %q", e.Stage)
		}
	}
}

func TestRecordingHookDrops(t *testing.T) {
	hook := &testkit.RecordingHook{}
	// A bounded buffer with DropNewest policy will drop items when full.
	// Use a slow sink to cause backpressure.
	p := kitsune.FromSlice(make([]int, 20))
	p2 := kitsune.Map(p,
		func(_ context.Context, v int) (int, error) { return v, nil },
		kitsune.WithName("drop-stage"),
		kitsune.Buffer(1),
		kitsune.Overflow(kitsune.DropNewest),
	)
	if err := p2.Drain().Run(context.Background(), kitsune.WithHook(hook)); err != nil {
		t.Fatal(err)
	}
	// Drops may or may not occur depending on scheduling; we just verify
	// that if drops occurred they are captured correctly.
	for _, d := range hook.Drops() {
		if d.Stage == "" {
			t.Error("drop event has empty stage name")
		}
	}
}

func TestRecordingHookRestarts(t *testing.T) {
	hook := &testkit.RecordingHook{}
	attempts := 0
	boom := errors.New("transient")
	p := kitsune.Map(
		kitsune.FromSlice([]int{1}),
		func(_ context.Context, v int) (int, error) {
			attempts++
			if attempts <= 2 {
				return 0, boom
			}
			return v, nil
		},
		kitsune.WithName("supervised-stage"),
		kitsune.Supervise(kitsune.RestartOnError(3, kitsune.FixedBackoff(0))),
	)
	if err := p.Drain().Run(context.Background(), kitsune.WithHook(hook)); err != nil {
		t.Fatal(err)
	}
	restarts := hook.Restarts()
	if len(restarts) == 0 {
		t.Fatal("expected at least one RestartEvent")
	}
	for _, r := range restarts {
		if r.Stage != "supervised-stage" {
			t.Errorf("restart event has wrong stage %q", r.Stage)
		}
		if r.Cause == nil {
			t.Error("restart event has nil cause")
		}
	}
}

func TestMustCollect(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	got := testkit.MustCollect(t, p)
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}
}

func TestCollectAndExpect(t *testing.T) {
	p := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) { return v * 2, nil })
	testkit.CollectAndExpect(t, p, []int{2, 4, 6})
}

func TestCollectAndExpectUnordered(t *testing.T) {
	p := kitsune.FromSlice([]int{3, 1, 2})
	testkit.CollectAndExpectUnordered(t, p, []int{1, 2, 3})
}

func TestRecordingHookBasic(t *testing.T) {
	hook := &testkit.RecordingHook{}
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) { return v * 10, nil },
		kitsune.WithName("multiply"),
	)
	runner := p.Drain()
	if err := runner.Run(context.Background(), kitsune.WithHook(hook), kitsune.WithSampleRate(1)); err != nil {
		t.Fatal(err)
	}

	if len(hook.Starts()) == 0 {
		t.Error("expected at least one StageStartEvent")
	}
	if len(hook.Items()) == 0 {
		t.Error("expected at least one ItemEvent")
	}
	if len(hook.Errors()) != 0 {
		t.Errorf("expected no error events, got %d", len(hook.Errors()))
	}
	if len(hook.Graph()) == 0 {
		t.Error("expected graph snapshot")
	}
}

func TestRecordingHookErrors(t *testing.T) {
	hook := &testkit.RecordingHook{}
	boom := errors.New("fail")
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				return 0, boom
			}
			return v, nil
		},
		kitsune.OnError(kitsune.Skip()),
	)
	if err := p.Drain().Run(context.Background(), kitsune.WithHook(hook)); err != nil {
		t.Fatal(err)
	}
	errs := hook.Errors()
	if len(errs) != 1 {
		t.Errorf("expected 1 error event, got %d", len(errs))
	}
}

func TestRecordingHookGraph(t *testing.T) {
	hook := &testkit.RecordingHook{}
	p := kitsune.Map(
		kitsune.FromSlice([]int{1}),
		func(_ context.Context, v int) (int, error) { return v, nil },
		kitsune.WithName("stage-a"),
	)
	if err := p.Drain().Run(context.Background(), kitsune.WithHook(hook)); err != nil {
		t.Fatal(err)
	}
	graph := hook.Graph()
	if len(graph) == 0 {
		t.Fatal("expected graph nodes")
	}
	var found bool
	for _, n := range graph {
		if n.Name == "stage-a" {
			found = true
		}
	}
	if !found {
		t.Errorf("graph does not contain 'stage-a'; got %v", graph)
	}
}

// ---------------------------------------------------------------------------
// New helper tests
// ---------------------------------------------------------------------------

func TestMustRun(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	testkit.MustRun(t, p.Drain())
}

func TestMustRunWithHook(t *testing.T) {
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) { return v * 2, nil },
		kitsune.WithName("mul"),
	)
	hook := testkit.MustRunWithHook(t, p.Drain())
	testkit.AssertNoErrors(t, hook)
	testkit.AssertStageProcessed(t, hook, "mul", 3)
}

func TestMustCollectWithHook(t *testing.T) {
	p := kitsune.Map(
		kitsune.FromSlice([]int{10, 20}),
		func(_ context.Context, v int) (int, error) { return v, nil },
		kitsune.WithName("pass"),
	)
	items, hook := testkit.MustCollectWithHook(t, p)
	if len(items) != 2 {
		t.Errorf("got %d items, want 2", len(items))
	}
	testkit.AssertNoErrors(t, hook)
	testkit.AssertStageProcessed(t, hook, "pass", 2)
}

func TestAssertErrorCount(t *testing.T) {
	boom := errors.New("boom")
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				return 0, boom
			}
			return v, nil
		},
		kitsune.OnError(kitsune.Skip()),
	)
	_, hook := testkit.MustCollectWithHook(t, p)
	testkit.AssertErrorCount(t, hook, 1)
}

func TestAssertDropCount(t *testing.T) {
	hook := &testkit.RecordingHook{}
	// Fast producer into very small buffer with DropNewest; run enough items to
	// guarantee at least one drop.
	items := make([]int, 100)
	p := kitsune.Map(kitsune.FromSlice(items),
		func(_ context.Context, v int) (int, error) { return v, nil },
		kitsune.Buffer(1),
		kitsune.Overflow(kitsune.DropNewest),
		kitsune.WithName("dropper"),
	)
	if err := p.ForEach(func(_ context.Context, _ int) error {
		time.Sleep(time.Millisecond)
		return nil
	}).Run(context.Background(), kitsune.WithHook(hook)); err != nil {
		t.Fatal(err)
	}
	drops := hook.Drops()
	// We can't assert an exact count because of scheduling non-determinism,
	// but we can verify the assertion helper itself works for the observed count.
	testkit.AssertDropCount(t, hook, len(drops)) // should always pass
}

func TestAssertRestartCount(t *testing.T) {
	boom := errors.New("boom")
	attempts := 0
	p := kitsune.Map(
		kitsune.FromSlice([]int{1}),
		func(_ context.Context, v int) (int, error) {
			attempts++
			if attempts <= 1 {
				return 0, boom
			}
			return v, nil
		},
		kitsune.WithName("flaky"),
		kitsune.Supervise(kitsune.RestartOnError(2, kitsune.FixedBackoff(0))),
	)
	hook := testkit.MustRunWithHook(t, p.Drain())
	testkit.AssertRestartCount(t, hook, 1)
}

func TestAssertStageErrors(t *testing.T) {
	boom := errors.New("boom")
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2}),
		func(_ context.Context, v int) (int, error) {
			if v == 1 {
				return 0, boom
			}
			return v, nil
		},
		kitsune.WithName("errstage"),
		kitsune.OnError(kitsune.Skip()),
	)
	hook := testkit.MustRunWithHook(t, p.Drain())
	testkit.AssertStageErrors(t, hook, "errstage", 1)
}

func TestFailAt(t *testing.T) {
	boom := errors.New("boom")
	fn := testkit.FailAt[int](boom, 1, 3) // fail items at index 1 and 3
	p := kitsune.Map(kitsune.FromSlice([]int{0, 1, 2, 3, 4}), fn,
		kitsune.OnError(kitsune.Skip()))
	got := testkit.MustCollect(t, p)
	// Items at positions 1 and 3 are dropped; remaining: 0, 2, 4.
	if len(got) != 3 {
		t.Errorf("got %d items, want 3: %v", len(got), got)
	}
}

func TestFailEvery(t *testing.T) {
	boom := errors.New("boom")
	fn := testkit.FailEvery[int](boom, 2) // fail every 2nd item (positions 0, 2, 4)
	p := kitsune.Map(kitsune.FromSlice([]int{0, 1, 2, 3, 4}), fn,
		kitsune.OnError(kitsune.Skip()))
	got := testkit.MustCollect(t, p)
	// Items at positions 0, 2, 4 dropped; remaining: 1, 3.
	if len(got) != 2 {
		t.Errorf("got %d items, want 2: %v", len(got), got)
	}
}

func TestSlowMap(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping SlowMap timing test in short mode")
	}
	start := time.Now()
	fn := testkit.SlowMap[int](10 * time.Millisecond)
	p := kitsune.Map(kitsune.FromSlice([]int{1, 2}), fn)
	testkit.MustCollect(t, p)
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Errorf("SlowMap(%v) × 2 items took only %v, expected ≥ 20ms", 10*time.Millisecond, elapsed)
	}
}

func TestSlowSink(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping SlowSink timing test in short mode")
	}
	start := time.Now()
	testkit.MustRun(t, kitsune.FromSlice([]int{1, 2}).ForEach(testkit.SlowSink[int](10*time.Millisecond)))
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Errorf("SlowSink(%v) × 2 items took only %v, expected ≥ 20ms", 10*time.Millisecond, elapsed)
	}
}

// ---------------------------------------------------------------------------
// MetricsHook-based performance assertion tests
// ---------------------------------------------------------------------------

func TestAssertThroughputAbove_Passes(t *testing.T) {
	hook := kitsune.NewMetricsHook()
	p := kitsune.FromSlice(make([]int, 100))
	_, err := kitsune.Map(p, func(_ context.Context, n int) (int, error) { return n, nil },
		kitsune.WithName("fast")).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}
	// Throughput should be well above 1/s for an in-memory stage.
	testkit.AssertThroughputAbove(t, hook, "fast", 1.0)
}

func TestAssertMeanLatencyUnder_Passes(t *testing.T) {
	hook := kitsune.NewMetricsHook()
	_, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, n int) (int, error) { return n, nil },
		kitsune.WithName("q"),
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertMeanLatencyUnder(t, hook, "q", time.Second)
}

func TestAssertPercentileUnder_Passes(t *testing.T) {
	hook := kitsune.NewMetricsHook()
	_, err := kitsune.Map(
		kitsune.FromSlice(make([]int, 50)),
		func(_ context.Context, n int) (int, error) { return n, nil },
		kitsune.WithName("stage"),
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertPercentileUnder(t, hook, "stage", 0.99, time.Second)
}

func TestAssertNoDropsMetrics_Passes(t *testing.T) {
	hook := kitsune.NewMetricsHook()
	_, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, n int) (int, error) { return n, nil },
		kitsune.WithName("nodrop"),
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertNoDropsMetrics(t, hook)
}
