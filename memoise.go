package jit

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"

	"github.com/jpl-au/fluent"
	"github.com/jpl-au/fluent/node"
)

// Memoiser provides an alternative to [Differ] for render trees that
// use [Memoise] nodes. It is a standalone concern - use either the
// Differ or the Memoiser, not both on the same session.
//
// When the developer opts into memoisation, every Dynamic region in
// the render tree should contain a [Memoise] child with a cache
// key. On each Diff call, the Memoiser compares memoisation keys with the
// previous render. Matching keys skip the subtree - no HTML is
// rendered and no comparison runs. With the preferred Dynamic >
// Memoise nesting the closure never runs either; when a Memoise
// wraps the Dynamic node instead, the closure still runs to produce
// the tree during the walk, but its output is not rendered. See
// docs/memoise.md for both patterns. Mismatched keys call the
// closure and render the result into a snapshot for comparison.
//
// The Memoiser does not fall back to content-based diffing for
// non-memoised Dynamic nodes. If a Dynamic node has no memoised child, it
// is always re-rendered (treated as a miss).
type Memoiser struct {
	mu          sync.Mutex
	snapshots   map[string]*bytes.Buffer
	memoiseKeys map[string]string // Dynamic key -> stringified memoisation key
	order       []string
	seeded      bool

	// lastHits and lastMisses track memoisation cache performance from the
	// most recent Diff call. Read them via Stats() after Diff returns.
	lastHits   int
	lastMisses int

	// lastSharedHits and lastSharedMisses track how many misses were
	// served from (or added to) the process-global shared cache during
	// the most recent Diff. Read them via SharedStats(). A shared hit
	// is also counted as a memoise miss - the per-session key changed,
	// but the render was still avoided by reusing another session's
	// bytes.
	lastSharedHits   int
	lastSharedMisses int
}

// NewMemoiser creates an empty Memoiser ready for use.
func NewMemoiser() *Memoiser {
	return &Memoiser{
		snapshots:   make(map[string]*bytes.Buffer),
		memoiseKeys: make(map[string]string),
	}
}

// Render writes the full HTML for the tree to w and stores snapshots
// and memoisation keys for all Dynamic regions. Use this for the
// initial page load and after structural changes detected by Diff.
// Write errors are discarded - use WriteTo to observe them.
//
// The tree renders exactly once: the walk writes the page HTML and the
// snapshot for each Dynamic region is the same bytes, so closures run
// a single time and snapshots always match the page the client
// received. A [Shared] region whose key is already in the
// process-global cache does not run its closure at all - the cached
// bytes serve both the page and the snapshot.
func (m *Memoiser) Render(root node.Node, w io.Writer) {
	_, _ = m.WriteTo(root, w)
}

// WriteTo renders the full HTML for the tree to w and stores snapshots
// and memoisation keys, returning the byte count and any write error.
// This is the render path for network writers, where the error is the
// signal that the client has gone.
func (m *Memoiser) WriteTo(root node.Node, w io.Writer) (int64, error) {
	page := fluent.NewBuffer()
	m.seedCollect(root, page)
	n, err := page.WriteTo(w)
	fluent.PutBuffer(page)
	return n, err
}

// RenderBytes returns the full HTML for the tree as a byte slice and
// stores snapshots and memoisation keys. Use it where no writer is
// involved.
func (m *Memoiser) RenderBytes(root node.Node) []byte {
	var page bytes.Buffer
	m.seedCollect(root, &page)
	return page.Bytes()
}

// seedCollect resets the memoiser's state and renders the tree once
// into page, capturing snapshots and memoisation keys along the way.
func (m *Memoiser) seedCollect(root node.Node, page *bytes.Buffer) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Zero the counters so Stats after a Render does not report a
	// previous Diff. The shared counters are repopulated by
	// renderCollect below, so SharedStats reflects this seeding pass.
	m.lastHits, m.lastMisses = 0, 0
	m.lastSharedHits, m.lastSharedMisses = 0, 0

	m.returnBuffers()
	m.snapshots = make(map[string]*bytes.Buffer)
	m.memoiseKeys = make(map[string]string)
	m.order = nil

	m.renderCollect(root, page, "", false)
	m.seeded = true
}

// Diff compares the new tree against stored snapshots using memoisation
// keys. For each Dynamic node:
//
//   - If it carries a memoisation version (chained .Memoise, or a [Memoise] child) and the version matches the
//     previous render, the subtree is skipped. No closure is called,
//     no HTML is rendered. The previous snapshot is reused.
//   - If the version differs (or the region is not memoised), the
//     region renders and the result is compared against the stored
//     snapshot.
//
// Returns (patches, nil) when Dynamic keys match between renders.
// Returns (nil, *StructuralChange) when keys were added, removed,
// or reordered. Returns (nil, nil) if Render has not been called.
func (m *Memoiser) Diff(root node.Node) ([]Patch, *StructuralChange) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.seeded {
		return nil, nil
	}

	m.lastHits = 0
	m.lastMisses = 0
	m.lastSharedHits = 0
	m.lastSharedMisses = 0

	// Collect the current order and identify misses. Hits skip
	// entirely - no buffer allocated, no render, no comparison.
	// Only misses produce fresh buffers for comparison.
	misses := make(map[string]*bytes.Buffer)
	newKeys := make(map[string]string, len(m.memoiseKeys))
	currentOrder := make([]string, 0, len(m.order))
	m.collectDiff(root, misses, newKeys, &currentOrder, "", false)

	// Structural change: leave all state untouched, matching the
	// Differ. The caller must Render next, which rebuilds everything.
	// Adopting the new order here without new snapshots would leave
	// keys in m.order with no snapshot behind them, and Export would
	// crash on the gap.
	if !slices.Equal(m.order, currentOrder) {
		for _, buf := range misses {
			fluent.PutBuffer(buf)
		}
		return nil, describeChange(m.order, currentOrder)
	}

	// Compare misses and replace snapshots in a single pass. Hits
	// are identical by definition and not in the misses map.
	// Initialise as non-nil so callers can distinguish "nothing
	// changed" (empty slice) from "unseeded" (nil).
	patches := []Patch{}
	for key, cur := range misses {
		prev := m.snapshots[key]
		if prev == nil || !bytes.Equal(cur.Bytes(), prev.Bytes()) {
			patches = append(patches, Patch{Key: key, HTML: cur.Bytes()})
		}
		if prev != nil {
			fluent.PutBuffer(prev)
		}
		m.snapshots[key] = cur
	}
	m.memoiseKeys = newKeys
	m.order = currentOrder

	return patches, nil
}

// renderCollect walks the tree for the initial Render, writing the
// page HTML into page exactly once. Every keyed Dynamic region renders
// into its snapshot buffer and those same bytes are appended to the
// page, so region content never renders twice. Between regions,
// elements render decomposed - open tag, children, close tag - which
// the generated element code guarantees is byte-identical to
// RenderBuilder; containers without markup of their own contribute
// their children via Nodes(); nodes without children render via
// RenderBuilder. A container whose Nodes() is non-empty evaluates its
// closure once here; one whose closure returns nil looks like a leaf
// and falls through to a byte-equivalent second evaluation in
// RenderBuilder, safe because closures are cheap and deterministic (see
// renderBody in diff.go for the full note).
//
// memoKey carries the stringified version from the nearest memoised
// ancestor. When a Dynamic node is reached, the ancestor version is
// used if the node carries none of its own. This allows every nesting
// pattern to work:
//
//   - element .Dynamic(key).Memoise(version) (found via findMemoise)
//   - Dynamic > Memoise child (found via findMemoise)
//   - Memoise > Dynamic (ancestor version propagated via memoKey)
func (m *Memoiser) renderCollect(n node.Node, page *bytes.Buffer, memoKey string, memoShared bool) {
	// Capture the memo version from wrapper nodes and memoised
	// elements. The version propagates to any Dynamic descendant that
	// does not carry its own.
	if memo, ok := n.(Memoised); ok {
		if mk := memoiseKeyToString(memo.MemoiseKey()); mk != "" {
			memoKey = mk
			memoShared = isSharedMemoiser(memo)
		}
	}

	if d, ok := n.(node.Dynamic); ok {
		key := d.DynamicKey()
		if key != "" {
			// Prefer a direct child Memoiser (Dynamic > Memoise
			// pattern). Fall back to the ancestor key (Memoise >
			// Dynamic pattern).
			mk, shared := findMemoise(n)
			if mk == "" {
				mk, shared = memoKey, memoShared
			}

			var buf *bytes.Buffer
			if mk != "" && shared {
				buf = m.renderShared(n, mk)
			} else {
				if mk != "" {
					if el, ok := n.(node.Element); ok {
						node.SetData(el, "fluent-memoise", mk)
					}
				}
				buf = fluent.NewBuffer(SnapshotHint)
				n.RenderBuilder(buf)
			}
			// The snapshot bytes are also the page bytes.
			page.Write(buf.Bytes())
			// A duplicate key is invalid input, but the buffer it
			// already holds must go back to the pool before being
			// overwritten or it is lost to the pool entirely.
			if prior, ok := m.snapshots[key]; ok {
				fluent.PutBuffer(prior)
			}
			m.snapshots[key] = buf
			m.order = append(m.order, key)
			if mk != "" {
				m.memoiseKeys[key] = mk
			}
			return
		}
	}

	if el, ok := n.(node.Element); ok {
		el.RenderOpen(page)
		for _, child := range n.Nodes() {
			if child != nil {
				m.renderCollect(child, page, memoKey, memoShared)
			}
		}
		el.RenderClose(page)
		return
	}
	if children := n.Nodes(); len(children) > 0 {
		for _, child := range children {
			if child != nil {
				m.renderCollect(child, page, memoKey, memoShared)
			}
		}
		return
	}
	n.RenderBuilder(page)
}

// collectDiff walks the tree for a Diff call. For each Dynamic node,
// it checks whether the memoisation key matches the previous render. Hits
// are skipped entirely - no buffer allocated, no render. Misses are
// rendered into fresh buffers in the misses map.
//
// memoKey carries the stringified version from the nearest memoised
// ancestor, matching the propagation in renderCollect.
func (m *Memoiser) collectDiff(n node.Node, misses map[string]*bytes.Buffer, keys map[string]string, order *[]string, memoKey string, memoShared bool) {
	// Capture the memo version from wrapper nodes and memoised elements.
	if memo, ok := n.(Memoised); ok {
		if mk := memoiseKeyToString(memo.MemoiseKey()); mk != "" {
			memoKey = mk
			memoShared = isSharedMemoiser(memo)
		}
	}

	if d, ok := n.(node.Dynamic); ok {
		key := d.DynamicKey()
		if key != "" {
			*order = append(*order, key)

			// Prefer direct child Memoiser, fall back to ancestor.
			mk, shared := findMemoise(n)
			if mk == "" {
				mk, shared = memoKey, memoShared
			}
			if mk != "" {
				keys[key] = mk
				// Hit: same key as previous render. Skip entirely.
				if prev, exists := m.memoiseKeys[key]; exists && prev == mk {
					m.lastHits++
					return
				}
			}

			// Miss: the per-session key changed. A shared region can
			// still avoid the render by reusing another session's bytes
			// from the process-global cache.
			m.lastMisses++
			// A duplicate key is invalid input, but the buffer it
			// already holds must go back to the pool before being
			// overwritten or it is lost to the pool entirely.
			if prior, ok := misses[key]; ok {
				fluent.PutBuffer(prior)
			}
			if mk != "" && shared {
				misses[key] = m.renderShared(n, mk)
				return
			}
			if mk != "" {
				if el, ok := n.(node.Element); ok {
					node.SetData(el, "fluent-memoise", mk)
				}
			}
			buf := fluent.NewBuffer(SnapshotHint)
			n.RenderBuilder(buf)
			misses[key] = buf
			return
		}
	}
	for _, child := range n.Nodes() {
		if child != nil {
			m.collectDiff(child, misses, keys, order, memoKey, memoShared)
		}
	}
}

// findMemoise returns the memoisation version governing a Dynamic
// node, preferring the node's own chained .Memoise(version) over a
// [Memoise] child, plus whether the region opts into cross-session
// sharing. The version conversion uses type-switched strconv for
// common types (zero reflection, zero allocation for string keys).
// Returns ("", false) when neither the node nor a child is memoised.
func findMemoise(n node.Node) (key string, shared bool) {
	// The element itself may carry the version - every generated
	// element satisfies Memoised, with a nil version when unset.
	if memo, ok := n.(Memoised); ok {
		if mk := memoiseKeyToString(memo.MemoiseKey()); mk != "" {
			return mk, isSharedMemoiser(memo)
		}
	}
	for _, child := range n.Nodes() {
		if memo, ok := child.(Memoised); ok {
			if mk := memoiseKeyToString(memo.MemoiseKey()); mk != "" {
				return mk, isSharedMemoiser(memo)
			}
		}
	}
	return "", false
}

// isSharedMemoiser reports whether a memoised node opts into
// cross-session sharing. Plain [Memoise] nodes and memoised elements
// do not implement [SharedMemoised] and so are never shared.
func isSharedMemoiser(memo Memoised) bool {
	sm, ok := memo.(SharedMemoised)
	return ok && sm.MemoiseShared()
}

// renderShared returns the rendered bytes for a shared Dynamic region,
// serving them from the process-global cache when another session has
// already rendered the same key. The returned buffer is pooled and
// becomes the caller's snapshot; the cache keeps its own immutable copy.
// A cache hit skips the closure entirely - the whole point of sharing.
func (m *Memoiser) renderShared(n node.Node, mk string) *bytes.Buffer {
	// Mark the live element before the cache lookup, not only on the
	// miss path. On a hit the cached bytes already carry the attribute,
	// but the live tree must agree with them in case the caller renders
	// the same tree again through other means.
	if el, ok := n.(node.Element); ok {
		node.SetData(el, "fluent-memoise", mk)
	}

	if cached, ok := sharedCache.get(mk); ok {
		m.lastSharedHits++
		buf := fluent.NewBuffer(len(cached))
		buf.Write(cached)
		return buf
	}

	m.lastSharedMisses++
	buf := fluent.NewBuffer(SnapshotHint)
	n.RenderBuilder(buf)
	sharedCache.put(mk, buf.Bytes())
	return buf
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

// DiffKey re-renders a single Dynamic key against the stored snapshot
// and returns a patch if the content changed. Use this for targeted
// updates where the caller knows exactly which key changed. Does not
// check memoisation keys because the developer is explicitly targeting this
// key. The snapshot is updated so subsequent Diff calls see the new
// content.
func (m *Memoiser) DiffKey(key string, subtree node.Node) *Patch {
	m.mu.Lock()
	defer m.mu.Unlock()

	prev := m.snapshots[key]

	buf := fluent.NewBuffer(SnapshotHint)
	subtree.RenderBuilder(buf)

	if prev != nil && bytes.Equal(buf.Bytes(), prev.Bytes()) {
		fluent.PutBuffer(buf)
		return nil
	}

	patch := &Patch{Key: key, HTML: buf.Bytes()}

	if prev != nil {
		fluent.PutBuffer(prev)
	}
	m.snapshots[key] = buf

	return patch
}

// Stats returns the hit and miss counts from the most recent Diff
// call. A hit means the memoisation key matched and the subtree was skipped.
// A miss means the key differed (or was absent) and the subtree was
// re-rendered. Call this immediately after Diff to inspect cache
// performance.
func (m *Memoiser) Stats() (hits, misses int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastHits, m.lastMisses
}

// Memoised reports how many Dynamic regions carried a memoisation key
// in the most recent Diff. Zero means the render tree used no
// [Memoise] regions at all - the Memoiser is running but has
// nothing to skip, degrading to plain diff behaviour. Callers use this
// to detect a Memoise-enabled handler whose render forgot the keys.
func (m *Memoiser) Memoised() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.memoiseKeys)
}

// SharedStats returns how many shared regions in the most recent Diff
// (or seeding Render) were resolved through the process-global shared
// cache: hits reused another session's rendered bytes, misses rendered
// fresh and populated the cache for others. After a Diff both counts
// are a subset of the miss count from [Memoiser.Stats] - a shared
// region only reaches the cache when its per-session key changed.
// Call immediately after Diff or Render.
func (m *Memoiser) SharedStats() (hits, misses int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastSharedHits, m.lastSharedMisses
}

// returnBuffers returns all stored snapshot buffers to the pool.
func (m *Memoiser) returnBuffers() {
	for _, buf := range m.snapshots {
		fluent.PutBuffer(buf)
	}
}

// Export returns the Memoiser's snapshot and memoisation key data as raw
// bytes suitable for external storage. Returns nil if unseeded.
func (m *Memoiser) Export() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.seeded {
		return nil
	}

	var buf bytes.Buffer
	buf.WriteByte(exportVersion)

	// Snapshots: count, then (keyLen, key, valLen, val) pairs.
	binary.Write(&buf, binary.LittleEndian, uint32(len(m.order)))
	for _, key := range m.order {
		binary.Write(&buf, binary.LittleEndian, uint32(len(key)))
		buf.WriteString(key)
		snap := m.snapshots[key]
		binary.Write(&buf, binary.LittleEndian, uint32(snap.Len()))
		buf.Write(snap.Bytes())
	}

	// Memoisation keys: count, then (keyLen, key, valLen, val) pairs.
	// Values are already strings (converted once on entry via
	// memoiseKeyToString).
	binary.Write(&buf, binary.LittleEndian, uint32(len(m.memoiseKeys)))
	for k, v := range m.memoiseKeys {
		binary.Write(&buf, binary.LittleEndian, uint32(len(k)))
		buf.WriteString(k)
		binary.Write(&buf, binary.LittleEndian, uint32(len(v)))
		buf.WriteString(v)
	}

	return buf.Bytes()
}

// Import restores snapshot and memoisation key data from a prior Export.
func (m *Memoiser) Import(data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	r := bytes.NewReader(data)

	version, err := r.ReadByte()
	if err != nil {
		return fmt.Errorf("jit: memoiser import: reading version: %w", err)
	}
	if version != exportVersion {
		return fmt.Errorf("jit: memoiser import: unsupported export version %d (want %d)", version, exportVersion)
	}

	// Read snapshots.
	var count uint32
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return fmt.Errorf("jit: memoiser import: reading snapshot count: %w", err)
	}

	// Corrupt data must fail with an error, not an enormous allocation.
	// Each entry needs at least 8 bytes of length prefixes, so a count
	// the remaining data cannot hold is rejected before preallocating.
	if int64(count) > int64(r.Len())/8 {
		return fmt.Errorf("jit: memoiser import: snapshot count %d exceeds data size %d", count, r.Len())
	}

	snapshots := make(map[string]*bytes.Buffer, count)
	order := make([]string, 0, count)

	returnParsed := func() {
		for _, buf := range snapshots {
			fluent.PutBuffer(buf)
		}
	}

	for range count {
		var keyLen uint32
		if err := binary.Read(r, binary.LittleEndian, &keyLen); err != nil {
			returnParsed()
			return fmt.Errorf("jit: memoiser import: reading key length: %w", err)
		}
		// Reject lengths the remaining data cannot hold before
		// allocating for them - see the count check above.
		if int64(keyLen) > int64(r.Len()) {
			returnParsed()
			return fmt.Errorf("jit: memoiser import: key length %d exceeds remaining data %d", keyLen, r.Len())
		}
		keyBytes := make([]byte, keyLen)
		if _, err := io.ReadFull(r, keyBytes); err != nil {
			returnParsed()
			return fmt.Errorf("jit: memoiser import: reading key: %w", err)
		}
		key := string(keyBytes)

		var valLen uint32
		if err := binary.Read(r, binary.LittleEndian, &valLen); err != nil {
			returnParsed()
			return fmt.Errorf("jit: memoiser import: reading value length: %w", err)
		}
		if int64(valLen) > int64(r.Len()) {
			returnParsed()
			return fmt.Errorf("jit: memoiser import: value length %d exceeds remaining data %d", valLen, r.Len())
		}

		buf := fluent.NewBuffer(int(valLen))
		if _, err := io.CopyN(buf, r, int64(valLen)); err != nil {
			fluent.PutBuffer(buf)
			returnParsed()
			return fmt.Errorf("jit: memoiser import: reading value: %w", err)
		}

		snapshots[key] = buf
		order = append(order, key)
	}

	// Read memoisation keys if present. This section is tolerant of
	// truncation for compatibility with exports that predate it - keys
	// lost here just mean the next Diff treats those regions as misses.
	// The length checks still apply so corrupt data cannot demand a
	// huge allocation.
	memoiseKeys := make(map[string]string)
	var memoiseCount uint32
	if err := binary.Read(r, binary.LittleEndian, &memoiseCount); err == nil {
		for range memoiseCount {
			var kLen uint32
			if err := binary.Read(r, binary.LittleEndian, &kLen); err != nil {
				break
			}
			if int64(kLen) > int64(r.Len()) {
				break
			}
			kBytes := make([]byte, kLen)
			if _, err := io.ReadFull(r, kBytes); err != nil {
				break
			}
			var vLen uint32
			if err := binary.Read(r, binary.LittleEndian, &vLen); err != nil {
				break
			}
			if int64(vLen) > int64(r.Len()) {
				break
			}
			vBytes := make([]byte, vLen)
			if _, err := io.ReadFull(r, vBytes); err != nil {
				break
			}
			memoiseKeys[string(kBytes)] = string(vBytes)
		}
	}

	m.returnBuffers()
	m.snapshots = snapshots
	m.order = order
	m.memoiseKeys = memoiseKeys
	m.seeded = true
	return nil
}

// Clear releases snapshot buffers and resets all state.
func (m *Memoiser) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.returnBuffers()
	m.snapshots = make(map[string]*bytes.Buffer)
	m.memoiseKeys = make(map[string]string)
	m.order = nil
	m.seeded = false
	m.lastHits, m.lastMisses = 0, 0
	m.lastSharedHits, m.lastSharedMisses = 0, 0
}

// Validate checks a tree for duplicate dynamic keys.
//
// Validate walks the tree, evaluating any Func closures; validate-
// then-render therefore evaluates closures twice. Intended for
// startup and tests, not per-request use.
func (m *Memoiser) Validate(root node.Node) error {
	seen := make(map[string]bool)
	return validateKeys(root, seen)
}
