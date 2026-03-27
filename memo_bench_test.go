package jit

import (
	"testing"

	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/span"
	"github.com/jpl-au/fluent/node"
)

func benchTree(n int) func(int) node.Node {
	return func(count int) node.Node {
		children := make([]node.Node, n)
		for i := range n {
			children[i] = span.Text("item").Dynamic("k" + string(rune('a'+i%26)) + string(rune('0'+i/26)))
		}
		children[0] = span.Text(string(rune('0' + count%10))).Dynamic("counter")
		return div.New(children...)
	}
}

func BenchmarkDifferDiff10(b *testing.B) {
	tree := benchTree(10)
	d := NewDiffer()
	d.Render(tree(0))
	b.ResetTimer()
	for i := range b.N {
		d.Diff(tree(i))
	}
}

func BenchmarkMemoiserDiff10_NoMemo(b *testing.B) {
	tree := benchTree(10)
	m := NewMemoiser()
	m.Render(tree(0))
	b.ResetTimer()
	for i := range b.N {
		m.Diff(tree(i))
	}
}

func BenchmarkDifferDiff50(b *testing.B) {
	tree := benchTree(50)
	d := NewDiffer()
	d.Render(tree(0))
	b.ResetTimer()
	for i := range b.N {
		d.Diff(tree(i))
	}
}

func BenchmarkMemoiserDiff50_NoMemo(b *testing.B) {
	tree := benchTree(50)
	m := NewMemoiser()
	m.Render(tree(0))
	b.ResetTimer()
	for i := range b.N {
		m.Diff(tree(i))
	}
}

func benchMemoTree(n int) func(int, int) node.Node {
	return func(count int, version int) node.Node {
		children := make([]node.Node, n)
		for i := range n {
			v := version
			children[i] = div.New(
				node.Memo(v, func() node.Node {
					return span.Text("item")
				}),
			).Dynamic("k" + string(rune('a'+i%26)) + string(rune('0'+i/26)))
		}
		children[0] = div.New(
			node.Memo(count, func() node.Node {
				return span.Text(string(rune('0' + count%10)))
			}),
		).Dynamic("counter")
		return div.New(children...)
	}
}

func BenchmarkMemoiserDiff50_WithMemo_AllHit(b *testing.B) {
	tree := benchMemoTree(50)
	m := NewMemoiser()
	m.Render(tree(0, 1))
	b.ResetTimer()
	for i := range b.N {
		m.Diff(tree(0, 1)) // same keys - all hits
		_ = i
	}
}

// benchExpensiveTree simulates a realistic tree where each Dynamic
// region contains a non-trivial subtree (20 child elements). This is
// where memo should show real savings - skipping the render of large
// subtrees.
func benchExpensiveMemoTree(n int) func(int, int) node.Node {
	return func(count int, version int) node.Node {
		children := make([]node.Node, n)
		for i := range n {
			v := version
			children[i] = div.New(
				node.Memo(v, func() node.Node {
					rows := make([]node.Node, 20)
					for j := range 20 {
						rows[j] = div.New(span.Text("cell"), span.Text("data")).Class("row")
					}
					return div.New(rows...)
				}),
			).Dynamic("k" + string(rune('a'+i%26)) + string(rune('0'+i/26)))
		}
		children[0] = div.New(
			node.Memo(count, func() node.Node {
				return span.Text(string(rune('0' + count%10)))
			}),
		).Dynamic("counter")
		return div.New(children...)
	}
}

func benchExpensiveTree(n int) func(int) node.Node {
	return func(count int) node.Node {
		children := make([]node.Node, n)
		for i := range n {
			children[i] = div.New(func() []node.Node {
				rows := make([]node.Node, 20)
				for j := range 20 {
					rows[j] = div.New(span.Text("cell"), span.Text("data")).Class("row")
				}
				return rows
			}()...).Dynamic("k" + string(rune('a'+i%26)) + string(rune('0'+i/26)))
		}
		children[0] = div.New(span.Text(string(rune('0' + count%10)))).Dynamic("counter")
		return div.New(children...)
	}
}

func BenchmarkDifferDiff50_Expensive(b *testing.B) {
	tree := benchExpensiveTree(50)
	d := NewDiffer()
	d.Render(tree(0))
	b.ResetTimer()
	for i := range b.N {
		d.Diff(tree(i))
	}
}

func BenchmarkMemoiserDiff50_Expensive_AllHit(b *testing.B) {
	tree := benchExpensiveMemoTree(50)
	m := NewMemoiser()
	m.Render(tree(0, 1))
	b.ResetTimer()
	for range b.N {
		m.Diff(tree(0, 1))
	}
}

func BenchmarkMemoiserDiff50_Expensive_OneMiss(b *testing.B) {
	tree := benchExpensiveMemoTree(50)
	m := NewMemoiser()
	m.Render(tree(0, 1))
	b.ResetTimer()
	for i := range b.N {
		m.Diff(tree(i, 1))
	}
}

func BenchmarkMemoiserDiff50_WithMemo_OneMiss(b *testing.B) {
	tree := benchMemoTree(50)
	m := NewMemoiser()
	m.Render(tree(0, 1))
	b.ResetTimer()
	for i := range b.N {
		m.Diff(tree(i, 1)) // counter changes, rest hit
	}
}
