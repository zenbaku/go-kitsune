package engine

import (
	"context"
	"fmt"
)

// ---------------------------------------------------------------------------
// Stage fusion
//
// Stage fusion detects runs of consecutive fusible pipeline stages and
// collapses them into a single goroutine that calls stage functions directly,
// eliminating all inter-stage channel hops between them.
//
// A fusible node is one that satisfies ALL of:
//   - Kind is Map, FlatMap, Filter, or Sink (terminal)
//   - Concurrency <= 1
//   - No supervision policy
//   - Default error handler (nil)
//   - No per-item timeout
//   - No cache wrapping
//   - Block overflow (default — only blockingOutbox)
//
// Additionally, the run-time hook must be NoopHook; any instrumentation
// disables fusion entirely to preserve hook semantics.
//
// A fusible chain of length >= 2 is collapsed into one goroutine that:
//   - Reads from the head's real input channel (with receiveBatchSize drain)
//   - Calls each stage's function inline
//   - Writes to the tail's real output channel (or calls the sink fn)
//   - Closes all node output channels on exit
//
// Interior channels (created by CreateChannels but bypassed by fusion) are
// closed by the fused goroutine to release resources and prevent hangs.
// ---------------------------------------------------------------------------

// fusedStep is one stage in a fused pipeline chain.
type fusedStep struct {
	kind     NodeKind
	name     string
	mapFn    func(context.Context, any) (any, error) // Map
	filterFn func(any) bool                           // Filter
	sinkFn   func(context.Context, any) error         // Sink (terminal)
}

// fusedChain describes a sequence of consecutive fusible stages that will be
// executed as a single goroutine with direct function calls.
type fusedChain struct {
	steps      []fusedStep
	headID     int
	tailID     int
	nodeIDs    []int      // all node IDs, head to tail
	isSinkTail bool       // true when the last step is a Sink
	inCh       chan any   // channel feeding the head node
	outCh      chan any   // tail node output channel; nil when isSinkTail
	closeChs   []chan any // output channels to close on exit (all non-Sink nodes in chain)
}

// isFusibleNode reports whether n can participate in a fast-path fusion chain.
// The run-time NoopHook condition is checked separately in detectFusionChains.
func isFusibleNode(n *Node) bool {
	// Use nodeErrorHandler (same logic as the fast-path dispatchers) so that
	// both nil and explicit DefaultHandler{} are treated identically.
	// FlatMap is excluded from fusion: its 1-to-N expansion would require a
	// per-item yield closure allocation, negating the fusion benefit. FlatMap
	// already has runFlatMapFastPath which is zero-alloc per item.
	_, isDefault := nodeErrorHandler(n).(DefaultHandler)
	switch n.Kind {
	case Map, Filter:
		return n.Concurrency <= 1 &&
			!n.Supervision.HasSupervision() &&
			isDefault &&
			n.Timeout == 0 &&
			n.CacheWrapFn == nil &&
			n.Overflow == OverflowBlock
	case Sink:
		// Sink may be the terminal step of a chain.
		return n.Concurrency <= 1 &&
			!n.Supervision.HasSupervision() &&
			isDefault &&
			len(n.Inputs) == 1
	default:
		return false
	}
}

// makeFusedStep builds a fusedStep from a fusible node.
// Panics on an unexpected kind — callers must only pass fusible nodes.
func makeFusedStep(n *Node) fusedStep {
	name := n.Name
	if name == "" {
		name = kindName(n.Kind)
	}
	switch n.Kind {
	case Map:
		return fusedStep{kind: Map, name: name, mapFn: n.Fn.(func(context.Context, any) (any, error))}
	case Filter:
		return fusedStep{kind: Filter, name: name, filterFn: n.Fn.(func(any) bool)}
	case Sink:
		return fusedStep{kind: Sink, name: name, sinkFn: n.Fn.(func(context.Context, any) error)}
	}
	panic(fmt.Sprintf("kitsune: makeFusedStep called on non-fusible kind %d", n.Kind))
}

// detectFusionChains scans the graph for runs of consecutive fusible nodes.
// Returns a map from head node ID → fusedChain, or nil if no chains are found
// or if the hook is not NoopHook.
func detectFusionChains(g *Graph, chans map[ChannelKey]chan any, hook Hook) map[int]*fusedChain {
	// Instrumentation disables fusion: any hook that is not NoopHook may observe
	// per-item events that fusion would silently skip.
	if _, ok := hook.(NoopHook); !ok {
		return nil
	}

	// Build: each output (nodeID, port) → the single consuming node ID.
	// -1 means multiple consumers (fan-out), which breaks chain continuity.
	outputConsumer := make(map[ChannelKey]int, len(g.Nodes))
	for _, n := range g.Nodes {
		for _, ref := range n.Inputs {
			key := ChannelKey{ref.Node, ref.Port}
			if prev, exists := outputConsumer[key]; exists {
				if prev != -1 {
					outputConsumer[key] = -1
				}
			} else {
				outputConsumer[key] = n.ID
			}
		}
	}

	nodeByID := make(map[int]*Node, len(g.Nodes))
	for _, n := range g.Nodes {
		nodeByID[n.ID] = n
	}

	inChain := make(map[int]bool, len(g.Nodes)) // nodes already assigned to a chain
	result := make(map[int]*fusedChain)

	for _, n := range g.Nodes {
		if inChain[n.ID] || !isFusibleNode(n) {
			continue
		}
		// A head is a fusible node whose immediate upstream is not fusible.
		// Multi-input nodes (Merge, Zip, etc.) can't be chain heads.
		if len(n.Inputs) != 1 {
			continue
		}
		upstream := nodeByID[n.Inputs[0].Node]
		if upstream != nil && isFusibleNode(upstream) {
			continue // this node is interior; will be picked up by the head walk
		}

		// Walk forward, extending the chain while the downstream is fusible.
		chain := &fusedChain{headID: n.ID}
		cur := n
		for {
			chain.steps = append(chain.steps, makeFusedStep(cur))
			chain.nodeIDs = append(chain.nodeIDs, cur.ID)
			chain.tailID = cur.ID
			inChain[cur.ID] = true

			if cur.Kind == Sink {
				chain.isSinkTail = true
				break
			}

			consumerID, ok := outputConsumer[ChannelKey{cur.ID, 0}]
			if !ok || consumerID == -1 {
				break
			}
			next := nodeByID[consumerID]
			if next == nil || !isFusibleNode(next) {
				break
			}
			cur = next
		}

		// Single-node "chains" don't eliminate any channel hops; skip them.
		if len(chain.steps) < 2 {
			for _, id := range chain.nodeIDs {
				delete(inChain, id)
			}
			continue
		}

		// Populate channel fields.
		chain.inCh = chans[ChannelKey{n.Inputs[0].Node, n.Inputs[0].Port}]
		if !chain.isSinkTail {
			chain.outCh = chans[ChannelKey{chain.tailID, 0}]
		}
		// Collect all node output channels to close on exit.
		// Sink nodes have no output channel (not present in chans).
		for _, id := range chain.nodeIDs {
			if ch, ok := chans[ChannelKey{id, 0}]; ok {
				chain.closeChs = append(chain.closeChs, ch)
			}
		}

		result[chain.headID] = chain
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

// buildFusionSets returns two lookup maps derived from a fusion chains map:
// fusedIDs is the set of all node IDs in any chain (for skipping nodeRunner),
// and fusedHeads maps head node IDs to their chains (for launching fused runners).
func buildFusionSets(chains map[int]*fusedChain) (fusedIDs map[int]bool, fusedHeads map[int]*fusedChain) {
	if len(chains) == 0 {
		return nil, nil
	}
	fusedIDs = make(map[int]bool)
	fusedHeads = make(map[int]*fusedChain)
	for headID, chain := range chains {
		fusedHeads[headID] = chain
		for _, id := range chain.nodeIDs {
			fusedIDs[id] = true
		}
	}
	return
}

// fusedNodeRunner returns an errgroup-compatible goroutine function that
// runs the fused chain and closes all relevant output channels on exit.
func fusedNodeRunner(ctx context.Context, chain *fusedChain) func() error {
	return func() error {
		defer func() {
			for _, ch := range chain.closeChs {
				close(ch)
			}
		}()
		defer func() { go func() { for range chain.inCh {} }() }()
		return runFusedChain(ctx, chain.steps, chain.inCh, chain.outCh)
	}
}

// runFusedChain runs the fused pipeline chain. Only Map, Filter, and Sink
// steps can appear here — FlatMap is excluded from fusion.
func runFusedChain(ctx context.Context, steps []fusedStep, inCh chan any, outCh chan any) error {
	return runFusedLinear(ctx, steps, inCh, outCh)
}

// runFusedLinear executes a chain of Map, Filter, and/or Sink steps (no FlatMap)
// in a tight loop. Uses the same receiveBatchSize micro-batch drain pattern as
// the individual stage fast paths.
func runFusedLinear(ctx context.Context, steps []fusedStep, inCh chan any, outCh chan any) error {
	var buf [receiveBatchSize]any
	for {
		item, ok := <-inCh
		if !ok {
			return ctx.Err()
		}
		buf[0] = item
		n := 1
		closed := false
	fillLinear:
		for n < receiveBatchSize {
			select {
			case v, ok2 := <-inCh:
				if !ok2 {
					closed = true
					break fillLinear
				}
				buf[n] = v
				n++
			default:
				break fillLinear
			}
		}
		for i := range n {
			val := buf[i]
			buf[i] = nil
			skip := false
			for _, step := range steps {
				if skip {
					break
				}
				switch step.kind {
				case Map:
					r, err := step.mapFn(ctx, val)
					if err != nil {
						if err == ErrSkipped {
							skip = true
						} else {
							return &StageError{Stage: step.name, Cause: err}
						}
					} else {
						val = r
					}
				case Filter:
					if !step.filterFn(val) {
						skip = true
					}
				case Sink:
					if err := step.sinkFn(ctx, val); err != nil {
						if err == ErrSkipped {
							skip = true
						} else {
							return &StageError{Stage: step.name, Cause: err}
						}
					}
					skip = true // item consumed by sink; no channel send
				}
			}
			if skip {
				continue
			}
			outCh <- val
		}
		if closed {
			return ctx.Err()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

