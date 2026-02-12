package jit

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/html"
	"github.com/jpl-au/fluent/html5/p"
	"github.com/jpl-au/fluent/html5/span"
	"github.com/jpl-au/fluent/node"
)

// TestCompilerStaticOnly verifies the simplest case: a fully static tree.
// When there are no dynamic nodes, the compiler should produce the exact
// same output as standard rendering — the optimisation should be invisible.
func TestCompilerStaticOnly(t *testing.T) {
	compiler := NewCompiler()

	tree := div.New(span.Static("hello"), p.Static("world"))
	result := string(compiler.Render(tree))

	expected := "<div><span>hello</span><p>world</p></div>"
	if result != expected {
		t.Errorf("static-only tree should render identically to standard rendering:\n  got  %q\n  want %q", result, expected)
	}
}

// TestCompilerDynamicContentReEvaluated verifies the core property of the
// compiler: static content is frozen from the first render, but dynamic
// content is re-evaluated on each subsequent render using the new tree.
// This is the fundamental correctness guarantee — if dynamic content is
// accidentally frozen, users would see stale data.
func TestCompilerDynamicContentReEvaluated(t *testing.T) {
	compiler := NewCompiler()

	// First render: the compiler builds its execution plan here, freezing
	// the static "Hello " and marking span.Text as a dynamic path.
	tree1 := div.New(
		span.Static("Hello "),
		span.Text("Alice"),
	)
	result1 := string(compiler.Render(tree1))

	// Second render: same structure, different dynamic content. The compiler
	// reuses the frozen plan but re-evaluates the dynamic path.
	tree2 := div.New(
		span.Static("Hello "),
		span.Text("Bob"),
	)
	result2 := string(compiler.Render(tree2))

	if !strings.Contains(result1, "Alice") {
		t.Errorf("first render should contain dynamic content 'Alice', got %q", result1)
	}
	if !strings.Contains(result2, "Bob") {
		t.Errorf("second render should re-evaluate dynamic content to 'Bob', got %q — content may have been frozen", result2)
	}

	// Static portion should be identical in both renders — it was frozen
	// during the first render and should never change.
	if !strings.Contains(result1, "Hello ") || !strings.Contains(result2, "Hello ") {
		t.Errorf("static content 'Hello ' should be preserved in both renders — plan may have dropped static segments")
	}
}

// TestCompilerRenderToWriter verifies the optional io.Writer path. When a
// writer is provided, Render writes directly to it and returns nil instead
// of allocating a byte slice. This matters for HTTP handlers that stream
// directly to the response writer.
func TestCompilerRenderToWriter(t *testing.T) {
	compiler := NewCompiler()

	tree := div.New(span.Static("hello"))
	var buf bytes.Buffer
	result := compiler.Render(tree, &buf)

	if result != nil {
		t.Error("Render should return nil when writing to a writer — returning bytes would mean double allocation")
	}

	expected := "<div><span>hello</span></div>"
	if buf.String() != expected {
		t.Errorf("writer output should match byte-slice output:\n  got  %q\n  want %q", buf.String(), expected)
	}
}

// TestCompilerWithConditional verifies that node.When conditionals are
// treated as dynamic — re-evaluated on each render. The condition's boolean
// may change between renders, so the compiler must never freeze the branch.
func TestCompilerWithConditional(t *testing.T) {
	compiler := NewCompiler()

	// First render: condition is true, "active" should appear.
	tree1 := div.New(
		span.Static("Status: "),
		node.When(true, span.Static("active")),
	)
	result1 := string(compiler.Render(tree1))

	// Second render: condition is false, "active" should be absent.
	tree2 := div.New(
		span.Static("Status: "),
		node.When(false, span.Static("active")),
	)
	result2 := string(compiler.Render(tree2))

	if !strings.Contains(result1, "active") {
		t.Errorf("conditional with true should render its child, got %q", result1)
	}
	if strings.Contains(result2, "active") {
		t.Errorf("conditional with false should omit its child, got %q — condition may have been frozen from first render", result2)
	}
}

// TestCompilerWithFuncComponent verifies that node.Func components are
// re-evaluated on each render. Function components capture state via closures,
// so the compiler must call the function each time rather than caching its output.
func TestCompilerWithFuncComponent(t *testing.T) {
	compiler := NewCompiler()

	makeTree := func(name string) node.Node {
		return div.New(
			span.Static("User: "),
			node.Func(func() node.Node {
				return span.Text(name)
			}),
		)
	}

	result1 := string(compiler.Render(makeTree("Alice")))
	result2 := string(compiler.Render(makeTree("Bob")))

	if !strings.Contains(result1, "Alice") {
		t.Errorf("first render should evaluate Func closure to 'Alice', got %q", result1)
	}
	if !strings.Contains(result2, "Bob") {
		t.Errorf("second render should evaluate new Func closure to 'Bob', got %q — closure output may have been frozen", result2)
	}
}

// TestCompilerWithFragment verifies that html.Fragment nodes (which have no
// opening/closing tags of their own) are handled correctly. The compiler must
// recognise fragments as non-Element containers and render only their children.
func TestCompilerWithFragment(t *testing.T) {
	compiler := NewCompiler()

	tree := html.Fragment(
		span.Static("one"),
		span.Text("two"),
	)
	result := string(compiler.Render(tree))

	if !strings.Contains(result, "one") || !strings.Contains(result, "two") {
		t.Errorf("fragment should render all children without wrapping tags, got %q", result)
	}

	// Fragments should NOT produce any wrapping element tags.
	if strings.HasPrefix(result, "<fragment") {
		t.Errorf("fragment should not produce a wrapping element, got %q", result)
	}
}

// TestCompilerWithConfiguration verifies that CompilerCfg options are applied.
// Custom configuration controls the adaptive sizer's behaviour (sample count,
// variance threshold, growth factor) and the compilation threshold.
func TestCompilerWithConfiguration(t *testing.T) {
	compiler := NewCompiler(&CompilerCfg{
		Threshold:    10,
		Max:          3,
		Variance:     15,
		GrowthFactor: 120,
	})

	tree := div.Static("hello")
	result := string(compiler.Render(tree))

	expected := "<div>hello</div>"
	if result != expected {
		t.Errorf("configured compiler should still render correctly:\n  got  %q\n  want %q", result, expected)
	}
}

// TestCompilerValidateCompatibleTree verifies that Validate returns nil when
// the tree structure matches the compiled plan. This is the happy path — the
// tree has the same shape as the one used to build the plan, so all dynamic
// paths resolve correctly.
func TestCompilerValidateCompatibleTree(t *testing.T) {
	compiler := NewCompiler()

	// Build the plan from a tree with a dynamic child at position [1].
	original := div.New(span.Static("Hello "), span.Text("Alice"))
	compiler.Render(original)

	// Same structure, different dynamic content — should validate fine.
	compatible := div.New(span.Static("Hello "), span.Text("Bob"))
	if err := compiler.Validate(compatible); err != nil {
		t.Errorf("structurally identical tree should pass validation, got: %v", err)
	}
}

// TestCompilerValidateIncompatibleTree verifies that Validate returns
// ErrStructureMismatch when the tree has fewer children than the compiled
// plan expects. This catches the case where someone passes a structurally
// different tree to a compiled template — which would produce truncated
// output at render time.
func TestCompilerValidateIncompatibleTree(t *testing.T) {
	compiler := NewCompiler()

	// Build the plan from a tree with two children.
	original := div.New(span.Static("Hello "), span.Text("Alice"))
	compiler.Render(original)

	// Tree with fewer children — the dynamic path [1] no longer exists.
	incompatible := div.New(span.Static("Hello "))
	err := compiler.Validate(incompatible)
	if err == nil {
		t.Fatal("structurally different tree should fail validation — missing child would cause truncated output")
	}
	if !errors.Is(err, ErrStructureMismatch) {
		t.Errorf("error should wrap ErrStructureMismatch for programmatic checking, got: %v", err)
	}
}

// TestCompilerValidateBeforeCompile verifies that Validate returns nil when
// called before any Render — there is no plan to validate against yet, so
// there is nothing that could be incompatible.
func TestCompilerValidateBeforeCompile(t *testing.T) {
	compiler := NewCompiler()

	tree := div.New(span.Static("hello"))
	if err := compiler.Validate(tree); err != nil {
		t.Errorf("validate before compile should return nil (no plan yet), got: %v", err)
	}
}
