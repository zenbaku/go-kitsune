// Package engine implements the type-erased runtime for kitsune pipelines.
// All values flow as [any]; the public kitsune package adds generic type safety.
package engine

import (
	"sync"
	"time"
)

// Graph holds the pipeline DAG. Nodes are added during pipeline construction;
// execution happens when [Run] is called.
type Graph struct {
	mu    sync.Mutex
	Nodes []*Node

	// State management.
	KeyInits map[string]func(store Store) any // key name → factory that creates *Ref
	Refs     map[string]any                   // populated at Run time
}

// New returns an empty graph.
func New() *Graph {
	return &Graph{
		KeyInits: make(map[string]func(store Store) any),
	}
}

// AddNode appends a node and returns its ID.
func (g *Graph) AddNode(n *Node) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n.ID = len(g.Nodes)
	g.Nodes = append(g.Nodes, n)
	return n.ID
}

// RegisterKey records a state key factory. Only the first registration wins.
func (g *Graph) RegisterKey(name string, factory func(store Store) any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.KeyInits[name]; !ok {
		g.KeyInits[name] = factory
	}
}

// GetRef returns the Ref for the named key (must be called after InitRefs).
func (g *Graph) GetRef(name string) any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Refs[name]
}

// InitRefs creates all registered Refs from their factories.
// store is nil for memory-only mode.
func (g *Graph) InitRefs(store Store) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Refs = make(map[string]any, len(g.KeyInits))
	for name, factory := range g.KeyInits {
		g.Refs[name] = factory(store)
	}
}

// Node represents a single stage in the pipeline DAG.
type Node struct {
	ID   int
	Kind NodeKind
	Name string
	Fn   any // type-erased processing function; signature varies by Kind

	Inputs []InputRef

	Concurrency  int
	Ordered      bool // preserve input order when Concurrency > 1
	Buffer       int
	Overflow     int // 0=Block (default), 1=DropNewest, 2=DropOldest
	ErrorHandler ErrorHandler
	Supervision  SupervisionPolicy
	HasRetry     bool // true when an error handler with retry semantics is attached

	// Per-item timeout (nanoseconds; 0 = none). Set by the kitsune.Timeout StageOption.
	Timeout int64

	// Batch-specific.
	BatchSize    int
	BatchTimeout int64           // nanoseconds (avoids importing time)
	BatchConvert func([]any) any // []any → []T

	// Take-specific.
	TakeN int

	// Broadcast-specific.
	BroadcastN int

	// Zip-specific.
	ZipConvert func(any, any) any // (a, b) → Pair[A,B]

	// Throttle/Debounce-specific.
	ThrottleDuration int64 // nanoseconds; shared by ThrottleNode and DebounceNode

	// Reduce-specific.
	ReduceSeed any // initial accumulator value

	// Cache-specific.
	// CacheWrapFn, if set, is called at Run time to produce a cache-wrapped Fn.
	// It receives the run-level default cache and TTL, and returns a replacement Fn.
	// The factory closes over any per-stage overrides and falls back to the
	// run-level defaults when they are nil/zero.
	CacheWrapFn func(defaultCache Cache, defaultTTL time.Duration) any

	// MapResult-specific.
	// MapResultErrWrap converts an (input, error) pair into the type-erased ErrItem
	// value that is sent to port 1. Set by the public kitsune.MapResult function.
	MapResultErrWrap func(input any, err error) any
}

// InputRef identifies the output port of an upstream node.
type InputRef struct {
	Node int
	Port int
}

// ChannelKey identifies a single output channel (node + port).
type ChannelKey struct {
	Node int
	Port int
}

// NodeKind identifies the processing strategy for a node.
type NodeKind int

const (
	Source             NodeKind = iota
	Map                         // 1:1
	FlatMap                     // 1:N
	Filter                      // predicate gate
	Tap                         // side-effect passthrough
	Take                        // limit N items
	Batch                       // collect into slices
	Partition                   // split by predicate → two outputs
	BroadcastNode               // copy to all N outputs
	Merge                       // fan-in from multiple inputs
	Sink                        // terminal consumer
	TakeWhile                   // emit until predicate fails, then signal done
	ZipNode                     // pair items from two inputs by position
	ThrottleNode                // emit at most one item per duration window
	DebounceNode                // emit item only after a quiet period of duration d
	ReduceNode                  // fold entire stream into a single value, always emits once
	MapResultNode               // map with error routing: success → port 0, error → port 1
	WithLatestFromNode          // combine primary items with most-recent secondary value
)

// DefaultBuffer is the default channel buffer size between stages.
const DefaultBuffer = 16

// Overflow strategy constants (stored as int on Node to keep engine package dependency-free).
const (
	OverflowBlock      = 0 // block until space is available (default)
	OverflowDropNewest = 1 // discard the incoming item when the buffer is full
	OverflowDropOldest = 2 // evict the oldest buffered item to make room
)
