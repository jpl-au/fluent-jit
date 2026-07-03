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
// use [node.Memoise] nodes. It is a standalone concern - use either the
// Differ or the Memoiser, not both on the same session.
//
// When the developer opts into memoisation, every Dynamic region in
// the render tree should contain a [node.Memoise] child with a cache
// key. On each Diff call, the Memoiser compares memoisation keys with the
// previous render. Matching keys skip the subtree entirely - the
// closure never runs and no HTML is rendered. Mismatched keys call
// the closure and render the result into a snapshot for comparison.
//
// The Memoiser does not fall back to content-based diffing for
// non-memoised Dynamic nodes. If a Dynamic node has no memoised child, it
// is always re-rendered (treated as a miss). This keeps the
// implementation simple and fast - there is no tree walking via
// Nodes() that would materialise closures.
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

// Render produces the full HTML for the tree and stores snapshots
// and memoisation keys for all Dynamic regions. Use this for the initial
// page load and after structural changes detected by Diff.
func (m *Memoiser) Render(root node.Node, w ...io.Writer) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.returnBuffers()
	m.snapshots = make(map[string]*bytes.Buffer)
	m.memoiseKeys = make(map[string]string)
	m.order = nil
	m.collectAll(root, "", false)
	m.seeded = true

	return root.Render(w...)
}

// Diff compares the new tree against stored snapshots using memoisation
// keys. For each Dynamic node:
//
//   - If its child satisfies [node.Memoiser] and the key matches the
//     previous render, the subtree is skipped. No closure is called,
//     no HTML is rendered. The previous snapshot is reused.
//   - If the key differs (or there is no memoised child), [node.Memoiser].MemoiseRender
//     is called (or the node is rendered directly) and the result is
//     compared against the stored snapshot.
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

	if !slices.Equal(m.order, currentOrder) {
		for _, buf := range misses {
			fluent.PutBuffer(buf)
		}
		change := describeChange(m.order, currentOrder)
		m.memoiseKeys = newKeys
		m.order = currentOrder
		return nil, change
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

// collectAll walks the tree for the initial Render. Every Dynamic
// node is rendered and its memoisation key (if any) is recorded. This
// establishes the baseline for subsequent Diff calls.
//
// memoKey carries the stringified key from the nearest ancestor
// node.Memoiser. When a Dynamic node is reached, this ancestor key
// is used if no direct child Memoiser is found. This allows both
// nesting patterns to work:
//
//   - Dynamic > Memoise (child key found via findMemoiseKeyStr)
//   - Memoise > Dynamic (ancestor key propagated via memoKey)
func (m *Memoiser) collectAll(n node.Node, memoKey string, memoShared bool) {
	// Capture memo key from wrapper Memoiser nodes. The key
	// propagates to any Dynamic descendant that does not have
	// its own direct Memoiser child.
	if memo, ok := n.(node.Memoiser); ok {
		if mk := memoiseKeyToString(memo.MemoiseKey()); mk != "" {
			memoKey = mk
			memoShared = isSharedMemoiser(memo)
		}
	}

	if d, ok := n.(node.Dynamic); ok {
		key := d.DynamicKey()
		if key != "" && key != "_" {
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
						el.SetAttribute("data-tether-memoise", mk)
					}
				}
				buf = fluent.NewBuffer(SnapshotHint)
				n.RenderBuilder(buf)
			}
			m.snapshots[key] = buf
			m.order = append(m.order, key)
			if mk != "" {
				m.memoiseKeys[key] = mk
			}
			return
		}
	}
	for _, child := range n.Nodes() {
		if child != nil {
			m.collectAll(child, memoKey, memoShared)
		}
	}
}

// collectDiff walks the tree for a Diff call. For each Dynamic node,
// it checks whether the memoisation key matches the previous render. Hits
// are skipped entirely - no buffer allocated, no render. Misses are
// rendered into fresh buffers in the misses map.
//
// memoKey carries the stringified key from the nearest ancestor
// node.Memoiser, matching the propagation in collectAll.
func (m *Memoiser) collectDiff(n node.Node, misses map[string]*bytes.Buffer, keys map[string]string, order *[]string, memoKey string, memoShared bool) {
	// Capture memo key from wrapper Memoiser nodes.
	if memo, ok := n.(node.Memoiser); ok {
		if mk := memoiseKeyToString(memo.MemoiseKey()); mk != "" {
			memoKey = mk
			memoShared = isSharedMemoiser(memo)
		}
	}

	if d, ok := n.(node.Dynamic); ok {
		key := d.DynamicKey()
		if key != "" && key != "_" {
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
			if mk != "" && shared {
				misses[key] = m.renderShared(n, mk)
				return
			}
			if mk != "" {
				if el, ok := n.(node.Element); ok {
					el.SetAttribute("data-tether-memoise", mk)
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

// findMemoise checks the immediate children of a node for a
// [node.Memoiser] and returns its key as a string, plus whether that
// child opts into cross-session sharing ([node.Shared]). The key
// conversion uses type-switched strconv for common types (zero
// reflection, zero allocation for string keys). Returns ("", false)
// if no memoised child is found.
func findMemoise(n node.Node) (key string, shared bool) {
	for _, child := range n.Nodes() {
		if memo, ok := child.(node.Memoiser); ok {
			return memoiseKeyToString(memo.MemoiseKey()), isSharedMemoiser(memo)
		}
	}
	return "", false
}

// isSharedMemoiser reports whether a memoised node opts into
// cross-session sharing. Nodes created with [node.Memoise] do not
// implement [node.SharedMemoiser] and so are never shared.
func isSharedMemoiser(memo node.Memoiser) bool {
	sm, ok := memo.(node.SharedMemoiser)
	return ok && sm.MemoiseShared()
}

// renderShared returns the rendered bytes for a shared Dynamic region,
// serving them from the process-global cache when another session has
// already rendered the same key. The returned buffer is pooled and
// becomes the caller's snapshot; the cache keeps its own immutable copy.
// A cache hit skips the closure entirely - the whole point of sharing.
func (m *Memoiser) renderShared(n node.Node, mk string) *bytes.Buffer {
	if cached, ok := sharedCache.get(mk); ok {
		m.lastSharedHits++
		buf := fluent.NewBuffer(len(cached))
		buf.Write(cached)
		return buf
	}

	m.lastSharedMisses++
	if el, ok := n.(node.Element); ok {
		el.SetAttribute("data-tether-memoise", mk)
	}
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

// SharedStats returns how many memoise misses in the most recent Diff
// were resolved through the process-global shared cache: hits reused
// another session's rendered bytes, misses rendered fresh and populated
// the cache for others. Both counts are a subset of the miss count from
// [Memoiser.Stats] - a shared region only reaches the cache when its
// per-session key changed. Call immediately after Diff.
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

	// Read snapshots.
	var count uint32
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return fmt.Errorf("jit: memoiser import: reading snapshot count: %w", err)
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

		buf := fluent.NewBuffer(int(valLen))
		if _, err := io.CopyN(buf, r, int64(valLen)); err != nil {
			fluent.PutBuffer(buf)
			returnParsed()
			return fmt.Errorf("jit: memoiser import: reading value: %w", err)
		}

		snapshots[key] = buf
		order = append(order, key)
	}

	// Read memoisation keys if present.
	memoiseKeys := make(map[string]string)
	var memoiseCount uint32
	if err := binary.Read(r, binary.LittleEndian, &memoiseCount); err == nil {
		for range memoiseCount {
			var kLen uint32
			if err := binary.Read(r, binary.LittleEndian, &kLen); err != nil {
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
}

// Validate checks a tree for duplicate dynamic keys.
func (m *Memoiser) Validate(root node.Node) error {
	seen := make(map[string]bool)
	return validateKeys(root, seen)
}
