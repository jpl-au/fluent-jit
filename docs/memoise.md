# Memoiser

The Memoiser is an alternative to the [Differ](diff.md) that skips
unchanged subtrees entirely. Where the Differ always re-renders keyed
elements and compares the HTML output, the Memoiser checks a cache key
first and skips the render closure when the key matches.

Use the Memoiser when your render tree has expensive subtrees that
rarely change. Use the Differ when all subtrees are cheap to render
and content-based comparison is sufficient.

## How it works

1. Wrap expensive subtrees in `node.Memoise(key, func)` inside a
   `.Dynamic("key")` region
2. On first render, the closure runs and the output is stored
3. On subsequent renders, if the memoisation key matches, the closure
   is skipped and the stored snapshot is reused
4. If the key changes, the closure runs, the output is diffed against
   the stored snapshot, and a patch is produced

## Basic usage

```go
memoiser := jit.NewMemoiser()

// Initial render
html := memoiser.Render(tree)

// After state change
patches, change := memoiser.Diff(newTree)

if change != nil {
    html = memoiser.Render(newTree) // structural change - re-render
} else {
    for _, p := range patches {
        // only the changed regions
    }
}
```

## Render function patterns

### Pattern 1: Memoise inside Dynamic (preferred)

The Memoiser finds the memoisation key on the Dynamic node's child.
On a cache hit, the closure never executes.

```go
div.New(
    node.Memoise(state.Items.Version(), func() node.Node {
        return renderLargeTable(state.Items.Val)
    }),
).Dynamic("items")
```

### Pattern 2: Memoise wrapping Dynamic

The Memoiser propagates the ancestor key to the Dynamic descendant.
On a cache hit, the snapshot comparison is skipped. The closure still
executes to produce the tree structure, but no HTML is generated.

```go
node.Memoise(state.Items.Version(), func() node.Node {
    return itemsTable(state.Items.Val) // returns node with .Dynamic("items")
})
```

Pattern 1 is preferred because the closure is fully skipped on a hit.
Pattern 2 is supported for convenience when the Dynamic key is set
inside a component function.

## Full example

```go
func render(state State) node.Node {
    return div.New(
        div.New(
            node.Memoise(state.Items.Version(), func() node.Node {
                return renderTable(state.Items.Val)
            }),
        ).Dynamic("items"),
        div.New(
            span.Text(strconv.Itoa(state.Count)),
        ).Dynamic("counter"),
    )
}
```

Dynamic regions without a `node.Memoise` key (neither as a child nor
as an ancestor) are always re-rendered - treated as a cache miss. The
Memoiser does not fall back to content-based diffing for non-memoised
nodes.

## DiffKey

Like the Differ, the Memoiser supports targeted single-key diffs:

```go
patch := memoiser.DiffKey("items", renderTable(newItems))
if patch != nil {
    // send patch
}
```

`DiffKey` does not check memoisation keys - the developer is
explicitly targeting this key, so the closure runs unconditionally.

## Stats

After each `Diff` call, `Stats()` returns the hit and miss counts:

```go
patches, change := memoiser.Diff(tree)
hits, misses := memoiser.Stats()
```

A hit means the memoisation key matched and the subtree was skipped.
A miss means the key differed (or was absent) and the subtree was
re-rendered. Zero overhead - just two integer increments per memoised
node during the tree walk.

## Export/Import

Like the Differ, the Memoiser supports snapshot persistence. The
exported data includes memoisation keys alongside the snapshot HTML.

```go
data := memoiser.Export()
memoiser.Clear()
memoiser.Import(data)
```

## Comparison with Differ

| | Differ | Memoiser |
|---|---|---|
| Skips unchanged subtrees | No - always re-renders, compares HTML | Yes - matching keys skip entirely |
| Requires `node.Memoise` | No | Yes, for each Dynamic region |
| Content-based diffing | Yes - compares rendered HTML | Only for misses |
| DiffKey | Yes | Yes |
| Export/Import | Yes | Yes (includes memoisation keys) |
| Best for | Cheap renders, frequent changes | Expensive renders, infrequent changes |

Use one or the other per session, not both. They are standalone
engines with the same external API (`Render`, `Diff`, `DiffKey`,
`Export`, `Import`, `Clear`).
