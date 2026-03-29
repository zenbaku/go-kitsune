package engine

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// failingOutbox returns sentinelErr after the first N successful sends.
// sent is an atomic counter so concurrent goroutines don't race on it.
type failingOutbox struct {
	ch          chan any
	failAfter   int32
	sent        atomic.Int32
	sentinelErr error
}

func (o *failingOutbox) Send(_ context.Context, item any) error {
	if o.sent.Load() >= o.failAfter {
		return o.sentinelErr
	}
	select {
	case o.ch <- item:
		o.sent.Add(1)
		return nil
	default:
		return nil
	}
}

func (o *failingOutbox) Dropped() int64 { return 0 }

// TestMergeOutboxSendError verifies that runMerge surfaces the first
// outbox.Send error rather than silently discarding it.
func TestMergeOutboxSendError(t *testing.T) {
	sentinel := errors.New("outbox failure")

	ch1 := make(chan any, 10)
	ch2 := make(chan any, 10)
	outCh := make(chan any, 10)

	// Write items to the input channels and close them.
	for i := range 5 {
		ch1 <- i
		ch2 <- i + 100
	}
	close(ch1)
	close(ch2)

	ob := &failingOutbox{ch: outCh, failAfter: 3, sentinelErr: sentinel} //nolint:govet

	err := runMerge(context.Background(), []chan any{ch1, ch2}, ob, NoopHook{}, "test-merge")
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}
}
