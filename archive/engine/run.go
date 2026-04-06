package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

// ---------------------------------------------------------------------------
// Ordered-stage slot types and pools
// ---------------------------------------------------------------------------

// mapSlot is a unit of work for runMapConcurrentOrdered.
// Slots are pooled to avoid per-item allocation of both the struct and its
// done channel. The done channel is buffered (cap 1) so that a worker can
// send without blocking regardless of whether the collector is ready.
type mapSlot struct {
	item   any
	result any
	err    error
	done   chan struct{}
}

var mapSlotPool = sync.Pool{
	New: func() any { return &mapSlot{done: make(chan struct{}, 1)} },
}

func getMapSlot(item any) *mapSlot {
	s := mapSlotPool.Get().(*mapSlot)
	s.item = item
	return s
}

func putMapSlot(s *mapSlot) {
	s.item = nil
	s.result = nil
	s.err = nil
	// Drain any unconsumed signal (defensive; should not happen in steady state).
	select {
	case <-s.done:
	default:
	}
	mapSlotPool.Put(s)
}

// flatMapSlot is the ordered-FlatMap equivalent of mapSlot.
type flatMapSlot struct {
	item    any
	results []any
	err     error
	done    chan struct{}
}

var flatMapSlotPool = sync.Pool{
	New: func() any { return &flatMapSlot{done: make(chan struct{}, 1)} },
}

func getFlatMapSlot(item any) *flatMapSlot {
	s := flatMapSlotPool.Get().(*flatMapSlot)
	s.item = item
	return s
}

func putFlatMapSlot(s *flatMapSlot) {
	s.item = nil
	s.results = s.results[:0]
	s.err = nil
	select {
	case <-s.done:
	default:
	}
	flatMapSlotPool.Put(s)
}

// receiveBatchSize is the maximum number of items drained per channel receive
// in the fast-path stage functions. One blocking receive is followed by up to
// receiveBatchSize-1 non-blocking drains, amortising per-channel-op overhead
// across a micro-batch while preserving per-item timing correctness.
const receiveBatchSize = 16

// RunConfig holds runtime options for a pipeline execution.
type RunConfig struct {
	Hook         Hook
	Store        Store         // nil means memory-only
	DrainTimeout time.Duration // 0 = no drain (default); >0 = graceful drain on context cancel

	// Default cache for Map stages that use CacheBy without an explicit backend.
	DefaultCache    Cache         // nil = no cache
	DefaultCacheTTL time.Duration // 0 = no expiry

	// SampleRate is the item interval at which [SampleHook.OnItemSample] is called.
	// 0 means use the default (10). Set to a negative value to disable sampling.
	SampleRate int

	// Codec serialises values for Store-backed state and CacheBy stages.
	// nil defaults to JSONCodec.
	Codec Codec

	// Gate, when non-nil, enables pause/resume of source stages.
	// Sources block in their yield callback while the gate is paused.
	// Downstream stages continue draining in-flight items naturally.
	Gate *Gate
}

// effectiveCodec returns c if non-nil, otherwise the default JSONCodec.
func effectiveCodec(c Codec) Codec {
	if c == nil {
		return JSONCodec{}
	}
	return c
}

// Run validates the graph, wires channels, and executes all stages.
func Run(ctx context.Context, g *Graph, cfg RunConfig) error {
	if err := Validate(g); err != nil {
		return err
	}

	g.InitRefs(cfg.Store, effectiveCodec(cfg.Codec))

	hook := cfg.Hook
	if hook == nil {
		hook = NoopHook{}
	}

	// Notify graph observer before any stage starts.
	if gh, ok := hook.(GraphHook); ok {
		gh.OnGraph(buildGraphNodes(g))
	}

	chans := CreateChannels(g)
	outboxes := CreateOutboxes(g, chans, hook)

	// Notify buffer observer with a query function that snapshots channel fill levels.
	if bh, ok := hook.(BufferHook); ok {
		nodes := g.Nodes
		bh.OnBuffers(func() []BufferStatus {
			out := make([]BufferStatus, 0, len(chans))
			for _, n := range nodes {
				if n.Kind == Sink {
					continue
				}
				name := n.Name
				if name == "" {
					name = kindName(n.Kind)
				}
				// For multi-port nodes (Partition, Broadcast), sum across ports.
				total, cap_ := 0, 0
				if n.Kind == Partition || n.Kind == MapResultNode {
					for port := range 2 {
						if ch, ok := chans[ChannelKey{n.ID, port}]; ok {
							total += len(ch)
							cap_ += cap(ch)
						}
					}
				} else if n.Kind == BroadcastNode || n.Kind == BalanceNode {
					for port := range n.BroadcastN {
						if ch, ok := chans[ChannelKey{n.ID, port}]; ok {
							total += len(ch)
							cap_ += cap(ch)
						}
					}
				} else {
					if ch, ok := chans[ChannelKey{n.ID, 0}]; ok {
						total = len(ch)
						cap_ = cap(ch)
					}
				}
				out = append(out, BufferStatus{Stage: name, Length: total, Capacity: cap_})
			}
			return out
		})
	}

	// done is closed by Take (or similar early-exit nodes) to tell
	// sources to stop producing. This avoids cancelling the whole
	// context, which would disrupt downstream stages still draining.
	done := make(chan struct{})
	var doneOnce sync.Once
	signalDone := func() { doneOnce.Do(func() { close(done) }) }

	if cfg.DrainTimeout > 0 {
		return runWithDrain(ctx, cfg, g, chans, outboxes, hook, done, signalDone)
	}

	fusionChains := detectFusionChains(g, chans, hook)
	fusedIDs, fusedHeads := buildFusionSets(fusionChains)

	eg, egCtx := errgroup.WithContext(ctx)
	for _, n := range g.Nodes {
		if fusedIDs[n.ID] {
			if chain := fusedHeads[n.ID]; chain != nil {
				eg.Go(fusedNodeRunner(egCtx, chain))
			}
			// Interior and tail nodes are handled by the head's goroutine.
			continue
		}
		eg.Go(nodeRunner(egCtx, n, cfg, chans, outboxes, hook, done, signalDone))
	}
	return eg.Wait()
}

// runWithDrain executes the pipeline with graceful drain semantics.
// When parentCtx is cancelled, sources are told to stop and the pipeline is
// given up to drainTimeout to finish processing in-flight items naturally.
// After the timeout, a hard cancel forces all remaining stages to exit.
func runWithDrain(parentCtx context.Context, cfg RunConfig, g *Graph, chans map[ChannelKey]chan any, outboxes map[ChannelKey]Outbox, hook Hook, done chan struct{}, signalDone func()) error {
	// drainCtx is independent of parentCtx so stages keep running after parent cancel.
	drainCtx, drainCancel := context.WithCancel(context.Background())
	defer drainCancel() // always clean up; also stops the monitor goroutine

	eg, egCtx := errgroup.WithContext(drainCtx)

	// Monitor goroutine: bridges parent cancellation into the two-phase drain.
	// Lives OUTSIDE the errgroup so it never blocks eg.Wait() on normal completion.
	// The defer drainCancel() above will unblock it when the function returns.
	go func() {
		select {
		case <-parentCtx.Done():
			// Phase 1: stop sources from producing new items.
			signalDone()
			// Phase 2: wait for natural drain or timeout.
			select {
			case <-time.After(cfg.DrainTimeout):
				// Drain timeout exceeded — force hard stop via drainCancel.
				drainCancel()
			case <-drainCtx.Done():
				// Pipeline finished draining (or error) before the timeout.
			}
		case <-drainCtx.Done():
			// Pipeline completed normally or via error — nothing to do.
		}
	}()

	fusionChains := detectFusionChains(g, chans, hook)
	fusedIDs, fusedHeads := buildFusionSets(fusionChains)

	for _, n := range g.Nodes {
		if fusedIDs[n.ID] {
			if chain := fusedHeads[n.ID]; chain != nil {
				eg.Go(fusedNodeRunner(egCtx, chain))
			}
			continue
		}
		eg.Go(nodeRunner(egCtx, n, cfg, chans, outboxes, hook, done, signalDone))
	}

	return eg.Wait()
}

// ---------------------------------------------------------------------------
// Node dispatch
// ---------------------------------------------------------------------------

func nodeRunner(ctx context.Context, n *Node, cfg RunConfig, chans map[ChannelKey]chan any, outboxes map[ChannelKey]Outbox, hook Hook, done <-chan struct{}, signalDone func()) func() error {
	outCh := chans[ChannelKey{n.ID, 0}]
	outbox := outboxes[ChannelKey{n.ID, 0}]

	var inChs []chan any
	for _, ref := range n.Inputs {
		inChs = append(inChs, chans[ChannelKey{ref.Node, ref.Port}])
	}
	var inCh chan any
	if len(inChs) > 0 {
		inCh = inChs[0]
	}

	// Resolve effective sample rate: 0 → default 10, negative → disabled (0 stored as -1).
	sampleRate := int64(10)
	if cfg.SampleRate > 0 {
		sampleRate = int64(cfg.SampleRate)
	} else if cfg.SampleRate < 0 {
		sampleRate = 0 // 0 means "never" in the check below
	}

	// outCloser closes all output channels when this node finishes.
	// Centralised here so supervision can restart a stage without closing
	// its output channel prematurely.
	var outCloser func()
	var inner func() error

	name := nodeName(n, kindName(n.Kind))

	switch n.Kind {
	case Source:
		outCloser = func() { close(outCh) }
		inner = func() error { return runSource(ctx, n, outbox, hook, done, sampleRate, cfg.Gate) }
	case Map:
		mn := resolveCacheWrap(n, cfg)
		outCloser = func() { close(outCh) }
		inner = func() error { return runMap(ctx, mn, inCh, outbox, hook, sampleRate) }
	case FlatMap:
		mn := resolveCacheWrap(n, cfg)
		outCloser = func() { close(outCh) }
		inner = func() error { return runFlatMap(ctx, mn, inCh, outbox, hook) }
	case Filter:
		outCloser = func() { close(outCh) }
		inner = func() error { return runFilter(ctx, n, inCh, outbox, hook, name) }
	case Tap:
		outCloser = func() { close(outCh) }
		inner = func() error { return runTap(ctx, n, inCh, outbox, hook, name) }
	case Take:
		outCloser = func() { close(outCh) }
		inner = func() error { return runTake(ctx, n, inCh, outbox, signalDone, hook, name) }
	case Batch:
		outCloser = func() { close(outCh) }
		batchClk := n.Clock
		if batchClk == nil {
			batchClk = RealClock{}
		}
		inner = func() error { return runBatch(ctx, n, inCh, outbox, hook, name, batchClk) }
	case Partition:
		matchCh := chans[ChannelKey{n.ID, 0}]
		restCh := chans[ChannelKey{n.ID, 1}]
		matchOutbox := outboxes[ChannelKey{n.ID, 0}]
		restOutbox := outboxes[ChannelKey{n.ID, 1}]
		outCloser = func() { close(matchCh); close(restCh) }
		inner = func() error { return runPartition(ctx, n, inCh, matchOutbox, restOutbox, hook, name) }
	case BroadcastNode:
		outChs := make([]chan any, n.BroadcastN)
		bcastOutboxes := make([]Outbox, n.BroadcastN)
		for i := range n.BroadcastN {
			outChs[i] = chans[ChannelKey{n.ID, i}]
			bcastOutboxes[i] = outboxes[ChannelKey{n.ID, i}]
		}
		outCloser = func() {
			for _, ch := range outChs {
				close(ch)
			}
		}
		inner = func() error { return runBroadcast(ctx, inCh, bcastOutboxes, hook, name) }
	case Merge:
		outCloser = func() { close(outCh) }
		inner = func() error { return runMerge(ctx, inChs, outbox, hook, name) }
	case Sink:
		outCloser = func() {} // sinks have no output channel
		inner = func() error { return runSink(ctx, n, inCh, hook, sampleRate) }
	case TakeWhile:
		outCloser = func() { close(outCh) }
		inner = func() error { return runTakeWhile(ctx, n, inCh, outbox, signalDone, hook, name) }
	case ZipNode:
		outCloser = func() { close(outCh) }
		var inCh2 chan any
		if len(inChs) >= 2 {
			inCh2 = inChs[1]
		}
		inner = func() error { return runZip(ctx, n, inCh, inCh2, outbox, hook, name) }
	case ReduceNode:
		outCloser = func() { close(outCh) }
		inner = func() error { return runReduce(ctx, n, inCh, outbox, hook, name) }
	case ThrottleNode:
		outCloser = func() { close(outCh) }
		throttleClk := n.Clock
		if throttleClk == nil {
			throttleClk = RealClock{}
		}
		inner = func() error { return runThrottle(ctx, n, inCh, outbox, hook, name, throttleClk) }
	case DebounceNode:
		outCloser = func() { close(outCh) }
		debounceClk := n.Clock
		if debounceClk == nil {
			debounceClk = RealClock{}
		}
		inner = func() error { return runDebounce(ctx, n, inCh, outbox, hook, name, debounceClk) }
	case MapResultNode:
		okCh := chans[ChannelKey{n.ID, 0}]
		errCh := chans[ChannelKey{n.ID, 1}]
		okOutbox := outboxes[ChannelKey{n.ID, 0}]
		errOutbox := outboxes[ChannelKey{n.ID, 1}]
		outCloser = func() { close(okCh); close(errCh) }
		inner = func() error { return runMapResult(ctx, n, inCh, okOutbox, errOutbox, hook, name, sampleRate) }
	case WithLatestFromNode:
		outCloser = func() { close(outCh) }
		var inCh2 chan any
		if len(inChs) >= 2 {
			inCh2 = inChs[1]
		}
		inner = func() error { return runWithLatestFrom(ctx, n, inCh, inCh2, outbox, hook, name) }
	case CombineLatestNode:
		outCloser = func() { close(outCh) }
		var inCh2 chan any
		if len(inChs) >= 2 {
			inCh2 = inChs[1]
		}
		inner = func() error { return runCombineLatest(ctx, n, inCh, inCh2, outbox, hook, name) }
	case BalanceNode:
		outChs := make([]chan any, n.BroadcastN)
		balOutboxes := make([]Outbox, n.BroadcastN)
		for i := range n.BroadcastN {
			outChs[i] = chans[ChannelKey{n.ID, i}]
			balOutboxes[i] = outboxes[ChannelKey{n.ID, i}]
		}
		outCloser = func() {
			for _, ch := range outChs {
				close(ch)
			}
		}
		inner = func() error { return runBalance(ctx, inCh, balOutboxes, hook, name) }
	case SwitchMapNode:
		outCloser = func() { close(outCh) }
		inner = func() error { return runSwitchMap(ctx, n, inCh, outCh, hook) }
	case ExhaustMapNode:
		outCloser = func() { close(outCh) }
		inner = func() error { return runExhaustMap(ctx, n, inCh, outCh, hook) }
	case SessionWindowNode:
		outCloser = func() { close(outCh) }
		sessionClk := n.Clock
		if sessionClk == nil {
			sessionClk = RealClock{}
		}
		inner = func() error { return runSessionWindow(ctx, n, inCh, outbox, hook, name, sessionClk) }
	default:
		outCloser = func() {}
		inner = func() error { return fmt.Errorf("kitsune: unknown node kind %d", n.Kind) }
	}

	return func() error {
		defer outCloser()
		// Drain all input channels when this stage exits. This unblocks any
		// upstream that is doing a plain send (drain protocol fast path), and
		// prevents goroutine leaks if the pipeline cancels mid-stream.
		if n.Kind != Source {
			for _, ch := range inChs {
				ch := ch
				defer func() { go func() { for range ch {} }() }()
			}
		}
		return supervise(ctx, n.Supervision, hook, name, inner)
	}
}

// ---------------------------------------------------------------------------
// ProcessItem — error / retry loop
// ---------------------------------------------------------------------------

// ProcessItem invokes fn with retry/skip/halt logic.
// The third return value is the final attempt index (0-based); callers use it
// to populate [StageError] via [wrapStageErr].
func ProcessItem(ctx context.Context, fn func(context.Context, any) (any, error), item any, h ErrorHandler) (any, error, int) {
	for attempt := 0; ; attempt++ {
		result, err := fn(ctx, item)
		if err == nil {
			return result, nil, attempt
		}
		switch h.Handle(err, attempt) {
		case ActionSkip:
			return nil, ErrSkipped, attempt
		case ActionReturn:
			if r, ok := h.(Returner); ok {
				return r.ReturnValue(), nil, attempt
			}
			return nil, err, attempt
		case ActionRetry:
			bo := h.Backoff()
			if bo == nil {
				return nil, err, attempt
			}
			select {
			case <-time.After(bo(attempt)):
				continue
			case <-ctx.Done():
				return nil, ctx.Err(), attempt
			}
		default:
			return nil, err, attempt
		}
	}
}

// wrapStageErr wraps err in a [StageError] unless it is nil, [ErrSkipped],
// [context.Canceled], or [context.DeadlineExceeded] — errors that either carry
// no stage context or are infrastructure signals rather than user-function failures.
func wrapStageErr(name string, err error, attempt int) error {
	if err == nil || err == ErrSkipped || err == context.Canceled || err == context.DeadlineExceeded {
		return err
	}
	// Don't double-wrap.
	var se *StageError
	if errors.As(err, &se) {
		return err
	}
	return &StageError{Stage: name, Attempt: attempt, Cause: err}
}

// ---------------------------------------------------------------------------
// Source
// ---------------------------------------------------------------------------

func runSourceFastPath(ctx context.Context, fn func(context.Context, func(any) bool) error, outCh chan any, done <-chan struct{}) error {
	srcCtx, srcCancel := context.WithCancel(ctx)
	defer srcCancel()
	go func() {
		select {
		case <-done:
			srcCancel()
		case <-srcCtx.Done():
		}
	}()
	err := fn(srcCtx, func(item any) bool {
		outCh <- item                 // plain send — drain protocol prevents deadlock
		return srcCtx.Err() == nil   // detect cancellation after send
	})
	select {
	case <-done:
		return nil
	default:
	}
	return err
}

func runSource(ctx context.Context, n *Node, outbox Outbox, hook Hook, done <-chan struct{}, sampleRate int64, gate *Gate) error {
	name := nodeName(n, "source")

	if _, ok := hook.(NoopHook); ok {
		if bo, ok := outbox.(*blockingOutbox); ok {
			if gate == nil {
				fn := n.Fn.(func(context.Context, func(any) bool) error)
				return runSourceFastPath(ctx, fn, bo.ch, done)
			}
		}
	}

	// Create a source-scoped context that also fires when the drain-done signal
	// is received. This lets source functions use <-ctx.Done() uniformly for
	// both hard cancellation and graceful-drain stop signals.
	srcCtx, srcCancel := context.WithCancel(ctx)
	defer srcCancel()
	go func() {
		select {
		case <-done:
			srcCancel()
		case <-srcCtx.Done():
		}
	}()

	hook.OnStageStart(srcCtx, name)
	var count int64

	fn := n.Fn.(func(context.Context, func(any) bool) error)
	err := fn(srcCtx, func(item any) bool {
		// Check early-exit signals before sending (non-blocking).
		// For blocking outboxes this means done/ctx are polled once per item;
		// one extra item may be produced after done fires — that is acceptable.
		select {
		case <-done:
			return false
		case <-srcCtx.Done():
			return false
		default:
		}
		if gate != nil {
			if err := gate.Wait(srcCtx); err != nil {
				return false
			}
		}
		if err := outbox.Send(srcCtx, item); err != nil {
			return false
		}
		count++
		hook.OnItem(srcCtx, name, 0, nil)
		if sampleRate > 0 && count%sampleRate == 0 {
			if sh, ok := hook.(SampleHook); ok {
				sh.OnItemSample(srcCtx, name, item)
			}
		}
		return true
	})

	hook.OnStageDone(srcCtx, name, count, 0)
	// If the done signal triggered the cancellation, the source stopping is
	// expected (graceful drain) — treat it as a clean exit regardless of err.
	select {
	case <-done:
		return nil
	default:
	}
	return err
}

// ---------------------------------------------------------------------------
// Map (1:1)
// ---------------------------------------------------------------------------

func runMap(ctx context.Context, n *Node, inCh chan any, outbox Outbox, hook Hook, sampleRate int64) error {
	fn := n.Fn.(func(context.Context, any) (any, error))
	handler := nodeErrorHandler(n)
	name := nodeName(n, "map")

	if n.Concurrency <= 1 {
		// Batch-drain fast path is unsafe when supervision is active: pre-fetching
		// items from the channel means they are lost if the stage errors and is
		// restarted. Only use it when supervision is disabled.
		if !n.Supervision.HasSupervision() {
			if _, ok := handler.(DefaultHandler); ok {
				if _, ok := hook.(NoopHook); ok {
					if bo, ok := outbox.(*blockingOutbox); ok {
						return runMapFastPath(ctx, fn, inCh, bo.ch, name)
					}
				}
			}
		}
		return runMapSingle(ctx, fn, inCh, outbox, handler, name, hook, sampleRate)
	}
	if n.Ordered {
		return runMapConcurrentOrdered(ctx, fn, inCh, outbox, n.Concurrency, handler, name, hook)
	}
	return runMapConcurrent(ctx, fn, inCh, outbox, n.Concurrency, handler, name, hook, sampleRate)
}

// runMapFastPath is a stripped-down Map loop for the common case where no
// instrumentation, retries, or overflow handling are active. It replaces the
// two-case receive select with a range loop and calls fn directly.
// Drain protocol: on exit a goroutine drains inCh so upstream senders never
// deadlock on a plain channel send.
func runMapFastPath(ctx context.Context, fn func(context.Context, any) (any, error), inCh chan any, outCh chan any, name string) error {
	defer func() { go func() { for range inCh {} }() }()
	var buf [receiveBatchSize]any
	for {
		item, ok := <-inCh
		if !ok {
			return ctx.Err()
		}
		buf[0] = item
		n := 1
		closed := false
	fillMapBuf:
		for n < receiveBatchSize {
			select {
			case v, ok2 := <-inCh:
				if !ok2 {
					closed = true
					break fillMapBuf
				}
				buf[n] = v
				n++
			default:
				break fillMapBuf
			}
		}
		for i := range n {
			result, err := fn(ctx, buf[i])
			buf[i] = nil
			if err != nil {
				if err == ErrSkipped {
					continue
				}
				return &StageError{Stage: name, Cause: err}
			}
			outCh <- result
		}
		if closed {
			return ctx.Err()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func runMapSingle(ctx context.Context, fn func(context.Context, any) (any, error), inCh chan any, outbox Outbox, handler ErrorHandler, name string, hook Hook, sampleRate int64) error {
	hook.OnStageStart(ctx, name)
	var processed, errCount int64
	defer func() { hook.OnStageDone(ctx, name, processed, errCount) }()

	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return nil
			}
			start := time.Now()
			result, err, attempt := ProcessItem(ctx, fn, item, handler)
			err = wrapStageErr(name, err, attempt)
			dur := time.Since(start)

			if err == ErrSkipped {
				errCount++
				hook.OnItem(ctx, name, dur, err)
				continue
			}
			if err != nil {
				hook.OnItem(ctx, name, dur, err)
				return err
			}
			processed++
			hook.OnItem(ctx, name, dur, nil)
			if sampleRate > 0 && processed%sampleRate == 0 {
				if sh, ok := hook.(SampleHook); ok {
					sh.OnItemSample(ctx, name, result)
				}
			}

			if err := outbox.Send(ctx, result); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func runMapConcurrent(ctx context.Context, fn func(context.Context, any) (any, error), inCh chan any, outbox Outbox, concurrency int, handler ErrorHandler, name string, hook Hook, sampleRate int64) error {
	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)

	// Fast path: skip timing, hook dispatch, ProcessItem overhead, and atomic
	// counters when no custom handler or hook is attached.
	if _, ok := handler.(DefaultHandler); ok {
		if _, ok := hook.(NoopHook); ok {
			for range concurrency {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case item, ok := <-inCh:
							if !ok {
								return
							}
							result, err := fn(innerCtx, item)
							if err != nil {
								if err == ErrSkipped {
									continue
								}
								errOnce.Do(func() {
									firstErr = &StageError{Stage: name, Cause: err}
								})
								innerCancel()
								return
							}
							if err := outbox.Send(innerCtx, result); err != nil {
								errOnce.Do(func() { firstErr = err })
								innerCancel()
								return
							}
						case <-innerCtx.Done():
							return
						}
					}
				}()
			}
			wg.Wait()
			return firstErr
		}
	}

	hook.OnStageStart(ctx, name)
	var processed, errCount atomic.Int64

	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return
					}
					start := time.Now()
					result, err, attempt := ProcessItem(innerCtx, fn, item, handler)
					err = wrapStageErr(name, err, attempt)
					dur := time.Since(start)

					if err == ErrSkipped {
						errCount.Add(1)
						hook.OnItem(innerCtx, name, dur, err)
						continue
					}
					if err != nil {
						hook.OnItem(innerCtx, name, dur, err)
						errOnce.Do(func() { firstErr = err })
						innerCancel()
						return
					}
					p := processed.Add(1)
					hook.OnItem(innerCtx, name, dur, nil)
					if sampleRate > 0 && p%sampleRate == 0 {
						if sh, ok := hook.(SampleHook); ok {
							sh.OnItemSample(innerCtx, name, result)
						}
					}

					if err := outbox.Send(innerCtx, result); err != nil {
						errOnce.Do(func() { firstErr = err })
						innerCancel()
						return
					}
				case <-innerCtx.Done():
					return
				}
			}
		}()
	}

	wg.Wait()
	hook.OnStageDone(ctx, name, processed.Load(), errCount.Load())
	return firstErr
}

// runMapConcurrentOrdered processes items with n workers while preserving input order.
//
// A slot is allocated per input item. The dispatcher sends each slot to both
// the pending queue (maintaining input order) and the jobs queue (for workers).
// Workers process slots concurrently and signal completion via slot.done.
// The collector drains pending in order, waiting on each slot before emitting.
//
// Memory bound: at most concurrency slots in flight at any time.
func runMapConcurrentOrdered(ctx context.Context, fn func(context.Context, any) (any, error), inCh chan any, outbox Outbox, concurrency int, handler ErrorHandler, name string, hook Hook) error {
	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	hook.OnStageStart(ctx, name)

	jobs := make(chan *mapSlot, concurrency)
	pending := make(chan *mapSlot, concurrency)

	// Workers — process slots concurrently, signal slot.done when finished.
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range jobs {
				start := time.Now()
				var attempt int
				s.result, s.err, attempt = ProcessItem(innerCtx, fn, s.item, handler)
				s.err = wrapStageErr(name, s.err, attempt)
				hook.OnItem(innerCtx, name, time.Since(start), s.err)
				s.done <- struct{}{}
			}
		}()
	}

	// Collector — emits results in input order.
	var processed, errCount atomic.Int64
	collErr := make(chan error, 1)
	go func() {
		for s := range pending {
			// Wait for this slot's worker to finish.
			select {
			case <-s.done:
			case <-innerCtx.Done():
				collErr <- innerCtx.Err()
				return
			}
			if s.err == ErrSkipped {
				errCount.Add(1)
				putMapSlot(s)
				continue
			}
			if s.err != nil {
				errCount.Add(1)
				innerCancel()
				err := s.err
				putMapSlot(s)
				collErr <- err
				return
			}
			processed.Add(1)
			result := s.result
			putMapSlot(s)
			if err := outbox.Send(innerCtx, result); err != nil {
				collErr <- err
				return
			}
		}
		collErr <- nil
	}()

	// Dispatcher — reads inCh, assigns slots, preserves order via pending.
	for item := range inCh {
		s := getMapSlot(item)
		// Send to pending first to guarantee output ordering.
		select {
		case pending <- s:
		case <-innerCtx.Done():
			putMapSlot(s)
			goto dispatchDone
		}
		select {
		case jobs <- s:
		case <-innerCtx.Done():
			goto dispatchDone
		}
	}
dispatchDone:
	close(jobs)
	wg.Wait()
	close(pending)

	err := <-collErr
	hook.OnStageDone(ctx, name, processed.Load(), errCount.Load())
	return err
}

// ---------------------------------------------------------------------------
// FlatMap (1:N)
// ---------------------------------------------------------------------------

func runFlatMap(ctx context.Context, n *Node, inCh chan any, outbox Outbox, hook Hook) error {
	fn := n.Fn.(func(context.Context, any, func(any) error) error)
	handler := nodeErrorHandler(n)
	name := nodeName(n, "flatmap")

	if n.Concurrency <= 1 {
		if !n.Supervision.HasSupervision() {
			if _, ok := handler.(DefaultHandler); ok {
				if _, ok := hook.(NoopHook); ok {
					if bo, ok := outbox.(*blockingOutbox); ok {
						return runFlatMapFastPath(ctx, fn, inCh, bo.ch, name)
					}
				}
			}
		}
		return runFlatMapSingle(ctx, fn, inCh, outbox, handler, name, hook)
	}
	if n.Ordered {
		return runFlatMapConcurrentOrdered(ctx, fn, inCh, outbox, n.Concurrency, handler, name, hook)
	}
	return runFlatMapConcurrent(ctx, fn, inCh, outbox, n.Concurrency, handler, name, hook)
}

// flatMapProcessItem invokes fn with a buffering yield callback, handling retry
// and skip semantics. Only used when a custom error handler is attached; the
// common default-handler path yields directly to outbox.Send instead.
func flatMapProcessItem(ctx context.Context, fn func(context.Context, any, func(any) error) error, item any, h ErrorHandler, send func(any) error) (err error, attempt int) {
	for {
		var buf []any
		err = fn(ctx, item, func(v any) error {
			buf = append(buf, v)
			return nil
		})
		if err == nil {
			for _, v := range buf {
				if err = send(v); err != nil {
					return err, attempt
				}
			}
			return nil, attempt
		}
		switch h.Handle(err, attempt) {
		case ActionSkip:
			return ErrSkipped, attempt
		case ActionReturn:
			// FlatMap has no single replacement value; treat as skip.
			return ErrSkipped, attempt
		case ActionRetry:
			bo := h.Backoff()
			if bo == nil {
				return err, attempt
			}
			select {
			case <-time.After(bo(attempt)):
				attempt++
				continue
			case <-ctx.Done():
				return ctx.Err(), attempt
			}
		default:
			return err, attempt
		}
	}
}

// runFlatMapFastPath is the instrumentation-free FlatMap loop. The yield
// closure sends each output directly to outCh without going through the
// outbox interface or accumulating into an intermediate slice.
// Drain protocol: on exit a goroutine drains inCh so upstream senders never
// deadlock on a plain channel send.
func runFlatMapFastPath(ctx context.Context, fn func(context.Context, any, func(any) error) error, inCh chan any, outCh chan any, name string) error {
	defer func() { go func() { for range inCh {} }() }()
	var buf [receiveBatchSize]any
	for {
		item, ok := <-inCh
		if !ok {
			return ctx.Err()
		}
		buf[0] = item
		n := 1
		closed := false
	fillFlatMapBuf:
		for n < receiveBatchSize {
			select {
			case v, ok2 := <-inCh:
				if !ok2 {
					closed = true
					break fillFlatMapBuf
				}
				buf[n] = v
				n++
			default:
				break fillFlatMapBuf
			}
		}
		for i := range n {
			err := fn(ctx, buf[i], func(v any) error {
				outCh <- v
				return nil
			})
			buf[i] = nil
			if err != nil {
				if err == ErrSkipped {
					continue
				}
				return &StageError{Stage: name, Cause: err}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		if closed {
			return ctx.Err()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func runFlatMapSingle(ctx context.Context, fn func(context.Context, any, func(any) error) error, inCh chan any, outbox Outbox, handler ErrorHandler, name string, hook Hook) error {
	hook.OnStageStart(ctx, name)
	var processed, errCount int64
	defer func() { hook.OnStageDone(ctx, name, processed, errCount) }()

	_, isDefault := handler.(DefaultHandler)

	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return nil
			}
			start := time.Now()
			var err error
			var attempt int

			if isDefault {
				// Fast path: yield directly to outbox — no intermediate []any allocation.
				err = fn(ctx, item, func(v any) error {
					return outbox.Send(ctx, v)
				})
			} else {
				err, attempt = flatMapProcessItem(ctx, fn, item, handler, func(v any) error {
					return outbox.Send(ctx, v)
				})
			}

			err = wrapStageErr(name, err, attempt)
			dur := time.Since(start)

			if err == ErrSkipped {
				errCount++
				hook.OnItem(ctx, name, dur, err)
				continue
			}
			if err != nil {
				hook.OnItem(ctx, name, dur, err)
				return err
			}
			processed++
			hook.OnItem(ctx, name, dur, nil)

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func runFlatMapConcurrent(ctx context.Context, fn func(context.Context, any, func(any) error) error, inCh chan any, outbox Outbox, concurrency int, handler ErrorHandler, name string, hook Hook) error {
	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)

	// Fast path: skip timing, hook dispatch, and wrapStageErr overhead when
	// no custom handler or hook is attached.
	if _, ok := handler.(DefaultHandler); ok {
		if _, ok := hook.(NoopHook); ok {
			for range concurrency {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case item, ok := <-inCh:
							if !ok {
								return
							}
							err := fn(innerCtx, item, func(v any) error {
								return outbox.Send(innerCtx, v)
							})
							if err != nil {
								if err == ErrSkipped {
									continue
								}
								errOnce.Do(func() {
									firstErr = &StageError{Stage: name, Cause: err}
								})
								innerCancel()
								return
							}
						case <-innerCtx.Done():
							return
						}
					}
				}()
			}
			wg.Wait()
			return firstErr
		}
	}

	hook.OnStageStart(ctx, name)
	var processed, errCount atomic.Int64

	_, isDefault := handler.(DefaultHandler)

	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return
					}
					start := time.Now()
					var err error
					var attempt int

					if isDefault {
						err = fn(innerCtx, item, func(v any) error {
							return outbox.Send(innerCtx, v)
						})
					} else {
						err, attempt = flatMapProcessItem(innerCtx, fn, item, handler, func(v any) error {
							return outbox.Send(innerCtx, v)
						})
					}

					err = wrapStageErr(name, err, attempt)
					dur := time.Since(start)

					if err == ErrSkipped {
						errCount.Add(1)
						hook.OnItem(innerCtx, name, dur, err)
						continue
					}
					if err != nil {
						hook.OnItem(innerCtx, name, dur, err)
						errOnce.Do(func() { firstErr = err })
						innerCancel()
						return
					}
					processed.Add(1)
					hook.OnItem(innerCtx, name, dur, nil)

				case <-innerCtx.Done():
					return
				}
			}
		}()
	}

	wg.Wait()
	hook.OnStageDone(ctx, name, processed.Load(), errCount.Load())
	return firstErr
}

func runFlatMapConcurrentOrdered(ctx context.Context, fn func(context.Context, any, func(any) error) error, inCh chan any, outbox Outbox, concurrency int, handler ErrorHandler, name string, hook Hook) error {
	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	hook.OnStageStart(ctx, name)

	_, isDefault := handler.(DefaultHandler)

	jobs := make(chan *flatMapSlot, concurrency)
	pending := make(chan *flatMapSlot, concurrency)

	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range jobs {
				start := time.Now()
				if isDefault {
					s.err = fn(innerCtx, s.item, func(v any) error {
						s.results = append(s.results, v)
						return nil
					})
					s.err = wrapStageErr(name, s.err, 0)
				} else {
					var attempt int
					s.err, attempt = flatMapProcessItem(innerCtx, fn, s.item, handler, func(v any) error {
						s.results = append(s.results, v)
						return nil
					})
					s.err = wrapStageErr(name, s.err, attempt)
				}
				hook.OnItem(innerCtx, name, time.Since(start), s.err)
				s.done <- struct{}{}
			}
		}()
	}

	var processed, errCount atomic.Int64
	collErr := make(chan error, 1)
	go func() {
		for s := range pending {
			select {
			case <-s.done:
			case <-innerCtx.Done():
				collErr <- innerCtx.Err()
				return
			}
			if s.err == ErrSkipped {
				errCount.Add(1)
				putFlatMapSlot(s)
				continue
			}
			if s.err != nil {
				errCount.Add(1)
				innerCancel()
				err := s.err
				putFlatMapSlot(s)
				collErr <- err
				return
			}
			processed.Add(1)
			for _, r := range s.results {
				if err := outbox.Send(innerCtx, r); err != nil {
					putFlatMapSlot(s)
					collErr <- err
					return
				}
			}
			putFlatMapSlot(s)
		}
		collErr <- nil
	}()

	for item := range inCh {
		s := getFlatMapSlot(item)
		select {
		case pending <- s:
		case <-innerCtx.Done():
			putFlatMapSlot(s)
			goto dispatchDone
		}
		select {
		case jobs <- s:
		case <-innerCtx.Done():
			goto dispatchDone
		}
	}
dispatchDone:
	close(jobs)
	wg.Wait()
	close(pending)

	err := <-collErr
	hook.OnStageDone(ctx, name, processed.Load(), errCount.Load())
	return err
}

// ---------------------------------------------------------------------------
// SwitchMap
// ---------------------------------------------------------------------------

// runSwitchMap applies fn to each upstream item, cancelling any still-running
// inner pipeline when the next item arrives. Only the latest inner pipeline's
// output reaches outCh; superseded goroutines are cancelled immediately.
func runSwitchMap(ctx context.Context, n *Node, inCh chan any, outCh chan any, hook Hook) error {
	fn := n.Fn.(func(context.Context, any, func(any) error) error)
	name := nodeName(n, "switchmap")

	var (
		innerCancel context.CancelFunc
		innerDone   <-chan error
	)

	cancelInner := func() {
		if innerCancel != nil {
			innerCancel()
			if innerDone != nil {
				<-innerDone
			}
			innerCancel = nil
			innerDone = nil
		}
	}

	launchInner := func(item any) {
		innerCtx, cancel := context.WithCancel(ctx)
		innerCancel = cancel
		done := make(chan error, 1)
		innerDone = done
		go func(it any, ic context.Context, d chan<- error) {
			yield := func(out any) error {
				select {
				case outCh <- out:
					return nil
				case <-ic.Done():
					return ic.Err()
				}
			}
			err := fn(ic, it, yield)
			// If the inner context was cancelled (superseded by a new item), swallow the error.
			if err != nil && ic.Err() != nil {
				err = nil
			}
			d <- err
		}(item, innerCtx, done)
	}

	hook.OnStageStart(ctx, name)
	defer hook.OnStageDone(ctx, name, 0, 0)

	for {
		if innerDone != nil {
			select {
			case <-ctx.Done():
				cancelInner()
				return ctx.Err()
			case err := <-innerDone:
				innerCancel()
				innerCancel = nil
				innerDone = nil
				if err != nil {
					return &StageError{Stage: name, Cause: err}
				}
			case item, ok := <-inCh:
				if !ok {
					// Upstream closed while an inner pipeline is active.
					// Wait for it to finish (no new items will arrive to supersede it).
					select {
					case err := <-innerDone:
						innerCancel()
						if err != nil {
							return &StageError{Stage: name, Cause: err}
						}
						return nil
					case <-ctx.Done():
						cancelInner()
						return ctx.Err()
					}
				}
				cancelInner()
				launchInner(item)
			}
		} else {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case item, ok := <-inCh:
				if !ok {
					return nil
				}
				launchInner(item)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// ExhaustMap
// ---------------------------------------------------------------------------

// runExhaustMap applies fn to each upstream item, but ignores new upstream
// items while an inner pipeline is still running. Only the first item wins;
// subsequent arrivals during active processing are dropped.
func runExhaustMap(ctx context.Context, n *Node, inCh chan any, outCh chan any, hook Hook) error {
	fn := n.Fn.(func(context.Context, any, func(any) error) error)
	name := nodeName(n, "exhaustmap")

	var innerDone <-chan error // non-nil when an inner pipeline is active

	launchInner := func(item any) {
		done := make(chan error, 1)
		innerDone = done
		go func(it any, d chan<- error) {
			yield := func(out any) error {
				select {
				case outCh <- out:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			d <- fn(ctx, it, yield)
		}(item, done)
	}

	hook.OnStageStart(ctx, name)
	defer hook.OnStageDone(ctx, name, 0, 0)

	for {
		if innerDone != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case err := <-innerDone:
				innerDone = nil
				if err != nil {
					return &StageError{Stage: name, Cause: err}
				}
			case _, ok := <-inCh:
				if !ok {
					// Upstream closed while inner is still active; wait for it to finish.
					select {
					case err := <-innerDone:
						if err != nil {
							return &StageError{Stage: name, Cause: err}
						}
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				// Inner pipeline still active — drop this item.
			}
		} else {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case item, ok := <-inCh:
				if !ok {
					return nil
				}
				launchInner(item)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Filter / Tap / Take
// ---------------------------------------------------------------------------

// runFilterFastPath is the instrumentation-free Filter loop.
// Drain protocol: on exit a goroutine drains inCh so upstream senders never
// deadlock on a plain channel send.
func runFilterFastPath(ctx context.Context, fn func(any) bool, inCh chan any, outCh chan any) error {
	defer func() { go func() { for range inCh {} }() }()
	var buf [receiveBatchSize]any
	for {
		item, ok := <-inCh
		if !ok {
			return ctx.Err()
		}
		buf[0] = item
		n := 1
		closed := false
	fillFilterBuf:
		for n < receiveBatchSize {
			select {
			case v, ok2 := <-inCh:
				if !ok2 {
					closed = true
					break fillFilterBuf
				}
				buf[n] = v
				n++
			default:
				break fillFilterBuf
			}
		}
		for i := range n {
			it := buf[i]
			buf[i] = nil
			if fn(it) {
				outCh <- it
			}
		}
		if closed {
			return ctx.Err()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func runFilter(ctx context.Context, n *Node, inCh chan any, outbox Outbox, hook Hook, name string) error {
	fn := n.Fn.(func(any) bool)
	if _, ok := hook.(NoopHook); ok {
		if bo, ok := outbox.(*blockingOutbox); ok {
			return runFilterFastPath(ctx, fn, inCh, bo.ch)
		}
	}
	hook.OnStageStart(ctx, name)
	var count int64
	defer func() { hook.OnStageDone(ctx, name, count, 0) }()
	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return nil
			}
			count++
			hook.OnItem(ctx, name, 0, nil)
			if fn(item) {
				if err := outbox.Send(ctx, item); err != nil {
					return err
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func runTap(ctx context.Context, n *Node, inCh chan any, outbox Outbox, hook Hook, name string) error {
	fn := n.Fn.(func(any))
	hook.OnStageStart(ctx, name)
	var count int64
	defer func() { hook.OnStageDone(ctx, name, count, 0) }()
	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return nil
			}
			count++
			hook.OnItem(ctx, name, 0, nil)
			fn(item)
			if err := outbox.Send(ctx, item); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func runTake(ctx context.Context, n *Node, inCh chan any, outbox Outbox, signalDone func(), hook Hook, name string) error {
	hook.OnStageStart(ctx, name)
	var count int64
	defer func() { hook.OnStageDone(ctx, name, count, 0) }()
	remaining := n.TakeN
	for remaining > 0 {
		select {
		case item, ok := <-inCh:
			if !ok {
				return nil
			}
			remaining--
			count++
			hook.OnItem(ctx, name, 0, nil)
			if err := outbox.Send(ctx, item); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Signal sources to stop, then drain in-flight items from upstream
	// until the channel closes naturally.
	signalDone()
	for {
		select {
		case _, ok := <-inCh:
			if !ok {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ---------------------------------------------------------------------------
// TakeWhile
// ---------------------------------------------------------------------------

func runTakeWhile(ctx context.Context, n *Node, inCh chan any, outbox Outbox, signalDone func(), hook Hook, name string) error {
	hook.OnStageStart(ctx, name)
	var count int64
	defer func() { hook.OnStageDone(ctx, name, count, 0) }()
	pred := n.Fn.(func(any) bool)
	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return nil
			}
			if !pred(item) {
				// Predicate failed — signal sources to stop, then drain upstream.
				signalDone()
				for {
					select {
					case _, ok := <-inCh:
						if !ok {
							return nil
						}
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
			count++
			hook.OnItem(ctx, name, 0, nil)
			if err := outbox.Send(ctx, item); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ---------------------------------------------------------------------------
// Zip
// ---------------------------------------------------------------------------

func runZip(ctx context.Context, n *Node, inCh1, inCh2 chan any, outbox Outbox, hook Hook, name string) error {
	hook.OnStageStart(ctx, name)
	var count int64
	defer func() { hook.OnStageDone(ctx, name, count, 0) }()
	convert := n.ZipConvert
	for {
		// Read from first channel.
		var a any
		select {
		case item, ok := <-inCh1:
			if !ok {
				return nil
			}
			a = item
		case <-ctx.Done():
			return ctx.Err()
		}
		// Read from second channel.
		var b any
		select {
		case item, ok := <-inCh2:
			if !ok {
				return nil
			}
			b = item
		case <-ctx.Done():
			return ctx.Err()
		}
		count++
		hook.OnItem(ctx, name, 0, nil)
		if err := outbox.Send(ctx, convert(a, b)); err != nil {
			return err
		}
	}
}

// ---------------------------------------------------------------------------
// Batch
// ---------------------------------------------------------------------------

func runBatch(ctx context.Context, n *Node, inCh chan any, outbox Outbox, hook Hook, name string, clk Clock) error {
	hook.OnStageStart(ctx, name)
	var count int64
	defer func() { hook.OnStageDone(ctx, name, count, 0) }()

	size := n.BatchSize
	timeout := time.Duration(n.BatchTimeout)
	convert := n.BatchConvert

	// Cap initial capacity to avoid huge allocations for Window (size=MaxInt).
	initCap := size
	if initCap > 4096 {
		initCap = 4096
	}
	batch := make([]any, 0, initCap)

	var timer Timer
	var timerCh <-chan time.Time
	if timeout > 0 {
		timer = clk.NewTimer(timeout)
		timer.Stop()
		defer timer.Stop()
		timerCh = timer.C()
	}

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		converted := convert(batch)
		// Reuse the backing array: nil out interface values so the GC can
		// collect the referenced items, then reset the length to zero.
		// convert copies items into a new []T and does not retain this slice.
		clear(batch)
		batch = batch[:0]
		if timer != nil {
			timer.Stop()
		}
		return outbox.Send(ctx, converted)
	}

	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return flush()
			}
			count++
			hook.OnItem(ctx, name, 0, nil)
			batch = append(batch, item)
			if len(batch) >= size {
				if err := flush(); err != nil {
					return err
				}
			} else if timer != nil && len(batch) == 1 {
				timer.Reset(timeout)
			}
		case <-timerCh:
			if err := flush(); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ---------------------------------------------------------------------------
// Partition / Merge
// ---------------------------------------------------------------------------

func runPartition(ctx context.Context, n *Node, inCh chan any, matchOutbox, restOutbox Outbox, hook Hook, name string) error {
	fn := n.Fn.(func(any) bool)
	hook.OnStageStart(ctx, name)
	var count int64
	defer func() { hook.OnStageDone(ctx, name, count, 0) }()

	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return nil
			}
			count++
			hook.OnItem(ctx, name, 0, nil)
			target := restOutbox
			if fn(item) {
				target = matchOutbox
			}
			if err := target.Send(ctx, item); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func runBroadcast(ctx context.Context, inCh chan any, outboxes []Outbox, hook Hook, name string) error {
	hook.OnStageStart(ctx, name)
	var count int64
	defer func() { hook.OnStageDone(ctx, name, count, 0) }()

	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return nil
			}
			count++
			hook.OnItem(ctx, name, 0, nil)
			for _, ob := range outboxes {
				if err := ob.Send(ctx, item); err != nil {
					return err
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func runMerge(ctx context.Context, inChs []chan any, outbox Outbox, hook Hook, name string) error {
	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	hook.OnStageStart(ctx, name)
	var count atomic.Int64
	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	for _, ch := range inChs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case item, ok := <-ch:
					if !ok {
						return
					}
					count.Add(1)
					hook.OnItem(innerCtx, name, 0, nil)
					if err := outbox.Send(innerCtx, item); err != nil {
						errOnce.Do(func() { firstErr = err })
						innerCancel()
						return
					}
				case <-innerCtx.Done():
					return
				}
			}
		}()
	}
	wg.Wait()
	hook.OnStageDone(ctx, name, count.Load(), 0)
	return firstErr
}

// ---------------------------------------------------------------------------
// Sink (terminal)
// ---------------------------------------------------------------------------

// runSinkFastPath is the instrumentation-free Sink loop for Concurrency(1).
func runSinkFastPath(ctx context.Context, fn func(context.Context, any) error, inCh chan any, name string) error {
	defer func() { go func() { for range inCh {} }() }()
	var buf [receiveBatchSize]any
	for {
		item, ok := <-inCh
		if !ok {
			return ctx.Err()
		}
		buf[0] = item
		n := 1
		closed := false
	fillSinkBuf:
		for n < receiveBatchSize {
			select {
			case v, ok2 := <-inCh:
				if !ok2 {
					closed = true
					break fillSinkBuf
				}
				buf[n] = v
				n++
			default:
				break fillSinkBuf
			}
		}
		for i := range n {
			it := buf[i]
			buf[i] = nil
			if err := fn(ctx, it); err != nil {
				if err == ErrSkipped {
					continue
				}
				return &StageError{Stage: name, Cause: err}
			}
		}
		if closed {
			return ctx.Err()
		}
	}
}

func runSink(ctx context.Context, n *Node, inCh chan any, hook Hook, sampleRate int64) error {
	fn := n.Fn.(func(context.Context, any) error)
	handler := nodeErrorHandler(n)
	name := nodeName(n, "sink")
	adapted := func(ctx context.Context, in any) (any, error) { return nil, fn(ctx, in) }

	if n.Concurrency <= 1 && !n.Supervision.HasSupervision() {
		if _, ok := handler.(DefaultHandler); ok {
			if _, ok := hook.(NoopHook); ok {
				return runSinkFastPath(ctx, fn, inCh, name)
			}
		}
	}

	hook.OnStageStart(ctx, name)
	var processed, errCount int64
	defer func() { hook.OnStageDone(ctx, name, processed, errCount) }()

	if n.Concurrency <= 1 {
		for {
			select {
			case item, ok := <-inCh:
				if !ok {
					return nil
				}
				start := time.Now()
				_, err, attempt := ProcessItem(ctx, adapted, item, handler)
				err = wrapStageErr(name, err, attempt)
				dur := time.Since(start)

				if err == ErrSkipped {
					errCount++
					hook.OnItem(ctx, name, dur, err)
					continue
				}
				if err != nil {
					hook.OnItem(ctx, name, dur, err)
					return err
				}
				processed++
				hook.OnItem(ctx, name, dur, nil)
				if sampleRate > 0 && processed%sampleRate == 0 {
					if sh, ok := hook.(SampleHook); ok {
						sh.OnItemSample(ctx, name, item)
					}
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
		proc     atomic.Int64
		errs     atomic.Int64
	)

	for range n.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return
					}
					start := time.Now()
					_, err, attempt := ProcessItem(innerCtx, adapted, item, handler)
					err = wrapStageErr(name, err, attempt)
					dur := time.Since(start)

					if err == ErrSkipped {
						errs.Add(1)
						hook.OnItem(innerCtx, name, dur, err)
						continue
					}
					if err != nil {
						hook.OnItem(innerCtx, name, dur, err)
						errOnce.Do(func() { firstErr = err })
						innerCancel()
						return
					}
					proc.Add(1)
					hook.OnItem(innerCtx, name, dur, nil)
				case <-innerCtx.Done():
					return
				}
			}
		}()
	}

	wg.Wait()
	processed = proc.Load()
	errCount = errs.Load()
	return firstErr
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func buildGraphNodes(g *Graph) []GraphNode {
	nodes := make([]GraphNode, len(g.Nodes))
	for i, n := range g.Nodes {
		inputs := make([]int, len(n.Inputs))
		for j, ref := range n.Inputs {
			inputs[j] = ref.Node
		}
		name := n.Name
		if name == "" {
			name = kindName(n.Kind)
		}
		buf := n.Buffer
		if buf <= 0 {
			buf = DefaultBuffer
		}
		nodes[i] = GraphNode{
			ID:             n.ID,
			Name:           name,
			Kind:           kindName(n.Kind),
			Inputs:         inputs,
			Concurrency:    n.Concurrency,
			Buffer:         buf,
			Overflow:       n.Overflow,
			BatchSize:      n.BatchSize,
			Timeout:        time.Duration(n.Timeout),
			HasRetry:       n.HasRetry,
			HasSupervision: n.Supervision.MaxRestarts > 0,
		}
	}
	return nodes
}

func nodeName(n *Node, fallback string) string {
	if n.Name != "" {
		return n.Name
	}
	return fallback
}

func kindName(k NodeKind) string {
	switch k {
	case Source:
		return "source"
	case Map:
		return "map"
	case FlatMap:
		return "flatmap"
	case Filter:
		return "filter"
	case Tap:
		return "tap"
	case Take:
		return "take"
	case Batch:
		return "batch"
	case Partition:
		return "partition"
	case BroadcastNode:
		return "broadcast"
	case Merge:
		return "merge"
	case Sink:
		return "sink"
	case TakeWhile:
		return "takewhile"
	case ZipNode:
		return "zip"
	case ThrottleNode:
		return "throttle"
	case DebounceNode:
		return "debounce"
	case ReduceNode:
		return "reduce"
	case MapResultNode:
		return "mapresult"
	case WithLatestFromNode:
		return "withlatestfrom"
	case SwitchMapNode:
		return "switchmap"
	case ExhaustMapNode:
		return "exhaustmap"
	case CombineLatestNode:
		return "combinelatest"
	case BalanceNode:
		return "balance"
	case SessionWindowNode:
		return "sessionwindow"
	default:
		return "unknown"
	}
}

func nodeErrorHandler(n *Node) ErrorHandler {
	if n.ErrorHandler != nil {
		return n.ErrorHandler
	}
	return DefaultHandler{}
}

// ---------------------------------------------------------------------------
// Reduce
// ---------------------------------------------------------------------------

// runReduce folds the entire input stream into a single accumulated value and
// emits it once when the input closes. Always emits exactly one item — the seed
// value is emitted unchanged on empty input.
func runReduce(ctx context.Context, n *Node, inCh chan any, outbox Outbox, hook Hook, name string) error {
	fn := n.Fn.(func(any, any) any)
	hook.OnStageStart(ctx, name)
	var count int64
	defer func() { hook.OnStageDone(ctx, name, count, 0) }()

	acc := n.ReduceSeed
	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				// Input exhausted — emit the accumulated value.
				hook.OnItem(ctx, name, 0, nil)
				return outbox.Send(ctx, acc)
			}
			count++
			acc = fn(acc, item)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ---------------------------------------------------------------------------
// Throttle
// ---------------------------------------------------------------------------

// runThrottle emits the first item that arrives in each window of d, dropping
// all subsequent items that arrive before d has elapsed since the last emission.
func runThrottle(ctx context.Context, n *Node, inCh chan any, outbox Outbox, hook Hook, name string, clk Clock) error {
	d := time.Duration(n.ThrottleDuration)
	hook.OnStageStart(ctx, name)
	var processed, dropped int64
	defer func() { hook.OnStageDone(ctx, name, processed, dropped) }()

	var lastEmit time.Time // zero value → nothing emitted yet
	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return nil
			}
			now := clk.Now()
			if lastEmit.IsZero() || now.Sub(lastEmit) >= d {
				lastEmit = now
				processed++
				hook.OnItem(ctx, name, 0, nil)
				if err := outbox.Send(ctx, item); err != nil {
					return err
				}
			} else {
				dropped++
				hook.OnItem(ctx, name, 0, nil)
				if oh, ok := hook.(OverflowHook); ok {
					oh.OnDrop(ctx, name, item)
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ---------------------------------------------------------------------------
// Debounce
// ---------------------------------------------------------------------------

// runDebounce emits an item only after d has passed with no new items arriving.
// Each new arrival resets the timer; only the last item in a burst is emitted.
// On input close, any pending item is flushed immediately.
func runDebounce(ctx context.Context, n *Node, inCh chan any, outbox Outbox, hook Hook, name string, clk Clock) error {
	d := time.Duration(n.ThrottleDuration)
	hook.OnStageStart(ctx, name)
	var processed, dropped int64
	defer func() { hook.OnStageDone(ctx, name, processed, dropped) }()

	var pending any
	hasPending := false

	timer := clk.NewTimer(d)
	timer.Stop()
	// Drain the channel in case Stop raced with a fire.
	select {
	case <-timer.C():
	default:
	}
	defer timer.Stop()

	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				// Input closed — flush any pending item.
				if hasPending {
					processed++
					hook.OnItem(ctx, name, 0, nil)
					_ = outbox.Send(ctx, pending)
				}
				return nil
			}
			// Replace pending item; each replacement is a "drop" of the prior.
			if hasPending {
				dropped++
			}
			pending = item
			hasPending = true
			// Reset the quiet-period timer.
			if !timer.Stop() {
				select {
				case <-timer.C():
				default:
				}
			}
			timer.Reset(d)

		case <-timer.C():
			if hasPending {
				processed++
				hook.OnItem(ctx, name, 0, nil)
				if err := outbox.Send(ctx, pending); err != nil {
					return err
				}
				hasPending = false
			}

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// runSessionWindow groups items into variable-length windows based on inactivity.
// The window is flushed as a []T when no new item arrives within gap duration.
// Each incoming item resets the gap timer. If upstream closes, the current
// session is flushed immediately regardless of the gap timer state.
func runSessionWindow(ctx context.Context, n *Node, inCh chan any, outbox Outbox, hook Hook, name string, clk Clock) error {
	gap := time.Duration(n.ThrottleDuration)
	convert := n.BatchConvert

	hook.OnStageStart(ctx, name)
	var count int64
	defer func() { hook.OnStageDone(ctx, name, count, 0) }()

	timer := clk.NewTimer(gap)
	timer.Stop()
	// Drain the channel in case Stop raced with a fire.
	select {
	case <-timer.C():
	default:
	}
	defer timer.Stop()

	var buf []any

	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		out := convert(buf)
		clear(buf)
		buf = buf[:0]
		// Stop and drain the timer.
		if !timer.Stop() {
			select {
			case <-timer.C():
			default:
			}
		}
		hook.OnItem(ctx, name, 0, nil)
		count++
		return outbox.Send(ctx, out)
	}

	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return flush()
			}
			buf = append(buf, item)
			// Reset the gap timer (stop + drain + reset, same as runDebounce).
			if !timer.Stop() {
				select {
				case <-timer.C():
				default:
				}
			}
			timer.Reset(gap)

		case <-timer.C():
			if err := flush(); err != nil {
				return err
			}

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// runMapResult executes a Map-like transformation but routes successful results
// to okOutbox (port 0) and failed results to errOutbox (port 1).
// It does not use ErrorHandler — every error is always routed.
func runMapResult(ctx context.Context, n *Node, inCh chan any, okOutbox, errOutbox Outbox, hook Hook, name string, sampleRate int64) error {
	fn := n.Fn.(func(context.Context, any) (any, error))
	wrapErr := n.MapResultErrWrap
	hook.OnStageStart(ctx, name)
	var processed, errCount int64
	defer func() { hook.OnStageDone(ctx, name, processed, errCount) }()

	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return nil
			}
			start := time.Now()
			result, err := fn(ctx, item)
			dur := time.Since(start)
			if err != nil {
				errCount++
				hook.OnItem(ctx, name, dur, err)
				if sendErr := errOutbox.Send(ctx, wrapErr(item, err)); sendErr != nil {
					return sendErr
				}
				continue
			}
			processed++
			hook.OnItem(ctx, name, dur, nil)
			if sampleRate > 0 && processed%sampleRate == 0 {
				if sh, ok := hook.(SampleHook); ok {
					sh.OnItemSample(ctx, name, result)
				}
			}
			if sendErr := okOutbox.Send(ctx, result); sendErr != nil {
				return sendErr
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// runWithLatestFrom combines each item from the primary channel with the most
// recently seen value from the secondary channel. Primary items received before
// any secondary value has arrived are silently dropped.
func runWithLatestFrom(ctx context.Context, n *Node, primaryCh, secondaryCh chan any, outbox Outbox, hook Hook, name string) error {
	convert := n.ZipConvert
	hook.OnStageStart(ctx, name)
	var count int64
	defer func() { hook.OnStageDone(ctx, name, count, 0) }()

	var (
		mu       sync.Mutex
		latest   any
		hasValue bool
	)

	// Background goroutine: drain secondary and keep the latest value.
	go func() {
		for {
			select {
			case item, ok := <-secondaryCh:
				if !ok {
					return
				}
				mu.Lock()
				latest = item
				hasValue = true
				mu.Unlock()
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case item, ok := <-primaryCh:
			if !ok {
				return nil
			}
			mu.Lock()
			hv, lv := hasValue, latest
			mu.Unlock()
			if !hv {
				continue // no secondary value yet — drop
			}
			count++
			hook.OnItem(ctx, name, 0, nil)
			if err := outbox.Send(ctx, convert(item, lv)); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// runCombineLatest is the symmetric counterpart to runWithLatestFrom: either
// input channel emitting triggers an output paired with the latest value from
// the other side. No output is produced until both sides have emitted at least once.
func runCombineLatest(ctx context.Context, n *Node, aCh, bCh chan any, outbox Outbox, hook Hook, name string) error {
	convert := n.ZipConvert
	hook.OnStageStart(ctx, name)
	var count atomic.Int64
	defer func() { hook.OnStageDone(ctx, name, count.Load(), 0) }()

	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	var (
		mu      sync.Mutex
		latestA any
		latestB any
		hasA    bool
		hasB    bool

		errOnce  sync.Once
		firstErr error
	)

	eg, egCtx := errgroup.WithContext(innerCtx)

	// Goroutine A: reads from aCh, updates latestA, emits when both sides ready.
	eg.Go(func() error {
		for {
			select {
			case item, ok := <-aCh:
				if !ok {
					return nil
				}
				mu.Lock()
				latestA = item
				hasA = true
				ready := hasB
				lB := latestB
				mu.Unlock()
				if !ready {
					continue
				}
				count.Add(1)
				hook.OnItem(egCtx, name, 0, nil)
				if err := outbox.Send(egCtx, convert(item, lB)); err != nil {
					errOnce.Do(func() { firstErr = err })
					innerCancel()
					return err
				}
			case <-egCtx.Done():
				return nil
			}
		}
	})

	// Goroutine B: reads from bCh, updates latestB, emits when both sides ready.
	eg.Go(func() error {
		for {
			select {
			case item, ok := <-bCh:
				if !ok {
					return nil
				}
				mu.Lock()
				latestB = item
				hasB = true
				ready := hasA
				lA := latestA
				mu.Unlock()
				if !ready {
					continue
				}
				count.Add(1)
				hook.OnItem(egCtx, name, 0, nil)
				if err := outbox.Send(egCtx, convert(lA, item)); err != nil {
					errOnce.Do(func() { firstErr = err })
					innerCancel()
					return err
				}
			case <-egCtx.Done():
				return nil
			}
		}
	})

	_ = eg.Wait()
	return firstErr
}

// runBalance distributes items from inCh across outboxes in round-robin order.
// Each item goes to exactly one output.
func runBalance(ctx context.Context, inCh chan any, outboxes []Outbox, hook Hook, name string) error {
	hook.OnStageStart(ctx, name)
	var count int64
	defer func() { hook.OnStageDone(ctx, name, count, 0) }()

	n := len(outboxes)
	var i int
	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return nil
			}
			count++
			hook.OnItem(ctx, name, 0, nil)
			if err := outboxes[i%n].Send(ctx, item); err != nil {
				return err
			}
			i++
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// resolveCacheWrap returns n unchanged if CacheWrapFn is nil; otherwise it
// returns a shallow copy of n with Fn replaced by the cache-wrapped version.
// This avoids mutating the shared graph node between runs.
func resolveCacheWrap(n *Node, cfg RunConfig) *Node {
	if n.CacheWrapFn == nil {
		return n
	}
	wrapped := n.CacheWrapFn(cfg.DefaultCache, cfg.DefaultCacheTTL, effectiveCodec(cfg.Codec))
	if wrapped == nil {
		return n
	}
	cp := *n
	cp.Fn = wrapped
	return &cp
}
