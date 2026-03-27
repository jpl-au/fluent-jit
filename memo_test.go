package jit

import (
	"testing"

	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/span"
	"github.com/jpl-au/fluent/node"
)

// TestMemoiserSkipsUnchangedSubtree verifies that when a memo key
// matches the previous render, the closure is not called and no
// patch is emitted for that Dynamic region.
func TestMemoiserSkipsUnchangedSubtree(t *testing.T) {
	m := NewMemoiser()

	calls := 0
	tree := func(version int) node.Node {
		return div.New(
			div.New(
				node.Memo(version, func() node.Node {
					calls++
					return span.Text("expensive")
				}),
			).Dynamic("items"),
		)
	}

	m.Render(tree(1))
	calls = 0

	patches, change := m.Diff(tree(1))
	if change != nil {
		t.Fatal("expected no structural change")
	}
	if len(patches) != 0 {
		t.Errorf("expected no patches, got %d", len(patches))
	}
	if calls != 0 {
		t.Errorf("expected closure to be skipped, but it was called %d times", calls)
	}
}

// TestMemoiserRendersOnKeyChange verifies that when the memo key
// changes, the closure is called and a patch is emitted.
func TestMemoiserRendersOnKeyChange(t *testing.T) {
	m := NewMemoiser()

	tree := func(version int, text string) node.Node {
		return div.New(
			div.New(
				node.Memo(version, func() node.Node {
					return span.Text(text)
				}),
			).Dynamic("items"),
		)
	}

	m.Render(tree(1, "old"))
	patches, change := m.Diff(tree(2, "new"))

	if change != nil {
		t.Fatal("expected no structural change")
	}
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch, got %d", len(patches))
	}
	if patches[0].Key != "items" {
		t.Errorf("expected patch for key 'items', got %q", patches[0].Key)
	}
}

// TestMemoiserNonMemoAlwaysRenders verifies that Dynamic nodes
// without a memo child are always re-rendered (treated as a miss).
func TestMemoiserNonMemoAlwaysRenders(t *testing.T) {
	m := NewMemoiser()

	tree := func(count string) node.Node {
		return div.New(
			span.Text(count).Dynamic("count"),
		)
	}

	m.Render(tree("1"))
	patches, change := m.Diff(tree("2"))

	if change != nil {
		t.Fatal("expected no structural change")
	}
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch, got %d", len(patches))
	}
	if patches[0].Key != "count" {
		t.Errorf("expected patch for 'count', got %q", patches[0].Key)
	}
}

// TestMemoiserMixedRegions verifies that a tree with both memo and
// non-memo Dynamic regions works correctly.
func TestMemoiserMixedRegions(t *testing.T) {
	m := NewMemoiser()

	calls := 0
	tree := func(version int, count string) node.Node {
		return div.New(
			div.New(
				node.Memo(version, func() node.Node {
					calls++
					return span.Text("static content")
				}),
			).Dynamic("items"),
			span.Text(count).Dynamic("count"),
		)
	}

	m.Render(tree(1, "1"))
	calls = 0

	patches, change := m.Diff(tree(1, "2"))
	if change != nil {
		t.Fatal("expected no structural change")
	}
	if calls != 0 {
		t.Errorf("memo closure should not be called, was called %d times", calls)
	}
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch (count only), got %d", len(patches))
	}
	if patches[0].Key != "count" {
		t.Errorf("expected patch for 'count', got %q", patches[0].Key)
	}
}

// TestMemoiserExportImportPreservesKeys verifies that memo keys
// survive Export/Import.
func TestMemoiserExportImportPreservesKeys(t *testing.T) {
	m1 := NewMemoiser()

	calls := 0
	tree := func(version int) node.Node {
		return div.New(
			div.New(
				node.Memo(version, func() node.Node {
					calls++
					return span.Text("expensive")
				}),
			).Dynamic("items"),
		)
	}

	m1.Render(tree(1))
	data := m1.Export()
	if data == nil {
		t.Fatal("Export should return data")
	}

	m2 := NewMemoiser()
	if err := m2.Import(data); err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	calls = 0
	patches, change := m2.Diff(tree(1))
	if change != nil {
		t.Fatal("expected no structural change after import")
	}
	if len(patches) != 0 {
		t.Errorf("expected no patches (same version after import), got %d", len(patches))
	}
	if calls != 0 {
		t.Errorf("expected closure to be skipped after import, called %d times", calls)
	}
}

// TestMemoiserClear resets snapshots and memo keys.
func TestMemoiserClear(t *testing.T) {
	m := NewMemoiser()
	tree := div.New(span.Text("hello").Dynamic("msg"))
	m.Render(tree)
	m.Clear()

	patches, change := m.Diff(tree)
	if patches != nil || change != nil {
		t.Error("expected nil, nil after Clear")
	}
}

// TestMemoiserUnseedeedReturnsNil verifies Diff returns nil before
// Render has been called.
func TestMemoiserUnseedeedReturnsNil(t *testing.T) {
	m := NewMemoiser()
	tree := div.New(span.Text("hello").Dynamic("msg"))

	patches, change := m.Diff(tree)
	if patches != nil || change != nil {
		t.Error("expected nil, nil before Render")
	}
}

// TestMemoiserDetectsStructuralChange verifies that structural
// changes (Dynamic keys added/removed) are detected.
func TestMemoiserDetectsStructuralChange(t *testing.T) {
	m := NewMemoiser()

	tree1 := div.New(span.Text("a").Dynamic("a"))
	tree2 := div.New(
		span.Text("a").Dynamic("a"),
		span.Text("b").Dynamic("b"),
	)

	m.Render(tree1)
	_, change := m.Diff(tree2)

	if change == nil {
		t.Fatal("expected structural change when key added")
	}
	if len(change.Added) != 1 || change.Added[0] != "b" {
		t.Errorf("expected key 'b' added, got %v", change.Added)
	}
}
