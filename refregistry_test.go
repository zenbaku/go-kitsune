package kitsune

import (
	"runtime"
	"sync"
	"testing"

	"github.com/zenbaku/go-kitsune/internal"
)

// White-box tests for refRegistry. Package kitsune (not kitsune_test) so we
// can reach the unexported type directly.

func TestRefRegistry_RegisterIsIdempotent(t *testing.T) {
	r := newRefRegistry()
	calls := 0
	f1 := func(_ internal.Store, _ internal.Codec) any { calls++; return "first" }
	f2 := func(_ internal.Store, _ internal.Codec) any { calls++; return "second" }

	r.register("k", f1)
	r.register("k", f2) // second registration must be silently ignored

	r.init(nil, nil)

	if got := r.get("k"); got != "first" {
		t.Fatalf("register idempotency: want %q, got %v", "first", got)
	}
	if calls != 1 {
		t.Fatalf("factory called %d times, want 1", calls)
	}
}

func TestRefRegistry_InitMaterialisesAllKeys(t *testing.T) {
	r := newRefRegistry()
	r.register("a", func(_ internal.Store, _ internal.Codec) any { return "alpha" })
	r.register("b", func(_ internal.Store, _ internal.Codec) any { return "beta" })
	r.register("c", func(_ internal.Store, _ internal.Codec) any { return "gamma" })

	r.init(nil, nil)

	for key, want := range map[string]string{"a": "alpha", "b": "beta", "c": "gamma"} {
		if got := r.get(key); got != want {
			t.Errorf("key %q: want %q, got %v", key, want, got)
		}
	}
}

func TestRefRegistry_InitIsIdempotent(t *testing.T) {
	r := newRefRegistry()
	calls := 0
	r.register("k", func(_ internal.Store, _ internal.Codec) any { calls++; return calls })

	r.init(nil, nil)
	r.init(nil, nil) // second init must not re-run the factory

	if calls != 1 {
		t.Fatalf("factory called %d times after two inits, want 1", calls)
	}
}

func TestRefRegistry_ConcurrentGet_Race(t *testing.T) {
	const numKeys = 5
	r := newRefRegistry()
	keys := []string{"k0", "k1", "k2", "k3", "k4"}
	for i, k := range keys {
		val := i // capture
		r.register(k, func(_ internal.Store, _ internal.Codec) any { return val })
	}
	r.init(nil, nil)

	// Many goroutines read all keys concurrently. Under -race any unsynchronised
	// access to r.vals would be reported immediately.
	workers := runtime.GOMAXPROCS(0) * 8
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for range 10_000 {
				for i, k := range keys {
					got := r.get(k)
					if got != i {
						// Use panic so the goroutine can report without t.Fatal
						// (which is not safe to call from non-test goroutines).
						panic("unexpected value")
					}
				}
			}
		}()
	}
	wg.Wait()
}
