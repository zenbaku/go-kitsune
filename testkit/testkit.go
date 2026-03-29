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

	kitsune "github.com/jonathan/go-kitsune"
)

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
