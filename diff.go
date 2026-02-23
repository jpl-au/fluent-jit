package jit

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync"

	"github.com/jpl-au/fluent"
	"github.com/jpl-au/fluent/node"
)

// ErrDuplicateKey is returned when a render tree contains duplicate dynamic keys.
// Keys must be unique within a tree so the diff engine can unambiguously track
// each dynamic element across renders.
var ErrDuplicateKey = fmt.Errorf("duplicate dynamic key in render tree")

// Patch represents a targeted change to a dynamic element in the rendered output.
// Key matches the value passed to .Dynamic("key") on the element.
// HTML is the new rendered content for that element.
type Patch struct {
	Key  string
	HTML []byte
}

// Differ tracks rendered output of keyed dynamic nodes across renders and
// produces targeted patches when their content changes.
//
// Each session should own its own Differ — they are not shared across sessions.
// The typical lifecycle is:
//
//  1. Render() on initial page load — returns full HTML, stores snapshots
//  2. Diff() after each state change — returns patches for changed elements
//  3. If Diff returns fullRender=true, call Render() again for a full re-render
type Differ struct {
	mu        sync.Mutex
	snapshots map[string]*bytes.Buffer
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
	collectSnapshots(root, d.snapshots)
	d.seeded = true

	return root.Render(w...)
}

// Diff compares the new tree against stored snapshots and returns
// targeted patches for any keyed dynamic nodes whose content changed.
//
// Returns (patches, false) when all keys match between renders.
// The patches slice is nil if nothing changed.
//
// Returns (nil, true) when keys were added or removed — the caller
// should use Render for a full re-render instead.
//
// Returns (nil, false) if Render has not been called yet.
func (d *Differ) Diff(root node.Node) ([]Patch, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.seeded {
		return nil, false
	}

	current := make(map[string]*bytes.Buffer)
	collectSnapshots(root, current)

	// Structural change — keys were added or removed
	if !sameKeys(d.snapshots, current) {
		// Return the current buffers since we won't store them.
		for _, buf := range current {
			fluent.PutBuffer(buf)
		}
		return nil, true
	}

	// Compare each keyed element's rendered output in sorted key order
	// so patches are deterministic regardless of map iteration.
	var patches []Patch
	for _, key := range slices.Sorted(maps.Keys(current)) {
		cur := current[key]
		prev := d.snapshots[key]
		if !bytes.Equal(cur.Bytes(), prev.Bytes()) {
			patches = append(patches, Patch{Key: key, HTML: cur.Bytes()})
		}
	}

	// Return old buffers to the pool and store the new ones.
	d.returnBuffers()
	d.snapshots = current

	return patches, false
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
// Once a keyed node is found its children are not searched for further
// keys. This avoids redundant patches when both a parent and child are
// keyed — only the outermost key is tracked.
func collectSnapshots(n node.Node, snapshots map[string]*bytes.Buffer) {
	if d, ok := n.(node.Dynamic); ok {
		key := d.DynamicKey()
		if key != "" && key != "_" {
			buf := fluent.NewBuffer()
			n.RenderBuilder(buf)
			snapshots[key] = buf
			return
		}
	}
	for _, child := range n.Nodes() {
		collectSnapshots(child, snapshots)
	}
}

// sameKeys reports whether two snapshot maps have identical key sets.
func sameKeys(a, b map[string]*bytes.Buffer) bool {
	if len(a) != len(b) {
		return false
	}
	for key := range a {
		if _, ok := b[key]; !ok {
			return false
		}
	}
	return true
}

// validateKeys walks the tree and checks for duplicate dynamic keys.
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
		if err := validateKeys(child, seen); err != nil {
			return err
		}
	}
	return nil
}
