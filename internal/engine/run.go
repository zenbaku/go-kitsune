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
	Hook  Hook
	Store any // type-erased kitsune.Store; nil means memory-only
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

	// done is closed by Take (or similar early-exit nodes) to tell
	// sources to stop producing. This avoids cancelling the whole
	// context, which would disrupt downstream stages still draining.
	done := make(chan struct{})
	var doneOnce sync.Once
	signalDone := func() { doneOnce.Do(func() { close(done) }) }

	chans := CreateChannels(g)
	eg, ctx := errgroup.WithContext(ctx)

	for _, n := range g.Nodes {
		eg.Go(nodeRunner(ctx, n, chans, hook, done, signalDone))
	}

	return eg.Wait()
}

// ---------------------------------------------------------------------------
// Node dispatch
// ---------------------------------------------------------------------------

func nodeRunner(ctx context.Context, n *Node, chans map[ChannelKey]chan any, hook Hook, done <-chan struct{}, signalDone func()) func() error {
	outCh := chans[ChannelKey{n.ID, 0}]

	var inChs []chan any
	for _, ref := range n.Inputs {
		inChs = append(inChs, chans[ChannelKey{ref.Node, ref.Port}])
	}
	var inCh chan any
	if len(inChs) > 0 {
		inCh = inChs[0]
	}

	switch n.Kind {
	case Source:
		return func() error { return runSource(ctx, n, outCh, hook, done) }
	case Map:
		return func() error { return runMap(ctx, n, inCh, outCh, hook) }
	case FlatMap:
		return func() error { return runFlatMap(ctx, n, inCh, outCh, hook) }
	case Filter:
		return func() error { return runFilter(ctx, n, inCh, outCh) }
	case Tap:
		return func() error { return runTap(ctx, n, inCh, outCh) }
	case Take:
		return func() error { return runTake(ctx, n, inCh, outCh, signalDone) }
	case Batch:
		return func() error { return runBatch(ctx, n, inCh, outCh) }
	case Partition:
		matchCh := chans[ChannelKey{n.ID, 0}]
		restCh := chans[ChannelKey{n.ID, 1}]
		return func() error { return runPartition(ctx, n, inCh, matchCh, restCh) }
	case BroadcastNode:
		outChs := make([]chan any, n.BroadcastN)
		for i := range n.BroadcastN {
			outChs[i] = chans[ChannelKey{n.ID, i}]
		}
		return func() error { return runBroadcast(ctx, inCh, outChs) }
	case Merge:
		return func() error { return runMerge(ctx, inChs, outCh) }
	case Sink:
		return func() error { return runSink(ctx, n, inCh, hook) }
	default:
		return func() error { return fmt.Errorf("kitsune: unknown node kind %d", n.Kind) }
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

func runSource(ctx context.Context, n *Node, outCh chan any, hook Hook, done <-chan struct{}) error {
	defer close(outCh)
	name := nodeName(n, "source")
	hook.OnStageStart(ctx, name)
	var count int64

	fn := n.Fn.(func(context.Context, func(any) bool) error)
	err := fn(ctx, func(item any) bool {
		select {
		case outCh <- item:
			count++
			return true
		case <-done:
			return false
		case <-ctx.Done():
			return false
		}
	})

	hook.OnStageDone(ctx, name, count, 0)
	return err
}

// ---------------------------------------------------------------------------
// Map (1:1)
// ---------------------------------------------------------------------------

func runMap(ctx context.Context, n *Node, inCh, outCh chan any, hook Hook) error {
	defer close(outCh)
	fn := n.Fn.(func(context.Context, any) (any, error))
	handler := nodeErrorHandler(n)
	name := nodeName(n, "map")

	if n.Concurrency <= 1 {
		return runMapSingle(ctx, fn, inCh, outCh, handler, name, hook)
	}
	return runMapConcurrent(ctx, fn, inCh, outCh, n.Concurrency, handler, name, hook)
}

func runMapSingle(ctx context.Context, fn func(context.Context, any) (any, error), inCh, outCh chan any, handler ErrorHandler, name string, hook Hook) error {
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

			select {
			case outCh <- result:
			case <-ctx.Done():
				return ctx.Err()
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func runMapConcurrent(ctx context.Context, fn func(context.Context, any) (any, error), inCh, outCh chan any, concurrency int, handler ErrorHandler, name string, hook Hook) error {
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
					processed.Add(1)
					hook.OnItem(innerCtx, name, dur, nil)

					select {
					case outCh <- result:
					case <-innerCtx.Done():
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

// ---------------------------------------------------------------------------
// FlatMap (1:N)
// ---------------------------------------------------------------------------

func runFlatMap(ctx context.Context, n *Node, inCh, outCh chan any, hook Hook) error {
	defer close(outCh)
	fn := n.Fn.(func(context.Context, any) ([]any, error))
	handler := nodeErrorHandler(n)
	name := nodeName(n, "flatmap")

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
				select {
				case outCh <- r:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ---------------------------------------------------------------------------
// Filter / Tap / Take
// ---------------------------------------------------------------------------

func runFilter(ctx context.Context, n *Node, inCh, outCh chan any) error {
	defer close(outCh)
	fn := n.Fn.(func(any) bool)
	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return nil
			}
			if fn(item) {
				select {
				case outCh <- item:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func runTap(ctx context.Context, n *Node, inCh, outCh chan any) error {
	defer close(outCh)
	fn := n.Fn.(func(any))
	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return nil
			}
			fn(item)
			select {
			case outCh <- item:
			case <-ctx.Done():
				return ctx.Err()
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func runTake(ctx context.Context, n *Node, inCh, outCh chan any, signalDone func()) error {
	defer close(outCh)
	remaining := n.TakeN
	for remaining > 0 {
		select {
		case item, ok := <-inCh:
			if !ok {
				return nil
			}
			remaining--
			select {
			case outCh <- item:
			case <-ctx.Done():
				return ctx.Err()
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
// Batch
// ---------------------------------------------------------------------------

func runBatch(ctx context.Context, n *Node, inCh, outCh chan any) error {
	defer close(outCh)

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
		select {
		case outCh <- converted:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return flush()
			}
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

func runPartition(ctx context.Context, n *Node, inCh, matchCh, restCh chan any) error {
	defer close(matchCh)
	defer close(restCh)
	fn := n.Fn.(func(any) bool)

	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return nil
			}
			target := restCh
			if fn(item) {
				target = matchCh
			}
			select {
			case target <- item:
			case <-ctx.Done():
				return ctx.Err()
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func runBroadcast(ctx context.Context, inCh chan any, outChs []chan any) error {
	defer func() {
		for _, ch := range outChs {
			close(ch)
		}
	}()
	for {
		select {
		case item, ok := <-inCh:
			if !ok {
				return nil
			}
			for _, ch := range outChs {
				select {
				case ch <- item:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func runMerge(ctx context.Context, inChs []chan any, outCh chan any) error {
	defer close(outCh)
	var wg sync.WaitGroup
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
					select {
					case outCh <- item:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
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

func nodeName(n *Node, fallback string) string {
	if n.Name != "" {
		return n.Name
	}
	return fallback
}

func nodeErrorHandler(n *Node) ErrorHandler {
	if n.ErrorHandler != nil {
		return n.ErrorHandler
	}
	return DefaultHandler{}
}
