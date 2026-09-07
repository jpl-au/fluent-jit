package jit

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/jpl-au/fluent"
	"github.com/jpl-au/fluent/node"
)

// Memoiser tracks Dynamic regions like Differ, with version-based rendering
// skips. A matching Memoise version reuses the whole region and its nested
// snapshots. Regions without a version render and compare on every Diff.
// Shared additionally reuses a region across sessions. Use one engine per session.
type Memoiser struct {
	mu                               sync.Mutex
	tree                             regionTree
	lastHits, lastMisses             int
	lastSharedHits, lastSharedMisses int
}

// NewMemoiser creates an empty Memoiser ready for use.
func NewMemoiser() *Memoiser { return &Memoiser{tree: regionTree{}} }

// Render writes the full page and seeds snapshots in one rendering pass.
// Shared cache hits skip their closures. Write errors are discarded; use WriteTo.
func (m *Memoiser) Render(root node.Node, w io.Writer) { _, _ = m.WriteTo(root, w) }

// WriteTo renders and seeds snapshots, returning the byte count and write error.
func (m *Memoiser) WriteTo(root node.Node, w io.Writer) (int64, error) {
	page := fluent.NewBuffer()
	m.seed(root, page)
	n, err := page.WriteTo(w)
	fluent.PutBuffer(page)
	return n, err
}

// RenderBytes returns the full page and seeds snapshots from those same bytes.
func (m *Memoiser) RenderBytes(root node.Node) []byte {
	var page bytes.Buffer
	m.seed(root, &page)
	return page.Bytes()
}

func (m *Memoiser) seed(root node.Node, page *bytes.Buffer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetStats()
	w := newRegionRenderer(nil, m)
	w.walk(root, page, nil, "", memoVersion{}, false)
	m.tree.releaseExcept(nil)
	m.tree = w.next
	m.tree.seeded = true
}

// Diff compares Dynamic regions, skipping those whose memoisation version
// matches the stored version. A parent cache hit skips its entire subtree.
// An empty, non-nil patch slice means nothing changed; (nil, nil) means unseeded.
// Patch selection follows Differ, including widening patches for moves between
// containers. A StructuralChange leaves the baseline intact and requires Render.
func (m *Memoiser) Diff(root node.Node) ([]Patch, *StructuralChange) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.tree.seeded {
		return nil, nil
	}
	m.resetStats()
	w := newRegionRenderer(&m.tree, m)
	w.walk(root, nil, nil, "", memoVersion{}, false)
	return m.tree.diff(w.next)
}

// DiffKey renders a region unconditionally, bypassing memoisation. It refreshes
// nested snapshots and the enclosing HTML baseline, and invalidates enclosing
// memoisation versions so the next Diff can reconcile the explicit update.
func (m *Memoiser) DiffKey(key string, subtree node.Node) *Patch {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tree.diffKey(key, subtree)
}

// Stats returns the hit and miss counts from the most recent Diff. A parent hit
// counts once: its descendants are not visited. Render and Clear reset these.
func (m *Memoiser) Stats() (hits, misses int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastHits, m.lastMisses
}

// SharedStats reports regions resolved through the process-global Shared cache
// during the most recent Diff or Render. During Diff these are session misses.
func (m *Memoiser) SharedStats() (hits, misses int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastSharedHits, m.lastSharedMisses
}

// Memoised returns the number of stored Dynamic regions with a cache version.
func (m *Memoiser) Memoised() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, r := range m.tree.regions {
		if r.version != "" {
			count++
		}
	}
	return count
}

func (m *Memoiser) resetStats() {
	m.lastHits, m.lastMisses = 0, 0
	m.lastSharedHits, m.lastSharedMisses = 0, 0
}

// Export returns an opaque snapshot blob including nesting and memoisation
// versions, or nil before Render. Clear releases the snapshots after saving.
func (m *Memoiser) Export() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tree.export()
}

// Import restores a snapshot blob. An import error leaves the current baseline
// intact.
func (m *Memoiser) Import(data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tree, err := decodeRegions(data)
	if err != nil {
		return err
	}
	m.tree.releaseExcept(nil)
	m.tree = tree
	return nil
}

// Clear releases pooled snapshots and resets memoisation statistics.
func (m *Memoiser) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tree.releaseExcept(nil)
	m.tree = regionTree{}
	m.resetStats()
}

// Validate checks for duplicate Dynamic keys and whitespace in their ids.
func (m *Memoiser) Validate(root node.Node) error {
	return validateKeys(root, make(map[string]bool))
}

func isSharedMemoiser(memo Memoised) bool {
	sm, ok := memo.(SharedMemoised)
	return ok && sm.MemoiseShared()
}

// memoiseKeyToString converts a memoisation key to a string using fast paths
// for common types. Only the fallback uses fmt.Sprint; the common
// cases (string, int, bool) use strconv with no reflection.
func memoiseKeyToString(v any) string {
	switch k := v.(type) {
	case nil:
		// Every generated element satisfies Memoised; nil is the
		// "not memoised" default and must never become a version.
		return ""
	case string:
		return k
	case int:
		return strconv.Itoa(k)
	case int64:
		return strconv.FormatInt(k, 10)
	case int32:
		return strconv.FormatInt(int64(k), 10)
	case uint:
		return strconv.FormatUint(uint64(k), 10)
	case uint64:
		return strconv.FormatUint(k, 10)
	case bool:
		return strconv.FormatBool(k)
	case float64:
		return strconv.FormatFloat(k, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(k), 'f', -1, 32)
	default:
		return fmt.Sprint(v)
	}
}
