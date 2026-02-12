package jit

import (
	"testing"

	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/html"
	"github.com/jpl-au/fluent/html5/span"
	"github.com/jpl-au/fluent/node"
)

// TestIsDynamicNode verifies single-node classification — the check that
// determines whether a node itself produces different output across renders.
// This is distinct from isDynamic (below) which checks an entire subtree.
// Getting this wrong means the compiler will either freeze dynamic content
// (producing stale output) or re-evaluate static content (wasting work).
func TestIsDynamicNode(t *testing.T) {
	tests := []struct {
		name    string
		node    node.Node
		dynamic bool
	}{
		// Static elements never change, so they can be frozen safely.
		{"static element", div.Static("hello"), false},

		// An element with a dynamic child is NOT itself dynamic — only its
		// subtree is. The compiler handles this by walking children separately.
		{"element with dynamic child is not itself dynamic", div.Text("hello"), false},

		// A bare element (no children) is static — it always renders the same tags.
		{"bare element", div.New(), false},

		// Function components must be re-evaluated every render because their
		// output depends on the closure's captured state.
		{"function component", node.Func(func() node.Node { return nil }), true},
		{"function nodes component", node.FuncNodes(func() []node.Node { return nil }), true},

		// Conditionals are dynamic because the branch taken can change between renders.
		{"conditional", node.When(true, span.Static("yes")), true},
		{"condition builder", node.Condition(false).True(nil).False(nil), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDynamicNode(tt.node); got != tt.dynamic {
				t.Errorf("isDynamicNode(%s) = %v, want %v — misclassification will cause the compiler to %s",
					tt.name, got, tt.dynamic,
					map[bool]string{
						true:  "wastefully re-evaluate static content",
						false: "freeze dynamic content, producing stale output",
					}[got])
			}
		})
	}
}

// TestIsDynamic verifies recursive subtree classification — the check that
// determines whether any node in a tree requires re-evaluation. The compiler
// uses this to decide whether a subtree can be frozen as a single static
// chunk or must be walked node-by-node on each render.
func TestIsDynamic(t *testing.T) {
	tests := []struct {
		name    string
		node    node.Node
		dynamic bool
	}{
		// Fully static trees can be frozen entirely — no per-render work needed.
		{"fully static tree", div.New(span.Static("hello")), false},

		// A single dynamic child anywhere in the tree makes the whole subtree dynamic,
		// because the compiler must walk into it to find and re-evaluate that child.
		{"dynamic text child", div.New(span.Text("hello")), true},

		// Dynamic detection must recurse through arbitrary nesting depth.
		{"deeply nested dynamic", div.New(div.New(span.Text("deep"))), true},

		// Mixed siblings: one dynamic child is enough to mark the parent as dynamic.
		{"mixed static and dynamic siblings", div.New(span.Static("a"), span.Text("b")), true},

		// All-static siblings keep the parent static.
		{"static with static children", div.New(span.Static("a"), span.Static("b")), false},

		// Fragments (no wrapping element) follow the same rules as elements.
		{"html fragment with static children", html.Fragment(span.Static("a"), span.Static("b")), false},
		{"html fragment with dynamic child", html.Fragment(span.Static("a"), span.Text("b")), true},

		// Dynamic node types as children propagate dynamism upward.
		{"conditional as child", div.New(node.When(true, span.Static("yes"))), true},
		{"func as child", div.New(node.Func(func() node.Node { return nil })), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDynamic(tt.node); got != tt.dynamic {
				t.Errorf("isDynamic(%s) = %v, want %v — the compiler will incorrectly %s this subtree",
					tt.name, got, tt.dynamic,
					map[bool]string{
						true:  "walk",
						false: "freeze",
					}[got])
			}
		})
	}
}
