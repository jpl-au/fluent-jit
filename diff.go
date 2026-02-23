package jit

import (
	"bytes"
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
// Each session should own its own Differ — they are not shared across sessions.
// The typical lifecycle is:
//
//  1. Render() on initial page load — returns full HTML, stores snapshots
//  2. Diff() after each state change — returns patches for changed elements
//  3. If Diff returns a *StructuralChange, call Render() again for a full re-render
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
func (d *Differ) Render(root node.Node, w ...io.Writer) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Return old buffers to the pool before collecting new snapshots.
	d.returnBuffers()
	d.snapshots = make(map[string]*bytes.Buffer)
	d.order = nil
	collectSnapshots(root, d.snapshots, &d.order)
	d.seeded = true

	return root.Render(w...)
}

// Diff compares the new tree against stored snapshots and returns
// targeted patches for any keyed dynamic nodes whose content changed.
//
// Returns (patches, nil) when all keys match between renders.
// The patches slice is nil if nothing changed.
//
// Returns (nil, *StructuralChange) when keys were added, removed, or
// reordered — the caller should use Render for a full re-render and
// can use the StructuralChange for diagnostics.
//
// Returns (nil, nil) if Render has not been called yet.
func (d *Differ) Diff(root node.Node) ([]Patch, *StructuralChange) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.seeded {
		return nil, nil
	}

	current := make(map[string]*bytes.Buffer)
	var currentOrder []string
	collectSnapshots(root, current, &currentOrder)

	// Structural change — keys were added, removed, or reordered.
	// Comparing the ordered slices catches all three cases in one check.
	if !slices.Equal(d.order, currentOrder) {
		for _, buf := range current {
			fluent.PutBuffer(buf)
		}
		return nil, describeChange(d.order, currentOrder)
	}

	// Compare each keyed element's rendered output in tree-walk order.
	var patches []Patch
	for _, key := range currentOrder {
		cur := current[key]
		prev := d.snapshots[key]
		if !bytes.Equal(cur.Bytes(), prev.Bytes()) {
			patches = append(patches, Patch{Key: key, HTML: cur.Bytes()})
		}
	}

	// Return old buffers to the pool and store the new ones.
	d.returnBuffers()
	d.snapshots = current

	return patches, nil
}

// describeChange compares the previous and current key orders and
// returns a StructuralChange describing what happened.
func describeChange(prev, current []string) *StructuralChange {
	prevSet := make(map[string]bool, len(prev))
	for _, k := range prev {
		prevSet[k] = true
	}
	curSet := make(map[string]bool, len(current))
	for _, k := range current {
		curSet[k] = true
	}

	var added, removed []string
	for _, k := range current {
		if !prevSet[k] {
			added = append(added, k)
		}
	}
	for _, k := range prev {
		if !curSet[k] {
			removed = append(removed, k)
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

// returnBuffers returns all stored snapshot buffers to the pool.
// Caller must hold d.mu.
func (d *Differ) returnBuffers() {
	for _, buf := range d.snapshots {
		fluent.PutBuffer(buf)
	}
}

// collectSnapshots walks the tree depth-first and renders each keyed
// dynamic node into a pooled buffer. Nodes with the key "_" (marked
// dynamic without a tracking key) are skipped.
//
// Keys are appended to order in tree-walk order so the caller can
// detect reordering as a structural change.
//
// Once a keyed node is found its children are not searched for further
// keys. This avoids redundant patches when both a parent and child are
// keyed — only the outermost key is tracked.
func collectSnapshots(n node.Node, snapshots map[string]*bytes.Buffer, order *[]string) {
	if d, ok := n.(node.Dynamic); ok {
		key := d.DynamicKey()
		if key != "" && key != "_" {
			buf := fluent.NewBuffer(SnapshotHint)
			n.RenderBuilder(buf)
			snapshots[key] = buf
			*order = append(*order, key)
			return
		}
	}
	for _, child := range n.Nodes() {
		if child != nil {
			collectSnapshots(child, snapshots, order)
		}
	}
}

// validateKeys walks the tree depth-first and checks for duplicate dynamic
// keys. Unlike collectSnapshots it does not stop at keyed nodes — nested
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
