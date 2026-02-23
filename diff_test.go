package jit

import (
	"bytes"
	"errors"
	"testing"

	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/p"
	"github.com/jpl-au/fluent/html5/span"
	"github.com/jpl-au/fluent/node"
)

// TestDifferInitialRender verifies that the first Render call produces
// the full HTML output — identical to calling Render on the root node
// directly. The diff engine should be invisible on the first render.
func TestDifferInitialRender(t *testing.T) {
	differ := NewDiffer()

	tree := div.New(
		span.Static("Count: "),
		span.Text("42").Dynamic("count"),
	)

	result := string(differ.Render(tree))
	expected := string(tree.Render())

	if result != expected {
		t.Errorf("initial Render should produce identical output to direct rendering:\n  got  %q\n  want %q", result, expected)
	}
}

// TestDifferRenderToWriter verifies that Render writes to the provided
// writer and returns nil, matching the standard node.Node.Render behaviour.
func TestDifferRenderToWriter(t *testing.T) {
	differ := NewDiffer()

	tree := div.New(span.Text("hello").Dynamic("msg"))
	var buf bytes.Buffer
	result := differ.Render(tree, &buf)

	if result != nil {
		t.Error("Render should return nil when writing to a writer")
	}
	if buf.Len() == 0 {
		t.Error("Render should write output to the provided writer")
	}
}

// TestDifferNoPatchesWhenUnchanged verifies that Diff returns no patches
// when the keyed elements produce identical output. This is the common case
// for events that don't affect visible state — the diff engine should detect
// that nothing changed and avoid sending unnecessary patches.
func TestDifferNoPatchesWhenUnchanged(t *testing.T) {
	differ := NewDiffer()

	tree := func() node.Node {
		return div.New(
			span.Static("Count: "),
			span.Text("42").Dynamic("count"),
		)
	}

	differ.Render(tree())
	patches, fullRender := differ.Diff(tree())

	if fullRender {
		t.Error("identical trees should not trigger a full re-render")
	}
	if len(patches) != 0 {
		t.Errorf("identical trees should produce no patches, got %d", len(patches))
	}
}

// TestDifferDetectsContentChange verifies the core property of the diff
// engine: when a keyed element's rendered output changes between renders,
// a Patch is returned with the new HTML. This is the fundamental mechanism
// for sending targeted updates to the client.
func TestDifferDetectsContentChange(t *testing.T) {
	differ := NewDiffer()

	makeTree := func(count string) node.Node {
		return div.New(
			span.Static("Count: "),
			span.Text(count).Dynamic("count"),
		)
	}

	differ.Render(makeTree("42"))
	patches, fullRender := differ.Diff(makeTree("43"))

	if fullRender {
		t.Error("same keys should not trigger a full re-render")
	}
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch for changed element, got %d", len(patches))
	}
	if patches[0].Key != "count" {
		t.Errorf("patch key should be %q, got %q", "count", patches[0].Key)
	}
	if !bytes.Contains(patches[0].HTML, []byte("43")) {
		t.Errorf("patch HTML should contain the new content '43', got %q", patches[0].HTML)
	}
}

// TestDifferMultipleChanges verifies that Diff returns patches for all
// keyed elements that changed, not just the first one found.
func TestDifferMultipleChanges(t *testing.T) {
	differ := NewDiffer()

	makeTree := func(name, count string) node.Node {
		return div.New(
			span.Text(name).Dynamic("name"),
			span.Text(count).Dynamic("count"),
		)
	}

	differ.Render(makeTree("Alice", "10"))
	patches, fullRender := differ.Diff(makeTree("Bob", "20"))

	if fullRender {
		t.Error("same keys should not trigger a full re-render")
	}
	if len(patches) != 2 {
		t.Fatalf("expected 2 patches when both elements changed, got %d", len(patches))
	}

	// Collect patches by key for order-independent assertions
	patchMap := make(map[string][]byte)
	for _, p := range patches {
		patchMap[p.Key] = p.HTML
	}

	if !bytes.Contains(patchMap["name"], []byte("Bob")) {
		t.Errorf("name patch should contain 'Bob', got %q", patchMap["name"])
	}
	if !bytes.Contains(patchMap["count"], []byte("20")) {
		t.Errorf("count patch should contain '20', got %q", patchMap["count"])
	}
}

// TestDifferPartialChange verifies that only changed elements produce
// patches — unchanged elements are left alone. This is what makes the
// diff engine efficient: skip the 90% that didn't change.
func TestDifferPartialChange(t *testing.T) {
	differ := NewDiffer()

	makeTree := func(name, count string) node.Node {
		return div.New(
			span.Text(name).Dynamic("name"),
			span.Text(count).Dynamic("count"),
		)
	}

	differ.Render(makeTree("Alice", "10"))
	// Only count changes, name stays the same
	patches, fullRender := differ.Diff(makeTree("Alice", "20"))

	if fullRender {
		t.Error("same keys should not trigger a full re-render")
	}
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch when only one element changed, got %d", len(patches))
	}
	if patches[0].Key != "count" {
		t.Errorf("patch should be for the changed element 'count', got %q", patches[0].Key)
	}
}

// TestDifferStructuralChangeKeyAdded verifies that when a new keyed element
// appears between renders, Diff signals a structural change. The caller
// should respond by doing a full re-render via Render().
func TestDifferStructuralChangeKeyAdded(t *testing.T) {
	differ := NewDiffer()

	tree1 := div.New(
		span.Text("42").Dynamic("count"),
	)
	tree2 := div.New(
		span.Text("42").Dynamic("count"),
		p.Text("Help text").Dynamic("help"),
	)

	differ.Render(tree1)
	patches, fullRender := differ.Diff(tree2)

	if !fullRender {
		t.Error("adding a new keyed element should trigger a full re-render")
	}
	if patches != nil {
		t.Error("structural change should return nil patches — the caller should use Render instead")
	}
}

// TestDifferStructuralChangeKeyRemoved verifies that when a keyed element
// disappears between renders, Diff signals a structural change.
func TestDifferStructuralChangeKeyRemoved(t *testing.T) {
	differ := NewDiffer()

	tree1 := div.New(
		span.Text("42").Dynamic("count"),
		p.Text("Help text").Dynamic("help"),
	)
	tree2 := div.New(
		span.Text("42").Dynamic("count"),
	)

	differ.Render(tree1)
	patches, fullRender := differ.Diff(tree2)

	if !fullRender {
		t.Error("removing a keyed element should trigger a full re-render")
	}
	if patches != nil {
		t.Error("structural change should return nil patches")
	}
}

// TestDifferDiffBeforeRender verifies that calling Diff before Render
// returns nil patches and no structural change. This is a no-op — the
// differ has no baseline to compare against yet.
func TestDifferDiffBeforeRender(t *testing.T) {
	differ := NewDiffer()

	tree := div.New(span.Text("hello").Dynamic("msg"))
	patches, fullRender := differ.Diff(tree)

	if fullRender {
		t.Error("Diff before Render should not signal structural change")
	}
	if patches != nil {
		t.Error("Diff before Render should return nil patches")
	}
}

// TestDifferUnkeyedDynamicNotTracked verifies that elements marked with
// .Dynamic() (no key argument) are not tracked by the diff engine. The "_"
// sentinel marks the element as dynamic for the JIT compiler but without
// a tracking key for the diff engine.
func TestDifferUnkeyedDynamicNotTracked(t *testing.T) {
	differ := NewDiffer()

	makeTree := func(value string) node.Node {
		return div.New(
			span.Text(value).Dynamic(), // no key — uses "_" sentinel
		)
	}

	differ.Render(makeTree("hello"))
	patches, fullRender := differ.Diff(makeTree("world"))

	if fullRender {
		t.Error("unkeyed dynamic elements should not affect structural change detection")
	}
	if len(patches) != 0 {
		t.Errorf("unkeyed dynamic elements should not produce patches, got %d", len(patches))
	}
}

// TestDifferRenderAfterStructuralChange verifies that calling Render after
// a structural change resets the snapshots correctly. This is the recovery
// path: Diff detects a structural change, the caller does a full Render,
// and subsequent Diffs work against the new baseline.
func TestDifferRenderAfterStructuralChange(t *testing.T) {
	differ := NewDiffer()

	// Initial render with one key
	tree1 := div.New(span.Text("42").Dynamic("count"))
	differ.Render(tree1)

	// Structural change — new key added
	tree2 := div.New(
		span.Text("42").Dynamic("count"),
		p.Text("Help").Dynamic("help"),
	)
	_, fullRender := differ.Diff(tree2)
	if !fullRender {
		t.Fatal("expected structural change")
	}

	// Re-render to establish new baseline
	differ.Render(tree2)

	// Now Diff should work against the new baseline
	tree3 := div.New(
		span.Text("43").Dynamic("count"),
		p.Text("Help").Dynamic("help"),
	)
	patches, fullRender := differ.Diff(tree3)

	if fullRender {
		t.Error("same keys after re-render should not trigger structural change")
	}
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch after re-render baseline, got %d", len(patches))
	}
	if patches[0].Key != "count" {
		t.Errorf("patch should be for 'count', got %q", patches[0].Key)
	}
}

// TestDifferValidateDuplicateKeys verifies that Validate catches duplicate
// dynamic keys in a tree. Duplicate keys would cause the diff engine to
// lose track of one element — only the last one visited would be stored.
func TestDifferValidateDuplicateKeys(t *testing.T) {
	differ := NewDiffer()

	tree := div.New(
		span.Text("first").Dynamic("same-key"),
		span.Text("second").Dynamic("same-key"),
	)

	err := differ.Validate(tree)
	if err == nil {
		t.Fatal("duplicate keys should fail validation — the diff engine cannot track both elements")
	}
	if !errors.Is(err, ErrDuplicateKey) {
		t.Errorf("error should wrap ErrDuplicateKey for programmatic checking, got: %v", err)
	}
}

// TestDifferValidateUniqueKeys verifies that Validate passes when all
// dynamic keys in the tree are unique.
func TestDifferValidateUniqueKeys(t *testing.T) {
	differ := NewDiffer()

	tree := div.New(
		span.Text("name").Dynamic("name"),
		span.Text("42").Dynamic("count"),
	)

	if err := differ.Validate(tree); err != nil {
		t.Errorf("unique keys should pass validation, got: %v", err)
	}
}

// TestDifferValidateNoKeys verifies that Validate passes when the tree
// has no keyed dynamic elements at all.
func TestDifferValidateNoKeys(t *testing.T) {
	differ := NewDiffer()

	tree := div.New(span.Static("hello"), p.Static("world"))

	if err := differ.Validate(tree); err != nil {
		t.Errorf("tree with no dynamic keys should pass validation, got: %v", err)
	}
}

// TestDifferNoKeysNoPatchesNoStructuralChange verifies that a tree with
// no keyed elements at all produces no patches and no structural change.
// This is the degenerate case — the diff engine has nothing to track.
func TestDifferNoKeysNoPatchesNoStructuralChange(t *testing.T) {
	differ := NewDiffer()

	tree := func() node.Node {
		return div.New(span.Static("hello"))
	}

	differ.Render(tree())
	patches, fullRender := differ.Diff(tree())

	if fullRender {
		t.Error("no keys in either tree should not trigger structural change")
	}
	if len(patches) != 0 {
		t.Errorf("no keys should produce no patches, got %d", len(patches))
	}
}
