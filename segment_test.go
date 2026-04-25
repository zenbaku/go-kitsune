package kitsune_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/zenbaku/go-kitsune"
)

// composableFunc is a non-Stage type that implements Composable, used in
// later tests to verify Then/Through truly accept the interface and not
// just Stage values.
type composableFunc[I, O any] func(*kitsune.Pipeline[I]) *kitsune.Pipeline[O]

func (f composableFunc[I, O]) Apply(p *kitsune.Pipeline[I]) *kitsune.Pipeline[O] {
	return f(p)
}

// TestComposable_StageSatisfies asserts that Stage[I,O] satisfies the
// Composable[I,O] interface. The check is done at compile time via assignment.
func TestComposable_StageSatisfies(t *testing.T) {
	var s kitsune.Stage[int, int] = func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v + 1, nil })
	}
	var c kitsune.Composable[int, int] = s
	got, err := kitsune.Collect(context.Background(), c.Apply(kitsune.FromSlice([]int{1, 2, 3})))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int{2, 3, 4}) {
		t.Errorf("got %v, want [2 3 4]", got)
	}
}

// TestComposable_ThenAcceptsComposable verifies Then accepts any value
// implementing Composable[I,M] / Composable[M,O], not only Stage values.
// We pass a composableFunc (non-Stage) on the left and a Stage on the right
// to prove heterogeneous composition works.
func TestComposable_ThenAcceptsComposable(t *testing.T) {
	increment := composableFunc[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v + 1, nil })
	})
	double := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v * 2, nil })
	})

	composed := kitsune.Then[int, int, int](increment, double)
	got, err := kitsune.Collect(context.Background(), composed.Apply(kitsune.FromSlice([]int{1, 2, 3})))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{4, 6, 8}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
