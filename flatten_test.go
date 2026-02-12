package jit

import (
	"bytes"
	"testing"

	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/span"
	"github.com/jpl-au/fluent/node"
)

// TestFlattenerStaticContent verifies the happy path: a fully static tree
// is rendered once and cached as a byte slice. The flattener is the most
// aggressive optimisation — it eliminates all per-render work by pre-computing
// the entire output. This only works for trees with no dynamic content.
func TestFlattenerStaticContent(t *testing.T) {
	n := div.New(span.Static("hello"))

	f, err := NewFlattener(n)
	if err != nil {
		t.Fatalf("static content should be accepted by the flattener, got error: %v", err)
	}

	result := string(f.Render())
	expected := "<div><span>hello</span></div>"
	if result != expected {
		t.Errorf("flattened output should match standard rendering:\n  got  %q\n  want %q", result, expected)
	}
}

// TestFlattenerRejectsDynamicContent verifies that the flattener refuses trees
// containing dynamic nodes. Unlike the compiler (which handles mixed trees) or
// the global Flatten function (which silently falls back), NewFlattener returns
// ErrDynamicContent so callers can make an informed decision about alternatives.
func TestFlattenerRejectsDynamicContent(t *testing.T) {
	// span.Text is dynamic — its content changes between renders.
	n := div.New(span.Text("hello"))

	_, err := NewFlattener(n)
	if err == nil {
		t.Fatal("flattener should reject dynamic content — caching it would freeze changing values")
	}
	if err != ErrDynamicContent {
		t.Errorf("error should be ErrDynamicContent (so callers can check it), got %v", err)
	}
}

// TestFlattenerRejectsFuncComponent verifies that function components are
// correctly identified as dynamic. Even though a Func might return static
// content, the function itself must be called each render to capture any
// closure state changes — so the flattener cannot safely cache it.
func TestFlattenerRejectsFuncComponent(t *testing.T) {
	n := div.New(node.Func(func() node.Node { return span.Static("hello") }))

	_, err := NewFlattener(n)
	if err != ErrDynamicContent {
		t.Errorf("func components should be rejected as dynamic (closure state may change), got error: %v", err)
	}
}

// TestFlattenerRenderToWriter verifies the optional io.Writer path. When a
// writer is provided, the flattener writes its cached bytes directly to it
// and returns nil. This avoids copying the cached slice when streaming to
// an HTTP response writer.
func TestFlattenerRenderToWriter(t *testing.T) {
	n := div.Static("hello")

	f, err := NewFlattener(n)
	if err != nil {
		t.Fatalf("static content should be accepted by the flattener, got error: %v", err)
	}

	var buf bytes.Buffer
	result := f.Render(&buf)

	if result != nil {
		t.Error("Render should return nil when writing to a writer — returning bytes would mean double allocation")
	}

	expected := "<div>hello</div>"
	if buf.String() != expected {
		t.Errorf("writer output should match byte-slice output:\n  got  %q\n  want %q", buf.String(), expected)
	}
}

// TestFlattenerRenderConsistency verifies that repeated Render calls return
// identical bytes. The flattener caches its output on construction, so every
// call should return the same pre-computed slice. If this fails, the cache
// may be getting corrupted between calls.
func TestFlattenerRenderConsistency(t *testing.T) {
	n := div.New(span.Static("consistent"))

	f, err := NewFlattener(n)
	if err != nil {
		t.Fatalf("static content should be accepted by the flattener, got error: %v", err)
	}

	first := f.Render()
	second := f.Render()

	if !bytes.Equal(first, second) {
		t.Errorf("cached flattener should return identical bytes on every call:\n  first  %q\n  second %q", first, second)
	}
}
