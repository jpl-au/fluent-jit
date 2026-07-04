package jit

import (
	"strconv"
	"testing"

	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/span"
	"github.com/jpl-au/fluent/node"
)

// memoBenchTree builds a page with n memoised Dynamic regions, each
// wrapping a moderately sized subtree, mixed with unkeyed static
// content. Mirrors the shape the Memoiser targets: expensive regions
// behind cache keys inside a larger page.
func memoBenchTree(n int) node.Node {
	children := make([]node.Node, 0, n*2)
	for i := range n {
		key := "region" + strconv.Itoa(i)
		children = append(children,
			span.Static("filler between regions"),
			div.New(
				node.Memoise(1, func() node.Node {
					items := make([]node.Node, 10)
					for j := range 10 {
						items[j] = span.Text("row " + strconv.Itoa(j))
					}
					return div.New(items...)
				}),
			).Dynamic(key),
		)
	}
	return div.New(children...)
}

// plainBenchTree builds a page with n keyed Dynamic regions and
// unkeyed filler, no memoisation - the Differ's target shape.
func plainBenchTree(n int) node.Node {
	children := make([]node.Node, 0, n*2)
	for i := range n {
		children = append(children,
			span.Static("filler between regions"),
			div.New(
				span.Text("value "+strconv.Itoa(i)),
			).Dynamic("region"+strconv.Itoa(i)),
		)
	}
	return div.New(children...)
}

func BenchmarkDifferRender50(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		NewDiffer().Render(plainBenchTree(50))
	}
}

func BenchmarkMemoiserRender50(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		NewMemoiser().Render(memoBenchTree(50))
	}
}

func BenchmarkMemoiserRender50_SharedHit(b *testing.B) {
	ResetSharedCache()
	tree := func() node.Node {
		children := make([]node.Node, 0, 100)
		for i := range 50 {
			key := "shared" + strconv.Itoa(i)
			children = append(children,
				span.Static("filler between regions"),
				div.New(
					node.Shared(key+":v1", func() node.Node {
						items := make([]node.Node, 10)
						for j := range 10 {
							items[j] = span.Text("row " + strconv.Itoa(j))
						}
						return div.New(items...)
					}),
				).Dynamic(key),
			)
		}
		return div.New(children...)
	}
	// Populate the cache once so every benchmark iteration hits.
	NewMemoiser().Render(tree())
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		NewMemoiser().Render(tree())
	}
}
