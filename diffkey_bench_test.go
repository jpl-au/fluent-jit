package jit

import (
	"strconv"
	"testing"

	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/span"
	"github.com/jpl-au/fluent/node"
)

// benchExpensiveTreeForKey builds a tree with n expensive Dynamic
// regions. Used to compare DiffKey (one key) vs Diff (all keys).
func benchExpensiveTreeForKey(n int) func(int) node.Node {
	return func(count int) node.Node {
		children := make([]node.Node, n)
		for i := range n {
			children[i] = div.New(func() []node.Node {
				rows := make([]node.Node, 20)
				for j := range 20 {
					rows[j] = div.New(span.Text("cell"), span.Text("data")).Class("row")
				}
				return rows
			}()...).Dynamic("k" + strconv.Itoa(i))
		}
		// First key changes with count.
		children[0] = div.New(span.Text("count-" + strconv.Itoa(count))).Dynamic("k0")
		return div.New(children...)
	}
}

// BenchmarkDifferDiff50_Expensive_Baseline is the full Diff across
// 50 expensive keys. Baseline for comparison with DiffKey.
func BenchmarkDifferDiff50_Expensive_Baseline(b *testing.B) {
	tree := benchExpensiveTreeForKey(50)
	d := NewDiffer()
	d.Render(tree(0))
	b.ResetTimer()
	for i := range b.N {
		d.Diff(tree(i))
	}
}

// BenchmarkDifferDiffKey_OneOf50 targets a single key out of 50.
// Should be ~50x cheaper than the full Diff.
func BenchmarkDifferDiffKey_OneOf50(b *testing.B) {
	tree := benchExpensiveTreeForKey(50)
	d := NewDiffer()
	d.Render(tree(0))
	b.ResetTimer()
	for i := range b.N {
		subtree := div.New(span.Text("count-" + strconv.Itoa(i))).Dynamic("k0")
		d.DiffKey("k0", subtree)
	}
}

// BenchmarkMemoiserDiffKey_OneOf50 same as above but on the Memoiser
// to confirm equivalent performance.
func BenchmarkMemoiserDiffKey_OneOf50(b *testing.B) {
	tree := benchExpensiveTreeForKey(50)
	m := NewMemoiser()
	m.Render(tree(0))
	b.ResetTimer()
	for i := range b.N {
		subtree := div.New(span.Text("count-" + strconv.Itoa(i))).Dynamic("k0")
		m.DiffKey("k0", subtree)
	}
}
