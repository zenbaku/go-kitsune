package kitsune_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// TTLDedupSet
// ---------------------------------------------------------------------------

func TestTTLDedupSetBasicAddContains(t *testing.T) {
	ctx := context.Background()
	set := kitsune.TTLDedupSet(5 * time.Second)

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
	ok, err := set.Contains(ctx, "z")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Contains(\"z\") = true for never-inserted key")
	}
}

func TestTTLDedupSetExpiresAfterTTL(t *testing.T) {
	ctx := context.Background()
	set := kitsune.TTLDedupSet(20 * time.Millisecond)

	if err := set.Add(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	ok, err := set.Contains(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Contains(\"x\") = false immediately after Add")
	}

	time.Sleep(40 * time.Millisecond)

	ok, err = set.Contains(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("Contains(\"x\") = true after TTL elapsed, expected expiry")
	}
}

func TestTTLDedupSetReAddRefreshesTTL(t *testing.T) {
	ctx := context.Background()
	const ttl = 40 * time.Millisecond
	set := kitsune.TTLDedupSet(ttl)

	if err := set.Add(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	// Sleep for half the TTL then re-Add to refresh expiry.
	time.Sleep(ttl / 2)
	if err := set.Add(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	// Sleep past the original TTL but not past the refreshed one.
	time.Sleep(ttl/2 + 5*time.Millisecond)

	ok, err := set.Contains(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Contains(\"x\") = false after re-Add should have refreshed TTL")
	}
}

func TestTTLDedupSetPanicsOnBadInput(t *testing.T) {
	cases := []struct {
		name string
		ttl  time.Duration
	}{
		{"zero ttl", 0},
		{"negative ttl", -1 * time.Millisecond},
		{"large negative", -time.Hour},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("TTLDedupSet(%v) did not panic", tc.ttl)
				}
			}()
			kitsune.TTLDedupSet(tc.ttl)
		})
	}
}

func TestTTLDedupSetConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	set := kitsune.TTLDedupSet(5 * time.Second)
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

func TestTTLDedupSetWithDistinctBy(t *testing.T) {
	ctx := context.Background()
	set := kitsune.TTLDedupSet(5 * time.Second)
	p := kitsune.FromSlice([]int{1, 2, 1, 3, 2, 4})
	got, err := kitsune.Collect(ctx, kitsune.DistinctBy(p, func(v int) int { return v },
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

func TestTTLDedupSetMultipleKeysExpire(t *testing.T) {
	ctx := context.Background()
	const ttl = 20 * time.Millisecond
	set := kitsune.TTLDedupSet(ttl)

	keys := []string{"a", "b", "c", "d", "e"}
	for _, k := range keys {
		if err := set.Add(ctx, k); err != nil {
			t.Fatalf("Add(%q): %v", k, err)
		}
	}

	time.Sleep(2 * ttl)

	for _, k := range keys {
		ok, err := set.Contains(ctx, k)
		if err != nil {
			t.Fatalf("Contains(%q): %v", k, err)
		}
		if ok {
			t.Errorf("Contains(%q) = true after TTL, expected expiry", k)
		}
	}
}
