// Package kprometheus provides a Prometheus metrics hook for Kitsune pipelines.
//
// PrometheusHook implements hooks.Hook plus the optional OverflowHook and
// SupervisionHook interfaces. Pass it to kitsune.WithHook to record per-stage
// counters and latency histograms against any prometheus.Registerer.
//
// Usage:
//
//	reg  := prometheus.NewRegistry() // or prometheus.DefaultRegisterer
//	hook := kprometheus.New(reg, "myapp")
//	runner.Run(ctx, kitsune.WithHook(hook))
//
// The hook records:
//   - <ns>_stage_items_total: CounterVec{stage, status="ok"|"error"|"skipped"}
//   - <ns>_stage_duration_seconds: HistogramVec{stage}
//   - <ns>_stage_drops_total: CounterVec{stage}
//   - <ns>_stage_restarts_total: CounterVec{stage}
package kprometheus

import (
	"context"
	"time"

	"github.com/zenbaku/go-kitsune/hooks"
	"github.com/prometheus/client_golang/prometheus"
)

// PrometheusHook records Kitsune pipeline events as Prometheus metrics.
// Create with [New].
type PrometheusHook struct {
	items    *prometheus.CounterVec
	duration *prometheus.HistogramVec
	drops    *prometheus.CounterVec
	restarts *prometheus.CounterVec
}

// Verify interface compliance at compile time.
var (
	_ hooks.Hook            = (*PrometheusHook)(nil)
	_ hooks.OverflowHook    = (*PrometheusHook)(nil)
	_ hooks.SupervisionHook = (*PrometheusHook)(nil)
)

// New creates a PrometheusHook that registers metrics with reg under the given
// namespace. All metrics are registered eagerly; a registration conflict panics
// to surface configuration mistakes at startup rather than silently dropping data.
//
// namespace is used as the Prometheus metric namespace (prefix), e.g. "myapp"
// produces "myapp_stage_items_total". Pass "" for no prefix.
func New(reg prometheus.Registerer, namespace string) *PrometheusHook {
	items := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "stage_items_total",
		Help:      "Total number of items processed by each stage.",
	}, []string{"stage", "status"})
	reg.MustRegister(items)

	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "stage_duration_seconds",
		Help:      "Item processing duration per stage in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"stage"})
	reg.MustRegister(duration)

	drops := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "stage_drops_total",
		Help:      "Total number of items dropped due to buffer overflow.",
	}, []string{"stage"})
	reg.MustRegister(drops)

	restarts := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "stage_restarts_total",
		Help:      "Total number of stage restarts due to supervision.",
	}, []string{"stage"})
	reg.MustRegister(restarts)

	return &PrometheusHook{
		items:    items,
		duration: duration,
		drops:    drops,
		restarts: restarts,
	}
}

// OnStageStart implements hooks.Hook.
func (h *PrometheusHook) OnStageStart(_ context.Context, _ string) {}

// OnItem implements hooks.Hook.
func (h *PrometheusHook) OnItem(_ context.Context, stage string, dur time.Duration, err error) {
	status := "ok"
	if err != nil {
		if err.Error() == "kitsune: item skipped" {
			status = "skipped"
		} else {
			status = "error"
		}
	}
	h.items.WithLabelValues(stage, status).Inc()
	if dur > 0 {
		h.duration.WithLabelValues(stage).Observe(dur.Seconds())
	}
}

// OnStageDone implements hooks.Hook.
func (h *PrometheusHook) OnStageDone(_ context.Context, _ string, _ int64, _ int64) {}

// OnDrop implements hooks.OverflowHook.
func (h *PrometheusHook) OnDrop(_ context.Context, stage string, _ any) {
	h.drops.WithLabelValues(stage).Inc()
}

// OnStageRestart implements hooks.SupervisionHook.
func (h *PrometheusHook) OnStageRestart(_ context.Context, stage string, _ int, _ error) {
	h.restarts.WithLabelValues(stage).Inc()
}
