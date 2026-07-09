package jit

import (
	"testing"

	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/span"
	"github.com/jpl-au/fluent/node"
)

// buildFlattenTree builds a tree that takes the global Flatten "uncacheable"
// path: span.Text is dynamic (only Static is not), so isDynamic returns true
// and the content is never stored. Each row also carries a node.When
// conditional, whose Nodes() allocates - so the per-call isDynamic re-walk is
// not just time but allocation.
func buildFlattenTree(rows int) node.Node {
	children := make([]node.Node, rows)
	for i := range children {
		children[i] = div.New(
			span.Text("cell"),
			node.When(true, span.Text("shown")),
		)
	}
	return div.New(children...)
}

// buildLateDynamicTree builds a large tree that is entirely static EXCEPT the
// very last leaf, which is dynamic. isDynamic short-circuits on the first
// dynamic descendant it finds, so here it must walk almost the whole tree
// before discovering dynamism - the worst case for the per-call re-walk, and
// a realistic one (a mostly-static page with a single dynamic value at the
// bottom).
func buildLateDynamicTree(rows int) node.Node {
	children := make([]node.Node, rows)
	for i := range children {
		children[i] = div.New(span.Static("cell"), span.Static("more"))
	}
	children[rows-1] = div.New(span.Static("cell"), span.Text("dynamic tail"))
	return div.New(children...)
}

// BenchmarkGlobalFlattenDynamic measures repeated FlattenBytes calls under a
// stable id with dynamic content whose dynamism appears EARLY, so isDynamic
// short-circuits almost immediately. This is the common case and the walk is
// cheap; the negative cache should barely move it.
func BenchmarkGlobalFlattenDynamic(b *testing.B) {
	tree := buildFlattenTree(50)
	ResetFlatten()
	defer ResetFlatten()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = FlattenBytes("bench-flatten-dynamic", tree)
	}
}

// BenchmarkGlobalFlattenLateDynamic is the worst case for the re-walk: the
// only dynamic node is the last leaf, so every call walks nearly the whole
// tree before falling back. This is where the negative cache earns its keep.
func BenchmarkGlobalFlattenLateDynamic(b *testing.B) {
	tree := buildLateDynamicTree(200)
	ResetFlatten()
	defer ResetFlatten()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = FlattenBytes("bench-flatten-late-dynamic", tree)
	}
}
