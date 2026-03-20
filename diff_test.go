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
// the full HTML output - identical to calling Render on the root node
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
// for events that don't affect visible state - the diff engine should detect
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
	patches, change := differ.Diff(tree())

	if change != nil {
		t.Error("identical trees should not trigger a structural change")
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
	patches, change := differ.Diff(makeTree("43"))

	if change != nil {
		t.Error("same keys should not trigger a structural change")
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
	patches, change := differ.Diff(makeTree("Bob", "20"))

	if change != nil {
		t.Error("same keys should not trigger a structural change")
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
// patches - unchanged elements are left alone. This is what makes the
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
	patches, change := differ.Diff(makeTree("Alice", "20"))

	if change != nil {
		t.Error("same keys should not trigger a structural change")
	}
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch when only one element changed, got %d", len(patches))
	}
	if patches[0].Key != "count" {
		t.Errorf("patch should be for the changed element 'count', got %q", patches[0].Key)
	}
}

// TestDifferStructuralChangeKeyAdded verifies that when a new keyed element
// appears between renders, Diff signals a structural change and reports
// which key was added.
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
	patches, change := differ.Diff(tree2)

	if change == nil {
		t.Fatal("adding a new keyed element should trigger a structural change")
	}
	if patches != nil {
		t.Error("structural change should return nil patches - the caller should use Render instead")
	}
	if len(change.Added) != 1 || change.Added[0] != "help" {
		t.Errorf("should report 'help' as added, got Added=%v", change.Added)
	}
	if len(change.Removed) != 0 {
		t.Errorf("should report no removals, got Removed=%v", change.Removed)
	}
}

// TestDifferStructuralChangeKeyRemoved verifies that when a keyed element
// disappears between renders, Diff signals a structural change and reports
// which key was removed.
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
	patches, change := differ.Diff(tree2)

	if change == nil {
		t.Fatal("removing a keyed element should trigger a structural change")
	}
	if patches != nil {
		t.Error("structural change should return nil patches")
	}
	if len(change.Removed) != 1 || change.Removed[0] != "help" {
		t.Errorf("should report 'help' as removed, got Removed=%v", change.Removed)
	}
	if len(change.Added) != 0 {
		t.Errorf("should report no additions, got Added=%v", change.Added)
	}
}

// TestDifferStructuralChangeKeyReordered verifies that when the same set
// of keys appears in a different order between renders, Diff signals a
// structural change with Reordered=true. Reordering (e.g. sorting a list)
// changes the DOM structure even though the same elements are present.
func TestDifferStructuralChangeKeyReordered(t *testing.T) {
	differ := NewDiffer()

	tree1 := div.New(
		span.Text("Alice").Dynamic("name"),
		span.Text("42").Dynamic("count"),
	)
	tree2 := div.New(
		span.Text("42").Dynamic("count"),
		span.Text("Alice").Dynamic("name"),
	)

	differ.Render(tree1)
	patches, change := differ.Diff(tree2)

	if change == nil {
		t.Fatal("reordering keyed elements should trigger a structural change")
	}
	if patches != nil {
		t.Error("structural change should return nil patches")
	}
	if !change.Reordered {
		t.Error("should report Reordered=true when same keys in different order")
	}
	if len(change.Added) != 0 || len(change.Removed) != 0 {
		t.Errorf("reorder should have no additions or removals, got Added=%v Removed=%v",
			change.Added, change.Removed)
	}
}

// TestDifferStructuralChangeString verifies that StructuralChange.String
// produces a human-readable description suitable for log output.
func TestDifferStructuralChangeString(t *testing.T) {
	tests := []struct {
		name   string
		change StructuralChange
		want   string
	}{
		{
			name:   "single key added",
			change: StructuralChange{Added: []string{"sidebar"}},
			want:   "key 'sidebar' added",
		},
		{
			name:   "multiple keys added",
			change: StructuralChange{Added: []string{"sidebar", "help"}},
			want:   "keys 'sidebar', 'help' added",
		},
		{
			name:   "single key removed",
			change: StructuralChange{Removed: []string{"help"}},
			want:   "key 'help' removed",
		},
		{
			name:   "added and removed",
			change: StructuralChange{Added: []string{"nav"}, Removed: []string{"help"}},
			want:   "key 'nav' added, key 'help' removed",
		},
		{
			name:   "reordered",
			change: StructuralChange{Reordered: true},
			want:   "keys reordered",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.change.String()
			if got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDifferDiffBeforeRender verifies that calling Diff before Render
// returns nil patches and no structural change. This is a no-op - the
// differ has no baseline to compare against yet.
func TestDifferDiffBeforeRender(t *testing.T) {
	differ := NewDiffer()

	tree := div.New(span.Text("hello").Dynamic("msg"))
	patches, change := differ.Diff(tree)

	if change != nil {
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
			span.Text(value).Dynamic(), // no key - uses "_" sentinel
		)
	}

	differ.Render(makeTree("hello"))
	patches, change := differ.Diff(makeTree("world"))

	if change != nil {
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

	// Structural change - new key added
	tree2 := div.New(
		span.Text("42").Dynamic("count"),
		p.Text("Help").Dynamic("help"),
	)
	_, change := differ.Diff(tree2)
	if change == nil {
		t.Fatal("expected structural change")
	}

	// Re-render to establish new baseline
	differ.Render(tree2)

	// Now Diff should work against the new baseline
	tree3 := div.New(
		span.Text("43").Dynamic("count"),
		p.Text("Help").Dynamic("help"),
	)
	patches, change := differ.Diff(tree3)

	if change != nil {
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
// lose track of one element - only the last one visited would be stored.
func TestDifferValidateDuplicateKeys(t *testing.T) {
	differ := NewDiffer()

	tree := div.New(
		span.Text("first").Dynamic("same-key"),
		span.Text("second").Dynamic("same-key"),
	)

	err := differ.Validate(tree)
	if err == nil {
		t.Fatal("duplicate keys should fail validation - the diff engine cannot track both elements")
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
// This is the degenerate case - the diff engine has nothing to track.
func TestDifferNoKeysNoPatchesNoStructuralChange(t *testing.T) {
	differ := NewDiffer()

	tree := func() node.Node {
		return div.New(span.Static("hello"))
	}

	differ.Render(tree())
	patches, change := differ.Diff(tree())

	if change != nil {
		t.Error("no keys in either tree should not trigger structural change")
	}
	if len(patches) != 0 {
		t.Errorf("no keys should produce no patches, got %d", len(patches))
	}
}

// TestDifferWhenTrueExposesKey verifies that a keyed element inside
// node.When(true, ...) is visible to the Differ and produces patches
// when its content changes.
func TestDifferWhenTrueExposesKey(t *testing.T) {
	differ := NewDiffer()

	makeTree := func(msg string) node.Node {
		return div.New(
			node.When(true, span.Text(msg).Dynamic("msg")),
		)
	}

	differ.Render(makeTree("hello"))
	patches, change := differ.Diff(makeTree("world"))

	if change != nil {
		t.Error("same keys should not trigger a structural change")
	}
	if len(patches) != 1 || patches[0].Key != "msg" {
		t.Fatalf("expected 1 patch for 'msg', got %d patches", len(patches))
	}
}

// TestDifferWhenToggledDetectsStructuralChange verifies that toggling a
// condition from true to false causes the keyed element to disappear,
// which the Differ should detect as a structural change.
func TestDifferWhenToggledDetectsStructuralChange(t *testing.T) {
	differ := NewDiffer()

	tree1 := div.New(
		span.Text("count").Dynamic("count"),
		node.When(true, p.Text("help").Dynamic("help")),
	)
	tree2 := div.New(
		span.Text("count").Dynamic("count"),
		node.When(false, p.Text("help").Dynamic("help")),
	)

	differ.Render(tree1)
	_, change := differ.Diff(tree2)

	if change == nil {
		t.Fatal("toggling When from true to false should trigger a structural change - the 'help' key disappeared")
	}
	if len(change.Removed) != 1 || change.Removed[0] != "help" {
		t.Errorf("should report 'help' as removed, got Removed=%v", change.Removed)
	}
}

// TestDifferConditionBothBranches verifies that switching between
// Condition(true) and Condition(false) with different keyed branches
// triggers a structural change.
func TestDifferConditionBothBranches(t *testing.T) {
	differ := NewDiffer()

	tree1 := div.New(
		node.Condition(true).
			True(span.Text("yes").Dynamic("yes")).
			False(span.Text("no").Dynamic("no")),
	)
	tree2 := div.New(
		node.Condition(false).
			True(span.Text("yes").Dynamic("yes")).
			False(span.Text("no").Dynamic("no")),
	)

	differ.Render(tree1)
	_, change := differ.Diff(tree2)

	if change == nil {
		t.Fatal("switching condition branches should trigger a structural change - different keys are active")
	}
}

// TestDifferFuncExposesKeyedChildren verifies that keyed elements returned
// by node.Func are visible to the Differ.
func TestDifferFuncExposesKeyedChildren(t *testing.T) {
	differ := NewDiffer()

	makeTree := func(msg string) node.Node {
		return div.New(
			node.Func(func() node.Node {
				return span.Text(msg).Dynamic("msg")
			}),
		)
	}

	differ.Render(makeTree("hello"))
	patches, change := differ.Diff(makeTree("world"))

	if change != nil {
		t.Error("same keys should not trigger a structural change")
	}
	if len(patches) != 1 || patches[0].Key != "msg" {
		t.Fatalf("expected 1 patch for 'msg', got %d patches", len(patches))
	}
}

// TestDifferFuncsExposesKeyedChildren verifies that keyed elements
// returned by node.Funcs are visible to the Differ.
func TestDifferFuncsExposesKeyedChildren(t *testing.T) {
	differ := NewDiffer()

	makeTree := func(items []string) node.Node {
		return div.New(
			node.Funcs(func() []node.Node {
				nodes := make([]node.Node, len(items))
				for i, item := range items {
					nodes[i] = span.Text(item).Dynamic("item-" + item)
				}
				return nodes
			}),
		)
	}

	differ.Render(makeTree([]string{"a", "b"}))
	_, change := differ.Diff(makeTree([]string{"a", "b", "c"}))

	if change == nil {
		t.Fatal("adding a keyed element via Funcs should trigger a structural change")
	}
	if len(change.Added) != 1 || change.Added[0] != "item-c" {
		t.Errorf("should report 'item-c' as added, got Added=%v", change.Added)
	}
}

// TestExportImportRoundTrip verifies that Export and Import preserve
// the differ's snapshots across a round-trip. After importing, Diff
// should detect changes against the restored baseline.
func TestExportImportRoundTrip(t *testing.T) {
	d1 := NewDiffer()
	tree := div.New(
		span.Text("Alice").Dynamic("name"),
		span.Text("42").Dynamic("count"),
	)
	d1.Render(tree)

	data := d1.Export()
	if data == nil {
		t.Fatal("Export should return non-nil after Render")
	}

	d2 := NewDiffer()
	if err := d2.Import(data); err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Diff against the imported baseline with changed content.
	changed := div.New(
		span.Text("Bob").Dynamic("name"),
		span.Text("42").Dynamic("count"),
	)
	patches, change := d2.Diff(changed)
	if change != nil {
		t.Error("same keys should not trigger a structural change")
	}
	if len(patches) != 1 || patches[0].Key != "name" {
		t.Fatalf("expected 1 patch for 'name', got %d patches", len(patches))
	}
	if !bytes.Contains(patches[0].HTML, []byte("Bob")) {
		t.Errorf("patch should contain 'Bob', got %q", patches[0].HTML)
	}
}

// TestExportImportNoChange verifies that Diff returns no patches when
// the tree is identical to the imported baseline.
func TestExportImportNoChange(t *testing.T) {
	d1 := NewDiffer()
	tree := func() node.Node {
		return div.New(span.Text("hello").Dynamic("msg"))
	}
	d1.Render(tree())

	data := d1.Export()

	d2 := NewDiffer()
	if err := d2.Import(data); err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	patches, change := d2.Diff(tree())
	if change != nil {
		t.Error("identical tree should not trigger structural change")
	}
	if len(patches) != 0 {
		t.Errorf("identical tree should produce no patches, got %d", len(patches))
	}
}

// TestExportBeforeSeed verifies that Export returns nil when the
// differ has not been seeded with an initial Render.
func TestExportBeforeSeed(t *testing.T) {
	d := NewDiffer()
	if data := d.Export(); data != nil {
		t.Errorf("Export before Render should return nil, got %d bytes", len(data))
	}
}

// TestImportCorruptData verifies that Import returns an error when
// given invalid data rather than panicking or silently producing
// a broken state.
func TestImportCorruptData(t *testing.T) {
	d := NewDiffer()
	if err := d.Import([]byte{0xFF}); err == nil {
		t.Error("Import of corrupt data should return an error")
	}
}

// TestClearFreesMemory verifies that Clear resets the differ to an
// unseeded state. After Clear, Diff should behave as if Render was
// never called, and Export should return nil.
func TestClearFreesMemory(t *testing.T) {
	d := NewDiffer()
	tree := div.New(span.Text("hello").Dynamic("msg"))
	d.Render(tree)

	d.Clear()

	if data := d.Export(); data != nil {
		t.Error("Export after Clear should return nil")
	}

	patches, change := d.Diff(tree)
	if change != nil {
		t.Error("Diff after Clear should not signal structural change")
	}
	if patches != nil {
		t.Error("Diff after Clear should return nil patches")
	}
}

// TestClearThenRender verifies that a differ can be reused after Clear.
// Clear → Render → Diff should work as if the differ was freshly created.
func TestClearThenRender(t *testing.T) {
	d := NewDiffer()
	tree1 := div.New(span.Text("hello").Dynamic("msg"))
	d.Render(tree1)
	d.Clear()

	tree2 := div.New(span.Text("world").Dynamic("msg"))
	d.Render(tree2)

	tree3 := div.New(span.Text("again").Dynamic("msg"))
	patches, change := d.Diff(tree3)

	if change != nil {
		t.Error("same keys should not trigger structural change")
	}
	if len(patches) != 1 || patches[0].Key != "msg" {
		t.Fatalf("expected 1 patch for 'msg', got %d", len(patches))
	}
}

// TestExportDoesNotClearState verifies that Export is non-destructive.
// The differ should still function normally after Export - Diff should
// continue to work against the same baseline.
func TestExportDoesNotClearState(t *testing.T) {
	d := NewDiffer()
	tree := func() node.Node {
		return div.New(span.Text("hello").Dynamic("msg"))
	}
	d.Render(tree())

	_ = d.Export()

	// Differ should still work - Export didn't destroy anything.
	changed := div.New(span.Text("world").Dynamic("msg"))
	patches, change := d.Diff(changed)
	if change != nil {
		t.Error("same keys should not trigger structural change")
	}
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch, got %d", len(patches))
	}
}

// TestImportStructuralChange verifies that after Import, the differ
// detects structural changes when the new tree has different keys.
func TestImportStructuralChange(t *testing.T) {
	d1 := NewDiffer()
	d1.Render(div.New(span.Text("hello").Dynamic("msg")))

	data := d1.Export()

	d2 := NewDiffer()
	if err := d2.Import(data); err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Different key set - should be a structural change.
	different := div.New(span.Text("hello").Dynamic("other"))
	_, change := d2.Diff(different)
	if change == nil {
		t.Fatal("different keys should trigger a structural change")
	}
}

// buildKeyedTree creates a tree with n keyed span elements, simulating
// a page with many dynamic components.
func buildKeyedTree(n int, prefix string) node.Node {
	children := make([]node.Node, n)
	for i := range children {
		key := prefix + string(rune('A'+i%26)) + string(rune('0'+i/26))
		children[i] = span.Text(prefix + key).Dynamic(key)
	}
	return div.New(children...)
}

func BenchmarkDifferRender(b *testing.B) {
	tree := buildKeyedTree(50, "v1-")
	differ := NewDiffer()

	b.ResetTimer()
	for b.Loop() {
		differ.Render(tree)
	}
}

func BenchmarkDifferDiffNoChange(b *testing.B) {
	tree := buildKeyedTree(50, "v1-")
	differ := NewDiffer()
	differ.Render(tree)

	b.ResetTimer()
	for b.Loop() {
		differ.Diff(tree)
	}
}

func BenchmarkDifferDiffWithChanges(b *testing.B) {
	tree1 := buildKeyedTree(50, "v1-")
	tree2 := buildKeyedTree(50, "v2-")
	differ := NewDiffer()
	differ.Render(tree1)

	b.ResetTimer()
	for b.Loop() {
		differ.Diff(tree2)
	}
}
