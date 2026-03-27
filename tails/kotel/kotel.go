// Package kotel provides an OpenTelemetry metrics hook for Kitsune pipelines.
//
// OTelHook implements kitsune.Hook plus the optional OverflowHook,
// SupervisionHook, and GraphHook interfaces. Pass it to kitsune.WithHook to
// record per-stage counters and latency histograms using any OTel-compatible
// metrics backend.
//
// Usage:
//
//	meter := otel.Meter("my-app")
//	hook  := kotel.New(meter)
//	runner.Run(ctx, kitsune.WithHook(hook))
//
// The hook records:
//   - kitsune.stage.items        — Counter{stage, status="ok"|"error"|"skipped"}
//   - kitsune.stage.duration_ms  — Histogram[ms]{stage}
//   - kitsune.stage.drops        — Counter{stage}
//   - kitsune.stage.restarts     — Counter{stage}
//   - kitsune.pipeline.stages    — UpDownCounter (total stage count)
package kotel

import (
	"context"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// OTelHook records Kitsune pipeline events as OpenTelemetry metrics.
// Create with [New].
type OTelHook struct {
	items    metric.Int64Counter
	duration metric.Float64Histogram
	drops    metric.Int64Counter
	restarts metric.Int64Counter
	stages   metric.Int64UpDownCounter
}

// New creates an OTelHook using the provided meter.
// All metric instruments are created eagerly; any registration error panics
// to surface configuration mistakes at startup rather than silently dropping data.
func New(meter metric.Meter) *OTelHook {
	items, err := meter.Int64Counter(
		"kitsune.stage.items",
		metric.WithDescription("Number of items processed by each stage"),
		metric.WithUnit("{item}"),
	)
	if err != nil {
		panic("kotel: create kitsune.stage.items: " + err.Error())
	}

	duration, err := meter.Float64Histogram(
		"kitsune.stage.duration_ms",
		metric.WithDescription("Item processing duration per stage"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(0.01, 0.05, 0.1, 0.5, 1, 5, 10, 50, 100, 500, 1000),
	)
	if err != nil {
		panic("kotel: create kitsune.stage.duration_ms: " + err.Error())
	}

	dropsC, err := meter.Int64Counter(
		"kitsune.stage.drops",
		metric.WithDescription("Number of items dropped due to buffer overflow"),
		metric.WithUnit("{item}"),
	)
	if err != nil {
		panic("kotel: create kitsune.stage.drops: " + err.Error())
	}

	restartsC, err := meter.Int64Counter(
		"kitsune.stage.restarts",
		metric.WithDescription("Number of stage restarts due to supervision"),
		metric.WithUnit("{restart}"),
	)
	if err != nil {
		panic("kotel: create kitsune.stage.restarts: " + err.Error())
	}

	stagesC, err := meter.Int64UpDownCounter(
		"kitsune.pipeline.stages",
		metric.WithDescription("Number of stages in the pipeline"),
		metric.WithUnit("{stage}"),
	)
	if err != nil {
		panic("kotel: create kitsune.pipeline.stages: " + err.Error())
	}

	return &OTelHook{
		items:    items,
		duration: duration,
		drops:    dropsC,
		restarts: restartsC,
		stages:   stagesC,
	}
}

// OnStageStart implements kitsune.Hook.
func (h *OTelHook) OnStageStart(ctx context.Context, stage string) {}

// OnItem implements kitsune.Hook.
func (h *OTelHook) OnItem(ctx context.Context, stage string, dur time.Duration, err error) {
	status := "ok"
	if err != nil {
		if err.Error() == "kitsune: item skipped" {
			status = "skipped"
		} else {
			status = "error"
		}
	}
	attrs := metric.WithAttributes(
		attribute.String("stage", stage),
		attribute.String("status", status),
	)
	h.items.Add(ctx, 1, attrs)
	if dur > 0 {
		h.duration.Record(ctx, float64(dur.Nanoseconds())/1e6,
			metric.WithAttributes(attribute.String("stage", stage)))
	}
}

// OnStageDone implements kitsune.Hook.
func (h *OTelHook) OnStageDone(ctx context.Context, stage string, processed int64, errors int64) {}

// OnDrop implements kitsune.OverflowHook.
func (h *OTelHook) OnDrop(ctx context.Context, stage string, _ any) {
	h.drops.Add(ctx, 1, metric.WithAttributes(attribute.String("stage", stage)))
}

// OnStageRestart implements kitsune.SupervisionHook.
func (h *OTelHook) OnStageRestart(ctx context.Context, stage string, _ int, _ error) {
	h.restarts.Add(ctx, 1, metric.WithAttributes(attribute.String("stage", stage)))
}

// OnGraph implements kitsune.GraphHook.
func (h *OTelHook) OnGraph(nodes []kitsune.GraphNode) {
	h.stages.Add(context.Background(), int64(len(nodes)))
}
