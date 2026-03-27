package jit

import (
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
	d.Render(tree)

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

// TestDiffKeyNoChange verifies that DiffKey returns nil when the
// content is unchanged.
func TestDiffKeyNoChange(t *testing.T) {
	d := NewDiffer()

	tree := div.New(span.Text("same").Dynamic("target"))
	d.Render(tree)

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
	d.Render(tree)

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
	d.Render(tree)

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

// TestMemoiserDiffKey verifies that DiffKey works on the Memoiser
// with the same behaviour as the Differ.
func TestMemoiserDiffKey(t *testing.T) {
	m := NewMemoiser()

	tree := div.New(
		div.New(
			node.Memo(1, func() node.Node { return span.Text("old") }),
		).Dynamic("target"),
	)
	m.Render(tree)

	patch := m.DiffKey("target",
		div.New(
			node.Memo(2, func() node.Node { return span.Text("new") }),
		).Dynamic("target"),
	)
	if patch == nil {
		t.Fatal("expected patch for changed key")
	}
}
