# Fluent JIT

Just-In-Time optimisation strategies for the [Fluent](https://github.com/jpl-au/fluent) HTML5 component framework.

## Features

**Compile static content.** Pre-render static portions of your templates once into a `[]byte`, then execute a linear plan on subsequent renders. Dynamic content is re-evaluated at runtime using path-based navigation.

**Adaptive buffer sizing.** Learn optimal buffer sizes over repeated renders. Reduces memory allocations and garbage collection pressure without manual tuning.

**Flatten for maximum speed.** For fully static content, pre-render everything to a single `[]byte` that's returned directly on every call. Strategy largely expects the use of `.Static()` calls. Dynamic nodes will cause an error when attempting to render.

**Diff engine.** Track keyed dynamic elements across renders and produce targeted patches for live updates. A standalone engine for any reactive/live-update UI. Supports full-tree diffs and single-key diffs via `DiffKey`.

**Memoiser.** An alternative to the Differ that skips unchanged subtrees entirely. A region carries a cache version - chained as `.Memoise(version)` on a keyed element, or wrapped in `jit.Memoise` - and when the version matches the previous render, the region is skipped. `jit.Shared` regions go further, caching rendered bytes across every session in the process. Use the Differ or the Memoiser per session, not both.

## Documentation

- [Getting Started](docs/getting-started.md) - add JIT to your Fluent app, step by step
- [Differ](docs/diff.md) - keyed element tracking, targeted patches, snapshot persistence
- [Memoiser](docs/memoise.md) - skip unchanged subtrees with cache versions
- `AGENTS.md` - comprehensive technical reference for LLMs

## Install

```bash
go get github.com/jpl-au/fluent-jit
```

Requires [Fluent](https://github.com/jpl-au/fluent) as a dependency.

## Optimisation Strategies

Fluent JIT provides three render strategies - each with an Instance API and a Global API - plus two instance-only diff engines (Differ and Memoiser) for live updates.

### Compile

Works with any Fluent code. Analyses the node tree once, pre-renders static portions, and uses path-based navigation to re-evaluate dynamic nodes.

```go
// Instance API - fine-grained control
compiler := jit.NewCompiler()
compiler.Render(myTemplate, w)  // First call builds plan + renders
compiler.Render(sameStructure, w)  // Reuses plan, re-evaluates dynamic content

// Global API - string-keyed registry
jit.Compile("homepage", myTemplate, w)
```

**How it works:**
- Static subtrees become raw `[]byte` chunks
- Dynamic nodes (`Text()`, `Textf()`, `RawText()`, `RawTextf()`, `Condition()`, `Func()`, `Funcs()`) store paths for tree navigation
- Adaptive buffer sizing optimises memory allocation over time

**Important:** The compiler expects the same tree structure on each call. Static content is frozen at first render; dynamic content is re-evaluated from the new tree.

### Tune

Adaptive buffer sizing for any content. Learns optimal buffer sizes without compile-time analysis.

```go
// Instance API
tuner := jit.NewTuner()
tuner.Render(myTemplate, w)

// Global API
jit.Tune("user-profile", myTemplate, w)
```

**Use when:**
- Content patterns change over time
- Attribute values vary between renders
- You want buffer optimisation without compilation overhead

### Flatten

Only works with fully static content. Pre-renders everything to a single `[]byte`.

```go
// Instance API - returns error if dynamic content found
flattener, err := jit.NewFlattener(staticTemplate)
if err != nil {
    // Contains dynamic content
}
flattener.Render(w)

// Global API - falls back to normal render if dynamic
jit.Flatten("footer", staticTemplate, w)
```

**Use for:** Headers, footers, navigation, any content that never changes.

### Differ

Tracks keyed dynamic elements across renders and produces targeted patches for live updates. A general-purpose diff engine, usable by any reactive UI layer.

```go
differ := jit.NewDiffer()

// Initial render - stores snapshots of all keyed elements
html := differ.RenderBytes(tree)

// After state change - returns only what changed
patches, change := differ.Diff(newTree)

if change != nil {
    // Outermost keys were added, removed, or reordered.
    // change.String() describes what happened, e.g. "key 'sidebar' added"
    html = differ.RenderBytes(newTree)
} else {
    for _, p := range patches {
        // p.Key identifies which element, p.HTML is the new content
    }
}
```

Mark elements for tracking with `.Dynamic("key")`. Both engines track nested keys. Content-only changes target the affected child; additions, removals and reorders inside a Dynamic container patch that container. A parent patch covers its descendants, so overlapping patches are never emitted. Moves between containers patch their shared keyed ancestor to preserve DOM identity. A full render is required when no keyed container covers the change.

```go
div.New(
    span.Textf("Count: %d", count).Dynamic("count"),  // Tracked
    span.Static("Footer"),                              // Ignored
)
```

Use `Validate` to catch duplicate keys at startup:

```go
if err := differ.Validate(tree); err != nil {
    log.Fatal(err)  // "duplicate dynamic key in render tree: "count""
}
```

### Snapshot persistence

The Differ supports exporting and importing its snapshot state as opaque bytes, useful for offloading disconnected-session snapshots to external storage.

```go
// Export returns the snapshot data as raw bytes (nil if not seeded)
data := differ.Export()

// Import restores snapshots from a prior Export
if err := differ.Import(data); err != nil {
    log.Fatal(err)
}

// Clear releases snapshot buffers back to the pool
differ.Clear()
```

The encoding is opaque - callers must not interpret or manipulate the bytes. `Export` is non-destructive and does not clear the Differ's state.

### DiffKey (targeted single-key diff)

When you know exactly which key changed, `DiffKey` re-renders and
diffs the supplied subtree against its stored snapshot without
evaluating the rest of the tree.

```go
patch := differ.DiffKey("count", span.Textf("Count: %d", newCount).Dynamic("count"))
if patch != nil {
    // patch.Key is "count", patch.HTML is the new content
}
```

`DiffKey` refreshes nested snapshots and splices the new bytes into
enclosing snapshots, so subsequent diffs compare against the content
already sent to the client. Unrelated regions are unaffected. The
Memoiser also invalidates enclosing versions after a targeted change.

### Memoiser

An alternative to the Differ that skips unchanged subtrees. Each
Dynamic region carries a cache version. When the version matches the
previous render, the region is skipped - no HTML is produced and no
diff is done.

```go
memoiser := jit.NewMemoiser()

// Initial render - stores snapshots and memoisation versions
html := memoiser.RenderBytes(tree)

// After state change - skips unchanged subtrees
patches, change := memoiser.Diff(newTree)
```

The Memoiser is a standalone engine, not a wrapper around the
Differ. Use one or the other per session, not both. Both support
`DiffKey` for targeted single-key diffs.

A region gets its version by chaining `.Memoise(version)` on a keyed
element:

```go
div.New(rows...).Dynamic("board").Memoise(version)
```

or by wrapping it in `jit.Memoise`, which also defers building the
subtree until it is actually needed:

```go
div.New(
    jit.Memoise(version, func() node.Node {
        return expensiveRender()
    }),
).Dynamic("items")
```

Either way, when `version` matches the previous render the region is
reused from its stored snapshot; when it changes, the subtree renders
and is diffed against that snapshot.

### Shared regions

`jit.Shared` marks a region whose rendered bytes may be reused across
every session in the process, not just across renders of one session.
The first session to render a given key populates a process-global
cache; every other session with the same key is served those bytes.
Use it for regions that render identically for every user - a shared
header, a broadcast leaderboard.

```go
div.New(
    jit.Shared("leaderboard:"+boardVersion, func() node.Node {
        return renderBoard(board)
    }),
).Dynamic("board")
```

The key must be globally unique and fully determine the rendered bytes:
namespace it and derive it from the content (`"nav:v3"`), never from
per-session state. Tune the cache at startup with `jit.SetSharedCacheSize`,
`jit.SetSharedCacheBudget`, and clear it with `jit.ResetSharedCache`.

## Configuration

Both Compiler and Tuner support custom configuration:

```go
// Compiler configuration
compiler := jit.NewCompiler(&jit.CompilerCfg{
    Threshold:    15,  // Deviation % before updating buffer stats
    Max:          5,   // Samples before establishing baseline
    Variance:     20,  // Threshold % for detecting size changes
    GrowthFactor: 115, // Multiplier % for average size
})

// Or configure after creation
compiler.Configure(threshold, max, variance, growthFactor)

// Tuner configuration
tuner := jit.NewTuner(&jit.TunerCfg{
    Max:          5,
    Variance:     20,
    GrowthFactor: 115,
})
```

## Global API Memory Warning

The global API uses `sync.Map` registries that grow indefinitely. Use constant string IDs (e.g., `"header"`, `"footer"`). If using dynamic IDs, manually reset when no longer needed.

```go
// Clear specific entries
jit.ResetCompile("homepage")
jit.ResetTune("user-profile")
jit.ResetFlatten("footer")

// Clear all entries
jit.ResetCompile()
jit.ResetTune()
jit.ResetFlatten()
```

## When to Use JIT

The base Fluent API already performs well with automatic buffer pooling. JIT optimisation is for squeezing out extra performance in high-throughput scenarios.

**Recommendations:**
1. Build and test without JIT first
2. Profile to identify actual bottlenecks
3. Apply JIT selectively where it matters

## Profile-Guided Optimisation (PGO)

Applications using Fluent JIT benefit from [Profile-Guided Optimisation](https://go.dev/doc/pgo) (Go 1.21+). PGO uses a CPU profile from your running application to make more aggressive inlining decisions at compile time, improving the JIT compilation, tuning, and flattening paths. Expect **10-20% speed improvements** with no code changes.

1. Collect a CPU profile under realistic load:
   ```bash
   curl -o default.pgo http://localhost:8080/debug/pprof/profile?seconds=30
   ```
2. Place `default.pgo` in your main package directory
3. `go build` - PGO is applied automatically

Allocations are unaffected; PGO improves speed only. Collect fresh profiles periodically as your application evolves.

## Licence

MIT
