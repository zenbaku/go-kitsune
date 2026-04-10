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
// Per-item tracing: if your item type implements [kitsune.ContextCarrier]
// (i.e. has a Context() context.Context method carrying a trace span), the
// engine automatically threads that context into the stage function call. Stage
// functions can then call tracer.Start(ctx, "my-work") to create per-item
// child spans with no extra plumbing.
//
//	ctx, span := tracer.Start(ctx, "process-batch")
//	defer span.End()
//
//	hook := kotel.NewWithTracing(otel.Meter("my-app"), otel.Tracer("my-app"))
//	runner.Run(ctx, kitsune.WithHook(hook))
//	// → stage spans appear as children of "process-batch" in your trace backend
//	// → if items implement ContextCarrier, per-item child spans are also supported
func NewWithTracing(meter metric.Meter, tracer trace.Tracer) *OTelHook {
	h := New(meter)
	h.tracer = tracer
	return h
}
