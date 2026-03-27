package kitsune

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/jonathan/go-kitsune/internal/engine"
)

// ---------------------------------------------------------------------------
// Option types
// ---------------------------------------------------------------------------

// StageOption configures the behavior of an individual pipeline stage.
type StageOption func(*stageConfig)

// RunOption configures pipeline execution.
type RunOption func(*runConfig)

type stageConfig struct {
	name         string
	concurrency  int
	buffer       int
	errorHandler engine.ErrorHandler
	batchTimeout time.Duration
}

type runConfig struct {
	store Store
	hook  Hook
}

func buildStageConfig(opts []StageOption) stageConfig {
	cfg := stageConfig{
		concurrency:  1,
		buffer:       engine.DefaultBuffer,
		errorHandler: engine.DefaultHandler{},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

func buildRunConfig(opts []RunOption) runConfig {
	cfg := runConfig{hook: engine.NoopHook{}}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// newNode creates a standard engine.Node from a stage config and pipeline position.
func newNode[T any](kind engine.NodeKind, fn any, p *Pipeline[T], opts []StageOption) *engine.Node {
	cfg := buildStageConfig(opts)
	return &engine.Node{
		Kind:         kind,
		Name:         cfg.name,
		Fn:           fn,
		Inputs:       []engine.InputRef{{Node: p.node, Port: p.port}},
		Concurrency:  cfg.concurrency,
		Buffer:       cfg.buffer,
		ErrorHandler: cfg.errorHandler,
	}
}

// ---------------------------------------------------------------------------
// Stage options
// ---------------------------------------------------------------------------

// Concurrency sets the number of parallel workers for a stage (default: 1).
// With n > 1, output order is NOT preserved.
func Concurrency(n int) StageOption {
	return func(c *stageConfig) {
		if n > 0 {
			c.concurrency = n
		}
	}
}

// Buffer sets the channel buffer size between this stage and the next (default: 16).
func Buffer(n int) StageOption {
	return func(c *stageConfig) {
		if n >= 0 {
			c.buffer = n
		}
	}
}

// WithName labels the stage for metrics, traces, and debugging.
func WithName(name string) StageOption {
	return func(c *stageConfig) { c.name = name }
}

// OnError sets the error handling policy for a stage (default: [Halt]).
func OnError(h ErrorHandler) StageOption {
	return func(c *stageConfig) { c.errorHandler = h.h }
}

// BatchTimeout sets the maximum time to wait before flushing a partial batch.
// Only meaningful when used with [Batch].
func BatchTimeout(d time.Duration) StageOption {
	return func(c *stageConfig) { c.batchTimeout = d }
}

// ---------------------------------------------------------------------------
// Run options
// ---------------------------------------------------------------------------

// WithStore sets the state backend for the pipeline run (default: [MemoryStore]).
func WithStore(s Store) RunOption {
	return func(c *runConfig) { c.store = s }
}

// WithHook sets the observability hook for the pipeline run.
func WithHook(h Hook) RunOption {
	return func(c *runConfig) { c.hook = h }
}

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

// ErrorHandler configures how a stage responds to errors from its processing function.
type ErrorHandler struct {
	h engine.ErrorHandler
}

// Backoff computes a wait duration for a given retry attempt (0-indexed).
type Backoff func(attempt int) time.Duration

// Halt returns an ErrorHandler that stops the pipeline on the first error.
// This is the default behavior.
func Halt() ErrorHandler { return ErrorHandler{h: engine.DefaultHandler{}} }

// Skip returns an ErrorHandler that drops the failing item and continues.
func Skip() ErrorHandler { return ErrorHandler{h: &skipHandler{}} }

// Retry returns an ErrorHandler that retries up to n times with the given
// backoff strategy, then halts.
func Retry(n int, b Backoff) ErrorHandler {
	return ErrorHandler{h: &retryHandler{max: n, bo: b, fallback: engine.DefaultHandler{}}}
}

// RetryThen returns an ErrorHandler that retries up to n times, then
// delegates to the fallback handler (e.g., [Skip]).
func RetryThen(n int, b Backoff, fallback ErrorHandler) ErrorHandler {
	return ErrorHandler{h: &retryHandler{max: n, bo: b, fallback: fallback.h}}
}

// FixedBackoff returns a [Backoff] that always waits the same duration.
func FixedBackoff(d time.Duration) Backoff {
	return func(_ int) time.Duration { return d }
}

// ExponentialBackoff returns a [Backoff] that starts at initial and doubles
// each attempt, capped at max.
func ExponentialBackoff(initial, max time.Duration) Backoff {
	return func(attempt int) time.Duration {
		d := time.Duration(float64(initial) * math.Pow(2, float64(attempt)))
		if d > max {
			return max
		}
		return d
	}
}

type skipHandler struct{}

func (*skipHandler) Handle(error, int) engine.ErrorAction     { return engine.ActionSkip }
func (*skipHandler) Backoff() func(attempt int) time.Duration { return nil }

type retryHandler struct {
	max      int
	bo       Backoff
	fallback engine.ErrorHandler
}

func (h *retryHandler) Handle(err error, attempt int) engine.ErrorAction {
	if attempt < h.max {
		return engine.ActionRetry
	}
	return h.fallback.Handle(err, 0)
}

func (h *retryHandler) Backoff() func(int) time.Duration { return h.bo }

// ---------------------------------------------------------------------------
// Observability
// ---------------------------------------------------------------------------

// LogHook returns a [Hook] that logs pipeline events via the given [slog.Logger].
func LogHook(logger *slog.Logger) Hook {
	return &logHook{logger: logger}
}

type logHook struct{ logger *slog.Logger }

func (h *logHook) OnStageStart(ctx context.Context, stage string) {
	h.logger.InfoContext(ctx, "stage started", "stage", stage)
}

func (h *logHook) OnItem(ctx context.Context, stage string, dur time.Duration, err error) {
	if err != nil {
		h.logger.WarnContext(ctx, "item error", "stage", stage, "duration", dur, "error", err)
	}
}

func (h *logHook) OnStageDone(ctx context.Context, stage string, processed int64, errors int64) {
	h.logger.InfoContext(ctx, "stage done", "stage", stage, "processed", processed, "errors", errors)
}
