package testkit_test

import (
	"context"
	"errors"
	"testing"

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
