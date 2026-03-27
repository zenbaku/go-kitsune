package engine

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

// RunConfig holds runtime options for a pipeline execution.
type RunConfig struct {
	Hook         Hook
	Store        any           // type-erased kitsune.Store; nil means memory-only
	DrainTimeout time.Duration // 0 = no drain (default); >0 = graceful drain on context cancel
}

// Run validates the graph, wires channels, and executes all stages.
func Run(ctx context.Context, g *Graph, cfg RunConfig) error {
	if err := Validate(g); err != nil {
		return err
	}

	g.InitRefs(cfg.Store)

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
				if n.Kind == Partition {
					for port := range 2 {
						if ch, ok := chans[ChannelKey{n.ID, port}]; ok {
							total += len(ch)
							cap_ += cap(ch)
						}
					}
				} else if n.Kind == BroadcastNode {
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
		return runWithDrain(ctx, cfg.DrainTimeout, g, chans, outboxes, hook, done, signalDone)
	}

	eg, egCtx := errgroup.WithContext(ctx)
	for _, n := range g.Nodes {
		eg.Go(nodeRunner(egCtx, n, chans, outboxes, hook, done, signalDone))
	}
	return eg.Wait()
}

// runWithDrain executes the pipeline with graceful drain semantics.
// When parentCtx is cancelled, sources are told to stop and the pipeline is
// given up to drainTimeout to finish processing in-flight items naturally.
// After the timeout, a hard cancel forces all remaining stages to exit.
func runWithDrain(parentCtx context.Context, drainTimeout time.Duration, g *Graph, chans map[ChannelKey]chan any, outboxes map[ChannelKey]Outbox, hook Hook, done chan struct{}, signalDone func()) error {
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
			case <-time.After(drainTimeout):
				// Drain timeout exceeded — force hard stop via drainCancel.
				drainCancel()
			case <-drainCtx.Done():
				// Pipeline finished draining (or error) before the timeout.
			}
		case <-drainCtx.Done():
			// Pipeline completed normally or via error — nothing to do.
		}
	}()

	for _, n := range g.Nodes {
		eg.Go(nodeRunner(egCtx, n, chans, outboxes, hook, done, signalDone))
	}

	return eg.Wait()
}

// ---------------------------------------------------------------------------
// Node dispatch
// ---------------------------------------------------------------------------

func nodeRunner(ctx context.Context, n *Node, chans map[ChannelKey]chan any, outboxes map[ChannelKey]Outbox, hook Hook, done <-chan struct{}, signalDone func()) func() error {
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

	// outCloser closes all output channels when this node finishes.
	// Centralised here so supervision can restart a stage without closing
	// its output channel prematurely.
	var outCloser func()
	var inner func() error

	name := nodeName(n, kindName(n.Kind))

	switch n.Kind {
	case Source:
		outCloser = func() { close(outCh) }
		inner = func() error { return runSource(ctx, n, outbox, hook, done) }
	case Map:
		outCloser = func() { close(outCh) }
		inner = func() error { return runMap(ctx, n, inCh, outbox, hook) }
	case FlatMap:
		outCloser = func() { close(outCh) }
		inner = func() error { return runFlatMap(ctx, n, inCh, outbox, hook) }
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
		inner = func() error { return runBatch(ctx, n, inCh, outbox, hook, name) }
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
		inner = func() error { return runSink(ctx, n, inCh, hook) }
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
	default:
		outCloser = func() {}
		inner = func() error { return fmt.Errorf("kitsune: unknown node kind %d", n.Kind) }
	}

	return func() error {
		defer outCloser()
		return supervise(ctx, n.Supervision, hook, name, inner)
	}
}

// ---------------------------------------------------------------------------
// ProcessItem — error / retry loop
// ---------------------------------------------------------------------------

// ProcessItem invokes fn with retry/skip/halt logic.
func ProcessItem(ctx context.Context, fn func(context.Context, any) (any, error), item any, h ErrorHandler) (any, error) {
	for attempt := 0; ; attempt++ {
		result, err := fn(ctx, item)
		if err == nil {
			return result, nil
		}
		switch h.Handle(err, attempt) {
		case ActionSkip:
			return nil, ErrSkipped
		case ActionRetry:
			bo := h.Backoff()
			if bo == nil {
				return nil, err
			}
			select {
			case <-time.After(bo(attempt)):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		default:
			return nil, err
		}
	}
}

// ---------------------------------------------------------------------------
// Source
// ---------------------------------------------------------------------------

func runSource(ctx context.Context, n *Node, outbox Outbox, hook Hook, done <-chan struct{}) error {
	name := nodeName(n, "source")

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
		if err := outbox.Send(srcCtx, item); err != nil {
			return false
		}
		count++
		hook.OnItem(srcCtx, name, 0, nil)
		if count%10 == 0 {
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

func runMap(ctx context.Context, n *Node, inCh chan any, outbox Outbox, hook Hook) error {
	fn := n.Fn.(func(context.Context, any) (any, error))
	handler := nodeErrorHandler(n)
	name := nodeName(n, "map")

	if n.Concurrency <= 1 {
		return runMapSingle(ctx, fn, inCh, outbox, handler, name, hook)
	}
	if n.Ordered {
		return runMapConcurrentOrdered(ctx, fn, inCh, outbox, n.Concurrency, handler, name, hook)
	}
	return runMapConcurrent(ctx, fn, inCh, outbox, n.Concurrency, handler, name, hook)
}

func runMapSingle(ctx context.Context, fn func(context.Context, any) (any, error), inCh chan any, outbox Outbox, handler ErrorHandler, name string, hook Hook) error {
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
			result, err := ProcessItem(ctx, fn, item, handler)
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
			if processed%10 == 0 {
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

func runMapConcurrent(ctx context.Context, fn func(context.Context, any) (any, error), inCh chan any, outbox Outbox, concurrency int, handler ErrorHandler, name string, hook Hook) error {
	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	hook.OnStageStart(ctx, name)
	var processed, errCount atomic.Int64

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)

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
					result, err := ProcessItem(innerCtx, fn, item, handler)
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
					if p%10 == 0 {
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

	type slot struct {
		item   any
		result any
		err    error
		done   chan struct{}
	}

	jobs := make(chan *slot, concurrency)
	pending := make(chan *slot, concurrency)

	// Workers — process slots concurrently, close slot.done when finished.
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range jobs {
				start := time.Now()
				s.result, s.err = ProcessItem(innerCtx, fn, s.item, handler)
				hook.OnItem(innerCtx, name, time.Since(start), s.err)
				close(s.done)
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
				continue
			}
			if s.err != nil {
				errCount.Add(1)
				innerCancel()
				collErr <- s.err
				return
			}
			processed.Add(1)
			if err := outbox.Send(innerCtx, s.result); err != nil {
				collErr <- err
				return
			}
		}
		collErr <- nil
	}()

	// Dispatcher — reads inCh, assigns slots, preserves order via pending.
	for item := range inCh {
		s := &slot{item: item, done: make(chan struct{})}
		// Send to pending first to guarantee output ordering.
		select {
		case pending <- s:
		case <-innerCtx.Done():
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
	fn := n.Fn.(func(context.Context, any) ([]any, error))
	handler := nodeErrorHandler(n)
	name := nodeName(n, "flatmap")

	if n.Concurrency <= 1 {
		return runFlatMapSingle(ctx, fn, inCh, outbox, handler, name, hook)
	}
	if n.Ordered {
		return runFlatMapConcurrentOrdered(ctx, fn, inCh, outbox, n.Concurrency, handler, name, hook)
	}
	return runFlatMapConcurrent(ctx, fn, inCh, outbox, n.Concurrency, handler, name, hook)
}

func runFlatMapSingle(ctx context.Context, fn func(context.Context, any) ([]any, error), inCh chan any, outbox Outbox, handler ErrorHandler, name string, hook Hook) error {
	adapted := func(ctx context.Context, in any) (any, error) { return fn(ctx, in) }

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
			resultAny, err := ProcessItem(ctx, adapted, item, handler)
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

			for _, r := range resultAny.([]any) {
				if err := outbox.Send(ctx, r); err != nil {
					return err
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func runFlatMapConcurrent(ctx context.Context, fn func(context.Context, any) ([]any, error), inCh chan any, outbox Outbox, concurrency int, handler ErrorHandler, name string, hook Hook) error {
	adapted := func(ctx context.Context, in any) (any, error) { return fn(ctx, in) }
	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	hook.OnStageStart(ctx, name)
	var processed, errCount atomic.Int64

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)

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
					resultAny, err := ProcessItem(innerCtx, adapted, item, handler)
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

					for _, r := range resultAny.([]any) {
						if err := outbox.Send(innerCtx, r); err != nil {
							errOnce.Do(func() { firstErr = err })
							innerCancel()
							return
						}
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

func runFlatMapConcurrentOrdered(ctx context.Context, fn func(context.Context, any) ([]any, error), inCh chan any, outbox Outbox, concurrency int, handler ErrorHandler, name string, hook Hook) error {
	adapted := func(ctx context.Context, in any) (any, error) { return fn(ctx, in) }
	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	hook.OnStageStart(ctx, name)

	type slot struct {
		item    any
		results []any
		err     error
		done    chan struct{}
	}

	jobs := make(chan *slot, concurrency)
	pending := make(chan *slot, concurrency)

	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range jobs {
				start := time.Now()
				resultAny, err := ProcessItem(innerCtx, adapted, s.item, handler)
				hook.OnItem(innerCtx, name, time.Since(start), err)
				if err == nil {
					s.results = resultAny.([]any)
				} else {
					s.err = err
				}
				close(s.done)
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
				continue
			}
			if s.err != nil {
				errCount.Add(1)
				innerCancel()
				collErr <- s.err
				return
			}
			processed.Add(1)
			for _, r := range s.results {
				if err := outbox.Send(innerCtx, r); err != nil {
					collErr <- err
					return
				}
			}
		}
		collErr <- nil
	}()

	for item := range inCh {
		s := &slot{item: item, done: make(chan struct{})}
		select {
		case pending <- s:
		case <-innerCtx.Done():
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
// Filter / Tap / Take
// ---------------------------------------------------------------------------

func runFilter(ctx context.Context, n *Node, inCh chan any, outbox Outbox, hook Hook, name string) error {
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

func runBatch(ctx context.Context, n *Node, inCh chan any, outbox Outbox, hook Hook, name string) error {
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

	var timer *time.Timer
	var timerCh <-chan time.Time
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		timer.Stop()
		defer timer.Stop()
		timerCh = timer.C
	}

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		converted := convert(batch)
		batch = make([]any, 0, initCap)
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

func runSink(ctx context.Context, n *Node, inCh chan any, hook Hook) error {
	fn := n.Fn.(func(context.Context, any) error)
	handler := nodeErrorHandler(n)
	name := nodeName(n, "sink")
	adapted := func(ctx context.Context, in any) (any, error) { return nil, fn(ctx, in) }

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
				_, err := ProcessItem(ctx, adapted, item, handler)
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
				if processed%10 == 0 {
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
					_, err := ProcessItem(innerCtx, adapted, item, handler)
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
			ID:          n.ID,
			Name:        name,
			Kind:        kindName(n.Kind),
			Inputs:      inputs,
			Concurrency: n.Concurrency,
			Buffer:      buf,
			Overflow:    n.Overflow,
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
