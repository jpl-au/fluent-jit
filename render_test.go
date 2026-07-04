package jit

import (
	"testing"

	"github.com/jpl-au/fluent/html5/br"
	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/html"
	"github.com/jpl-au/fluent/html5/img"
	"github.com/jpl-au/fluent/html5/p"
	"github.com/jpl-au/fluent/html5/span"
	"github.com/jpl-au/fluent/node"
)

// TestRenderMatchesPlainRender is the safety net for the single-pass
// render walk: for every node shape the walk must produce bytes
// identical to node.Node.Render. The walk decomposes elements into
// open tag, children, close tag, so any element type whose
// RenderBuilder is not exactly that sequence would diverge here.
func TestRenderMatchesPlainRender(t *testing.T) {
	cases := []struct {
		name  string
		build func() node.Node
	}{
		{"static only", func() node.Node {
			return div.New(span.Static("hello"), p.Static("world"))
		}},
		{"keyed element", func() node.Node {
			return div.New(span.Static("Count: "), span.Text("42").Dynamic("count"))
		}},
		{"nested keys", func() node.Node {
			return div.New(
				div.New(
					span.Text("inner").Dynamic("child"),
					span.Static("more"),
				).Dynamic("parent"),
			)
		}},
		{"void elements", func() node.Node {
			return div.New(span.Static("a"), br.New(), img.Src("x.png"), span.Text("b").Dynamic("k"))
		}},
		{"fragment", func() node.Node {
			return html.Fragment(span.Static("one"), span.Text("two").Dynamic("k"))
		}},
		{"conditional true", func() node.Node {
			return div.New(node.When(true, span.Static("active")).Dynamic("auth"))
		}},
		{"conditional false", func() node.Node {
			return div.New(node.When(false, span.Static("active")).Dynamic("auth"))
		}},
		{"func component", func() node.Node {
			return div.New(node.Func(func() node.Node {
				return span.Text("fn")
			}))
		}},
		{"keyed func component", func() node.Node {
			return div.New(node.Func(func() node.Node {
				return span.Text("fn")
			}).Dynamic("greeting"))
		}},
		{"funcs component", func() node.Node {
			return div.New(node.Funcs(func() []node.Node {
				return []node.Node{span.Text("a"), span.Text("b")}
			}).Dynamic("items"))
		}},
		{"memoised region", func() node.Node {
			return div.New(
				div.New(
					node.Memoise(1, func() node.Node { return span.Text("memo") }),
				).Dynamic("m"),
			)
		}},
		{"empty element", func() node.Node {
			return div.New()
		}},
		{"attributes", func() node.Node {
			el := div.New(span.Static("x"))
			el.SetAttribute("class", "box")
			return el
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := string(tc.build().Render())

			d := NewDiffer()
			if got := string(d.Render(tc.build())); got != want {
				t.Errorf("Differ.Render diverged from plain Render:\n got  %q\n want %q", got, want)
			}

			// Render and Diff use different walks; their snapshots
			// must agree byte-for-byte or an identical rebuild would
			// produce phantom patches.
			patches, change := d.Diff(tc.build())
			if change != nil {
				t.Errorf("identical rebuild reported a structural change: %v", change)
			}
			if len(patches) != 0 {
				t.Errorf("identical rebuild produced %d phantom patches, first: %q", len(patches), patches[0].HTML)
			}

			// The Memoiser stamps data-tether-memoise on memoised
			// regions, so compare against the same (mutated) tree.
			ResetSharedCache()
			tree := tc.build()
			got := string(NewMemoiser().Render(tree))
			if want := string(tree.Render()); got != want {
				t.Errorf("Memoiser.Render diverged from plain Render:\n got  %q\n want %q", got, want)
			}
		})
	}
}

// TestDifferRenderRunsClosuresOnce verifies the single-pass guarantee:
// the initial Render runs each closure exactly once. The old two-pass
// design ran a closure inside a keyed region twice (snapshot + page),
// and three times when the region was nested inside another keyed
// region.
func TestDifferRenderRunsClosuresOnce(t *testing.T) {
	outer, inner := 0, 0
	tree := div.New(
		div.New(
			node.Func(func() node.Node {
				outer++
				return span.Text("outer")
			}),
			div.New(
				node.Func(func() node.Node {
					inner++
					return span.Text("inner")
				}),
			).Dynamic("child"),
		).Dynamic("parent"),
	)

	NewDiffer().Render(tree)

	if outer != 1 {
		t.Errorf("closure in keyed region should run once, ran %d times", outer)
	}
	if inner != 1 {
		t.Errorf("closure in nested keyed region should run once, ran %d times", inner)
	}
}

// TestDifferDiffRunsClosuresOnce verifies the single-render guarantee
// on the Diff path: each closure inside a keyed region runs exactly
// once per Diff. The old walk ran a closure twice (once rendering the
// snapshot, once materialising Nodes to keep searching for keys) and
// rendered a nested keyed region twice.
func TestDifferDiffRunsClosuresOnce(t *testing.T) {
	outer, inner := 0, 0
	tree := func() node.Node {
		return div.New(
			div.New(
				node.Func(func() node.Node {
					outer++
					return span.Text("outer")
				}),
				div.New(
					node.Func(func() node.Node {
						inner++
						return span.Text("inner")
					}),
				).Dynamic("child"),
			).Dynamic("parent"),
		)
	}

	d := NewDiffer()
	d.Render(tree())
	outer, inner = 0, 0

	patches, change := d.Diff(tree())
	if change != nil {
		t.Fatalf("unexpected structural change: %v", change)
	}
	if len(patches) != 0 {
		t.Fatalf("identical tree should produce no patches, got %d", len(patches))
	}
	if outer != 1 {
		t.Errorf("closure in keyed region should run once per Diff, ran %d times", outer)
	}
	if inner != 1 {
		t.Errorf("closure in nested keyed region should run once per Diff, ran %d times", inner)
	}
}
