// Package kdatadog provides a Datadog DogStatsD metrics hook for Kitsune
// pipelines.
//
// DatadogHook implements kitsune.Hook plus the optional OverflowHook and
// SupervisionHook interfaces. Pass it to kitsune.WithHook to emit per-stage
// DogStatsD metrics via an existing [statsd.Client].
//
// Usage:
//
//	client, _ := statsd.New("127.0.0.1:8125", statsd.WithNamespace("myapp."))
//	defer client.Close()
//	hook := kdatadog.New(client)
//	runner.Run(ctx, kitsune.WithHook(hook))
//
// The hook records:
//   - stage.items_total    — Count{stage, status="ok"|"error"|"skipped"}
//   - stage.duration_ms    — Distribution{stage} (milliseconds)
//   - stage.drops_total    — Count{stage}
//   - stage.restarts_total — Count{stage}
package kdatadog

import (
	"context"
	"time"

	"github.com/DataDog/datadog-go/v5/statsd"
	kitsune "github.com/jonathan/go-kitsune"
)

// DatadogHook records Kitsune pipeline events as Datadog DogStatsD metrics.
// Create with [New].
type DatadogHook struct {
	client *statsd.Client
}

// Verify interface compliance at compile time.
var (
	_ kitsune.Hook            = (*DatadogHook)(nil)
	_ kitsune.OverflowHook    = (*DatadogHook)(nil)
	_ kitsune.SupervisionHook = (*DatadogHook)(nil)
)

// New creates a DatadogHook that sends metrics via client. The client is not
// closed when the pipeline ends — the caller owns it.
func New(client *statsd.Client) *DatadogHook {
	return &DatadogHook{client: client}
}

// OnStageStart implements kitsune.Hook.
func (h *DatadogHook) OnStageStart(_ context.Context, _ string) {}

// OnItem implements kitsune.Hook.
func (h *DatadogHook) OnItem(_ context.Context, stage string, dur time.Duration, err error) {
	status := "ok"
	if err != nil {
		if err.Error() == "kitsune: item skipped" {
			status = "skipped"
		} else {
			status = "error"
		}
	}
	tags := []string{"stage:" + stage, "status:" + status}
	_ = h.client.Count("stage.items_total", 1, tags, 1)
	if dur > 0 {
		_ = h.client.Distribution("stage.duration_ms", float64(dur.Milliseconds()), []string{"stage:" + stage}, 1)
	}
}

// OnStageDone implements kitsune.Hook.
func (h *DatadogHook) OnStageDone(_ context.Context, _ string, _ int64, _ int64) {}

// OnDrop implements kitsune.OverflowHook.
func (h *DatadogHook) OnDrop(_ context.Context, stage string, _ any) {
	_ = h.client.Count("stage.drops_total", 1, []string{"stage:" + stage}, 1)
}

// OnStageRestart implements kitsune.SupervisionHook.
func (h *DatadogHook) OnStageRestart(_ context.Context, stage string, _ int, _ error) {
	_ = h.client.Count("stage.restarts_total", 1, []string{"stage:" + stage}, 1)
}
