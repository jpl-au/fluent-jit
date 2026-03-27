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
// use [node.Memo] nodes. It is a standalone concern - use either the
// Differ or the Memoiser, not both on the same session.
//
// When the developer opts into memoisation, every Dynamic region in
// the render tree should contain a [node.Memo] child with a cache
// key. On each Diff call, the Memoiser compares memo keys with the
// previous render. Matching keys skip the subtree entirely - the
// closure never runs and no HTML is rendered. Mismatched keys call
// the closure and render the result into a snapshot for comparison.
//
// The Memoiser does not fall back to content-based diffing for
// non-memo Dynamic nodes. If a Dynamic node has no memo child, it
// is always re-rendered (treated as a miss). This keeps the
// implementation simple and fast - there is no tree walking via
// Nodes() that would materialise closures.
type Memoiser struct {
	mu        sync.Mutex
	snapshots map[string]*bytes.Buffer
	memoKeys  map[string]string // Dynamic key -> stringified memo key
	order     []string
	seeded    bool
}

// NewMemoiser creates an empty Memoiser ready for use.
func NewMemoiser() *Memoiser {
	return &Memoiser{
		snapshots: make(map[string]*bytes.Buffer),
		memoKeys:  make(map[string]string),
	}
}

// Render produces the full HTML for the tree and stores snapshots
// and memo keys for all Dynamic regions. Use this for the initial
// page load and after structural changes detected by Diff.
func (m *Memoiser) Render(root node.Node, w ...io.Writer) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.returnBuffers()
	m.snapshots = make(map[string]*bytes.Buffer)
	m.memoKeys = make(map[string]string)
	m.order = nil
	m.collectAll(root)
	m.seeded = true

	return root.Render(w...)
}

// Diff compares the new tree against stored snapshots using memo
// keys. For each Dynamic node:
//
//   - If its child satisfies [node.Memoiser] and the key matches the
//     previous render, the subtree is skipped. No closure is called,
//     no HTML is rendered. The previous snapshot is reused.
//   - If the key differs (or there is no memo child), [node.Memoiser].MemoRender
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

	// Collect the current order and identify misses. Hits skip
	// entirely - no buffer allocated, no render, no comparison.
	// Only misses produce fresh buffers for comparison.
	misses := make(map[string]*bytes.Buffer)
	newKeys := make(map[string]string, len(m.memoKeys))
	currentOrder := make([]string, 0, len(m.order))
	m.collectDiff(root, misses, newKeys, &currentOrder)

	if !slices.Equal(m.order, currentOrder) {
		for _, buf := range misses {
			fluent.PutBuffer(buf)
		}
		change := describeChange(m.order, currentOrder)
		m.memoKeys = newKeys
		m.order = currentOrder
		return nil, change
	}

	// Compare misses and replace snapshots in a single pass. Hits
	// are identical by definition and not in the misses map.
	var patches []Patch
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
	m.memoKeys = newKeys
	m.order = currentOrder

	return patches, nil
}

// collectAll walks the tree for the initial Render. Every Dynamic
// node is rendered and its memo key (if any) is recorded. This
// establishes the baseline for subsequent Diff calls.
func (m *Memoiser) collectAll(n node.Node) {
	if d, ok := n.(node.Dynamic); ok {
		key := d.DynamicKey()
		if key != "" && key != "_" {
			buf := fluent.NewBuffer(SnapshotHint)
			n.RenderBuilder(buf)
			m.snapshots[key] = buf
			m.order = append(m.order, key)

			if mk := findMemoKeyStr(n); mk != "" {
				m.memoKeys[key] = mk
			}
			return
		}
	}
	for _, child := range n.Nodes() {
		if child != nil {
			m.collectAll(child)
		}
	}
}

// collectDiff walks the tree for a Diff call. For each Dynamic node,
// it checks whether the memo key matches the previous render. Hits
// are skipped entirely - no buffer allocated, no render. Misses are
// rendered into fresh buffers in the misses map.
func (m *Memoiser) collectDiff(n node.Node, misses map[string]*bytes.Buffer, keys map[string]string, order *[]string) {
	if d, ok := n.(node.Dynamic); ok {
		key := d.DynamicKey()
		if key != "" && key != "_" {
			*order = append(*order, key)

			mk := findMemoKeyStr(n)
			if mk != "" {
				keys[key] = mk
				// Hit: same key as previous render. Skip entirely.
				if prev, exists := m.memoKeys[key]; exists && prev == mk {
					return
				}
			}

			// Miss: render the node.
			buf := fluent.NewBuffer(SnapshotHint)
			n.RenderBuilder(buf)
			misses[key] = buf
			return
		}
	}
	for _, child := range n.Nodes() {
		if child != nil {
			m.collectDiff(child, misses, keys, order)
		}
	}
}

// findMemoKeyStr checks the immediate children of a node for a
// [node.Memoiser] and returns the key as a string. The conversion
// uses type-switched strconv for common types (zero reflection,
// zero allocation for string keys). Returns "" if no memo child
// is found.
func findMemoKeyStr(n node.Node) string {
	for _, child := range n.Nodes() {
		if memo, ok := child.(node.Memoiser); ok {
			return memoKeyToString(memo.MemoKey())
		}
	}
	return ""
}

// memoKeyToString converts a memo key to a string using fast paths
// for common types. Only the fallback uses fmt.Sprint; the common
// cases (string, int, bool) use strconv with no reflection.
func memoKeyToString(v any) string {
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
// check memo keys because the developer is explicitly targeting this
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

// returnBuffers returns all stored snapshot buffers to the pool.
func (m *Memoiser) returnBuffers() {
	for _, buf := range m.snapshots {
		fluent.PutBuffer(buf)
	}
}

// Export returns the Memoiser's snapshot and memo key data as raw
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

	// Memo keys: count, then (keyLen, key, valLen, val) pairs.
	// Values are already strings (converted once on entry via
	// memoKeyToString).
	binary.Write(&buf, binary.LittleEndian, uint32(len(m.memoKeys)))
	for k, v := range m.memoKeys {
		binary.Write(&buf, binary.LittleEndian, uint32(len(k)))
		buf.WriteString(k)
		binary.Write(&buf, binary.LittleEndian, uint32(len(v)))
		buf.WriteString(v)
	}

	return buf.Bytes()
}

// Import restores snapshot and memo key data from a prior Export.
func (m *Memoiser) Import(data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	r := bytes.NewReader(data)

	// Read snapshots.
	var count uint32
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return fmt.Errorf("jit: memo import: reading snapshot count: %w", err)
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
			return fmt.Errorf("jit: memo import: reading key length: %w", err)
		}
		keyBytes := make([]byte, keyLen)
		if _, err := io.ReadFull(r, keyBytes); err != nil {
			returnParsed()
			return fmt.Errorf("jit: memo import: reading key: %w", err)
		}
		key := string(keyBytes)

		var valLen uint32
		if err := binary.Read(r, binary.LittleEndian, &valLen); err != nil {
			returnParsed()
			return fmt.Errorf("jit: memo import: reading value length: %w", err)
		}

		buf := fluent.NewBuffer(int(valLen))
		if _, err := io.CopyN(buf, r, int64(valLen)); err != nil {
			fluent.PutBuffer(buf)
			returnParsed()
			return fmt.Errorf("jit: memo import: reading value: %w", err)
		}

		snapshots[key] = buf
		order = append(order, key)
	}

	// Read memo keys if present.
	memoKeys := make(map[string]string)
	var memoCount uint32
	if err := binary.Read(r, binary.LittleEndian, &memoCount); err == nil {
		for range memoCount {
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
			memoKeys[string(kBytes)] = string(vBytes)
		}
	}

	m.returnBuffers()
	m.snapshots = snapshots
	m.order = order
	m.memoKeys = memoKeys
	m.seeded = true
	return nil
}

// Clear releases snapshot buffers and resets all state.
func (m *Memoiser) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.returnBuffers()
	m.snapshots = make(map[string]*bytes.Buffer)
	m.memoKeys = make(map[string]string)
	m.order = nil
	m.seeded = false
}

// Validate checks a tree for duplicate dynamic keys.
func (m *Memoiser) Validate(root node.Node) error {
	seen := make(map[string]bool)
	return validateKeys(root, seen)
}
