package jit

import (
	"bytes"
	"slices"
	"strconv"
	"testing"

	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/span"
	"github.com/jpl-au/fluent/node"
)

func eachRegionEngine(t *testing.T, test func(*testing.T, func() nestedPatchEngine)) {
	t.Helper()
	for name, build := range map[string]func() nestedPatchEngine{
		"Differ":   func() nestedPatchEngine { return NewDiffer() },
		"Memoiser": func() nestedPatchEngine { return NewMemoiser() },
	} {
		t.Run(name, func(t *testing.T) { test(t, build) })
	}
}

// Nested membership changes have an existing DOM target: their enclosing
// Dynamic container. Content-only changes should target the affected children.
// These expectations also prohibit overlapping ancestor/descendant patches.
func TestNestedRegionPatches(t *testing.T) {
	row := func(key, value string) node.Node { return span.Text(value).Dynamic(key) }
	list := func(class string, rows ...node.Node) node.Node {
		return div.New(rows...).Class(class).Dynamic("list")
	}
	page := func(title string, rows node.Node) node.Node {
		return div.New(span.Text(title), rows).Dynamic("panel")
	}
	base := func() node.Node {
		return page("Title", list("rows", row("a", "A"), row("b", "B")))
	}
	cases := []struct {
		name string
		next func() node.Node
		keys []string
	}{
		{"ChildContent", func() node.Node {
			return page("Title", list("rows", row("a", "updated"), row("b", "B")))
		}, []string{"a"}},
		{"TwoChildren", func() node.Node {
			return page("Title", list("rows", row("a", "updated"), row("b", "also updated")))
		}, []string{"a", "b"}},
		{"Add", func() node.Node {
			return page("Title", list("rows", row("a", "A"), row("b", "B"), row("c", "C")))
		}, []string{"list"}},
		{"Remove", func() node.Node {
			return page("Title", list("rows", row("a", "A")))
		}, []string{"list"}},
		{"RemoveAll", func() node.Node {
			return page("Title", list("rows"))
		}, []string{"list"}},
		{"Reorder", func() node.Node {
			return page("Title", list("rows", row("b", "B"), row("a", "A")))
		}, []string{"list"}},
		{"ContainerAttributeAndChild", func() node.Node {
			return page("Title", list("selected", row("a", "updated"), row("b", "B")))
		}, []string{"list"}},
		{"AncestorContentAndChild", func() node.Node {
			return page("New title", list("rows", row("a", "updated"), row("b", "B")))
		}, []string{"panel"}},
		{"UnkeyedWrapper", func() node.Node {
			return page("Title", list("rows", div.New(row("a", "A")), row("b", "B")))
		}, []string{"list"}},
	}
	eachRegionEngine(t, func(t *testing.T, build func() nestedPatchEngine) {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				e := build()
				defer e.Clear()
				e.RenderBytes(base())
				patches, change := e.Diff(tc.next())
				if change != nil {
					t.Fatalf("nested change required a root render: %v", change)
				}
				var keys []string
				wantHTML := make(map[string][]byte)
				var collect func(node.Node)
				collect = func(n node.Node) {
					if key := trackedKey(n); key != "" {
						wantHTML[key] = n.RenderBytes()
					}
					for _, c := range n.Nodes() {
						collect(c)
					}
				}
				collect(tc.next())
				for _, patch := range patches {
					keys = append(keys, patch.Key)
					if !bytes.Equal(patch.HTML, wantHTML[patch.Key]) {
						t.Errorf("incorrect HTML for %q: %q", patch.Key, patch.HTML)
					}
				}
				if !slices.Equal(keys, tc.keys) {
					t.Fatalf("patch keys = %v, want %v", keys, tc.keys)
				}
				restored := build()
				defer restored.Clear()
				if err := restored.Import(e.Export()); err != nil {
					t.Fatal(err)
				}
				for _, current := range []nestedPatchEngine{e, restored} {
					if patches, change := current.Diff(tc.next()); len(patches) != 0 || change != nil {
						t.Errorf("delivered update changed again: patches=%v change=%v", patches, change)
					}
				}
			})
		}
	})
}

func TestMoveRegionBetweenContainers(t *testing.T) {
	tree := func(right bool) node.Node {
		var leftChildren, rightChildren []node.Node
		row := span.Text("row").Dynamic("row")
		if right {
			rightChildren = append(rightChildren, row)
		} else {
			leftChildren = append(leftChildren, row)
		}
		return div.New(
			div.New(leftChildren...).Dynamic("left"),
			div.New(rightChildren...).Dynamic("right"),
		).Dynamic("panel")
	}
	eachRegionEngine(t, func(t *testing.T, build func() nestedPatchEngine) {
		e := build()
		defer e.Clear()
		e.RenderBytes(tree(false))
		patches, change := e.Diff(tree(true))
		var keys []string
		for _, p := range patches {
			keys = append(keys, p.Key)
		}
		// A single enclosing morph can move the existing DOM element. Two
		// independent container patches would remove it, then create a new one,
		// losing its client-owned state despite the stable Dynamic id.
		if change != nil || !slices.Equal(keys, []string{"panel"}) {
			t.Fatalf("move patches=%v change=%v", keys, change)
		}
		if p := e.DiffKey("row", span.Text("moved").Dynamic("row")); p == nil {
			t.Fatal("moved child lost its snapshot")
		}
		if p := e.DiffKey("right", div.New(span.Text("moved").Dynamic("row")).Dynamic("right")); p != nil {
			t.Error("targeted update did not advance the new parent baseline")
		}
	})
}

func TestMoveRegionBetweenOutermostContainers(t *testing.T) {
	tree := func(right bool) node.Node {
		var leftChildren, rightChildren []node.Node
		row := span.Text("row").Dynamic("row")
		if right {
			rightChildren = append(rightChildren, row)
		} else {
			leftChildren = append(leftChildren, row)
		}
		return div.New(div.New(leftChildren...).Dynamic("left"), div.New(rightChildren...).Dynamic("right"))
	}
	eachRegionEngine(t, func(t *testing.T, build func() nestedPatchEngine) {
		e := build()
		defer e.Clear()
		e.RenderBytes(tree(false))
		before := e.Export()
		patches, change := e.Diff(tree(true))
		if change == nil || !change.Reordered || len(patches) != 0 {
			t.Fatalf("move without a shared keyed ancestor: patches=%v change=%v", patches, change)
		}
		if !bytes.Equal(e.Export(), before) {
			t.Error("root fallback changed the baseline before Render")
		}
	})
}

func TestDiffKeyResizesNestedRanges(t *testing.T) {
	tree := func(a, b string) node.Node {
		return div.New(
			div.New(span.Text(a).Dynamic("a"), span.Text(b).Dynamic("b")).Dynamic("list"),
			span.Text("unrelated").Dynamic("sibling"),
		).Dynamic("panel")
	}
	eachRegionEngine(t, func(t *testing.T, build func() nestedPatchEngine) {
		e := build()
		defer e.Clear()
		e.RenderBytes(tree("short", "original"))
		e.DiffKey("a", span.Text("much longer text").Dynamic("a"))
		e.DiffKey("b", span.Text("B").Dynamic("b"))
		e.DiffKey("a", span.Text("").Dynamic("a"))
		if p := e.DiffKey("panel", tree("", "B")); p != nil {
			t.Fatalf("resized ranges corrupted the enclosing snapshot: %q", p.HTML)
		}
		if p := e.DiffKey("sibling", span.Text("unrelated").Dynamic("sibling")); p != nil {
			t.Error("unrelated sibling snapshot changed")
		}
	})
}

func TestNestedMemoisation(t *testing.T) {
	calls := [2]int{}
	tree := func(parentVersion, childVersion int) node.Node {
		return div.New(Memoise(parentVersion, func() node.Node {
			calls[0]++
			return div.New(Memoise(childVersion, func() node.Node {
				calls[1]++
				return span.Text(strconv.Itoa(childVersion))
			})).Dynamic("child")
		})).Dynamic("parent")
	}
	m := NewMemoiser()
	defer m.Clear()
	m.RenderBytes(tree(1, 1))
	calls = [2]int{}
	// A parent version governs its entire subtree. Changing only a child's
	// version cannot invalidate a parent that the application declares unchanged.
	if patches, change := m.Diff(tree(1, 2)); len(patches) != 0 || change != nil || calls != [2]int{} {
		t.Fatalf("parent hit did not skip the subtree: patches=%v change=%v calls=%v", patches, change, calls)
	}
	// Changing the parent version permits inspection of the child, whose own
	// unchanged version can still skip its closure.
	patches, change := m.Diff(tree(2, 1))
	if change != nil || len(patches) != 1 || patches[0].Key != "parent" || calls != [2]int{1, 0} {
		t.Fatalf("nested child hit: patches=%v change=%v calls=%v", patches, change, calls)
	}
	if hits, misses := m.Stats(); hits != 1 || misses != 1 {
		t.Errorf("stats = (%d, %d), want (1, 1)", hits, misses)
	}
	if n := m.Memoised(); n != 2 {
		t.Errorf("memoised regions = %d, want 2", n)
	}
}

func TestNestedMemoisationWithoutParentVersion(t *testing.T) {
	calls := [2]int{}
	tree := func(version int) node.Node {
		return div.New(
			div.New(Memoise(1, func() node.Node {
				calls[0]++
				return span.Text("unchanged")
			})).Dynamic("a"),
			div.New(Memoise(version, func() node.Node {
				calls[1]++
				return span.Text(strconv.Itoa(version))
			})).Dynamic("b"),
		).Dynamic("parent")
	}
	m := NewMemoiser()
	defer m.Clear()
	m.RenderBytes(tree(1))
	calls = [2]int{}
	patches, change := m.Diff(tree(2))
	if change != nil || len(patches) != 1 || patches[0].Key != "b" || calls != [2]int{0, 1} {
		t.Fatalf("independent nested versions: patches=%v change=%v calls=%v", patches, change, calls)
	}
}

func TestNestedChainedVersionsDoNotMemoiseParent(t *testing.T) {
	tree := func(version int) node.Node {
		return div.New(
			span.Text("unchanged").Dynamic("a").Memoise(1),
			span.Text(strconv.Itoa(version)).Dynamic("b").Memoise(version),
		).Dynamic("parent")
	}
	m := NewMemoiser()
	defer m.Clear()
	m.RenderBytes(tree(1))
	patches, change := m.Diff(tree(2))
	if change != nil || len(patches) != 1 || patches[0].Key != "b" {
		t.Fatalf("child a's version hid child b's update: patches=%v change=%v", patches, change)
	}
	if n := m.Memoised(); n != 2 {
		t.Errorf("an unversioned parent acquired its child's version: memoised=%d, want 2", n)
	}
}

func TestDiffKeyReplacesNestedMembership(t *testing.T) {
	list := func(keys ...string) node.Node {
		var rows []node.Node
		for _, key := range keys {
			rows = append(rows, span.Text(key).Dynamic(key))
		}
		return div.New(rows...).Dynamic("list")
	}
	eachRegionEngine(t, func(t *testing.T, build func() nestedPatchEngine) {
		e := build()
		defer e.Clear()
		e.RenderBytes(div.New(list("a", "b")).Dynamic("panel"))
		e.DiffKey("list", list("c", "a"))
		if patches, change := e.Diff(div.New(list("c", "a")).Dynamic("panel")); change != nil || len(patches) != 0 {
			t.Fatalf("targeted membership change left an old baseline: patches=%v change=%v", patches, change)
		}
		if p := e.DiffKey("c", span.Text("c").Dynamic("c")); p != nil {
			t.Error("new child's snapshot was not captured")
		}
		if p := e.DiffKey("b", span.Text("b").Dynamic("b")); p == nil {
			t.Error("removed child's stale snapshot suppressed an unknown-key patch")
		}
	})
}

func TestNestedImportFailuresLeaveBaselineIntact(t *testing.T) {
	eachRegionEngine(t, func(t *testing.T, build func() nestedPatchEngine) {
		e := build()
		defer e.Clear()
		tree := div.New(span.Text("child").Dynamic("child")).Dynamic("parent")
		e.RenderBytes(tree)
		blob := e.Export()
		if len(blob) == 0 {
			t.Fatal("seeded engine returned an empty snapshot")
		}
		if blob[0] != 1 {
			t.Fatalf("snapshot encoding marker = %d, want 1", blob[0])
		}
		for i := range len(blob) {
			if err := e.Import(blob[:i]); err == nil {
				t.Fatalf("accepted truncated snapshot at byte %d", i)
			}
			if got := e.Export(); !bytes.Equal(got, blob) {
				t.Fatalf("failed import at byte %d changed the baseline", i)
			}
		}
		unsupported := bytes.Clone(blob)
		unsupported[0] = exportVersion + 1
		if err := e.Import(unsupported); err == nil {
			t.Error("accepted an unsupported encoding marker")
		}
		if patches, change := e.Diff(tree); len(patches) != 0 || change != nil {
			t.Errorf("failed imports corrupted snapshots: patches=%v change=%v", patches, change)
		}
	})
}
