# Memoiser

The Memoiser is an alternative to the [Differ](diff.md) that skips
unchanged subtrees entirely. Where the Differ always re-renders keyed
elements and compares the HTML output, the Memoiser checks a cache
version first and skips the region when the version matches.

Use the Memoiser when your render tree has expensive subtrees that
rarely change. Use the Differ when all subtrees are cheap to render
and content-based comparison is sufficient.

Each Dynamic region carries its cache version one of two ways: chained
as `.Memoise(version)` on the keyed element, or wrapped in a
`jit.Memoise(version, func)` node. (`.Dynamic()` and `.Memoise()` are
chainable hook methods Fluent core provides on every element - plain
rendering ignores them; `jit.Memoise` and `jit.Shared` are node
constructors from this package.) Versions are converted to strings
using scalar formatting, with `fmt.Sprint` for other types. Prefer
counters or stable string keys that fully identify the region's content.

## How it works

1. Give an expensive Dynamic region a cache version - chained
   `.Memoise(version)` or a `jit.Memoise(version, func)` node
2. On first render, the region is rendered and its output stored
3. On subsequent renders, if the version matches, the region is
   skipped and the stored snapshot is reused
4. If the version changes, the region renders, the output is diffed
   against the stored snapshot, and a patch is produced

## Basic usage

```go
memoiser := jit.NewMemoiser()

// Initial render
html := memoiser.RenderBytes(tree)

// After state change
patches, change := memoiser.Diff(newTree)

if change != nil {
    html = memoiser.RenderBytes(newTree) // structural change - re-render
} else {
    for _, p := range patches {
        // only the changed regions
    }
}
```

## Render function patterns

### Chained form

Chain `.Memoise(version)` directly on the keyed element. The subtree
is built eagerly, but on a cache hit it is neither rendered nor
diffed. This is the most ergonomic form when the subtree is cheap to
build.

```go
div.New(renderRows(state.Items.Val)...).Dynamic("items").Memoise(state.Items.Version())
```

### Wrapped form, Memoise inside Dynamic

Wrap the subtree in `jit.Memoise`. The Memoiser finds the version on
the Dynamic node's child, and on a cache hit the closure never
executes - construction is deferred too, which matters when building
the subtree is itself expensive.

```go
div.New(
    jit.Memoise(state.Items.Version(), func() node.Node {
        return renderLargeTable(state.Items.Val)
    }),
).Dynamic("items")
```

### Wrapped form, Memoise wrapping Dynamic

The Memoiser propagates the ancestor version to the Dynamic
descendant. On a cache hit, the snapshot comparison is skipped. The
closure still executes to produce the tree structure, but no HTML is
generated.

```go
jit.Memoise(state.Items.Version(), func() node.Node {
    return itemsTable(state.Items.Val) // returns node with .Dynamic("items")
})
```

Prefer the chained form or Memoise-inside-Dynamic: both fully skip the
render on a hit. Memoise-wrapping-Dynamic is supported for convenience
when the Dynamic key is set inside a component function.

## Full example

```go
func render(state State) node.Node {
    return div.New(
        div.New(
            jit.Memoise(state.Items.Version(), func() node.Node {
                return renderTable(state.Items.Val)
            }),
        ).Dynamic("items"),
        div.New(
            span.Text(strconv.Itoa(state.Count)),
        ).Dynamic("counter"),
    )
}
```

Dynamic regions with no version (no chained `.Memoise`, and no
`jit.Memoise` child or ancestor) are always re-rendered - treated as a
cache miss. Their rendered HTML is compared with the previous snapshot.

## Nested regions

The Memoiser tracks nested Dynamic keys and selects patches using the
same rules as the [Differ](diff.md): content-only changes can target a
child, membership changes target its enclosing container, and parent
patches suppress descendant patches.

A matching parent version skips the entire subtree, including child
version checks. The parent's version must therefore change whenever
any content below it changes. If children should update independently,
leave the parent unversioned and give the children their own versions.
When a parent renders, an unchanged child version can still skip that
child's closure. A version inherited from a wrapper is consumed at the
first Dynamic region; it does not implicitly version every nested key.
A keyed child's chained version applies to that child, never to its parent.

Shared cache entries include nested snapshots as well as HTML. A cache
hit preserves the ability to target a child with `DiffKey` without
running the shared closure. Targeted session updates never alter the
process-global cached entry.

## Shared regions

A plain memoised region is cached within one session. `jit.Shared`
marks a region whose rendered bytes may be reused across every session
in the process, via a process-global fragment cache. The first session
to render a given key populates the cache; every other session with
the same key is served those bytes instead of running the closure. Use
it for regions that render identically for every user - a shared
header, a navigation bar, a live scoreboard broadcast to a room.

```go
div.New(
    jit.Shared("leaderboard:"+state.BoardVersion, func() node.Node {
        return renderBoard(state.Board)
    }),
).Dynamic("board")
```

The contract is stricter than plain memoisation: the key must be
globally unique and must fully determine the rendered bytes. Namespace
it and derive it from the content (`"nav:v3"`, `"board:"+hash`), never
from per-session state - two sessions with the same key are served the
same bytes.

The cache is bounded by a two-generation scheme with a per-generation
entry cap (default 2048) and byte budget (default 32MB); total
residency stays at most about twice each figure. Tune it at startup,
before serving traffic:

```go
jit.SetSharedCacheSize(4096)       // per-generation entry cap
jit.SetSharedCacheBudget(64 << 20) // per-generation byte budget
jit.ResetSharedCache()             // empty the cache (keeps size/budget)
n := jit.SharedCacheLen()          // distinct fragments currently resident
```

## DiffKey

Like the Differ, the Memoiser supports targeted single-key diffs:

```go
patch := memoiser.DiffKey("items", renderTable(newItems))
if patch != nil {
    // send patch
}
```

`DiffKey` does not check the memoisation version - the developer is
explicitly targeting this key, so the subtree renders unconditionally.
The target and its descendants receive fresh snapshots. Enclosing HTML
snapshots are updated too, and enclosing memoisation versions are
invalidated so a later full diff can reconcile the targeted update.
Unrelated memoised regions retain their cache hits.

## Stats

After each `Diff`, `Stats()` returns the hit and miss counts. `Render`
and `Clear` reset these counts; `SharedStats` also reports seeding hits
and misses:

```go
patches, change := memoiser.Diff(tree)
hits, misses := memoiser.Stats()
sharedHits, sharedMisses := memoiser.SharedStats() // subset resolved via the shared cache
regions := memoiser.Memoised()                     // Dynamic regions that carried a version
```

A hit means the version matched and the subtree was skipped. A miss
means the version differed (or was absent) and the subtree was
re-rendered. `SharedStats()` reports the subset of regions resolved
through the process-global `jit.Shared` cache. `Memoised()` reports
how many stored Dynamic regions carry a version, including nested ones;
zero means the tree used no memoisation, so the Memoiser degrades to
plain diff behaviour - a quick way to catch a Memoise-enabled handler
whose render forgot the versions. Overhead is a pair of integer
increments per memoised node during the tree walk.

## Export/Import

Like the Differ, the Memoiser supports snapshot persistence. The
exported data includes memoisation versions and the region hierarchy
alongside the snapshot HTML. A failed import leaves the existing
baseline intact.

```go
data := memoiser.Export()
memoiser.Clear()
memoiser.Import(data)
```

## Comparison with Differ

| | Differ | Memoiser |
|---|---|---|
| Skips unchanged subtrees | No - always re-renders, compares HTML | Yes - matching versions skip entirely |
| Requires a cache version | No | Yes, per Dynamic region (chained `.Memoise` or `jit.Memoise`) |
| Cross-session caching | No | Yes, via `jit.Shared` |
| Content-based diffing | Yes - compares rendered HTML | Only for misses |
| DiffKey | Yes | Yes |
| Export/Import | Yes | Yes (includes memoisation versions) |
| Best for | Cheap renders, frequent changes | Expensive renders, infrequent changes |

Use one or the other per session, not both. They are standalone
engines with the same external API (`Render`, `Diff`, `DiffKey`,
`Export`, `Import`, `Clear`).
