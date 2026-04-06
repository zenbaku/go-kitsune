package engine

import (
	"context"
	"sync"
	"testing"
	"time"
)

// FuzzOutboxDropNewest exercises the drop-newest outbox under concurrent senders
// with fuzzed buffer sizes and send counts.
//
// Invariants checked:
//   - No panics (caught by the fuzz framework)
//   - No data races (run with -race)
//   - Drop count is non-negative after all senders complete
func FuzzOutboxDropNewest(f *testing.F) {
	f.Add(uint8(4), uint8(2), uint16(20))
	f.Add(uint8(1), uint8(8), uint16(100))
	f.Add(uint8(16), uint8(1), uint16(5))
	f.Add(uint8(1), uint8(1), uint16(1))

	f.Fuzz(func(t *testing.T, bufSize uint8, senders uint8, items uint16) {
		if bufSize == 0 {
			bufSize = 1
		}
		if senders == 0 {
			senders = 1
		}
		if senders > 16 {
			senders = 16
		}
		if items > 500 {
			items = 500
		}

		ch := make(chan any, int(bufSize))
		ob := NewOutbox(ch, OverflowDropNewest, NoopHook{}, "fuzz-drop-newest")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var wg sync.WaitGroup
		for range int(senders) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				perSender := max(1, int(items)/int(senders))
				for range perSender {
					ob.Send(ctx, 1) //nolint:errcheck
				}
			}()
		}
		wg.Wait()

		dropped := ob.Dropped()
		if dropped < 0 {
			t.Fatalf("negative drop count: %d", dropped)
		}
	})
}

// FuzzOutboxDropOldest exercises the drop-oldest outbox under concurrent senders
// with fuzzed buffer sizes and send counts.
//
// Invariants checked:
//   - No panics (caught by the fuzz framework)
//   - No data races (run with -race)
//   - Drop count is non-negative after all senders complete
//   - Channel length never exceeds capacity
func FuzzOutboxDropOldest(f *testing.F) {
	f.Add(uint8(4), uint8(2), uint16(20))
	f.Add(uint8(1), uint8(8), uint16(100))
	f.Add(uint8(16), uint8(1), uint16(5))
	f.Add(uint8(1), uint8(1), uint16(1))

	f.Fuzz(func(t *testing.T, bufSize uint8, senders uint8, items uint16) {
		if bufSize == 0 {
			bufSize = 1
		}
		if senders == 0 {
			senders = 1
		}
		if senders > 16 {
			senders = 16
		}
		if items > 500 {
			items = 500
		}

		ch := make(chan any, int(bufSize))
		ob := NewOutbox(ch, OverflowDropOldest, NoopHook{}, "fuzz-drop-oldest")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var wg sync.WaitGroup
		for range int(senders) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				perSender := max(1, int(items)/int(senders))
				for range perSender {
					ob.Send(ctx, 1) //nolint:errcheck
				}
			}()
		}
		wg.Wait()

		dropped := ob.Dropped()
		if dropped < 0 {
			t.Fatalf("negative drop count: %d", dropped)
		}
		if len(ch) > int(bufSize) {
			t.Fatalf("channel over capacity: len=%d cap=%d", len(ch), bufSize)
		}
	})
}
