package jit

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/jpl-au/fluent"
	"github.com/jpl-au/fluent/node"
)

// ErrDuplicateKey is returned when a render tree contains duplicate dynamic keys.
// Keys must be unique within a tree so the diff engine can unambiguously track
// each dynamic element across renders.
var ErrDuplicateKey = fmt.Errorf("duplicate dynamic key in render tree")

// SnapshotHint is the initial capacity hint in bytes for snapshot buffers.
// Most keyed elements render to small HTML fragments, so 128 bytes avoids
// an early grow in the common case. Adjust if your elements are typically
// larger or smaller.
var SnapshotHint = 128

// Patch represents a targeted change to a dynamic element in the rendered output.
// Key matches the value passed to .Dynamic("key") on the element.
// HTML is the new rendered content for that element.
//
// HTML references memory owned by the diff engine's snapshot for the
// key. It stays valid until the next call that replaces or releases
// that snapshot (Diff, DiffKey, Render, Import or Clear) - copy the
// bytes if the patch must outlive that. The usual pattern of sending
// patches immediately after Diff needs no copy.
type Patch struct {
	Key  string
	HTML []byte
}

// StructuralChange describes why a diff detected a structural change.
// The caller can use this to produce actionable diagnostics that tell
// the developer exactly what changed and how to avoid root morphs.
type StructuralChange struct {
	Added     []string // keys present in the new tree but not the old
	Removed   []string // keys present in the old tree but not the new
	Reordered bool     // same keys, different order
}

// String returns a human-readable description of the change,
// e.g. "key 'sidebar' added" or "keys reordered".
func (c *StructuralChange) String() string {
	var parts []string
	if len(c.Added) > 0 {
		parts = append(parts, quotedKeys(c.Added)+" added")
	}
	if len(c.Removed) > 0 {
		parts = append(parts, quotedKeys(c.Removed)+" removed")
	}
	if c.Reordered {
		parts = append(parts, "keys reordered")
	}
	return strings.Join(parts, ", ")
}

// quotedKeys formats key names for human-readable diagnostics.
// A single key returns "key 'x'"; multiple keys return "keys 'x', 'y'".
func quotedKeys(keys []string) string {
	if len(keys) == 1 {
		return "key '" + keys[0] + "'"
	}
	quoted := make([]string, len(keys))
	for i, k := range keys {
		quoted[i] = "'" + k + "'"
	}
	return "keys " + strings.Join(quoted, ", ")
}

// Differ tracks rendered output of keyed dynamic nodes across renders and
// produces targeted patches when their content changes.
//
// Each session should own its own Differ - they are not shared across sessions.
// The typical lifecycle is:
//
//  1. Render() on initial page load - returns full HTML, stores snapshots
//  2. Diff() after each state change - returns patches for changed elements
//  3. If Diff returns a *StructuralChange, call Render() again for a full re-render
//
// Snapshot data can be serialised for external storage via [Differ.Export],
// restored with [Differ.Import], and freed with [Differ.Clear]. This
// supports offloading disconnected session data to reduce memory usage.
type Differ struct {
	mu        sync.Mutex
	snapshots map[string]*bytes.Buffer
	order     []string // outermost key order for reorder detection
	seeded    bool
}

// NewDiffer creates a new Differ instance.
func NewDiffer() *Differ {
	return &Differ{
		snapshots: make(map[string]*bytes.Buffer),
	}
}

// Render produces the full HTML for the tree and stores snapshots of all
// keyed dynamic nodes. Use this for the initial page load and after
// structural changes detected by Diff.
//
// If a writer is provided, the HTML is written to it and nil is returned.
// If no writer is provided, the HTML is returned as a byte slice.
//
// The tree renders exactly once: the walk writes the page HTML and
// captures each keyed region as a byte range of that output. Closures
// run a single time and snapshots are always byte-identical to the
// page the client received, even for closures that are not
// deterministic.
func (d *Differ) Render(root node.Node, w ...io.Writer) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Return old buffers to the pool before collecting new snapshots.
	d.returnBuffers()
	d.snapshots = make(map[string]*bytes.Buffer)
	d.order = nil

	page := fluent.NewBuffer()
	renderTracked(root, page, d.snapshots, &d.order)
	d.seeded = true

	if len(w) > 0 && w[0] != nil {
		_, _ = page.WriteTo(w[0])
		fluent.PutBuffer(page)
		return nil
	}
	// The returned bytes escape to the caller, so the page buffer
	// cannot go back to the pool - the same trade node.Node.Render makes.
	return page.Bytes()
}

// trackedKey returns n's Dynamic tracking key, or "" when the node is
// untracked (no key, or the "_" placeholder).
func trackedKey(n node.Node) string {
	if d, ok := n.(node.Dynamic); ok {
		if k := d.DynamicKey(); k != "" && k != "_" {
			return k
		}
	}
	return ""
}

// renderTracked renders n into page exactly once while capturing every
// keyed Dynamic region as a snapshot copied from the page bytes.
//
// Keys are recorded in pre-order (parent before children), and a
// nested keyed region is captured from the same bytes as its parent -
// nothing renders twice.
func renderTracked(n node.Node, page *bytes.Buffer, snapshots map[string]*bytes.Buffer, order *[]string) {
	key := trackedKey(n)
	if key != "" {
		*order = append(*order, key)
	}
	start := page.Len()

	renderBody(n, page, snapshots, order)

	if key != "" {
		buf := fluent.NewBuffer(page.Len() - start)
		buf.Write(page.Bytes()[start:page.Len()])
		// A duplicate key is invalid input, but the buffer it already
		// holds must go back to the pool before being overwritten.
		if prior, ok := snapshots[key]; ok {
			fluent.PutBuffer(prior)
		}
		snapshots[key] = buf
	}
}

// renderBody writes n's rendered form into page. Elements render
// decomposed - open tag, children, close tag - which the generated
// element code guarantees is byte-identical to RenderBuilder
// (RenderBuilder is defined as exactly that sequence). Containers
// without markup of their own (fragments, conditionals, function and
// memoised nodes) contribute their children, evaluating any closure
// once via Nodes(). Nodes without children render via RenderBuilder.
// Children recurse through renderTracked so nested keyed regions are
// captured along the way.
func renderBody(n node.Node, page *bytes.Buffer, snapshots map[string]*bytes.Buffer, order *[]string) {
	if el, ok := n.(node.Element); ok {
		el.RenderOpen(page)
		for _, child := range n.Nodes() {
			if child != nil {
				renderTracked(child, page, snapshots, order)
			}
		}
		el.RenderClose(page)
		return
	}
	if children := n.Nodes(); len(children) > 0 {
		for _, child := range children {
			if child != nil {
				renderTracked(child, page, snapshots, order)
			}
		}
		return
	}
	n.RenderBuilder(page)
}

// Diff compares the new tree against stored snapshots and returns
// targeted patches for any keyed dynamic nodes whose content changed.
//
// Returns (patches, nil) when all keys match between renders.
// The patches slice is nil if nothing changed.
//
// Returns (nil, *StructuralChange) when keys were added, removed, or
// reordered - the caller should use Render for a full re-render and
// can use the StructuralChange for diagnostics.
//
// Returns (nil, nil) if Render has not been called yet.
func (d *Differ) Diff(root node.Node) ([]Patch, *StructuralChange) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.seeded {
		return nil, nil
	}

	current := make(map[string]*bytes.Buffer, len(d.snapshots))
	currentOrder := make([]string, 0, len(d.order))
	collectTracked(root, current, &currentOrder)

	// Structural change - keys were added, removed, or reordered.
	// Comparing the ordered slices catches all three cases in one check.
	if !slices.Equal(d.order, currentOrder) {
		for _, buf := range current {
			fluent.PutBuffer(buf)
		}
		return nil, describeChange(d.order, currentOrder)
	}

	// Compare each keyed element's rendered output and build patches.
	// Unchanged keys reuse the previous buffer (returned to pool
	// immediately) to avoid keeping two copies of identical content.
	// Initialise as non-nil so callers can distinguish "nothing
	// changed" (empty slice) from "unseeded" (nil).
	//
	// Duplicate keys are invalid input (see Validate) but must not
	// corrupt the buffer pool: processing the same key twice would
	// return a buffer to the pool while it is still referenced as a
	// snapshot, so a repeated key is skipped after its first visit.
	patches := []Patch{}
	seen := make(map[string]bool, len(currentOrder))
	for _, key := range currentOrder {
		if seen[key] {
			continue
		}
		seen[key] = true
		cur := current[key]
		prev := d.snapshots[key]
		if bytes.Equal(cur.Bytes(), prev.Bytes()) {
			// Unchanged - return the fresh buffer to the pool and
			// keep the existing snapshot in place.
			fluent.PutBuffer(cur)
			current[key] = prev
		} else {
			patches = append(patches, Patch{Key: key, HTML: cur.Bytes()})
			// Return the old buffer since it's being replaced.
			fluent.PutBuffer(prev)
		}
	}

	d.snapshots = current
	d.order = currentOrder

	return patches, nil
}

// describeChange compares the previous and current key orders and
// returns a StructuralChange describing what happened. Occurrence
// counts matter: a key appearing more or fewer times than before is
// reported as added or removed, so a duplicated key reads as "key
// added" rather than sending the developer hunting for a reorder
// that never happened.
func describeChange(prev, current []string) *StructuralChange {
	prevCount := make(map[string]int, len(prev))
	for _, k := range prev {
		prevCount[k]++
	}
	curCount := make(map[string]int, len(current))
	for _, k := range current {
		curCount[k]++
	}

	// A key cannot be both added and removed, so one reported map
	// keeps each key to a single mention across both lists.
	var added, removed []string
	reported := make(map[string]bool)
	for _, k := range current {
		if !reported[k] && curCount[k] > prevCount[k] {
			added = append(added, k)
			reported[k] = true
		}
	}
	for _, k := range prev {
		if !reported[k] && prevCount[k] > curCount[k] {
			removed = append(removed, k)
			reported[k] = true
		}
	}

	return &StructuralChange{
		Added:     added,
		Removed:   removed,
		Reordered: len(added) == 0 && len(removed) == 0,
	}
}

// Validate checks a tree for duplicate dynamic keys. Keys must be unique
// within a tree so the diff engine can track each element unambiguously.
// Returns nil if all keys are unique.
func (d *Differ) Validate(root node.Node) error {
	seen := make(map[string]bool)
	return validateKeys(root, seen)
}

// DiffKey re-renders a single Dynamic key against the stored snapshot
// and returns a patch if the content changed. Use this for targeted
// updates where the caller knows exactly which key changed and wants
// to avoid the cost of a full tree walk via [Differ.Diff]. For a page
// with 50 Dynamic keys, DiffKey is over 1,000x faster than Diff.
//
// The snapshot for the targeted key is updated so subsequent Diff
// calls see the new content. Other keys are not touched.
//
// Returns nil if the content is unchanged. Returns a patch with the
// new HTML if the content changed or the key has no stored snapshot.
func (d *Differ) DiffKey(key string, subtree node.Node) *Patch {
	d.mu.Lock()
	defer d.mu.Unlock()

	prev := d.snapshots[key]

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
	d.snapshots[key] = buf

	return patch
}

// returnBuffers returns all stored snapshot buffers to the pool.
// Caller must hold d.mu.
func (d *Differ) returnBuffers() {
	for _, buf := range d.snapshots {
		fluent.PutBuffer(buf)
	}
}

// Export returns the differ's snapshot data as raw bytes suitable for
// external storage. The differ's internal state is unchanged - call
// Clear to release the memory after a successful save. Returns nil if
// the differ has not been seeded.
//
// The encoding is an internal detail. Callers must not interpret the
// bytes - use Import to restore them.
func (d *Differ) Export() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.seeded {
		return nil
	}

	var buf bytes.Buffer

	// Write snapshot count, then each key-value pair.
	binary.Write(&buf, binary.LittleEndian, uint32(len(d.order)))
	for _, key := range d.order {
		binary.Write(&buf, binary.LittleEndian, uint32(len(key)))
		buf.WriteString(key)
		snap := d.snapshots[key]
		binary.Write(&buf, binary.LittleEndian, uint32(snap.Len()))
		buf.Write(snap.Bytes())
	}

	return buf.Bytes()
}

// Import restores snapshot data from a prior Export call. The differ is
// marked as seeded after a successful import, allowing Diff to compare
// against the restored snapshots.
func (d *Differ) Import(data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	r := bytes.NewReader(data)

	var count uint32
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return fmt.Errorf("jit: import: reading snapshot count: %w", err)
	}

	// Corrupt data must fail with an error, not an enormous allocation.
	// Each entry needs at least 8 bytes of length prefixes, so a count
	// the remaining data cannot hold is rejected before preallocating.
	if int64(count) > int64(r.Len())/8 {
		return fmt.Errorf("jit: import: snapshot count %d exceeds data size %d", count, r.Len())
	}

	snapshots := make(map[string]*bytes.Buffer, count)
	order := make([]string, 0, count)

	// returnParsed returns any buffers allocated so far back to the
	// pool. Called on error so partial imports don't leak memory.
	returnParsed := func() {
		for _, buf := range snapshots {
			fluent.PutBuffer(buf)
		}
	}

	for range count {
		var keyLen uint32
		if err := binary.Read(r, binary.LittleEndian, &keyLen); err != nil {
			returnParsed()
			return fmt.Errorf("jit: import: reading key length: %w", err)
		}
		// Reject lengths the remaining data cannot hold before
		// allocating for them - see the count check above.
		if int64(keyLen) > int64(r.Len()) {
			returnParsed()
			return fmt.Errorf("jit: import: key length %d exceeds remaining data %d", keyLen, r.Len())
		}
		keyBytes := make([]byte, keyLen)
		if _, err := io.ReadFull(r, keyBytes); err != nil {
			returnParsed()
			return fmt.Errorf("jit: import: reading key: %w", err)
		}
		key := string(keyBytes)

		var valLen uint32
		if err := binary.Read(r, binary.LittleEndian, &valLen); err != nil {
			returnParsed()
			return fmt.Errorf("jit: import: reading value length: %w", err)
		}
		if int64(valLen) > int64(r.Len()) {
			returnParsed()
			return fmt.Errorf("jit: import: value length %d exceeds remaining data %d", valLen, r.Len())
		}

		buf := fluent.NewBuffer(int(valLen))
		if _, err := io.CopyN(buf, r, int64(valLen)); err != nil {
			fluent.PutBuffer(buf)
			returnParsed()
			return fmt.Errorf("jit: import: reading value: %w", err)
		}

		snapshots[key] = buf
		order = append(order, key)
	}

	// Release any existing buffers before replacing.
	d.returnBuffers()
	d.snapshots = snapshots
	d.order = order
	d.seeded = true
	return nil
}

// Clear releases the differ's snapshot buffers back to the pool and
// resets it to an unseeded state. Call this after a successful
// DiffStore.Save to free the memory that Export copied out.
func (d *Differ) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.returnBuffers()
	d.snapshots = make(map[string]*bytes.Buffer)
	d.order = nil
	d.seeded = false
}

// collectTracked walks the tree for Diff. Outside keyed regions
// nothing renders - the walk only descends looking for keys. At a
// keyed region the subtree renders exactly once into the region's
// snapshot buffer, with nested keyed regions captured as copies of
// that buffer's byte ranges. An earlier walk rendered a nested region
// twice (inside its parent's snapshot and again for its own) and ran
// closures inside keyed regions twice (once rendering, once
// materialising Nodes to keep searching for keys).
//
// Keys are appended to order in tree-walk order so the caller can
// detect reordering as a structural change. Nested Dynamic keys are
// tracked independently of their parents: a patch to a child key via
// sess.Patch must work even when the parent's content as a whole has
// not changed, and a child key absent from the order slice would be
// silently skipped by subsequent full Diffs.
func collectTracked(n node.Node, snapshots map[string]*bytes.Buffer, order *[]string) {
	if key := trackedKey(n); key != "" {
		*order = append(*order, key)
		buf := fluent.NewBuffer(SnapshotHint)
		renderBody(n, buf, snapshots, order)
		// A duplicate key is invalid input, but the buffer it already
		// holds must go back to the pool before being overwritten.
		if prior, ok := snapshots[key]; ok {
			fluent.PutBuffer(prior)
		}
		snapshots[key] = buf
		return
	}
	for _, child := range n.Nodes() {
		if child != nil {
			collectTracked(child, snapshots, order)
		}
	}
}

// validateKeys walks the tree depth-first and checks for duplicate dynamic
// keys. Unlike collectSnapshots it does not stop at keyed nodes - nested
// keys must also be unique because the Differ tracks by key name, not path.
func validateKeys(n node.Node, seen map[string]bool) error {
	if d, ok := n.(node.Dynamic); ok {
		key := d.DynamicKey()
		if key != "" && key != "_" {
			if seen[key] {
				return fmt.Errorf("%w: %q", ErrDuplicateKey, key)
			}
			seen[key] = true
		}
	}
	for _, child := range n.Nodes() {
		if child == nil {
			continue
		}
		if err := validateKeys(child, seen); err != nil {
			return err
		}
	}
	return nil
}
