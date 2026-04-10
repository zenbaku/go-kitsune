package kitsune_test

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/testkit"
)

// ---------------------------------------------------------------------------
// Pipeline.Describe
// ---------------------------------------------------------------------------

func TestDescribeSimpleChain(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, func(_ context.Context, v int) (int, error) { return v * 2, nil })
	filtered := kitsune.Filter(mapped, func(_ context.Context, v int) (bool, error) { return v > 2, nil })

	nodes := filtered.Describe()

	if len(nodes) != 3 {
		t.Fatalf("want 3 nodes, got %d", len(nodes))
	}

	// Topological order: source first, then map, then filter.
	kinds := []string{nodes[0].Kind, nodes[1].Kind, nodes[2].Kind}
	want := []string{"source", "map", "filter"}
	if !reflect.DeepEqual(kinds, want) {
		t.Errorf("want kinds %v, got %v", want, kinds)
	}

	// Inputs must reference earlier stage IDs.
	if len(nodes[0].Inputs) != 0 {
		t.Errorf("source node should have no inputs, got %v", nodes[0].Inputs)
	}
	if len(nodes[1].Inputs) != 1 || nodes[1].Inputs[0] != nodes[0].ID {
		t.Errorf("map node should reference source ID %d, got %v", nodes[0].ID, nodes[1].Inputs)
	}
	if len(nodes[2].Inputs) != 1 || nodes[2].Inputs[0] != nodes[1].ID {
		t.Errorf("filter node should reference map ID %d, got %v", nodes[1].ID, nodes[2].Inputs)
	}
}

func TestDescribeIntermediatePipeline(t *testing.T) {
	// Callable on a non-terminal *Pipeline[T] — the core new capability vs GraphHook.
	src := kitsune.FromSlice([]int{1, 2, 3})
	intermediate := kitsune.Map(src, func(_ context.Context, v int) (string, error) {
		return "x", nil
	})

	nodes := intermediate.Describe()
	if len(nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(nodes))
	}
	if nodes[1].Kind != "map" {
		t.Errorf("want last kind=map, got %q", nodes[1].Kind)
	}
}

func TestDescribeDoesNotRun(t *testing.T) {
	var calls atomic.Int64

	src := kitsune.Generate(func(_ context.Context, yield func(int) bool) error {
		calls.Add(1)
		yield(1)
		return nil
	})
	mapped := kitsune.Map(src, func(_ context.Context, v int) (int, error) { return v, nil })

	nodes := mapped.Describe()
	if len(nodes) == 0 {
		t.Fatal("expected nodes, got none")
	}
	if calls.Load() != 0 {
		t.Errorf("Describe must not execute user functions; generator was called %d time(s)", calls.Load())
	}
}

func TestDescribeIsRepeatable(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2})
	mapped := kitsune.Map(src, func(_ context.Context, v int) (int, error) { return v, nil })

	first := mapped.Describe()
	second := mapped.Describe()

	if len(first) != len(second) {
		t.Fatalf("repeated Describe returned different lengths: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Errorf("node %d: ID changed between calls: %d vs %d", i, first[i].ID, second[i].ID)
		}
		if first[i].Kind != second[i].Kind {
			t.Errorf("node %d: Kind changed: %q vs %q", i, first[i].Kind, second[i].Kind)
		}
	}
}

func TestDescribeFanOut(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	a, b := kitsune.Partition(src, func(v int) bool { return v%2 == 0 })

	nodesA := a.Describe()
	nodesB := b.Describe()

	if len(nodesA) == 0 || len(nodesB) == 0 {
		t.Fatal("expected non-empty node lists from partition branches")
	}

	// Both branches should reference the partition stage as their last input.
	lastA := nodesA[len(nodesA)-1]
	lastB := nodesB[len(nodesB)-1]
	if len(lastA.Inputs) == 0 {
		t.Error("partition branch A: terminal node has no inputs")
	}
	if len(lastB.Inputs) == 0 {
		t.Error("partition branch B: terminal node has no inputs")
	}
}

func TestDescribeMatchesGraphHook(t *testing.T) {
	// Describe on an intermediate pipeline returns the upstream stages only.
	// GraphHook receives all stages including the terminal (ForEach/Drain).
	// Verify that Describe's nodes appear as a prefix of the GraphHook nodes
	// with matching IDs, kinds, and names.
	src := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(src, func(_ context.Context, v int) (int, error) { return v * 2, nil })

	hook := &testkit.RecordingHook{}
	ctx := context.Background()
	if err := mapped.ForEach(func(_ context.Context, _ int) error { return nil }).Run(ctx, kitsune.WithHook(hook)); err != nil {
		t.Fatal(err)
	}

	graphNodes := hook.Graph()       // 3 nodes: source + map + for_each
	describeNodes := mapped.Describe() // 2 nodes: source + map

	if len(describeNodes) == 0 {
		t.Fatal("Describe returned no nodes")
	}
	if len(graphNodes) < len(describeNodes) {
		t.Fatalf("GraphHook got fewer nodes (%d) than Describe (%d)", len(graphNodes), len(describeNodes))
	}

	// The first len(describeNodes) nodes should match exactly.
	for i, d := range describeNodes {
		g := graphNodes[i]
		if g.ID != d.ID || g.Kind != d.Kind || g.Name != d.Name {
			t.Errorf("node %d mismatch: GraphHook=%+v Describe=%+v", i, g, d)
		}
	}
}

func TestDescribeCapturesMetadata(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	stage := kitsune.Map(src,
		func(_ context.Context, v int) (int, error) { return v, nil },
		kitsune.WithName("my-stage"),
		kitsune.Concurrency(4),
		kitsune.Buffer(16),
		kitsune.Timeout(500*time.Millisecond),
		kitsune.OnError(kitsune.Retry(3, kitsune.FixedBackoff(0))),
		kitsune.Supervise(kitsune.RestartOnError(1, nil)),
	)

	nodes := stage.Describe()
	if len(nodes) < 2 {
		t.Fatalf("expected at least 2 nodes, got %d", len(nodes))
	}

	n := nodes[len(nodes)-1] // Map node is last
	if n.Name != "my-stage" {
		t.Errorf("Name: want %q, got %q", "my-stage", n.Name)
	}
	if n.Concurrency != 4 {
		t.Errorf("Concurrency: want 4, got %d", n.Concurrency)
	}
	if n.Buffer != 16 {
		t.Errorf("Buffer: want 16, got %d", n.Buffer)
	}
	if n.Timeout != 500*time.Millisecond {
		t.Errorf("Timeout: want 500ms, got %v", n.Timeout)
	}
	if !n.HasRetry {
		t.Error("HasRetry: want true")
	}
	if !n.HasSupervision {
		t.Error("HasSupervision: want true")
	}
}
