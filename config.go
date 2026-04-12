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
	name            string
	concurrency     int
	ordered         bool
	buffer          int
	bufferExplicit  bool // true when Buffer(n) was called explicitly
	overflow        internal.Overflow
	errorHandler    internal.ErrorHandler
	batchTimeout    time.Duration
	supervision     internal.SupervisionPolicy
	cacheConfig     *stageCacheConfig
	timeout         time.Duration
	keyTTL          time.Duration // WithKeyTTL: evict per-key Ref after this much inactivity
	keyTTLExplicit  bool          // true when WithKeyTTL was called explicitly
	dedupSet        DedupSet
	clock           internal.Clock
	visitedKeyFn    any // func(T) string, type-erased; set by VisitedBy[T]
	contextMapperFn any // func(T) context.Context, type-erased; set by WithContextMapper[T]
}

// stageCacheConfig holds cache settings for a single Map stage.
type stageCacheConfig struct {
	keyFn any           // func(I) string, type-erased
	cache Cache         // explicit per-stage backend; nil = use runner default
	ttl   time.Duration // explicit per-stage TTL; 0 = use runner default
}

type runConfig struct {
	store               Store
	hook                Hook
	drainTimeout        time.Duration
	defaultCache        Cache
	defaultCacheTTL     time.Duration
	sampleRate          int // 0 = default (10); negative = disabled
	codec               Codec
	gate                *Gate
	defaultErrorHandler internal.ErrorHandler
	defaultBuffer       int           // 0 = use internal.DefaultBuffer (16)
	defaultKeyTTL       time.Duration // 0 = no eviction unless overridden per stage
}

func buildStageConfig(opts []StageOption) stageConfig {
	cfg := stageConfig{
		concurrency: 1,
		buffer:      internal.DefaultBuffer,
		// errorHandler intentionally nil — resolved at run time via resolveHandler,
		// which falls back to the pipeline-level WithErrorStrategy default, then
		// to internal.DefaultHandler (halt on first error).
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
// Overrides the run-level [WithDefaultBuffer] for this specific stage.
func Buffer(n int) StageOption {
	return func(c *stageConfig) {
		if n >= 0 {
			c.buffer = n
			c.bufferExplicit = true
		}
	}
}

// WithName labels the stage for metrics, traces, and debugging.
func WithName(name string) StageOption {
	return func(c *stageConfig) { c.name = name }
}

// OnError sets the error handling policy for a stage (default: [Halt]).
// OnError is evaluated per item: if the stage function returns an error, the
// handler decides whether to halt, drop, replace, or retry that item.
// To restart the entire stage goroutine when an error is not handled, combine
// with [Supervise].
func OnError(h ErrorHandler) StageOption {
	return func(c *stageConfig) { c.errorHandler = h.h }
}

// BatchTimeout sets the maximum time to wait before flushing a partial batch.
// Only meaningful when used with [Batch].
func BatchTimeout(d time.Duration) StageOption {
	return func(c *stageConfig) { c.batchTimeout = d }
}

// WithClock sets the time source for time-sensitive stages (Window, Batch, Throttle, Debounce)
// and source operators (Ticker, Timer).
// Use testkit.NewTestClock() for deterministic, sleep-free tests.
func WithClock(c internal.Clock) StageOption {
	return func(cfg *stageConfig) { cfg.clock = c }
}

// WithDedupSet specifies the [DedupSet] backend for a [Dedupe] or [ExpandMap]
// stage. Defaults to an in-process [MemoryDedupSet] when not set.
func WithDedupSet(s DedupSet) StageOption {
	return func(c *stageConfig) { c.dedupSet = s }
}

// VisitedBy enables cycle detection during [ExpandMap] graph walks. keyFn
// extracts a string key from each item; items whose key has already been seen
// are skipped, along with their entire subtree. Defaults to an in-process
// [MemoryDedupSet]; supply a different backend with [WithDedupSet].
//
// VisitedBy is only meaningful on [ExpandMap]. It is silently ignored on all
// other operators.
func VisitedBy[T any](keyFn func(T) string) StageOption {
	return func(c *stageConfig) {
		c.visitedKeyFn = keyFn
	}
}

// WithContextMapper extracts a context from each item using fn, enabling
// per-item context propagation (trace spans, baggage) without requiring the
// item type to implement [ContextCarrier]. The returned context contributes
// values only; cancellation and deadlines still come from the stage context.
//
// When set, WithContextMapper takes precedence over [ContextCarrier]: if the
// item type also implements ContextCarrier, only the mapper function is used
// and ContextCarrier.Context() is not called.
//
// WithContextMapper is supported on [Map], [FlatMap], and [ForEach].
//
// Example — propagating an OpenTelemetry span from a Kafka message without
// modifying the message type:
//
//	msgs := kitsune.FromChan(kafkaMsgs)
//	processed := kitsune.Map(msgs, processMsg,
//	    kitsune.WithContextMapper(func(m *sarama.ConsumerMessage) context.Context {
//	        return otel.GetTextMapPropagator().Extract(ctx, kafkaHeaderCarrier(m.Headers))
//	    }),
//	)
func WithContextMapper[T any](fn func(T) context.Context) StageOption {
	return func(c *stageConfig) {
		c.contextMapperFn = fn
	}
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

// WithKeyTTL sets an inactivity TTL for the per-entity state map used by
// [MapWithKey] and [FlatMapWithKey]. When no item has been observed for a
// given entity key for longer than d, the key's [Ref] is evicted from the
// internal map; the next item for that key starts with a fresh Ref at the
// key's initial value. Eviction is checked lazily on the next access; there
// is no background goroutine.
//
// Use this on long-running pipelines with unbounded key cardinality (user
// IDs, session tokens) to prevent unbounded memory growth.
//
// WithKeyTTL is independent of [StateTTL]: StateTTL expires the value held
// by a Ref; WithKeyTTL expires the map entry holding the Ref itself.
//
// A d of 0 disables stage-level eviction (the default). Per-stage
// WithKeyTTL overrides [WithDefaultKeyTTL] set at the runner level.
func WithKeyTTL(d time.Duration) StageOption {
	return func(c *stageConfig) {
		c.keyTTL = d
		c.keyTTLExplicit = true
	}
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
	// DropOldest evicts the oldest buffered item to make room for the incoming one.
	// When the buffer has space the send is lock-free. When the buffer is full a
	// sync.Mutex is held while the oldest item is drained and the new item is
	// inserted, serialising all concurrent senders on the slow path.
	//
	// Under sustained backpressure — exactly the scenario DropOldest is designed
	// for — the buffer is full most of the time, so the mutex slow path becomes
	// the hot path. With Concurrency(n), all n workers serialise on that lock.
	// Increase Buffer to keep the buffer from filling frequently and reduce time
	// on the slow path. For drop semantics without a mutex, consider DropNewest.
	// See "Overflow strategies" in doc/tuning.md for a full comparison.
	DropOldest OverflowStrategy = internal.OverflowDropOldest
)

// Overflow sets the overflow strategy for a stage's output buffer (default: [Block]).
// Combine with [Buffer] to tune capacity:
//
//	kitsune.Buffer(64), kitsune.Overflow(kitsune.DropNewest)
//
// When [Ordered] is also set, dropped items create gaps but remaining items stay
// in their original relative order.
//
// For performance trade-offs between strategies under high concurrency, see the
// "Overflow strategies" section of doc/tuning.md.
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
// When combined with [OnError], the error handler runs first per item; Supervise
// only triggers a restart when the error handler's final decision is Halt.
//
// Stateful stages and restarts: for [MapWith], [MapWithKey], [FlatMapWith], and
// [FlatMapWithKey], per-key [Ref] state is preserved across supervised restarts
// within the same [Run] call; the key map is allocated once per Run and captured
// by the restarted loop. State is NOT preserved when the process itself restarts
// (a new Run call) unless an external Store is configured via [WithStore]; the
// default in-memory store is scoped to a single Run.
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

// WithErrorStrategy sets the default error handling policy for all stages in
// the pipeline run. Stages that do not specify their own [OnError] option
// inherit this policy. Individual stages can override it with [OnError]:
//
//	// Skip bad items pipeline-wide; critical stages can still override.
//	runner.Run(ctx, kitsune.WithErrorStrategy(kitsune.ActionDrop()))
//
//	// Same default but one stage halts explicitly.
//	kitsune.Map(p, criticalFn, kitsune.OnError(kitsune.Halt()))
//
// Without this option, the default is [Halt] — the first error stops the
// pipeline. This option applies to [Map], [FlatMap], [ForEach], [MapWith],
// [FlatMapWith], [MapWithKey], [FlatMapWithKey], [SwitchMap], and
// [ExhaustMap]. It does not apply to [DeadLetter] or [MapResult], which
// have their own explicit routing semantics.
func WithErrorStrategy(h ErrorHandler) RunOption {
	return func(cfg *runConfig) { cfg.defaultErrorHandler = h.h }
}

// WithDefaultBuffer sets the channel buffer size for all stages in the pipeline
// run that do not specify their own [Buffer] option. The default when this
// option is not set is 16. Use smaller values (e.g. 0–4) for lower latency or
// memory use; use larger values (e.g. 64–256) for higher throughput.
//
// Per-stage [Buffer] takes precedence over this run-level default.
func WithDefaultBuffer(n int) RunOption {
	return func(cfg *runConfig) {
		if n >= 0 {
			cfg.defaultBuffer = n
		}
	}
}

// WithDefaultKeyTTL sets the default inactivity TTL for the per-entity state
// maps used by [MapWithKey] and [FlatMapWithKey] stages that do not specify
// their own [WithKeyTTL] option. When no item has been observed for a given
// entity key for longer than d, the key's [Ref] is evicted lazily from the
// internal map on the next access; the next item for that key starts fresh at
// the key's initial value.
//
// A value of 0 disables run-level eviction (the default). Per-stage
// [WithKeyTTL] takes precedence over this run-level default.
func WithDefaultKeyTTL(d time.Duration) RunOption {
	return func(cfg *runConfig) { cfg.defaultKeyTTL = d }
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

// ActionDrop returns an ErrorHandler that drops the failing item and continues.
// Use with [OnError] or [WithErrorStrategy] to silently skip items that fail
// processing rather than halting the pipeline:
//
//	kitsune.OnError(kitsune.ActionDrop())
func ActionDrop() ErrorHandler { return ErrorHandler{h: &skipHandler{}} }

// Skip returns an ErrorHandler that drops the failing item and continues.
//
// Deprecated: use [ActionDrop] instead. Skip will be removed in a future version.
func Skip() ErrorHandler { return ActionDrop() }

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
//
// Type safety caveat: [ErrorHandler] is not parameterized on the stage's output
// type, so the type parameter T on Return is inferred only from val and is not
// checked against the stage's output type at compile time. If T does not match
// the stage's output type, the substitution silently fails at runtime: the
// original error is propagated as though [Halt] had been used, and no replacement
// value is emitted. To stay safe, prefer letting the compiler infer T from a
// typed variable that matches the stage's output type:
//
//	var fallback User
//	kitsune.Map(orders, fetchUser, kitsune.OnError(kitsune.Return(fallback)))
//
// rather than passing an untyped literal whose type may drift from the stage
// signature. For a compile-time guarantee, use [TypedReturn] instead.
func Return[T any](val T) ErrorHandler {
	return ErrorHandler{h: &returnHandler{val: val}}
}

// TypedReturn is a type-safe alternative to [OnError]([Return](val)).
// Unlike [Return], the output type O is fixed at the call site, so a mismatch
// between val and the stage's output type is a compile-time error rather than
// a silent runtime fallback to [Halt]:
//
//	kitsune.Map(orders, fetchUser,
//	    kitsune.TypedReturn[User](User{Name: "unknown"}),
//	)
//
// TypedReturn returns a [StageOption] directly and cannot be composed as a
// fallback inside [RetryThen] or [RetryIfThen]. For composed chains use
// [Return] with a typed variable to get the compiler's help:
//
//	var fallback User
//	kitsune.OnError(kitsune.RetryThen(3, kitsune.FixedBackoff(time.Second), kitsune.Return(fallback)))
//
// Note: in [FlatMap] stages, TypedReturn behaves like [ActionDrop] because
// FlatMap has no single replacement value to emit.
func TypedReturn[O any](val O) StageOption {
	return func(c *stageConfig) { c.errorHandler = &typedReturnHandler[O]{val: val} }
}

// RetryMax returns an ErrorHandler that retries up to n times with the given
// backoff strategy, then halts.
func RetryMax(n int, b Backoff) ErrorHandler {
	return ErrorHandler{h: &retryHandler{max: n, bo: b, fallback: internal.DefaultHandler{}}}
}

// RetryThen returns an ErrorHandler that retries up to n times, then
// delegates to the fallback handler (e.g., [ActionDrop]).
func RetryThen(n int, b Backoff, fallback ErrorHandler) ErrorHandler {
	return ErrorHandler{h: &retryHandler{max: n, bo: b, fallback: fallback.h}}
}

// RetryIf returns an ErrorHandler that retries with backoff when predicate
// returns true for the error, and halts when it returns false. This enables
// per-error-type retry decisions:
//
//	kitsune.OnError(kitsune.RetryIf(
//	    func(err error) bool { return errors.Is(err, io.ErrTimeout) },
//	    kitsune.ExponentialBackoff(100*time.Millisecond, 5*time.Second),
//	))
func RetryIf(predicate func(error) bool, b Backoff) ErrorHandler {
	return ErrorHandler{h: &retryIfHandler{predicate: predicate, bo: b, fallback: internal.DefaultHandler{}}}
}

// RetryIfThen returns an ErrorHandler that retries with backoff when predicate
// returns true, and delegates to the fallback handler when it returns false.
// This lets you retry transient errors while dropping or halting on permanent ones:
//
//	kitsune.OnError(kitsune.RetryIfThen(
//	    func(err error) bool { return errors.Is(err, io.ErrTimeout) },
//	    kitsune.FixedBackoff(time.Second),
//	    kitsune.ActionDrop(), // permanent errors: drop the item
//	))
func RetryIfThen(predicate func(error) bool, b Backoff, fallback ErrorHandler) ErrorHandler {
	return ErrorHandler{h: &retryIfHandler{predicate: predicate, bo: b, fallback: fallback.h}}
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

func (*skipHandler) Handle(error, int) internal.ErrorAction   { return internal.ActionSkip }
func (*skipHandler) Backoff() func(attempt int) time.Duration { return nil }

type returnHandler struct {
	val any
}

func (h *returnHandler) Handle(error, int) internal.ErrorAction   { return internal.ActionReturn }
func (h *returnHandler) Backoff() func(attempt int) time.Duration { return nil }
func (h *returnHandler) ReturnValue() any                         { return h.val }

// typedReturnHandler stores the replacement value as its concrete type O.
// Because ReturnValue returns h.val whose dynamic type is already O, the
// val.(O) assertion in ProcessItem is guaranteed to succeed.
type typedReturnHandler[O any] struct {
	val O
}

func (h *typedReturnHandler[O]) Handle(error, int) internal.ErrorAction   { return internal.ActionReturn }
func (h *typedReturnHandler[O]) Backoff() func(attempt int) time.Duration { return nil }
func (h *typedReturnHandler[O]) ReturnValue() any                         { return h.val }

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

type retryIfHandler struct {
	predicate func(error) bool
	bo        Backoff
	fallback  internal.ErrorHandler
}

func (h *retryIfHandler) Handle(err error, _ int) internal.ErrorAction {
	if h.predicate(err) {
		return internal.ActionRetry
	}
	return h.fallback.Handle(err, 0)
}

func (h *retryIfHandler) Backoff() func(int) time.Duration { return h.bo }

func (h *retryIfHandler) ReturnValue() any {
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
