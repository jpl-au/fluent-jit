# Differ

The Differ tracks rendered output of keyed dynamic elements across
renders and produces targeted patches when content changes. It powers
[Tether](https://github.com/jpl-au/tether)'s live DOM updates but
works standalone for any use case that needs incremental HTML updates.

## How it works

1. Mark elements with `.Dynamic("key")` in your Fluent tree
2. Call `Render()` to capture the initial state (snapshots)
3. After state changes, call `Diff()` with the new tree
4. Receive patches for only the elements that changed

The Differ only tracks **outermost** keyed elements. If a parent and
child are both keyed, only the parent is tracked - changes to the
child are naturally captured in the parent's rendered output.

## Basic usage

```go
differ := jit.NewDiffer()

// Initial render - stores snapshots of all keyed elements
html := differ.Render(tree)

// After state change
patches, change := differ.Diff(newTree)

if change != nil {
    // Structural change - keys were added, removed, or reordered
    // Full re-render is needed
    html = differ.Render(newTree)
} else {
    // Apply targeted patches
    for _, p := range patches {
        fmt.Printf("key %q changed: %s\n", p.Key, p.HTML)
    }
}
```

## Dynamic keys

Elements are marked for tracking with `.Dynamic("key")`:

```go
span.Text(count).Dynamic("count")   // tracked by the Differ
span.Text(value).Dynamic()          // JIT-dynamic but not diff-tracked
span.Static("hello")                // static - invisible to the Differ
```

Calling `.Dynamic()` without a key (or with an empty string) marks the
element as dynamic for JIT purposes but does not register it with the
Differ. Only named keys produce patches.

## Structural changes

When `Diff()` returns a non-nil `*StructuralChange`, keys were added,
removed, or reordered. The change describes what happened:

```go
patches, change := differ.Diff(newTree)
if change != nil {
    fmt.Println(change.String())
    // "key 'help' added"
    // "key 'sidebar' removed"
    // "keys reordered"
}
```

Fields on `*StructuralChange`:

- `Added` - keys in the new tree not in the old
- `Removed` - keys in the old tree not in the new
- `Reordered` - same keys but different order

After a structural change, call `Render()` to re-establish the
baseline. Patches from `Diff()` are not reliable when keys have
changed.

## DiffKey - targeted single-key diffs

When you know exactly which key changed, `DiffKey` re-renders and
diffs only that key. The rest of the tree is untouched.

```go
patch := differ.DiffKey("count", span.Textf("Count: %d", newCount).Dynamic("count"))
if patch != nil {
    // patch.Key is "count", patch.HTML is the new content
}
```

`DiffKey` updates the stored snapshot for the targeted key, so
subsequent `Diff()` calls see the new content. Other keys are
unaffected.

This is significantly faster than a full `Diff` when targeting one key
out of many - it avoids walking the entire tree.

## Validation

Duplicate dynamic keys cause the Differ to lose track of elements.
Validate your tree at startup:

```go
if err := differ.Validate(tree); err != nil {
    log.Fatal(err) // "duplicate dynamic key in render tree: "count""
}
```

Returns `jit.ErrDuplicateKey` for programmatic checking.

## Snapshot persistence

The Differ supports exporting and importing its state as opaque bytes.
Tether uses this to offload disconnected session data via the
`DiffStore` interface.

```go
// Export snapshots to bytes (nil if not seeded)
data := differ.Export()

// Restore from prior export
if err := differ.Import(data); err != nil {
    log.Fatal(err)
}

// Release snapshot buffers back to the pool
differ.Clear()
```

`Export` is non-destructive - the Differ's state is unchanged after
export. The encoding is opaque; callers must not interpret or
manipulate the bytes.

## Pooled buffers

Snapshots use `fluent.NewBuffer` / `fluent.PutBuffer` to avoid
allocation overhead. Old snapshots are returned to the pool before new
ones are collected. Call `Clear()` when the Differ is no longer needed
to release buffers.

## Key order

The Differ tracks keys in tree-walk order, not just as a set.
Reordering keyed elements (e.g. sorting a list) triggers a structural
change, even when the same keys are present. This is intentional -
reordered DOM elements need a full re-render, not patches.
