package kitsune

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
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
	ordered      bool
	buffer       int
	overflow     internal.Overflow
	errorHandler internal.ErrorHandler
	batchTimeout time.Duration
	supervision  internal.SupervisionPolicy
	cacheConfig  *stageCacheConfig
	timeout      time.Duration
	dedupSet     DedupSet
	clock        internal.Clock
}

// stageCacheConfig holds cache settings for a single Map stage.
type stageCacheConfig struct {
	keyFn any           // func(I) string, type-erased
	cache Cache         // explicit per-stage backend; nil = use runner default
	ttl   time.Duration // explicit per-stage TTL; 0 = use runner default
}

type runConfig struct {
	store           Store
	hook            Hook
	drainTimeout    time.Duration
	defaultCache    Cache
	defaultCacheTTL time.Duration
	sampleRate      int // 0 = default (10); negative = disabled
	codec           Codec
	gate            *Gate
}

func buildStageConfig(opts []StageOption) stageConfig {
	cfg := stageConfig{
		concurrency:  1,
		buffer:       internal.DefaultBuffer,
		errorHandler: internal.DefaultHandler{},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

func buildRunConfig(opts []RunOption) runConfig {
	cfg := runConfig{hook: internal.NoopHook{}}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// ---------------------------------------------------------------------------
// Stage options
// ---------------------------------------------------------------------------

// Concurrency sets the number of parallel workers for a stage (default: 1).
// With n > 1, output order is NOT preserved unless [Ordered] is also set.
func Concurrency(n int) StageOption {
	return func(c *stageConfig) {
		if n > 0 {
			c.concurrency = n
		}
	}
}

// Ordered preserves input order for concurrent stages. Use with [Concurrency]
// to process items in parallel while emitting results in the original input order.
// Without Concurrency(n) where n > 1, Ordered has no effect.
// Supported on [Map] and [FlatMap] stages; has no effect on [ForEach].
func Ordered() StageOption {
	return func(c *stageConfig) { c.ordered = true }
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

// WithClock sets the time source for time-sensitive stages (Window, Batch, Throttle, Debounce)
// and source operators (Ticker, Interval, Timer).
// Use testkit.NewTestClock() for deterministic, sleep-free tests.
func WithClock(c internal.Clock) StageOption {
	return func(cfg *stageConfig) { cfg.clock = c }
}

// WithDedupSet specifies the [DedupSet] backend for a [Dedupe] stage.
// Defaults to an in-process [MemoryDedupSet] when not set.
func WithDedupSet(s DedupSet) StageOption {
	return func(c *stageConfig) { c.dedupSet = s }
}

// Timeout sets a per-item deadline for [Map] and [FlatMap] stages.
// If the processing function does not return within d, its context is cancelled
// and the item fails with [context.DeadlineExceeded]. Each retry attempt
// (when combined with [Retry]) gets a fresh timeout.
//
// Panics at pipeline construction time if used on any stage other than Map or FlatMap.
func Timeout(d time.Duration) StageOption {
	return func(c *stageConfig) { c.timeout = d }
}

// ---------------------------------------------------------------------------
// Overflow
// ---------------------------------------------------------------------------

// OverflowStrategy controls what happens when a stage's output buffer is full.
type OverflowStrategy = internal.Overflow

const (
	// Block waits until space is available (backpressure). This is the default.
	Block OverflowStrategy = internal.OverflowBlock
	// DropNewest discards the incoming item when the buffer is full.
	// The pipeline continues without blocking.
	DropNewest OverflowStrategy = internal.OverflowDropNewest
	// DropOldest evicts the oldest buffered item to make room for the new one.
	DropOldest OverflowStrategy = internal.OverflowDropOldest
)

// Overflow sets the overflow strategy for a stage's output buffer (default: [Block]).
// Combine with [Buffer] to tune capacity:
//
//	kitsune.Buffer(64), kitsune.Overflow(kitsune.DropNewest)
//
// When [Ordered] is also set, dropped items create gaps but remaining items stay
// in their original relative order.
func Overflow(s OverflowStrategy) StageOption {
	return func(c *stageConfig) { c.overflow = s }
}

// ---------------------------------------------------------------------------
// Supervision
// ---------------------------------------------------------------------------

// SupervisionPolicy configures per-stage restart and panic-recovery behavior.
// The zero value means no restarts; panics propagate (default v1 behavior).
type SupervisionPolicy struct {
	// MaxRestarts is the maximum number of times the stage may be restarted.
	// 0 means no restarts.
	MaxRestarts int
	// Window resets the restart counter after this much time without a crash.
	// 0 means the counter is never reset.
	Window time.Duration
	// Backoff delays between restarts. nil means no delay.
	Backoff Backoff
	// OnPanic controls what happens when the stage goroutine panics.
	// Default (zero value): [PanicPropagate].
	OnPanic PanicAction
	// PanicOnly limits the restart budget to panics only.
	// When true, regular errors halt the pipeline immediately without consuming
	// any restart budget. Only meaningful when [OnPanic] is [PanicRestart].
	// Set automatically by [RestartOnPanic]; not needed with [RestartAlways].
	PanicOnly bool
}

// PanicAction configures what happens when a stage goroutine panics.
type PanicAction int

const (
	// PanicPropagate re-panics, crashing the pipeline (default).
	PanicPropagate PanicAction = iota
	// PanicRestart treats the panic as a restartable error.
	// The stage is restarted up to [SupervisionPolicy.MaxRestarts] times.
	PanicRestart
	// PanicSkip recovers and continues; the item that caused the panic is lost.
	PanicSkip
)

// RestartOnError returns a [SupervisionPolicy] that restarts the stage up to
// maxRestarts times on error. Panics still propagate.
func RestartOnError(maxRestarts int, b Backoff) SupervisionPolicy {
	return SupervisionPolicy{MaxRestarts: maxRestarts, Backoff: b, OnPanic: PanicPropagate}
}

// RestartOnPanic returns a [SupervisionPolicy] that restarts the stage up to
// maxRestarts times when it panics (panic is treated as a restartable error).
// Normal errors still halt the pipeline.
func RestartOnPanic(maxRestarts int, b Backoff) SupervisionPolicy {
	return SupervisionPolicy{MaxRestarts: maxRestarts, Backoff: b, OnPanic: PanicRestart, PanicOnly: true}
}

// RestartAlways returns a [SupervisionPolicy] that restarts the stage on both
// errors and panics, up to maxRestarts times.
func RestartAlways(maxRestarts int, b Backoff) SupervisionPolicy {
	return SupervisionPolicy{MaxRestarts: maxRestarts, Backoff: b, OnPanic: PanicRestart}
}

// Supervise sets the supervision policy for a stage.
// See [RestartOnError], [RestartOnPanic], [RestartAlways] for convenience constructors.
func Supervise(policy SupervisionPolicy) StageOption {
	return func(c *stageConfig) {
		c.supervision = internal.SupervisionPolicy{
			MaxRestarts: policy.MaxRestarts,
			Window:      policy.Window,
			Backoff:     policy.Backoff,
			OnPanic:     internal.PanicAction(policy.OnPanic),
			PanicOnly:   policy.PanicOnly,
		}
	}
}

// ---------------------------------------------------------------------------
// Cache stage option
// ---------------------------------------------------------------------------

// CacheOpt configures per-stage cache overrides on a [Cache] stage option.
type CacheOpt func(*stageCacheConfig)

// CacheTTL overrides the TTL for a cached stage.
// If not set, the runner's default TTL (from [WithCache]) is used.
func CacheTTL(ttl time.Duration) CacheOpt {
	return func(c *stageCacheConfig) { c.ttl = ttl }
}

// CacheBackend overrides the cache backend for a cached stage.
// If not set, the runner's default cache (from [WithCache]) is used.
func CacheBackend(cache Cache) CacheOpt {
	return func(c *stageCacheConfig) { c.cache = cache }
}

// CacheBy returns a [StageOption] that enables caching on a [Map] stage.
// keyFn extracts the cache key from each input item. On a cache hit the map
// function is skipped; on a miss the function is called and the result stored.
//
// CacheBy is only supported on [Map]. Passing it to any other stage (FlatMap,
// Filter, etc.) panics at pipeline construction time.
//
// By default the runner's cache backend and TTL ([WithCache]) are used.
// Override either per-stage with [CacheBackend] and [CacheTTL].
func CacheBy[I any](keyFn func(I) string, opts ...CacheOpt) StageOption {
	cfg := &stageCacheConfig{keyFn: keyFn}
	for _, opt := range opts {
		opt(cfg)
	}
	return func(c *stageConfig) { c.cacheConfig = cfg }
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

// WithDrain enables graceful shutdown. When the pipeline's context is cancelled,
// sources are told to stop producing and the pipeline is given up to timeout for
// in-flight items to drain through the remaining stages naturally.
//
// Without this option, context cancellation immediately abandons all in-flight
// work and any partial batches are dropped. With it, items already in the
// pipeline continue processing until channels drain, then the pipeline exits
// cleanly. If the drain takes longer than timeout, a hard stop is forced.
func WithDrain(timeout time.Duration) RunOption {
	return func(c *runConfig) { c.drainTimeout = timeout }
}

// WithCache sets the default cache backend and TTL for all [Map] stages that
// use the [Cache] stage option without an explicit [CacheBackend] override.
//
//	runner.Run(ctx, kitsune.WithCache(myCache, 10*time.Minute))
func WithCache(cache Cache, ttl time.Duration) RunOption {
	return func(c *runConfig) {
		c.defaultCache = cache
		c.defaultCacheTTL = ttl
	}
}

// WithSampleRate sets how often [SampleHook.OnItemSample] is called for each
// stage. A rate of n means every n-th successfully processed item triggers a
// sample call. The default is 10. Pass a negative value to disable sampling.
//
//	runner.Run(ctx, kitsune.WithSampleRate(100)) // sample every 100th item
//	runner.Run(ctx, kitsune.WithSampleRate(-1))  // disable sampling
func WithSampleRate(n int) RunOption {
	return func(c *runConfig) { c.sampleRate = n }
}

// WithCodec sets the serialisation codec used by [Store]-backed state ([MapWith],
// [FlatMapWith]) and [CacheBy] stages. The default is JSON. Use this to
// substitute a binary format such as encoding/gob, protobuf, or msgpack:
//
//	runner.Run(ctx, kitsune.WithCodec(myGobCodec))
func WithCodec(c Codec) RunOption {
	return func(cfg *runConfig) { cfg.codec = c }
}

// WithPauseGate attaches an externally-created [Gate] to the pipeline run,
// enabling pause/resume control from another goroutine. Use this with
// [Runner.Run] when you need to pause the pipeline without going through
// [RunHandle]:
//
//	gate := kitsune.NewGate()
//	go func() {
//	    time.Sleep(5 * time.Second)
//	    gate.Pause()
//	    // ... maintenance window ...
//	    gate.Resume()
//	}()
//	runner.Run(ctx, kitsune.WithPauseGate(gate))
//
// For [Runner.RunAsync], a gate is created automatically and exposed on the
// returned [RunHandle] via [RunHandle.Pause] and [RunHandle.Resume].
func WithPauseGate(g *Gate) RunOption {
	return func(cfg *runConfig) { cfg.gate = g }
}

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

// ErrorHandler configures how a stage responds to errors from its processing function.
type ErrorHandler struct {
	h internal.ErrorHandler
}

// Backoff computes a wait duration for a given retry attempt (0-indexed).
type Backoff func(attempt int) time.Duration

// Halt returns an ErrorHandler that stops the pipeline on the first error.
// This is the default behavior.
func Halt() ErrorHandler { return ErrorHandler{h: internal.DefaultHandler{}} }

// Skip returns an ErrorHandler that drops the failing item and continues.
func Skip() ErrorHandler { return ErrorHandler{h: &skipHandler{}} }

// Return returns an [ErrorHandler] that replaces the failed item with val and continues.
// Use with [OnError] to substitute a default value on failure instead of dropping
// the item ([Skip]) or halting the pipeline ([Halt]):
//
//	kitsune.OnError(kitsune.Return(User{Name: "unknown"}))
//
// Return can be composed as a fallback after retries:
//
//	kitsune.OnError(kitsune.RetryThen(3, kitsune.FixedBackoff(time.Second), kitsune.Return(User{})))
//
// Note: in [FlatMap] stages, Return behaves like [Skip] because FlatMap has no
// single replacement value to emit.
func Return[T any](val T) ErrorHandler {
	return ErrorHandler{h: &returnHandler{val: val}}
}

// Retry returns an ErrorHandler that retries up to n times with the given
// backoff strategy, then halts.
func Retry(n int, b Backoff) ErrorHandler {
	return ErrorHandler{h: &retryHandler{max: n, bo: b, fallback: internal.DefaultHandler{}}}
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

func (*skipHandler) Handle(error, int) internal.ErrorAction     { return internal.ActionSkip }
func (*skipHandler) Backoff() func(attempt int) time.Duration   { return nil }

type returnHandler struct {
	val any
}

func (h *returnHandler) Handle(error, int) internal.ErrorAction     { return internal.ActionReturn }
func (h *returnHandler) Backoff() func(attempt int) time.Duration   { return nil }
func (h *returnHandler) ReturnValue() any                           { return h.val }

type retryHandler struct {
	max      int
	bo       Backoff
	fallback internal.ErrorHandler
}

func (h *retryHandler) Handle(err error, attempt int) internal.ErrorAction {
	if attempt < h.max {
		return internal.ActionRetry
	}
	return h.fallback.Handle(err, 0)
}

func (h *retryHandler) Backoff() func(int) time.Duration { return h.bo }

func (h *retryHandler) ReturnValue() any {
	if r, ok := h.fallback.(internal.Returner); ok {
		return r.ReturnValue()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Observability
// ---------------------------------------------------------------------------

// LogHook returns a [Hook] that logs pipeline events via the given [slog.Logger].
func LogHook(logger *slog.Logger) Hook {
	return &logHook{logger: logger}
}

// MultiHook combines multiple hooks into a single hook. Every registered hook
// receives all events. Optional extension interfaces ([OverflowHook],
// [SupervisionHook], [SampleHook], [GraphHook], [BufferHook]) are called only
// for the sub-hooks that implement them.
//
//	err := runner.Run(ctx, kitsune.WithHook(kitsune.MultiHook(
//	    kitsune.LogHook(slog.Default()),
//	    insp, // inspector implements all extension interfaces
//	)))
func MultiHook(hooks ...Hook) Hook {
	return &multiHook{hooks: hooks}
}

type multiHook struct{ hooks []Hook }

func (m *multiHook) OnStageStart(ctx context.Context, stage string) {
	for _, h := range m.hooks {
		h.OnStageStart(ctx, stage)
	}
}

func (m *multiHook) OnItem(ctx context.Context, stage string, dur time.Duration, err error) {
	for _, h := range m.hooks {
		h.OnItem(ctx, stage, dur, err)
	}
}

func (m *multiHook) OnStageDone(ctx context.Context, stage string, processed int64, errors int64) {
	for _, h := range m.hooks {
		h.OnStageDone(ctx, stage, processed, errors)
	}
}

func (m *multiHook) OnDrop(ctx context.Context, stage string, item any) {
	for _, h := range m.hooks {
		if oh, ok := h.(OverflowHook); ok {
			oh.OnDrop(ctx, stage, item)
		}
	}
}

func (m *multiHook) OnStageRestart(ctx context.Context, stage string, attempt int, cause error) {
	for _, h := range m.hooks {
		if sh, ok := h.(SupervisionHook); ok {
			sh.OnStageRestart(ctx, stage, attempt, cause)
		}
	}
}

func (m *multiHook) OnItemSample(ctx context.Context, stage string, item any) {
	for _, h := range m.hooks {
		if sh, ok := h.(SampleHook); ok {
			sh.OnItemSample(ctx, stage, item)
		}
	}
}

func (m *multiHook) OnGraph(nodes []GraphNode) {
	for _, h := range m.hooks {
		if gh, ok := h.(GraphHook); ok {
			gh.OnGraph(nodes)
		}
	}
}

func (m *multiHook) OnBuffers(query func() []BufferStatus) {
	for _, h := range m.hooks {
		if bh, ok := h.(BufferHook); ok {
			bh.OnBuffers(query)
		}
	}
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

func (h *logHook) OnDrop(ctx context.Context, stage string, _ any) {
	h.logger.WarnContext(ctx, "item dropped (buffer full)", "stage", stage)
}
