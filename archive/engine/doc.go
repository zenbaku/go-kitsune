// Package engine implements the type-erased runtime for kitsune pipelines.
//
// # Overview
//
// The engine is a standalone, dependency-free runtime that executes pipeline
// DAGs. It knows nothing about Go generics — all values flow as [any]. The
// public [kitsune] package wraps this package to provide type safety and a
// friendlier API.
//
// The split exists deliberately: the engine can be understood, tested, and
// extended in isolation, while the kitsune layer handles only the typed
// translation between user code and engine nodes.
//
// # Core model
//
// A pipeline is a [Graph] of [Node]s. Each node has:
//   - A [NodeKind] that selects its execution strategy.
//   - An [InputRef] list identifying which upstream (node, port) pairs it consumes.
//   - A type-erased Fn whose signature is determined by the kind.
//   - Concurrency and buffering settings.
//
// [Run] validates the graph, wires buffered channels between nodes, and
// launches one goroutine per node inside an [errgroup]. Items flow as [any]
// through [chan any] channels. Backpressure is applied via [Outbox], which
// also handles overflow strategies (block, drop-newest, drop-oldest).
//
// # Fn signatures by NodeKind
//
// The type assertion in each run function pins the expected signature:
//
//	Source:          func(ctx, yield func(any) bool) error
//	Map / FlatMap:   func(ctx, item any) (any, error)
//	FlatMap:         func(ctx, item any, yield func(any) error) error
//	Filter / Tap:    func(item any) bool
//	Sink:            func(ctx, item any) error
//	Batch:           aggregates items; BatchConvert func([]any) any converts the slice
//	Partition:       func(item any) bool  (true → port 0, false → port 1)
//	Reduce:          func(acc, item any) any
//
// # Extending the engine
//
// To add a new node kind:
//  1. Add a constant to [NodeKind].
//  2. Add any kind-specific fields to [Node].
//  3. Write a runXxx function in run.go.
//  4. Add a case to the switch in [nodeRunner].
//  5. Add a case to [kindName] (used in hook events and error messages).
//  6. Add tests in engine_test.go.
//  7. Wire up a builder in the kitsune package when ready to expose it.
//
// # Relationship to the kitsune package
//
// The kitsune package imports engine; engine never imports kitsune.
// The interfaces defined here ([Store], [Cache], [Hook] and its extensions)
// are re-exported from kitsune as type aliases — users see them as kitsune types.
//
// State refs ([kitsune.Ref]) are created by factories registered via
// [Graph.RegisterKey]. The factory receives a [Store] and returns a type-erased
// *Ref value. The engine holds the ref as [any] and passes it to stage
// functions via [Graph.GetRef]; the kitsune layer casts it back to the
// concrete *Ref[T] type.
package engine
