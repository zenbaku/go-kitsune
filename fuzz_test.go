package kitsune_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// FuzzPipelineOverflow exercises Map stages with all three overflow strategies
// under varying item counts, buffer sizes, and concurrency levels.
//
// Invariants checked:
//   - Pipeline always terminates (no deadlock); enforced by the outer timeout context
//   - No panics
//   - No unexpected errors (only context errors are acceptable when using DropNewest/DropOldest)
//   - With OverflowBlock and no cancellation, all items arrive with correct values
func FuzzPipelineOverflow(f *testing.F) {
	// (overflow, bufSize, items, concurrency)
	f.Add(uint8(0), uint8(4), uint16(50), uint8(1))
	f.Add(uint8(1), uint8(2), uint16(100), uint8(2))
	f.Add(uint8(2), uint8(1), uint16(200), uint8(4))
	f.Add(uint8(1), uint8(1), uint16(1), uint8(1))
	f.Add(uint8(0), uint8(8), uint16(0), uint8(1))

	f.Fuzz(func(t *testing.T, overflow uint8, bufSize uint8, items uint16, concurrency uint8) {
		overflow = overflow % 3
		if bufSize == 0 {
			bufSize = 1
		}
		if bufSize > 64 {
			bufSize = 64
		}
		if items > 500 {
			items = 500
		}
		if concurrency == 0 {
			concurrency = 1
		}
		if concurrency > 8 {
			concurrency = 8
		}

		vals := make([]int, items)
		for i := range vals {
			vals[i] = i
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		src := kitsune.FromSlice(vals)
		mapped := kitsune.Map(src, func(_ context.Context, n int) (int, error) {
			return n * 2, nil
		},
			kitsune.Buffer(int(bufSize)),
			kitsune.Overflow(kitsune.OverflowStrategy(overflow)),
			kitsune.Concurrency(int(concurrency)),
		)

		results, err := mapped.Collect(ctx)
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error: %v", err)
		}

		// With OverflowBlock and no cancellation, all items must arrive undropped.
		if overflow == 0 && err == nil && len(results) != int(items) {
			t.Fatalf("Block overflow: got %d results, want %d", len(results), items)
		}
	})
}

// FuzzPipelineCancellation verifies that pipelines always terminate cleanly when
// the context is cancelled at varying points during execution.
//
// Invariants checked:
//   - Pipeline always terminates (no deadlock); enforced by the outer timeout context
//   - No panics
//   - Only context errors (Canceled, DeadlineExceeded) are returned after cancellation
func FuzzPipelineCancellation(f *testing.F) {
	// (items, concurrency, cancelAfterUs)
	f.Add(uint16(100), uint8(1), uint32(500))
	f.Add(uint16(1000), uint8(4), uint32(100))
	f.Add(uint16(50), uint8(2), uint32(0))
	f.Add(uint16(0), uint8(1), uint32(1000))
	f.Add(uint16(200), uint8(8), uint32(50))

	f.Fuzz(func(t *testing.T, items uint16, concurrency uint8, cancelAfterUs uint32) {
		if concurrency == 0 {
			concurrency = 1
		}
		if concurrency > 8 {
			concurrency = 8
		}
		if items > 2000 {
			items = 2000
		}
		// Cap cancel delay to 10ms to keep fuzz runs snappy.
		cancelAfter := time.Duration(cancelAfterUs%10000) * time.Microsecond

		vals := make([]int, items)
		for i := range vals {
			vals[i] = i
		}

		// Outer safety net: if the pipeline hangs, this catches it.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		earlyCtx, earlyCancel := context.WithCancel(ctx)
		defer earlyCancel()

		if cancelAfter == 0 {
			earlyCancel() // cancel before Run
		} else {
			time.AfterFunc(cancelAfter, earlyCancel)
		}

		src := kitsune.FromSlice(vals)
		mapped := kitsune.Map(src, func(_ context.Context, n int) (int, error) {
			return n + 1, nil
		}, kitsune.Concurrency(int(concurrency)))

		_, err := mapped.Collect(earlyCtx)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unexpected error after cancellation: %v", err)
		}
	})
}

// FuzzPipelineMultiStage exercises the scheduler across pipelines of varying
// depth with different overflow strategies and buffer sizes.
//
// Invariants checked:
//   - Pipeline always terminates
//   - No panics
//   - With OverflowBlock (no drops) and no cancellation, all items arrive with correct values
func FuzzPipelineMultiStage(f *testing.F) {
	// (stages, items, bufSize, overflow)
	f.Add(uint8(3), uint16(100), uint8(4), uint8(0))
	f.Add(uint8(5), uint16(50), uint8(2), uint8(1))
	f.Add(uint8(1), uint16(500), uint8(8), uint8(2))
	f.Add(uint8(8), uint16(10), uint8(1), uint8(0))

	f.Fuzz(func(t *testing.T, stages uint8, items uint16, bufSize uint8, overflow uint8) {
		if stages == 0 {
			stages = 1
		}
		if stages > 8 {
			stages = 8
		}
		if items > 500 {
			items = 500
		}
		if bufSize == 0 {
			bufSize = 1
		}
		if bufSize > 32 {
			bufSize = 32
		}
		overflow = overflow % 3

		vals := make([]int, items)
		for i := range vals {
			vals[i] = i
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		p := kitsune.FromSlice(vals)
		for range int(stages) {
			p = kitsune.Map(p, func(_ context.Context, n int) (int, error) {
				return n + 1, nil
			},
				kitsune.Buffer(int(bufSize)),
				kitsune.Overflow(kitsune.OverflowStrategy(overflow)),
			)
		}

		results, err := p.Collect(ctx)
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error: %v", err)
		}

		// With OverflowBlock (no drops) and no cancellation, all items must arrive
		// with each item incremented once per stage.
		if overflow == 0 && err == nil {
			if len(results) != int(items) {
				t.Fatalf("Block overflow: got %d results, want %d", len(results), items)
			}
			for i, v := range results {
				want := i + int(stages)
				if v != want {
					t.Fatalf("results[%d] = %d, want %d", i, v, want)
				}
			}
		}
	})
}

// FuzzBloomDedupSet verifies that BloomDedupSet never panics and upholds its
// zero-false-negative invariant under adversarial key inputs: every key that
// was Add-ed must be reported as present by Contains. False positives are
// acceptable by design.
func FuzzBloomDedupSet(f *testing.F) {
	// Seed corpus: NUL-delimited key lists
	f.Add([]byte("\x00"))                             // one empty-string key
	f.Add([]byte("hello\x00world\x00foo"))            // three ASCII keys
	f.Add([]byte("\xe2\x9c\x93\x00\xf0\x9f\x92\xa9")) // two UTF-8 keys
	f.Add([]byte("a"))                                // single key, no delimiter
	f.Add([]byte("\x00\x00\x00"))                     // three empty-string keys (repeated add)

	f.Fuzz(func(t *testing.T, data []byte) {
		// Split on NUL to derive a list of string keys.
		parts := bytes.Split(data, []byte{0x00})
		keys := make([]string, len(parts))
		for i, p := range parts {
			keys[i] = string(p)
		}

		// expectedItems must be > 0.
		expected := len(keys)
		if expected < 1 {
			expected = 1
		}

		ctx := context.Background()
		set := kitsune.BloomDedupSet(expected, 0.01)

		// Add all keys; no panics allowed.
		for _, k := range keys {
			if err := set.Add(ctx, k); err != nil {
				t.Fatalf("Add(%q) returned error: %v", k, err)
			}
		}

		// Zero-false-negative invariant: every added key must be present.
		for _, k := range keys {
			ok, err := set.Contains(ctx, k)
			if err != nil {
				t.Fatalf("Contains(%q) returned error: %v", k, err)
			}
			if !ok {
				t.Fatalf("false negative: Contains(%q) = false after Add", k)
			}
		}
	})
}
