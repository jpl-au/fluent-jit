package jit

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/jpl-au/fluent"
	"github.com/jpl-au/fluent/node"
)

// ErrDuplicateKey is returned when a render tree contains duplicate dynamic keys.
// Keys must be unique within a tree so the diff engine can unambiguously track
// each dynamic element across renders.
var ErrDuplicateKey = fmt.Errorf("duplicate dynamic key in render tree")

// ErrInvalidKey is returned when a dynamic key contains whitespace. A key
// renders as the element's id, which cannot contain whitespace.
var ErrInvalidKey = fmt.Errorf("dynamic key contains whitespace")

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
	Reordered bool     // keys reordered or moved between outermost containers
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

// Differ tracks the HTML and nesting of Dynamic regions in one session.
// Render seeds its snapshots; Diff compares subsequent trees; DiffKey updates
// a known region directly. Export/Import persist the complete nested baseline.
type Differ struct {
	mu   sync.Mutex
	tree regionTree
}

// NewDiffer creates an empty Differ ready for use.
func NewDiffer() *Differ { return &Differ{tree: regionTree{}} }

// Render writes the full page and seeds snapshots in a single rendering pass.
// Write errors are discarded; use WriteTo to observe them.
func (d *Differ) Render(root node.Node, w io.Writer) { _, _ = d.WriteTo(root, w) }

// WriteTo renders and seeds snapshots, returning the byte count and write error.
func (d *Differ) WriteTo(root node.Node, w io.Writer) (int64, error) {
	page := fluent.NewBuffer()
	d.seed(root, page)
	n, err := page.WriteTo(w)
	fluent.PutBuffer(page)
	return n, err
}

// RenderBytes returns the full page and seeds snapshots from those same bytes.
func (d *Differ) RenderBytes(root node.Node) []byte {
	var page bytes.Buffer
	d.seed(root, &page)
	return page.Bytes()
}

func (d *Differ) seed(root node.Node, page *bytes.Buffer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := newRegionRenderer(nil, nil)
	w.walk(root, page, nil, "", memoVersion{}, false)
	d.tree.releaseExcept(nil)
	d.tree = w.next
	d.tree.seeded = true
}

// Diff compares Dynamic regions with the previous render. An empty, non-nil
// patch slice means nothing changed; (nil, nil) means Render has not been called.
// Nested changes patch their enclosing container. Moves between containers
// patch a shared ancestor to preserve DOM identity. A StructuralChange means
// no keyed container covers the change; it leaves the baseline intact and
// requires a fresh Render.
func (d *Differ) Diff(root node.Node) ([]Patch, *StructuralChange) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.tree.seeded {
		return nil, nil
	}
	w := newRegionRenderer(&d.tree, nil)
	w.walk(root, nil, nil, "", memoVersion{}, false)
	return d.tree.diff(w.next)
}

// DiffKey renders just the supplied region, bypassing a full tree walk. It
// updates descendant snapshots and splices the new bytes into its ancestors,
// leaving unrelated regions untouched. Returns nil when the HTML is unchanged.
// The key is explicit: the subtree's outer node need not carry Dynamic.
func (d *Differ) DiffKey(key string, subtree node.Node) *Patch {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tree.diffKey(key, subtree)
}

// Export returns an opaque snapshot blob, or nil before Render. It includes
// the region hierarchy needed to resume nested updates after Import.
func (d *Differ) Export() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tree.export()
}

// Import restores an Export blob and marks the Differ seeded. An import error
// leaves the current baseline intact.
func (d *Differ) Import(data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tree, err := decodeRegions(data)
	if err != nil {
		return err
	}
	d.tree.releaseExcept(nil)
	d.tree = tree
	return nil
}

// Clear releases pooled snapshot buffers and resets the Differ to unseeded.
func (d *Differ) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tree.releaseExcept(nil)
	d.tree = regionTree{}
}

// Validate checks for duplicate keys and whitespace in Dynamic ids. The walk
// evaluates Func closures; use it in tests or at startup, not on every update.
func (d *Differ) Validate(root node.Node) error {
	return validateKeys(root, make(map[string]bool))
}

func trackedKey(n node.Node) string {
	if d, ok := n.(node.Dynamic); ok {
		return d.DynamicKey()
	}
	return ""
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

// validateKeys walks the tree depth-first and checks for duplicate dynamic
// keys. Unlike collectSnapshots it does not stop at keyed nodes - nested
// keys must also be unique because the Differ tracks by key name, not path.
func validateKeys(n node.Node, seen map[string]bool) error {
	if d, ok := n.(node.Dynamic); ok {
		key := d.DynamicKey()
		if key != "" {
			if seen[key] {
				return fmt.Errorf("%w: %q", ErrDuplicateKey, key)
			}
			if strings.ContainsAny(key, " \t\n\r\f") {
				return fmt.Errorf("%w: %q", ErrInvalidKey, key)
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
