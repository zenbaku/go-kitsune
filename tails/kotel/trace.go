package kotel

import (
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// NewWithTracing creates an OTelHook that records both metrics and
// stage-level distributed traces.
//
// Each pipeline stage gets one span, named "kitsune.stage.<name>", created as
// a child of the context passed to [Runner.Run]. Spans are started in
// [OnStageStart] and ended in [OnStageDone] with processed/error counts as
// attributes.
//
// Note: trace context is not propagated per-item (items flow through channels
// without a per-item context envelope). All stage spans share the same parent
// from the run context, giving useful pipeline-level traces without engine
// changes.
//
//	ctx, span := tracer.Start(ctx, "process-batch")
//	defer span.End()
//
//	hook := kotel.NewWithTracing(otel.Meter("my-app"), otel.Tracer("my-app"))
//	runner.Run(ctx, kitsune.WithHook(hook))
//	// → stage spans appear as children of "process-batch" in your trace backend
func NewWithTracing(meter metric.Meter, tracer trace.Tracer) *OTelHook {
	h := New(meter)
	h.tracer = tracer
	return h
}
