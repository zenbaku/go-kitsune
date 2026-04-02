package typed_test

import (
	"context"
	"testing"

	"github.com/jonathan/go-kitsune/engine/typed"
)

func TestMapFilter(t *testing.T) {
	items := make([]int, 10)
	for i := range items {
		items[i] = i
	}
	p := typed.FromSlice(items)
	doubled := typed.Map(p, func(v int) int { return v * 2 })
	evens := typed.Filter(doubled, func(v int) bool { return v%4 == 0 })

	got, err := evens.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 0,2,4,6,8 → doubled → 0,4,8,12,16 → keep %4==0 → 0,4,8,12,16
	want := []int{0, 4, 8, 12, 16}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("got[%d] = %d, want %d", i, got[i], v)
		}
	}
}

func TestDrain(t *testing.T) {
	items := make([]int, 1000)
	for i := range items {
		items[i] = i
	}
	p := typed.FromSlice(items)
	mapped := typed.Map(p, func(v int) int { return v * 2 })
	if err := mapped.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMapTypeChange(t *testing.T) {
	items := []int{1, 2, 3}
	p := typed.FromSlice(items)
	strs := typed.Map(p, func(v int) string {
		return "x"
	})
	got, err := strs.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3", len(got))
	}
}
