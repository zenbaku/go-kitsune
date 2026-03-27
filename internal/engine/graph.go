// Package engine implements the type-erased runtime for kitsune pipelines.
// All values flow as [any]; the public kitsune package adds generic type safety.
package engine

import "sync"

// Graph holds the pipeline DAG. Nodes are added during pipeline construction;
// execution happens when [Run] is called.
type Graph struct {
	mu    sync.Mutex
	Nodes []*Node

	// State management.
	KeyInits map[string]func(store any) any // key name → factory that creates *Ref; store is type-erased kitsune.Store
	Refs     map[string]any                 // populated at Run time
}

// New returns an empty graph.
func New() *Graph {
	return &Graph{
		KeyInits: make(map[string]func(store any) any),
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
func (g *Graph) RegisterKey(name string, factory func(store any) any) {
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
// The store parameter (type-erased kitsune.Store) is passed to each factory;
// nil means memory-only mode.
func (g *Graph) InitRefs(store any) {
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
	Buffer       int
	ErrorHandler ErrorHandler

	// Batch-specific.
	BatchSize    int
	BatchTimeout int64           // nanoseconds (avoids importing time)
	BatchConvert func([]any) any // []any → []T

	// Take-specific.
	TakeN int
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
	Source    NodeKind = iota
	Map                // 1:1
	FlatMap            // 1:N
	Filter             // predicate gate
	Tap                // side-effect passthrough
	Take               // limit N items
	Batch              // collect into slices
	Partition          // split by predicate → two outputs
	Merge              // fan-in from multiple inputs
	Sink               // terminal consumer
)

// DefaultBuffer is the default channel buffer size between stages.
const DefaultBuffer = 16
