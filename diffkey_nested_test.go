package jit

import (
	"bytes"
	"testing"

	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/span"
	"github.com/jpl-au/fluent/node"
)

// nestedPatchEngine keeps the regression cases identical for both engines.
// These tests observe returned patches, including after persistence, rather
// than relying on how either engine represents its snapshots.
type nestedPatchEngine interface {
	RenderBytes(node.Node) []byte
	Diff(node.Node) ([]Patch, *StructuralChange)
	DiffKey(string, node.Node) *Patch
	Export() []byte
	Import([]byte) error
	Clear()
}

// TestDiffKeyNestedBaseline checks that a targeted update advances the whole
// enclosing baseline, including when another engine imports it on reconnect.
func TestDiffKeyNestedBaseline(t *testing.T) {
	for name, build := range map[string]func() nestedPatchEngine{
		"Differ":   func() nestedPatchEngine { return NewDiffer() },
		"Memoiser": func() nestedPatchEngine { return NewMemoiser() },
	} {
		t.Run(name, func(t *testing.T) {
			tree := func(value string) node.Node {
				return div.New(span.Text(value).Dynamic("child")).Dynamic("parent")
			}
			e := build()
			defer e.Clear()
			e.RenderBytes(tree("short"))
			e.DiffKey("child", span.Text("a much longer value").Dynamic("child"))
			restored := build()
			defer restored.Clear()
			if err := restored.Import(e.Export()); err != nil {
				t.Fatal(err)
			}
			for _, current := range []nestedPatchEngine{e, restored} {
				patches, change := current.Diff(tree("a much longer value"))
				if change != nil || len(patches) != 0 {
					t.Errorf("already-delivered child update produced patches=%v change=%v", patches, change)
				}
			}
		})
	}
}

func TestMemoiserDiffKeyInvalidatesEnclosingVersion(t *testing.T) {
	m := NewMemoiser()
	defer m.Clear()
	parentCalls, siblingCalls := 0, 0
	tree := func() node.Node {
		return div.New(
			div.New(Memoise(1, func() node.Node {
				parentCalls++
				return span.Text("0").Dynamic("child")
			})).Dynamic("parent"),
			div.New(Memoise(1, func() node.Node {
				siblingCalls++
				return span.Static("untouched")
			})).Dynamic("sibling"),
		)
	}
	m.RenderBytes(tree())
	m.DiffKey("child", span.Text("1").Dynamic("child"))
	parentCalls, siblingCalls = 0, 0
	patches, change := m.Diff(tree())
	if change != nil || len(patches) == 0 {
		t.Errorf("memoisation hid the child correction: patches=%v change=%v", patches, change)
	}
	if parentCalls != 1 || siblingCalls != 0 {
		t.Errorf("render calls: parent=%d sibling=%d, want 1 and 0", parentCalls, siblingCalls)
	}
}

func TestSharedNestedDiffKey(t *testing.T) {
	ResetSharedCache()
	t.Cleanup(ResetSharedCache)
	calls := 0
	tree := func() node.Node {
		return div.New(Shared("nested:v1", func() node.Node {
			calls++
			return span.Text("0").Dynamic("child")
		})).Dynamic("parent")
	}
	a, b := NewMemoiser(), NewMemoiser()
	defer a.Clear()
	defer b.Clear()
	a.RenderBytes(tree())
	want := b.RenderBytes(tree())
	if calls != 1 {
		t.Fatalf("shared cache hit evaluated the closure: calls=%d", calls)
	}
	b.DiffKey("child", span.Text("0").Dynamic("child"))
	b.DiffKey("parent", div.New(span.Text("1").Dynamic("child")).Dynamic("parent"))
	if p := b.DiffKey("child", span.Text("0").Dynamic("child")); p == nil {
		t.Error("parent patch left a stale child snapshot after a shared cache hit")
	}
	c := NewMemoiser()
	defer c.Clear()
	if got := c.RenderBytes(tree()); !bytes.Equal(got, want) || calls != 1 {
		t.Errorf("session patch changed the shared cache: HTML=%q calls=%d", got, calls)
	}
}

// TestDiffKeyNestedReplacements checks that replacing an element also changes
// the baseline seen by later patches to its ancestors and descendants. The
// browser has already applied the first patch, so comparing the second update
// with an older snapshot can incorrectly suppress a necessary correction.
func TestDiffKeyNestedReplacements(t *testing.T) {
	engines := []struct {
		name string
		new  func() nestedPatchEngine
	}{
		{"Differ", func() nestedPatchEngine { return NewDiffer() }},
		{"Memoiser", func() nestedPatchEngine { return NewMemoiser() }},
	}

	child := func(value string) node.Node {
		return span.Text(value).Dynamic("child")
	}
	parent := func(value string) node.Node {
		return div.New(child(value)).Dynamic("parent")
	}
	page := func(value string) node.Node {
		return div.New(parent(value), span.Static("untouched").Dynamic("sibling"))
	}

	cases := []struct {
		name      string
		firstKey  string
		first     func(string) node.Node
		secondKey string
		second    func(string) node.Node
	}{
		{"ParentThenChild", "parent", parent, "child", child},
		{"ChildThenParent", "child", child, "parent", parent},
	}

	for _, engine := range engines {
		t.Run(engine.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					for _, restore := range []bool{false, true} {
						name := "Live"
						if restore {
							name = "AfterImport"
						}
						t.Run(name, func(t *testing.T) {
							e := engine.new()
							defer e.Clear()
							e.RenderBytes(page("0"))

							// The Memoiser initially tracks the outer region. A
							// previous explicit child patch establishes a child
							// snapshot too, without changing what the browser shows.
							e.DiffKey("child", child("0"))

							first := e.DiffKey(tc.firstKey, tc.first("1"))
							if first == nil {
								t.Fatal("expected the first patch to change the browser from 0 to 1")
							}
							if first.Key != tc.firstKey || !bytes.Equal(first.HTML, tc.first("1").RenderBytes()) {
								t.Fatalf("incorrect first patch: key=%q HTML=%q", first.Key, first.HTML)
							}

							if restore {
								data := e.Export()
								if len(data) == 0 {
									t.Fatal("seeded engine returned no snapshot to restore")
								}
								restored := engine.new()
								defer restored.Clear()
								if err := restored.Import(data); err != nil {
									t.Fatalf("restore snapshots: %v", err)
								}
								e.Clear()
								e = restored
							}

							// The browser now shows 1. Reverting through the other
							// level must return a patch even if that level's last
							// individually stored HTML was already 0.
							second := e.DiffKey(tc.secondKey, tc.second("0"))
							if second == nil {
								t.Error("correction suppressed: browser shows 1, requested state is 0")
							} else if second.Key != tc.secondKey || !bytes.Equal(second.HTML, tc.second("0").RenderBytes()) {
								t.Errorf("incorrect correction: key=%q HTML=%q", second.Key, second.HTML)
							}

							// An unrelated region must retain its valid snapshot;
							// clearing every snapshot is not a sufficient fix.
							if p := e.DiffKey("sibling", span.Static("untouched").Dynamic("sibling")); p != nil {
								t.Errorf("unchanged sibling produced a patch: %q", p.HTML)
							}
						})
					}
				})
			}
		})
	}
}
