package jit

import (
	"strings"
	"testing"

	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/span"
	"github.com/jpl-au/fluent/node"
)

// TestMemoiserSkipsUnchangedSubtree verifies that when a memoisation key
// matches the previous render, the closure is not called and no
// patch is emitted for that Dynamic region.
func TestMemoiserSkipsUnchangedSubtree(t *testing.T) {
	m := NewMemoiser()

	calls := 0
	tree := func(version int) node.Node {
		return div.New(
			div.New(
				Memoise(version, func() node.Node {
					calls++
					return span.Text("expensive")
				}),
			).Dynamic("items"),
		)
	}

	m.RenderBytes(tree(1))
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

// TestMemoiserRendersOnKeyChange verifies that when the memoisation key
// changes, the closure is called and a patch is emitted.
func TestMemoiserRendersOnKeyChange(t *testing.T) {
	m := NewMemoiser()

	tree := func(version int, text string) node.Node {
		return div.New(
			div.New(
				Memoise(version, func() node.Node {
					return span.Text(text)
				}),
			).Dynamic("items"),
		)
	}

	m.RenderBytes(tree(1, "old"))
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
// without a memoised child are always re-rendered (treated as a miss).
func TestMemoiserNonMemoAlwaysRenders(t *testing.T) {
	m := NewMemoiser()

	tree := func(count string) node.Node {
		return div.New(
			span.Text(count).Dynamic("count"),
		)
	}

	m.RenderBytes(tree("1"))
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

// TestMemoiserMixedRegions verifies that a tree with both memoised and
// non-memoised Dynamic regions works correctly.
func TestMemoiserMixedRegions(t *testing.T) {
	m := NewMemoiser()

	calls := 0
	tree := func(version int, count string) node.Node {
		return div.New(
			div.New(
				Memoise(version, func() node.Node {
					calls++
					return span.Text("static content")
				}),
			).Dynamic("items"),
			span.Text(count).Dynamic("count"),
		)
	}

	m.RenderBytes(tree(1, "1"))
	calls = 0

	patches, change := m.Diff(tree(1, "2"))
	if change != nil {
		t.Fatal("expected no structural change")
	}
	if calls != 0 {
		t.Errorf("memoised closure should not be called, was called %d times", calls)
	}
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch (count only), got %d", len(patches))
	}
	if patches[0].Key != "count" {
		t.Errorf("expected patch for 'count', got %q", patches[0].Key)
	}
}

// TestMemoiserExportImportPreservesKeys verifies that memoisation keys
// survive Export/Import.
func TestMemoiserExportImportPreservesKeys(t *testing.T) {
	m1 := NewMemoiser()

	calls := 0
	tree := func(version int) node.Node {
		return div.New(
			div.New(
				Memoise(version, func() node.Node {
					calls++
					return span.Text("expensive")
				}),
			).Dynamic("items"),
		)
	}

	m1.RenderBytes(tree(1))
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

// TestMemoiserClear resets snapshots and memoisation keys.
func TestMemoiserClear(t *testing.T) {
	m := NewMemoiser()
	tree := div.New(span.Text("hello").Dynamic("msg"))
	m.RenderBytes(tree)
	m.Clear()

	patches, change := m.Diff(tree)
	if patches != nil || change != nil {
		t.Error("expected nil, nil after Clear")
	}
}

// TestMemoiserUnseedeedReturnsNil verifies Diff returns nil before
// Render has been called.
// TestMemoiserMemoisedCount verifies Memoised() reports the number of
// keyed regions - zero when the render uses no Memoise, which is how a
// live-update layer detects a Memoise-enabled handler that forgot the keys.
func TestMemoiserMemoisedCount(t *testing.T) {
	// No memoise nodes: a plain Dynamic region.
	plain := NewMemoiser()
	plainTree := div.New(span.Text("x").Dynamic("a"))
	plain.RenderBytes(plainTree)
	plain.Diff(plainTree)
	if n := plain.Memoised(); n != 0 {
		t.Errorf("plain render: Memoised() = %d, want 0", n)
	}

	// One memoised region.
	memo := NewMemoiser()
	memoTree := div.New(
		div.New(Memoise("v1", func() node.Node { return span.Text("x") })).Dynamic("a"),
	)
	memo.RenderBytes(memoTree)
	memo.Diff(memoTree)
	if n := memo.Memoised(); n != 1 {
		t.Errorf("memoised render: Memoised() = %d, want 1", n)
	}
}

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

	m.RenderBytes(tree1)
	_, change := m.Diff(tree2)

	if change == nil {
		t.Fatal("expected structural change when key added")
	}
	if len(change.Added) != 1 || change.Added[0] != "b" {
		t.Errorf("expected key 'b' added, got %v", change.Added)
	}
}

// TestMemoiserStructuralChangeLeavesStateIntact verifies that a Diff
// reporting a structural change leaves the memoiser's state untouched,
// matching the Differ. Before this guarantee the structural path adopted
// the new key order without snapshots behind the added keys, and a
// subsequent Export dereferenced the missing snapshot and panicked.
func TestMemoiserStructuralChangeLeavesStateIntact(t *testing.T) {
	m := NewMemoiser()

	one := func() node.Node {
		return div.New(
			div.New(
				Memoise(1, func() node.Node { return span.Text("a") }),
			).Dynamic("a"),
		)
	}
	two := func() node.Node {
		return div.New(
			div.New(
				Memoise(1, func() node.Node { return span.Text("a") }),
			).Dynamic("a"),
			div.New(
				Memoise(1, func() node.Node { return span.Text("b") }),
			).Dynamic("b"),
		)
	}

	m.RenderBytes(one())

	if _, change := m.Diff(two()); change == nil {
		t.Fatal("expected a structural change for the added key")
	}

	if data := m.Export(); data == nil {
		t.Fatal("Export after a structural diff should return data, not panic or nil")
	}

	// State was left alone, so the original tree still diffs cleanly.
	patches, change := m.Diff(one())
	if change != nil {
		t.Fatalf("original tree should not report a structural change, got %v", change)
	}
	if len(patches) != 0 {
		t.Errorf("expected no patches for the unchanged original tree, got %d", len(patches))
	}
}

// TestMemoiserStatsResetByRenderAndClear verifies that Stats after a
// fresh Render or Clear does not report counts left over from an
// earlier Diff cycle.
func TestMemoiserStatsResetByRenderAndClear(t *testing.T) {
	m := NewMemoiser()

	tree := func(version int) node.Node {
		return div.New(
			div.New(
				Memoise(version, func() node.Node { return span.Text("x") }),
			).Dynamic("items"),
		)
	}

	m.RenderBytes(tree(1))
	m.Diff(tree(1)) // one hit

	if hits, _ := m.Stats(); hits != 1 {
		t.Fatalf("expected 1 hit from the diff, got %d", hits)
	}

	m.RenderBytes(tree(1))
	if hits, misses := m.Stats(); hits != 0 || misses != 0 {
		t.Errorf("Stats after Render = (%d, %d), want (0, 0)", hits, misses)
	}

	m.Diff(tree(2)) // one miss
	m.Clear()
	if hits, misses := m.Stats(); hits != 0 || misses != 0 {
		t.Errorf("Stats after Clear = (%d, %d), want (0, 0)", hits, misses)
	}
}

// TestMemoiserRenderRunsClosureOnce verifies the single-pass guarantee
// for the seeding Render: the memoised closure runs exactly once, and
// the same bytes serve as both the page HTML and the snapshot. The old
// two-pass design ran every closure twice on the initial render.
func TestMemoiserRenderRunsClosureOnce(t *testing.T) {
	calls := 0
	tree := div.New(
		div.New(
			Memoise(1, func() node.Node {
				calls++
				return span.Text("expensive")
			}),
		).Dynamic("items"),
	)

	m := NewMemoiser()
	html := string(m.RenderBytes(tree))

	if calls != 1 {
		t.Errorf("memoised closure should run once during Render, ran %d times", calls)
	}
	if !strings.Contains(html, "expensive") {
		t.Errorf("page should contain the region content, got %q", html)
	}
}

// TestElementMemoiseSkips verifies the chained element form: a keyed
// element with .Memoise(version) participates in memoisation without
// any wrapper node. On a version match the whole region is skipped -
// closures inside never run - and on a change it renders and patches.
func TestElementMemoiseSkips(t *testing.T) {
	m := NewMemoiser()

	calls := 0
	tree := func(version int, text string) node.Node {
		return div.New(
			div.New(
				node.Func(func() node.Node {
					calls++
					return span.Text(text)
				}),
			).Dynamic("items").Memoise(version),
		)
	}

	html := string(m.RenderBytes(tree(1, "old")))
	if !strings.Contains(html, `data-fluent-memoise="1"`) {
		t.Errorf("memoised element should carry the memoise attribute, got %q", html)
	}
	calls = 0

	patches, change := m.Diff(tree(1, "old"))
	if change != nil {
		t.Fatalf("unexpected structural change: %v", change)
	}
	if len(patches) != 0 {
		t.Errorf("matching version should produce no patches, got %d", len(patches))
	}
	if calls != 0 {
		t.Errorf("matching version should skip the region's closures, ran %d times", calls)
	}

	patches, change = m.Diff(tree(2, "new"))
	if change != nil {
		t.Fatalf("unexpected structural change: %v", change)
	}
	if len(patches) != 1 || !strings.Contains(string(patches[0].HTML), "new") {
		t.Fatalf("changed version should produce one patch with the new content, got %v", patches)
	}
	if calls != 1 {
		t.Errorf("changed version should render the region once, ran %d times", calls)
	}
}
