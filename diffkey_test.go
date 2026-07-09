package jit

import (
	"bytes"
	"testing"

	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/span"
	"github.com/jpl-au/fluent/node"
)

// TestDiffKeyDetectsChange verifies that DiffKey returns a patch
// when the targeted key's content has changed.
func TestDiffKeyDetectsChange(t *testing.T) {
	d := NewDiffer()

	tree := div.New(
		span.Text("old").Dynamic("target"),
		span.Text("other").Dynamic("other"),
	)
	d.RenderBytes(tree)

	// DiffKey with new content for "target".
	patch := d.DiffKey("target", span.Text("new").Dynamic("target"))
	if patch == nil {
		t.Fatal("expected patch for changed key")
	}
	if patch.Key != "target" {
		t.Errorf("expected key 'target', got %q", patch.Key)
	}

	// Subsequent full Diff should see the updated snapshot.
	tree2 := div.New(
		span.Text("new").Dynamic("target"),
		span.Text("other").Dynamic("other"),
	)
	patches, change := d.Diff(tree2)
	if change != nil {
		t.Fatal("expected no structural change")
	}
	if len(patches) != 0 {
		t.Errorf("expected no patches after DiffKey updated the snapshot, got %d", len(patches))
	}
}

// TestDiffKeyWithSpecialChars verifies a Dynamic key containing characters that
// node.EscapeAttribute rewrites round-trips through the differ. The key is stored
// RAW (its diff identity), so the differ matches it across renders, the patch
// carries the raw key the developer passed, and DiffKey by that same raw key hits
// the stored snapshot. The key is escaped only where it renders, inside
// data-fluent-key, so it cannot break out of the attribute.
func TestDiffKeyWithSpecialChars(t *testing.T) {
	const raw = `item "1" & <2>`
	escaped := node.EscapeAttribute(raw)
	if escaped == raw {
		t.Fatal("test key must contain escapable characters")
	}

	makeTree := func(content string) node.Node {
		return div.New(
			span.Static("label"),
			span.Text(content).Dynamic(raw),
		)
	}

	d := NewDiffer()
	d.RenderBytes(makeTree("old"))
	patches, change := d.Diff(makeTree("new"))

	if change != nil {
		t.Fatal("raw key should match across renders, not signal a structural change")
	}
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch for the changed keyed element, got %d", len(patches))
	}
	if patches[0].Key != raw {
		t.Errorf("patch key should be the raw key %q, got %q", raw, patches[0].Key)
	}
	// The key renders escaped inside the attribute, so it cannot break out.
	if !bytes.Contains(patches[0].HTML, []byte(`data-fluent-key="`+escaped+`"`)) {
		t.Errorf("rendered key should be escaped, got %q", patches[0].HTML)
	}
	// DiffKey by the raw key finds the snapshot: no spurious patch for unchanged
	// content. Escaping the key at storage would have desynced this lookup.
	if p := d.DiffKey(raw, span.Text("new").Dynamic(raw)); p != nil {
		t.Errorf("DiffKey by the raw key should match the stored snapshot, got %v", p)
	}
}

// TestDiffKeyNoChange verifies that DiffKey returns nil when the
// content is unchanged.
func TestDiffKeyNoChange(t *testing.T) {
	d := NewDiffer()

	tree := div.New(span.Text("same").Dynamic("target"))
	d.RenderBytes(tree)

	patch := d.DiffKey("target", span.Text("same").Dynamic("target"))
	if patch != nil {
		t.Error("expected nil patch for unchanged content")
	}
}

// TestDiffKeyUnknownKey verifies that DiffKey returns a patch for a
// key that has no stored snapshot (new key).
func TestDiffKeyUnknownKey(t *testing.T) {
	d := NewDiffer()

	tree := div.New(span.Text("x").Dynamic("known"))
	d.RenderBytes(tree)

	patch := d.DiffKey("unknown", span.Text("new"))
	if patch == nil {
		t.Fatal("expected patch for unknown key")
	}
	if patch.Key != "unknown" {
		t.Errorf("expected key 'unknown', got %q", patch.Key)
	}
}

// TestDiffKeyDoesNotAffectOtherKeys verifies that DiffKey only
// updates the targeted key's snapshot, leaving others untouched.
func TestDiffKeyDoesNotAffectOtherKeys(t *testing.T) {
	d := NewDiffer()

	tree := div.New(
		span.Text("a").Dynamic("a"),
		span.Text("b").Dynamic("b"),
	)
	d.RenderBytes(tree)

	// Change "a" via DiffKey.
	d.DiffKey("a", span.Text("a-new").Dynamic("a"))

	// Full Diff with "a" changed (matches DiffKey) and "b" unchanged.
	tree2 := div.New(
		span.Text("a-new").Dynamic("a"),
		span.Text("b").Dynamic("b"),
	)
	patches, _ := d.Diff(tree2)
	if len(patches) != 0 {
		t.Errorf("expected no patches (DiffKey already updated 'a'), got %d", len(patches))
	}
}

// TestDiffKeyNestedInsideDynamicParent verifies that DiffKey works
// when the targeted key is nested inside a Dynamic parent container.
// This is the minimal reproduction of a real bug in the patch demo:
// a page-level Dynamic("page") wrapper contained many row-level
// Dynamic("row-N") children, and sess.Patch updates to the rows were
// orphaned because collectSnapshots treated the page key as a
// terminal snapshot and never walked into the children.
//
// After the fix, every Dynamic key in the tree is tracked
// independently regardless of nesting, so DiffKey and the subsequent
// full Diff interact correctly.
func TestDiffKeyNestedInsideDynamicParent(t *testing.T) {
	d := NewDiffer()

	// A page wrapper (Dynamic) containing two rows (also Dynamic).
	makeTree := func(a, b string) node.Node {
		return div.New(
			span.Text(a).Dynamic("row-a"),
			span.Text(b).Dynamic("row-b"),
		).Dynamic("page")
	}

	// Initial render: all zeros.
	d.RenderBytes(makeTree("0", "0"))

	// Patch row-a to "1" via DiffKey. This simulates a ticker or
	// background goroutine calling sess.Patch.
	patch := d.DiffKey("row-a", span.Text("1").Dynamic("row-a"))
	if patch == nil {
		t.Fatal("DiffKey should produce a patch for the nested key")
	}

	// Now simulate a Handle event that resets state back to all
	// zeros. A full Diff should detect that the previously-patched
	// row-a has a stale snapshot and produce a patch to restore it.
	patches, change := d.Diff(makeTree("0", "0"))
	if change != nil {
		t.Fatalf("unexpected structural change: %v", change)
	}

	foundRowA := false
	for _, p := range patches {
		if p.Key == "row-a" {
			foundRowA = true
			if !bytes.Contains(p.HTML, []byte(">0<")) {
				t.Errorf("expected row-a patch to restore value 0, got HTML: %s", p.HTML)
			}
		}
	}
	if !foundRowA {
		t.Errorf("expected a patch for row-a after full Diff, got %d patches: %v", len(patches), patches)
	}
}

// TestMemoiserDiffKey verifies that DiffKey works on the Memoiser
// with the same behaviour as the Differ.
func TestMemoiserDiffKey(t *testing.T) {
	m := NewMemoiser()

	tree := div.New(
		div.New(
			Memoise(1, func() node.Node { return span.Text("old") }),
		).Dynamic("target"),
	)
	m.RenderBytes(tree)

	patch := m.DiffKey("target",
		div.New(
			Memoise(2, func() node.Node { return span.Text("new") }),
		).Dynamic("target"),
	)
	if patch == nil {
		t.Fatal("expected patch for changed key")
	}
}

// TestRawAttributeSurvivesJIT verifies a SetAttributeRaw value flows through the
// compile, flatten and differ paths verbatim. The value is stored raw on the
// element, so every jit path that snapshots or replays rendered bytes must carry
// it unchanged; re-escaping it anywhere would corrupt a deliberately pre-escaped
// value (the same set-time versus render-time seam the raw Dynamic key test above
// pins for keys).
func TestRawAttributeSurvivesJIT(t *testing.T) {
	const raw = `a&amp;b`

	makeTree := func(content string) node.Node {
		el := span.Text(content)
		el.SetAttributeRaw("data-raw", raw)
		return div.New(el.Dynamic("target"))
	}

	want := makeTree("old").RenderBytes()
	if !bytes.Contains(want, []byte(`data-raw="`+raw+`"`)) {
		t.Fatalf("plain render should carry the raw value verbatim, got %q", want)
	}

	if got := CompileBytes("raw-attr-compile", makeTree("old")); !bytes.Equal(got, want) {
		t.Errorf("CompileBytes: got %q, want %q", got, want)
	}
	if got := FlattenBytes("raw-attr-flatten", makeTree("old")); !bytes.Equal(got, want) {
		t.Errorf("FlattenBytes: got %q, want %q", got, want)
	}

	d := NewDiffer()
	d.RenderBytes(makeTree("old"))
	patches, change := d.Diff(makeTree("new"))
	if change != nil {
		t.Fatal("content change should patch, not signal a structural change")
	}
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch, got %d", len(patches))
	}
	if !bytes.Contains(patches[0].HTML, []byte(`data-raw="`+raw+`"`)) {
		t.Errorf("patch should carry the raw value verbatim, got %q", patches[0].HTML)
	}
}
