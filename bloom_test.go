package kitsune_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// BloomDedupSet
// ---------------------------------------------------------------------------

func TestBloomDedupSetBasic(t *testing.T) {
	ctx := context.Background()
	set := kitsune.BloomDedupSet(100, 0.01)

	for _, key := range []string{"a", "b", "c"} {
		if err := set.Add(ctx, key); err != nil {
			t.Fatalf("Add(%q): %v", key, err)
		}
	}
	for _, key := range []string{"a", "b", "c"} {
		ok, err := set.Contains(ctx, key)
		if err != nil {
			t.Fatalf("Contains(%q): %v", key, err)
		}
		if !ok {
			t.Errorf("Contains(%q) = false, want true", key)
		}
	}
	// "z" was never added — should be absent (deterministically for a tiny set).
	ok, err := set.Contains(ctx, "z")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Contains(\"z\") = true for never-inserted key in a 3-element filter — unexpected collision")
	}
}

func TestBloomDedupSetZeroFalseNegatives(t *testing.T) {
	ctx := context.Background()
	const n = 1000
	set := kitsune.BloomDedupSet(n, 0.01)

	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", i)
		if err := set.Add(ctx, keys[i]); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	for _, k := range keys {
		ok, err := set.Contains(ctx, k)
		if err != nil {
			t.Fatalf("Contains(%q): %v", k, err)
		}
		if !ok {
			t.Errorf("false negative for key %q (must never happen)", k)
		}
	}
}

func TestBloomDedupSetFalsePositiveRate(t *testing.T) {
	ctx := context.Background()
	const insertN = 10_000
	const probeN = 100_000
	const targetFP = 0.01
	const maxFP = 0.02 // 2× headroom

	set := kitsune.BloomDedupSet(insertN, targetFP)

	for i := 0; i < insertN; i++ {
		_ = set.Add(ctx, fmt.Sprintf("insert-%d", i))
	}

	falsePositives := 0
	for i := 0; i < probeN; i++ {
		key := fmt.Sprintf("probe-%d", i) // disjoint from insert keys
		ok, _ := set.Contains(ctx, key)
		if ok {
			falsePositives++
		}
	}
	observed := float64(falsePositives) / float64(probeN)
	if observed > maxFP {
		t.Errorf("false positive rate %.4f exceeds tolerance %.4f (%d/%d)", observed, maxFP, falsePositives, probeN)
	}
}

func TestBloomDedupSetConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	set := kitsune.BloomDedupSet(10_000, 0.01)
	const goroutines = 100
	const perGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				key := fmt.Sprintf("g%d-k%d", g, i)
				_ = set.Add(ctx, key)
				_, _ = set.Contains(ctx, key)
			}
		}()
	}
	wg.Wait()
}

func TestDedupeByWithBloom(t *testing.T) {
	ctx := context.Background()
	set := kitsune.BloomDedupSet(100, 0.01)
	p := kitsune.FromSlice([]int{1, 2, 1, 3, 2, 4})
	got, err := kitsune.Collect(ctx, kitsune.DedupeBy(p, func(v int) int { return v },
		kitsune.WithDedupSet(set),
	))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("got[%d]=%d, want %d", i, got[i], w)
		}
	}
}

func TestBloomDedupSetPanicsOnBadInput(t *testing.T) {
	cases := []struct {
		name string
		n    int
		p    float64
	}{
		{"zero expectedItems", 0, 0.01},
		{"negative expectedItems", -1, 0.01},
		{"zero fp rate", 100, 0.0},
		{"fp rate == 1", 100, 1.0},
		{"fp rate > 1", 100, 1.5},
		{"negative fp rate", 100, -0.1},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("BloomDedupSet(%d, %v) did not panic", tc.n, tc.p)
				}
			}()
			kitsune.BloomDedupSet(tc.n, tc.p)
		})
	}
}
