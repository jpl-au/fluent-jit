package jit

import (
	"bytes"
	"slices"
	"sync"

	"github.com/jpl-au/fluent"
	"github.com/jpl-au/fluent/node"
)

// A region owns its HTML and the byte ranges of its immediate Dynamic children.
// Ranges let a targeted child replacement advance its ancestors' snapshots
// without evaluating any of their render closures.
type region struct {
	html     *bytes.Buffer
	children []regionChild
	version  string
}

type regionChild struct {
	key        string
	start, end int
}

// Region metadata follows the same lifecycle as its pooled HTML buffer. Reuse
// it too, so flat pages do not acquire an allocation per key on every Diff.
var regionPool = sync.Pool{New: func() any { return new(region) }}

func takeRegion(size int, version string) *region {
	r := regionPool.Get().(*region)
	r.html = fluent.NewBuffer(size)
	r.version = version
	return r
}

func putRegion(r *region) {
	fluent.PutBuffer(r.html)
	r.html, r.version = nil, ""
	clear(r.children)
	if cap(r.children) > 1024 {
		r.children = nil
	} else {
		r.children = r.children[:0]
	}
	regionPool.Put(r)
}

type regionTree struct {
	regions map[string]*region
	parents map[string]string
	roots   []string
	seeded  bool
}

// releaseExcept releases only buffers not retained by the next tree. Cache hits
// share immutable region pointers until the staged diff has been accepted.
func (t *regionTree) releaseExcept(next *regionTree) {
	for key, r := range t.regions {
		if next == nil || next.regions[key] != r {
			putRegion(r)
		}
	}
}

type memoVersion struct {
	key    string
	shared bool
}

func nodeVersion(n node.Node) memoVersion {
	if m, ok := n.(Memoised); ok {
		return memoVersion{memoiseKeyToString(m.MemoiseKey()), isSharedMemoiser(m)}
	}
	return memoVersion{}
}

type regionRenderer struct {
	next regionTree
	prev *regionTree
	memo *Memoiser
}

func newRegionRenderer(prev *regionTree, memo *Memoiser) regionRenderer {
	w := regionRenderer{next: regionTree{}, prev: prev, memo: memo}
	if prev != nil {
		w.next.regions = make(map[string]*region, len(prev.regions))
		w.next.roots = make([]string, 0, len(prev.roots))
	}
	return w
}

func (w *regionRenderer) put(key string, r *region) {
	if prior := w.next.regions[key]; prior != nil && prior != r && (w.prev == nil || w.prev.regions[key] != prior) {
		putRegion(prior)
	}
	if w.next.regions == nil {
		w.next.regions = make(map[string]*region)
	}
	w.next.regions[key] = r
}

func (t *regionTree) setParent(key, parent string) {
	if t.parents == nil {
		t.parents = make(map[string]string)
	}
	t.parents[key] = parent
}

// reuse also installs descendants, so a skipped parent still supports DiffKey
// on any nested key. Nothing in the retained subtree is rendered or copied.
func (w *regionRenderer) reuse(key string, from *regionTree) {
	r := from.regions[key]
	if w.next.regions[key] == r {
		return
	}
	w.put(key, r)
	for _, child := range r.children {
		w.next.setParent(child.key, key)
		w.reuse(child.key, from)
	}
}

// walk renders only inside Dynamic regions when page is nil (the Diff path).
// Children are materialised once and reused for both version discovery and
// rendering. A direct Memoise child supplies the enclosing region's version;
// that version is consumed there, rather than also memoising every nested key.
func (w *regionRenderer) walk(n node.Node, page *bytes.Buffer, parent *region, parentKey string, inherited memoVersion, suppressVersion bool) {
	if n == nil {
		return
	}
	version := inherited
	if w.memo != nil && !suppressVersion {
		if own := nodeVersion(n); own.key != "" {
			version = own
		}
	}
	key := trackedKey(n)
	children := n.Nodes()
	if key == "" {
		w.body(n, children, page, parent, parentKey, version, -1)
		return
	}

	consumed := -1
	if w.memo != nil && nodeVersion(n).key == "" {
		for i, child := range children {
			// A keyed child owns its version. Promoting it to the parent
			// would let one unchanged child hide changes to its siblings.
			if trackedKey(child) != "" {
				continue
			}
			if v := nodeVersion(child); v.key != "" {
				version, consumed = v, i
				break
			}
		}
	}
	var r *region
	if w.memo != nil && version.key != "" {
		if el, ok := n.(node.Element); ok {
			node.SetData(el, "fluent-memoise", version.key)
		}
		if w.prev != nil {
			if old := w.prev.regions[key]; old != nil && old.version == version.key {
				w.memo.lastHits++
				w.reuse(key, w.prev)
				r = old
			}
		}
	}
	if r == nil {
		if w.memo != nil && w.prev != nil {
			w.memo.lastMisses++
		}
		if w.memo != nil && version.key != "" && version.shared {
			if data, ok := sharedCache.get(version.key); ok {
				if cached, err := decodeRegions(data); err == nil {
					if len(cached.roots) == 1 && cached.roots[0] == key {
						w.memo.lastSharedHits++
						w.reuse(key, &cached)
						r = w.next.regions[key]
					} else {
						cached.releaseExcept(nil)
					}
				}
			}
			if r == nil {
				w.memo.lastSharedMisses++
			}
		}
		if r == nil {
			r = takeRegion(SnapshotHint, version.key)
			w.body(n, children, r.html, r, key, memoVersion{}, consumed)
			w.put(key, r)
			if w.memo != nil && version.key != "" && version.shared {
				sharedCache.put(version.key, w.next.encode([]string{key}, false))
			}
		}
	}
	w.attach(key, r, page, parent, parentKey)
}

func (w *regionRenderer) attach(key string, r *region, page *bytes.Buffer, parent *region, parentKey string) {
	if parent != nil {
		start := page.Len()
		page.Write(r.html.Bytes())
		parent.children = append(parent.children, regionChild{key, start, page.Len()})
		w.next.setParent(key, parentKey)
	} else {
		w.next.roots = append(w.next.roots, key)
		delete(w.next.parents, key)
		if page != nil {
			page.Write(r.html.Bytes())
		}
	}
}

func (w *regionRenderer) body(n node.Node, children []node.Node, page *bytes.Buffer, parent *region, parentKey string, inherited memoVersion, consumed int) {
	el, element := n.(node.Element)
	if element && page != nil {
		el.RenderOpen(page)
	}
	for i, child := range children {
		w.walk(child, page, parent, parentKey, inherited, i == consumed)
	}
	if page == nil {
		return
	}
	if element {
		el.RenderClose(page)
	} else if len(children) == 0 {
		n.RenderBuilder(page)
	}
}

func (t *regionTree) diff(next regionTree) ([]Patch, *StructuralChange) {
	// Only changes without a surviving keyed container require a full render.
	if !slices.Equal(t.roots, next.roots) {
		next.releaseExcept(t)
		return nil, describeChange(t.roots, next.roots)
	}
	// A key moved between containers must stay within one morph operation.
	// Removing it in one patch and recreating it in another loses DOM identity
	// and client-owned state. Widen those patches to a shared keyed ancestor.
	var movedContainers map[string]bool
	for key, parent := range next.parents {
		if t.regions[key] == nil || t.parents[key] == parent {
			continue
		}
		common := t.commonAncestor(t.parents[key], parent, &next)
		if common == "" {
			next.releaseExcept(t)
			return nil, &StructuralChange{Reordered: true}
		}
		if movedContainers == nil {
			movedContainers = make(map[string]bool)
		}
		movedContainers[common] = true
	}
	patches := []Patch{}
	seen := make(map[string]bool, len(next.regions))
	var selectPatches func(string)
	selectPatches = func(key string) {
		if seen[key] {
			return
		}
		seen[key] = true
		cur, prev := next.regions[key], t.regions[key]
		if cur == prev || (prev != nil && bytes.Equal(cur.html.Bytes(), prev.html.Bytes())) {
			return
		}
		if prev == nil || movedContainers[key] || !sameRegionShell(prev, cur) {
			// This patch includes every descendant, so none is emitted separately.
			patches = append(patches, Patch{key, cur.html.Bytes()})
			return
		}
		for _, child := range cur.children {
			selectPatches(child.key)
		}
	}
	for _, key := range next.roots {
		selectPatches(key)
	}
	t.releaseExcept(&next)
	next.seeded = true
	*t = next
	return patches, nil
}

func (t *regionTree) commonAncestor(oldParent, newParent string, next *regionTree) string {
	ancestors := make(map[string]bool)
	for i := 0; oldParent != "" && i < len(t.regions); i++ {
		ancestors[oldParent] = true
		oldParent = t.parents[oldParent]
	}
	for i := 0; newParent != "" && i < len(next.regions); i++ {
		if ancestors[newParent] {
			return newParent
		}
		newParent = next.parents[newParent]
	}
	return ""
}

// Compare the HTML between child regions, preserving the position of each
// gap. If membership, order, attributes or unkeyed content changed, replacing
// children alone cannot reproduce the new parent HTML.
func sameRegionShell(a, b *region) bool {
	if len(a.children) != len(b.children) {
		return false
	}
	aEnd, bEnd := 0, 0
	for i, ac := range a.children {
		bc := b.children[i]
		if ac.key != bc.key || !bytes.Equal(a.html.Bytes()[aEnd:ac.start], b.html.Bytes()[bEnd:bc.start]) {
			return false
		}
		aEnd, bEnd = ac.end, bc.end
	}
	return bytes.Equal(a.html.Bytes()[aEnd:], b.html.Bytes()[bEnd:])
}

// diffKey renders the supplied subtree unconditionally, including nested keys.
// Its outer node need not carry Dynamic: the explicit key is authoritative.
func (t *regionTree) diffKey(key string, subtree node.Node) *Patch {
	w := newRegionRenderer(nil, nil)
	r := takeRegion(SnapshotHint, "")
	w.body(subtree, subtree.Nodes(), r.html, r, key, memoVersion{}, -1)
	prev := t.regions[key]
	if prev != nil && bytes.Equal(prev.html.Bytes(), r.html.Bytes()) {
		w.next.releaseExcept(nil)
		putRegion(r)
		return nil
	}

	// Advance every enclosing snapshot by splicing the child's exact range.
	// These regions are exclusively owned by this session after the last diff.
	childKey, childHTML := key, r.html.Bytes()
	for parentKey := t.parents[key]; parentKey != ""; parentKey = t.parents[parentKey] {
		old := t.regions[parentKey]
		updated := fluent.NewBuffer(old.html.Len())
		for i, child := range old.children {
			if child.key != childKey {
				continue
			}
			updated.Write(old.html.Bytes()[:child.start])
			updated.Write(childHTML)
			updated.Write(old.html.Bytes()[child.end:])
			delta := len(childHTML) - (child.end - child.start)
			old.children[i].end += delta
			for j := i + 1; j < len(old.children); j++ {
				old.children[j].start += delta
				old.children[j].end += delta
			}
			break
		}
		fluent.PutBuffer(old.html)
		old.html, old.version = updated, ""
		childKey, childHTML = parentKey, updated.Bytes()
	}

	parentKey := t.parents[key]
	t.remove(key)
	if t.regions == nil {
		t.regions = make(map[string]*region)
	}
	t.regions[key] = r
	for k, current := range w.next.regions {
		t.regions[k] = current
	}
	for k, parent := range w.next.parents {
		t.setParent(k, parent)
	}
	if parentKey != "" {
		t.setParent(key, parentKey)
	}
	return &Patch{key, r.html.Bytes()}
}

func (t *regionTree) remove(key string) {
	r := t.regions[key]
	if r == nil {
		return
	}
	delete(t.regions, key)
	delete(t.parents, key)
	for _, child := range r.children {
		t.remove(child.key)
	}
	putRegion(r)
}
