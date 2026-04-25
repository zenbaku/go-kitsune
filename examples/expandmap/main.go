// Example: expandmap: bounded BFS traversal with MaxDepth / MaxItems.
//
// ExpandMap walks an arbitrary graph breadth-first. Without bounds it will
// traverse the entire reachable graph, which on a high-fanout input can
// exhaust memory silently. MaxDepth caps how deep the walk goes; MaxItems
// caps the total number of items emitted. Whichever limit fires first wins.
package main

import (
	"context"
	"fmt"

	kitsune "github.com/zenbaku/go-kitsune"
)

// node is a synthetic directory-tree entry.
type node struct {
	path     string
	children []string
}

// tree is a synthetic filesystem with branching factor 3 and depth 4.
// Total nodes: 1 + 3 + 9 + 27 + 81 = 121.
var tree = func() map[string]node {
	out := map[string]node{}
	var build func(path string, depth int)
	build = func(path string, depth int) {
		if depth == 4 {
			out[path] = node{path: path}
			return
		}
		n := node{path: path}
		for i := 0; i < 3; i++ {
			child := fmt.Sprintf("%s/%d", path, i)
			n.children = append(n.children, child)
			build(child, depth+1)
		}
		out[path] = n
	}
	build("/root", 0)
	return out
}()

func expand(_ context.Context, path string) *kitsune.Pipeline[string] {
	n, ok := tree[path]
	if !ok || len(n.children) == 0 {
		return nil
	}
	return kitsune.FromSlice(n.children)
}

func main() {
	ctx := context.Background()

	// Unbounded walk: visits all 121 nodes.
	all, err := kitsune.Collect(ctx, kitsune.ExpandMap(
		kitsune.FromSlice([]string{"/root"}),
		expand,
	))
	if err != nil {
		panic(err)
	}
	fmt.Printf("unbounded walk: %d nodes\n", len(all))

	// MaxDepth(2): root + 2 levels of children = 1 + 3 + 9 = 13 nodes.
	shallow, err := kitsune.Collect(ctx, kitsune.ExpandMap(
		kitsune.FromSlice([]string{"/root"}),
		expand,
		kitsune.MaxDepth(2),
	))
	if err != nil {
		panic(err)
	}
	fmt.Printf("MaxDepth(2): %d nodes\n", len(shallow))

	// MaxItems(10): stop after 10 emissions regardless of tree shape.
	capped, err := kitsune.Collect(ctx, kitsune.ExpandMap(
		kitsune.FromSlice([]string{"/root"}),
		expand,
		kitsune.MaxItems(10),
	))
	if err != nil {
		panic(err)
	}
	fmt.Printf("MaxItems(10): %d nodes\n", len(capped))

	// Both bounds: MaxDepth(3) allows up to 40 nodes; MaxItems(20) fires first.
	both, err := kitsune.Collect(ctx, kitsune.ExpandMap(
		kitsune.FromSlice([]string{"/root"}),
		expand,
		kitsune.MaxDepth(3),
		kitsune.MaxItems(20),
	))
	if err != nil {
		panic(err)
	}
	fmt.Printf("MaxDepth(3) + MaxItems(20): %d nodes\n", len(both))
}
