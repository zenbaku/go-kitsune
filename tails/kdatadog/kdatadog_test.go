package kdatadog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DataDog/datadog-go/v5/statsd"
	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/tails/kdatadog"
)

// newClient returns a statsd client that discards all metrics.
// We connect to a local UDP port; packets are silently dropped if nothing is
// listening, which is fine for smoke tests.
func newClient(t *testing.T) *statsd.Client {
	t.Helper()
	client, err := statsd.New("localhost:0", statsd.WithoutTelemetry())
	if err != nil {
		t.Skipf("statsd.New: %v", err)
	}
	return client
}

func TestDatadogHookItems(t *testing.T) {
	client := newClient(t)
	defer client.Close()
	hook := kdatadog.New(client)

	_, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) { return v * 2, nil },
		kitsune.WithName("double"),
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}
}

func TestDatadogHookErrors(t *testing.T) {
	client := newClient(t)
	defer client.Close()
	hook := kdatadog.New(client)

	boom := errors.New("boom")
	_ = kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) { return 0, boom },
		kitsune.OnError(kitsune.ActionDrop()),
		kitsune.WithName("failing"),
	).ForEach(func(_ context.Context, v int) error { return nil }).
		Run(context.Background(), kitsune.WithHook(hook))
}

func TestDatadogHookDrops(t *testing.T) {
	client := newClient(t)
	defer client.Close()
	hook := kdatadog.New(client)

	const n = 500
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	err := kitsune.Map(
		kitsune.FromSlice(items),
		func(_ context.Context, v int) (int, error) { return v, nil },
		kitsune.Buffer(2),
		kitsune.Overflow(kitsune.DropNewest),
		kitsune.WithName("overflow"),
	).ForEach(func(_ context.Context, v int) error {
		time.Sleep(time.Millisecond)
		return nil
	}).Run(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}
}

func TestDatadogHookRestarts(t *testing.T) {
	client := newClient(t)
	defer client.Close()
	hook := kdatadog.New(client)

	var calls int
	_, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			calls++
			if calls == 1 {
				return 0, errors.New("transient")
			}
			return v * 2, nil
		},
		kitsune.Supervise(kitsune.RestartOnError(1, kitsune.FixedBackoff(0))),
		kitsune.WithName("supervised"),
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}
}
