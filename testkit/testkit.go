// Package testkit provides helpers for testing Kitsune pipelines.
//
// It reduces boilerplate for common patterns: collecting pipeline output,
// asserting on results, and recording lifecycle events for inspection.
//
// # Quick start
//
//	func TestMyPipeline(t *testing.T) {
//	    p := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}),
//	        func(_ context.Context, v int) (int, error) { return v * 2, nil })
//	    testkit.CollectAndExpect(t, p, []int{2, 4, 6})
//	}
package testkit

import (
	"context"
	"slices"
	"testing"

	kitsune "github.com/zenbaku/go-kitsune"
)

// runnable is satisfied by *kitsune.Runner, *kitsune.DrainRunner[T],
// *kitsune.ForEachRunner[T], and any other terminal type that exposes Run.
type runnable interface {
	Run(context.Context, ...kitsune.RunOption) error
}

// MustRun runs the runner and calls t.Fatal if it returns an error.
func MustRun(t testing.TB, r runnable, opts ...kitsune.RunOption) {
	t.Helper()
	if err := r.Run(context.Background(), opts...); err != nil {
		t.Fatalf("testkit.MustRun: pipeline error: %v", err)
	}
}

// MustRunWithHook runs the runner with an automatically wired [RecordingHook]
// and calls t.Fatal on error.
func MustRunWithHook(t testing.TB, r runnable, opts ...kitsune.RunOption) *RecordingHook {
	t.Helper()
	hook := &RecordingHook{}
	allOpts := append([]kitsune.RunOption{kitsune.WithHook(hook)}, opts...)
	MustRun(t, r, allOpts...)
	return hook
}

// MustCollect runs the pipeline and returns all collected items.
// It calls t.Fatal if the pipeline returns an error.
func MustCollect[T any](t testing.TB, p *kitsune.Pipeline[T], opts ...kitsune.RunOption) []T {
	t.Helper()
	items, err := p.Collect(context.Background(), opts...)
	if err != nil {
		t.Fatalf("testkit.MustCollect: pipeline error: %v", err)
	}
	return items
}

// MustCollectWithHook runs the pipeline with an automatically wired
// [RecordingHook], collects all items, and calls t.Fatal on error.
func MustCollectWithHook[T any](t testing.TB, p *kitsune.Pipeline[T], opts ...kitsune.RunOption) ([]T, *RecordingHook) {
	t.Helper()
	hook := &RecordingHook{}
	allOpts := append([]kitsune.RunOption{kitsune.WithHook(hook)}, opts...)
	items := MustCollect(t, p, allOpts...)
	return items, hook
}

// CollectAndExpect runs the pipeline and asserts that the output equals expected
// in the same order. T must be comparable.
func CollectAndExpect[T comparable](t testing.TB, p *kitsune.Pipeline[T], expected []T, opts ...kitsune.RunOption) {
	t.Helper()
	got := MustCollect(t, p, opts...)
	if !slices.Equal(got, expected) {
		t.Errorf("testkit.CollectAndExpect:\n  got:  %v\n  want: %v", got, expected)
	}
}

// CollectAndExpectUnordered runs the pipeline and asserts that the output
// contains exactly the same elements as expected, regardless of order.
// T must be comparable.
func CollectAndExpectUnordered[T comparable](t testing.TB, p *kitsune.Pipeline[T], expected []T, opts ...kitsune.RunOption) {
	t.Helper()
	got := MustCollect(t, p, opts...)
	if !sameElements(got, expected) {
		t.Errorf("testkit.CollectAndExpectUnordered:\n  got:  %v\n  want: %v (any order)", got, expected)
	}
}

// sameElements returns true if a and b contain exactly the same multiset of elements.
func sameElements[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[T]int, len(a))
	for _, v := range a {
		counts[v]++
	}
	for _, v := range b {
		counts[v]--
		if counts[v] < 0 {
			return false
		}
	}
	return true
}
