package kotel_test

import (
	"context"
	"testing"

	kitsune "github.com/jonathan/go-kitsune"
	"github.com/jonathan/go-kitsune/tails/kotel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// newTestTracer creates an in-memory span exporter and a tracer backed by it.
func newTestTracer() (*tracetest.InMemoryExporter, *sdktrace.TracerProvider) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)
	return exporter, provider
}

func TestNewWithTracing_CreatesStageSpans(t *testing.T) {
	_, metricProvider := newTestMeter()
	defer metricProvider.Shutdown(context.Background())

	exporter, traceProvider := newTestTracer()
	defer traceProvider.Shutdown(context.Background())

	meter := metricProvider.Meter("test")
	tracer := traceProvider.Tracer("test")
	hook := kotel.NewWithTracing(meter, tracer)

	_, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, n int) (int, error) { return n * 2, nil },
		kitsune.WithName("double"),
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("expected at least one span, got none")
	}

	// Find the stage span.
	found := false
	for _, s := range spans {
		if s.Name == "kitsune.stage.double" {
			found = true
			break
		}
	}
	if !found {
		names := make([]string, len(spans))
		for i, s := range spans {
			names[i] = s.Name
		}
		t.Errorf("span 'kitsune.stage.double' not found; got: %v", names)
	}
}

func TestNewWithTracing_SpanEnded(t *testing.T) {
	_, metricProvider := newTestMeter()
	defer metricProvider.Shutdown(context.Background())

	exporter, traceProvider := newTestTracer()
	defer traceProvider.Shutdown(context.Background())

	hook := kotel.NewWithTracing(
		metricProvider.Meter("test"),
		traceProvider.Tracer("test"),
	)

	_, err := kitsune.Map(
		kitsune.FromSlice([]int{1}),
		func(_ context.Context, n int) (int, error) { return n, nil },
		kitsune.WithName("passthrough"),
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}

	spans := exporter.GetSpans()
	for _, s := range spans {
		if s.Name == "kitsune.stage.passthrough" {
			if s.EndTime.IsZero() {
				t.Error("span 'passthrough' was not ended")
			}
			return
		}
	}
	t.Error("span 'kitsune.stage.passthrough' not found")
}

func TestNewWithTracing_SpanHasAttributes(t *testing.T) {
	_, metricProvider := newTestMeter()
	defer metricProvider.Shutdown(context.Background())

	exporter, traceProvider := newTestTracer()
	defer traceProvider.Shutdown(context.Background())

	hook := kotel.NewWithTracing(
		metricProvider.Meter("test"),
		traceProvider.Tracer("test"),
	)

	_, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, n int) (int, error) { return n, nil },
		kitsune.WithName("attrs-stage"),
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}

	spans := exporter.GetSpans()
	for _, s := range spans {
		if s.Name == "kitsune.stage.attrs-stage" {
			// Check that processed count attribute was set.
			for _, a := range s.Attributes {
				if string(a.Key) == "kitsune.processed" && a.Value.AsInt64() >= 3 {
					return // found
				}
			}
			t.Errorf("span missing 'kitsune.processed' attribute; attrs: %v", s.Attributes)
			return
		}
	}
	t.Error("span 'kitsune.stage.attrs-stage' not found")
}

func TestNewWithTracing_NilTracerFallsBackToMetricsOnly(t *testing.T) {
	// NewWithTracing with a nil tracer should behave like New (no panics, no spans).
	_, metricProvider := newTestMeter()
	defer metricProvider.Shutdown(context.Background())

	hook := kotel.NewWithTracing(metricProvider.Meter("test"), nil)

	_, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, n int) (int, error) { return n, nil },
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}
	// No panics, no spans — metrics still work.
}

func TestNewWithTracing_MultipleStages(t *testing.T) {
	_, metricProvider := newTestMeter()
	defer metricProvider.Shutdown(context.Background())

	exporter, traceProvider := newTestTracer()
	defer traceProvider.Shutdown(context.Background())

	hook := kotel.NewWithTracing(
		metricProvider.Meter("test"),
		traceProvider.Tracer("test"),
	)

	p := kitsune.FromSlice([]int{1, 2, 3})
	p2 := kitsune.Map(p, func(_ context.Context, n int) (string, error) {
		return "x", nil
	}, kitsune.WithName("stringify"))
	p3 := kitsune.Map(p2, func(_ context.Context, s string) (string, error) {
		return s + "!", nil
	}, kitsune.WithName("exclaim"))

	_, err := p3.Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}

	spans := exporter.GetSpans()
	spanNames := make(map[string]bool)
	for _, s := range spans {
		spanNames[s.Name] = true
	}

	for _, want := range []string{"kitsune.stage.stringify", "kitsune.stage.exclaim"} {
		if !spanNames[want] {
			t.Errorf("expected span %q; got spans: %v", want, spanNames)
		}
	}
}

func TestNew_MetricsOnlyDoesNotCreateSpans(t *testing.T) {
	// Verify New() (no tracer) does not break even when called like NewWithTracing.
	_, metricProvider := newTestMeter()
	defer metricProvider.Shutdown(context.Background())

	hook := kotel.New(metricProvider.Meter("test"))

	_, err := kitsune.Map(
		kitsune.FromSlice([]int{1}),
		func(_ context.Context, n int) (int, error) { return n, nil },
	).Collect(context.Background(), kitsune.WithHook(hook))
	if err != nil {
		t.Fatal(err)
	}
}
