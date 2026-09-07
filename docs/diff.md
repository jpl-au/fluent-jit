# Differ

The Differ tracks rendered output of keyed dynamic elements across
renders and produces targeted patches when content changes. It works
standalone for any use case that needs incremental HTML updates.

## How it works

1. Mark elements with `.Dynamic("key")` in your Fluent tree
2. Call `Render(tree, w)` or `RenderBytes(tree)` to capture the initial state (snapshots)
3. After state changes, call `Diff()` with the new tree
4. Receive patches for only the elements that changed

The Differ tracks every keyed element, including nested keys. When
only a child's content changes, it returns a child patch. If the
parent's attributes, unkeyed content or child membership change, it
returns a parent patch covering that whole subtree. A patch list
never includes both an ancestor and its descendant.

## Basic usage

```go
differ := jit.NewDiffer()

// Initial render - stores snapshots of all keyed elements
html := differ.RenderBytes(tree)

// After state change
patches, change := differ.Diff(newTree)

if change != nil {
    // Outermost keys were added, removed, or reordered
    // Full re-render is needed
    html = differ.RenderBytes(newTree)
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
span.Text(value).Dynamic("")        // empty key - JIT-dynamic but not diff-tracked
span.Static("hello")                // static - invisible to the Differ
```

Passing an empty-string key (`.Dynamic("")`) marks the element as
dynamic for JIT purposes but does not register it with the Differ. A key
is required; only a non-empty key produces patches.

`.Dynamic()` is a chainable hook method Fluent core provides on every
element; outside a diff engine its only effect is that the key renders
as the element's `id` attribute.

## Structural changes

Adding, removing or reordering keys inside a surviving Dynamic
container produces an ordinary patch for that container. For example,
adding a keyed row inside `.Dynamic("items")` patches `items`.

When `Diff()` returns a non-nil `*StructuralChange`, outermost keys
were added, removed or reordered, so there is no enclosing Dynamic
container to target. Moving an existing key between containers patches
their nearest shared Dynamic ancestor, keeping the move within one DOM
morph so the element retains its identity. Without such an ancestor,
that move also returns a StructuralChange. The change describes what
happened:

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

- `Added` - outermost keys in the new tree not in the old
- `Removed` - outermost keys in the old tree not in the new
- `Reordered` - keys reordered, or moved between outermost containers

After a structural change, call `Render(tree, w)` or `RenderBytes(tree)`
to re-establish the baseline. That `Diff()` returns no patches and
leaves the previous baseline intact.

## DiffKey - targeted single-key diffs

When you know exactly which key changed, `DiffKey` re-renders and
diffs the supplied subtree without evaluating the rest of the tree.

```go
patch := differ.DiffKey("count", span.Textf("Count: %d", newCount).Dynamic("count"))
if patch != nil {
    // patch.Key is "count", patch.HTML is the new content
}
```

`DiffKey` refreshes the targeted key and all its nested snapshots,
then splices the replacement bytes into the enclosing snapshots.
Subsequent `Diff()` and `DiffKey()` calls therefore compare with the
HTML already sent to the client. Unrelated regions are unaffected.

This is significantly faster than a full `Diff` when targeting one key
out of many. Work is proportional to rendering the supplied subtree
and copying its enclosing HTML, rather than rendering the full page.

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

The Differ supports exporting and importing its state as opaque bytes,
useful for offloading disconnected-session snapshots to external
storage.

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
manipulate the bytes. Snapshots include nesting and child byte ranges,
so nested targeted updates remain correct after import.

Failed imports leave the existing baseline intact.

## Pooled buffers

Snapshots use `fluent.NewBuffer` / `fluent.PutBuffer` to avoid
allocation overhead. Superseded snapshots are returned after the new
baseline is accepted. Call `Clear()` when the Differ is no longer needed
to release buffers.

## Key order

The Differ preserves the order of keys within each container.
Reordering a nested list patches its Dynamic container. Reordering
outermost keys requires a full render because no keyed container
covers the move. Moves between containers also require a shared keyed
ancestor; otherwise they need a full render. Both engines return patches in
tree order.
