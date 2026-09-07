# Fluent JIT LLM Guide

Fluent JIT provides Just-In-Time optimisation strategies for HTML rendering. It depends on [Fluent](https://github.com/jpl-au/fluent) and provides three render strategies and two diff engines.

Render strategies (each has an Instance API and a Global API):

1. **Flatten** - Pre-render fully static content to raw bytes
2. **Tune** - Adaptive buffer sizing without compilation
3. **Compile** - Pre-render static content, path-based dynamic node evaluation, adaptive buffer sizing

Diff engines (instance-only, for live updates):

4. **Differ** - Track keyed dynamic elements across renders and produce targeted patches by comparing rendered HTML
5. **Memoiser** - Like the Differ, but skips a region entirely when its cache version is unchanged, and optionally caches shared regions across sessions

## Core Concepts

### Static vs Dynamic Content

The JIT strategies distinguish between static and dynamic content:

**Static content** (compiled once):
- `Static()` text nodes
- Element structure and attributes
- Structural elements with static children

**Dynamic content** (re-evaluated each render):
- `Text()`, `Textf()` - escaped dynamic text
- `RawText()`, `RawTextf()` - unescaped dynamic text
- `node.Condition()` - conditional rendering
- `node.Func()`, `node.Funcs()` - function components

```go
div.New(
    h1.Static("Welcome"),      // Static - compiled once
    p.Text(user.Name),         // Dynamic - re-evaluated
    node.When(user.IsAdmin,    // Dynamic - condition evaluated at render
        span.Static("Admin"),
    ),
).Class("card")  // Static - attribute frozen at first compile
```

### Fluent Element Attributes

Elements have named convenience methods for common HTML attributes. These are chainable:

```go
a.Static("Home").Href("/").Class("nav-link").ID("home-link")
img.Image("/logo.svg", "Logo").Class("logo")
meta.Charset(charset.UTF8)
```

Methods for enumerated attributes accept typed constants from `github.com/jpl-au/fluent/html5/attr/...` (e.g. `charset.UTF8`, `rel.Stylesheet`), not raw strings - a raw string is a compile error. See fluent's AGENTS.md for the full constant reference.

For attributes without a convenience method, use `SetAttribute()`. **This method does not return the element and cannot be chained:**

```go
el := div.New(p.Static("Hello")).Class("card")
el.SetAttribute("role", "region")
el.SetAttribute("tabindex", "0")
```

For `data-*` and `aria-*` attributes, use `SetData()` and `SetAria()`. These are chainable:

```go
div.New(
    span.Static("Tooltip text"),
).SetData("toggle", "tooltip").SetData("placement", "top").SetAria("label", "Help")
// Renders: data-toggle="tooltip" data-placement="top" aria-label="Help"
```

**There is no `.Attr()` method.** Use `SetAttribute()`, `SetData()`, or `SetAria()` as shown above.

Values passed to the typed methods, `SetAttribute()`, `SetData()`, and `SetAria()` are HTML-escaped at set time, and URL sinks (`href`, `src`, `action`, `formaction`, `<object>` `data`) are scheme-filtered - so untrusted values are safe by default and you should not pre-escape them. For a value you have already sanitised and trust verbatim, `SetAttributeRaw(key, value)` stores it without escaping (the attribute mirror of `RawText`).

### Configuration Values

Configuration values like `GrowthFactor: 115` and `Variance: 20` are integers representing percentages. This avoids floating point operations on the hot path. A `GrowthFactor` of 115 means 115% (or 1.15x), giving 15% headroom above the average buffer size.

## API Reference

### Flattener

The simplest strategy. Pre-renders fully static content to a single `[]byte` at initialisation. Returns an error if dynamic content is detected.

```go
// Instance API - returns error if dynamic content found
flattener, err := jit.NewFlattener(staticNode)
if err != nil {
    // Node contains dynamic content
}
output := flattener.RenderBytes()
flattener.Render(w)  // Writes to w (fire-and-forget); WriteTo(w) returns (int64, error)
```

### Tuner

Adaptive buffer sizing without compilation. Learns optimal buffer sizes over repeated renders to reduce allocations.

```go
// Instance API
tuner := jit.NewTuner()
tuner.Render(node, w)  // or tuner.RenderBytes(node) for []byte

// With configuration
tuner := jit.NewTuner(&jit.TunerCfg{
    Max:          5,   // Samples before establishing baseline
    Variance:     20,  // Threshold % for size change detection
    GrowthFactor: 115, // Multiplier % for average size
})

// Configuration after creation
tuner.Configure(max, variance, growthFactor)

// Reset statistics
tuner.Reset()
```

### Compiler

The most comprehensive strategy. Combines execution plan compilation with adaptive buffer sizing. On first render, analyses the node tree and builds an execution plan:

1. **StaticContent** - Pre-rendered `[]byte` chunks for static subtrees
2. **DynamicPath** - `[]int` paths to navigate to dynamic nodes

On subsequent renders, the plan executes linearly: write static bytes, navigate to dynamic nodes and render them, repeat. Buffer sizing adapts over time.

```go
// Instance API
compiler := jit.NewCompiler()
compiler.Render(node, w)  // First call: build plan + render; subsequent: reuse plan

// With configuration
compiler := jit.NewCompiler(&jit.CompilerCfg{
    Threshold:    15,  // Deviation % before updating buffer stats (default 15)
    Max:          5,   // Samples before establishing baseline (default 5)
    Variance:     20,  // Threshold % for size change detection (default 20)
    GrowthFactor: 115, // Multiplier % for average size (default 115)
})

// Configuration after creation
compiler.Configure(threshold, max, variance, growthFactor)

// RenderBytes returns []byte; Render writes to a writer
output := compiler.RenderBytes(node)
compiler.Render(node, w)  // Writes to w (fire-and-forget)
```

### Global API

String-keyed registry using `sync.Map`:

```go
// Flatten (falls back to normal render if dynamic)
jit.Flatten("id", node, w)                     // fire-and-forget
n, err := jit.FlattenWriteTo("id", node, w)    // returns (int64, error)
output := jit.FlattenBytes("id", node)

// Tune
jit.Tune("id", node, w)
n, err = jit.TuneWriteTo("id", node, w)
output = jit.TuneBytes("id", node)

// Compile
jit.Compile("id", node, w)
n, err = jit.CompileWriteTo("id", node, w)
output = jit.CompileBytes("id", node)

// Pre-configure before first use
jit.TuneConfig("id", jit.TunerCfg{...})
jit.CompileConfig("id", jit.CompilerCfg{...})

// Reset entries
jit.ResetFlatten("id")
jit.ResetFlatten()
jit.ResetTune("id")
jit.ResetTune()
jit.ResetCompile("id1", "id2")  // Specific IDs
jit.ResetCompile()              // All entries
```

## Adaptive Sizing

Both Compiler and Tuner use `AdaptiveSizer` for buffer optimisation.

**Two-phase operation:**
1. **Sampling phase** - Collects render size samples to establish baseline
2. **Baseline phase** - Uses established size with variance monitoring

**Performance characteristics:**
- Hot path (`GetBaseline`): lock-free atomic read
- Warm path (variance checks): occasional mutex
- Cold path (sampling): mutex for calculations

**Configuration parameters:**
- `Max` - Samples before establishing baseline (default: 5)
- `Variance` - Threshold % for detecting pattern changes (default: 20)
- `GrowthFactor` - Percentage multiplier applied to average (default: 115, i.e., 15% headroom)

## Usage Patterns

### Static-Only Content

```go
// Site header - never changes, perfect for Flatten
var headerFlattener, _ = jit.NewFlattener(
    header.New(
        nav.New(
            a.Static("Home").Href("/"),
            a.Static("Products").Href("/products"),
            a.Static("About").Href("/about"),
            a.Static("Contact").Href("/contact"),
        ).Class("nav"),
        div.New(
            img.Image("/logo.svg", "Company Logo"),
            span.Static("Company Name"),
        ).Class("logo"),
    ).Class("site-header"),
)

// Common head elements
var headFlattener, _ = jit.NewFlattener(
    html.Fragment(
        meta.UTF8(),
        meta.Viewport("width=device-width, initial-scale=1"),
        link.Stylesheet("/styles.css"),
        link.Icon("/favicon.ico"),
    ),
)

// Footer with static content
var footerFlattener, _ = jit.NewFlattener(
    footer.New(
        p.Static("© 2024 Company Name. All rights reserved."),
        nav.New(
            a.Static("Privacy").Href("/privacy"),
            a.Static("Terms").Href("/terms"),
        ).Class("footer-nav"),
    ).Class("site-footer"),
)

func handler(w http.ResponseWriter, r *http.Request) {
    w.Write([]byte("<!DOCTYPE html><html><head>"))
    headFlattener.Render(w)
    w.Write([]byte("</head><body>"))
    headerFlattener.Render(w)

    // Dynamic page content here...

    footerFlattener.Render(w)
    w.Write([]byte("</body></html>"))
}
```

### Template with Mixed Content

```go
func Page(pageTitle string, items []Item) node.Node {
    return html.New(
        head.New(
            title.Text(pageTitle),       // Dynamic
            link.Stylesheet("/app.css"), // Static
        ),
        body.New(
            header.Static("My Site"),    // Static
            primary.New(
                h1.Text(pageTitle),      // Dynamic
                ItemList(items),         // Dynamic (contains Func)
            ),
            footer.Static("Footer"),     // Static
        ),
    )
}

func ItemList(items []Item) node.Node {
    return node.Funcs(func() []node.Node {
        nodes := make([]node.Node, len(items))
        for i, item := range items {
            nodes[i] = li.Text(item.Name)
        }
        return nodes
    })
}
```

### HTTP Handler with Compiler

```go
var userCardCompiler = jit.NewCompiler()

func userHandler(w http.ResponseWriter, r *http.Request) {
    name := r.PathValue("name")
    userCardCompiler.Render(UserCard(name), w)
}
```

### Global API for Route-Based Templates

```go
func homeHandler(w http.ResponseWriter, r *http.Request) {
    jit.Compile("home", HomePage(), w)
}

func aboutHandler(w http.ResponseWriter, r *http.Request) {
    jit.Compile("about", AboutPage(), w)
}

func userHandler(w http.ResponseWriter, r *http.Request) {
    userID := r.PathValue("id")
    // WARNING: Dynamic IDs grow the registry indefinitely
    // Use Instance API or reset manually
    jit.Compile("user-"+userID, UserPage(userID), w)
}
```

## When to Use Each Strategy

| Strategy | Content Type | Use Case |
|----------|--------------|----------|
| Flatten | Fully static | Headers, footers, navigation, boilerplate |
| Tune | Any | Content with variable sizes, want buffer optimisation only |
| Compile | Mixed static/dynamic | Templates rendered many times with different data |
| Differ | Dynamic (keyed) | Live updates - tracks keyed elements, produces patches |
| Memoiser | Dynamic (keyed + versioned) | Like Differ but skips a region when its cache version is unchanged; also caches `jit.Shared` regions across sessions |

## Common Pitfalls

### Flatten with Dynamic Content

Flatten only works with static content:

```go
flattener, err := jit.NewFlattener(div.Text(user.Name))
// err != nil because Text() is dynamic

flattener, err := jit.NewFlattener(div.Static("Copyright 2024"))
// Use Static() for Flatten
```

### Dynamic IDs Without Cleanup

Dynamic IDs cause unbounded memory growth in the global registry:

```go
func handler(w http.ResponseWriter, r *http.Request) {
    userID := r.PathValue("id")
    jit.Compile(userID, UserPage(userID), w)  // Registry grows indefinitely
}
```

Use the Instance API instead:

```go
var userCompiler = jit.NewCompiler()

func handler(w http.ResponseWriter, r *http.Request) {
    userID := r.PathValue("id")
    userCompiler.Render(UserPage(userID), w)
}
```

### Passing Different Structures to Compiler

The compiler expects consistent tree structure across calls:

```go
compiler := jit.NewCompiler()

compiler.Render(div.New(p.Text("Hello")), w)
compiler.Render(div.New(span.Text("World")), w)  // Different structure - may produce incorrect output
```

## Differ

The Differ tracks rendered output of keyed dynamic nodes across renders and produces targeted patches when content changes. It is a standalone diff engine for live updates.

### Lifecycle

```go
differ := jit.NewDiffer()

// 1. Initial render - stores snapshots of all keyed elements
html := differ.RenderBytes(tree)

// 2. After state change - compare against stored snapshots
patches, change := differ.Diff(newTree)

// 3. If change is non-nil, outermost keys were added/removed/reordered
if change != nil {
    html = differ.RenderBytes(newTree)  // Re-render + reset baseline
    // change.String() → "key 'sidebar' added"
}

// 4. Otherwise, apply targeted patches
for _, p := range patches {
    // p.Key, p.HTML
}

// 5. Export/Import for persistence across disconnects
data := differ.Export()   // Serialise snapshots to []byte
differ.Clear()            // Release buffers to pool
differ.Import(data)       // Restore from prior export
```

### Key concepts

**Nested key tracking.** Both engines track every Dynamic region and its immediate keyed children. Content-only changes target the affected children. Changes to a container's own HTML or child membership target that container and suppress descendant patches.

**Key order detection.** Each container retains its child key order. Nested additions, removals and reorders produce a container patch. Moves between containers target their shared keyed ancestor to preserve DOM identity. Outermost membership/order changes and moves without a shared keyed ancestor return StructuralChange and require a full render. Returned patches follow tree order.

**Structural change diagnostics.** When `Diff()` returns a `*StructuralChange`, it reports exactly what happened:

- `change.Added` - keys in the new tree that weren't in the old
- `change.Removed` - keys in the old tree that aren't in the new
- `change.Reordered` - same keys, different order
- `change.String()` - human-readable description (e.g. `"key 'help' added"`, `"keys reordered"`)

A live-update layer can use this to log actionable diagnostics so developers know when and why a root morph was triggered.

**Pooled buffers.** Snapshots use `fluent.NewBuffer` / `fluent.PutBuffer` to avoid allocation overhead. Superseded snapshots are returned to the pool after the new baseline is accepted.

**Validation.** `Differ.Validate(tree)` checks for duplicate dynamic keys. Duplicate keys cause the diff engine to lose track of elements - only the last one visited would be stored. Returns `ErrDuplicateKey` for programmatic checking.

**Snapshot persistence.** Three methods support serialising and restoring Differ state, for offloading disconnected-session snapshots to external storage:

- `Export() []byte` - serialises all snapshot data into an opaque byte slice. Returns nil if the Differ has not been seeded (no prior `Render`). Non-destructive - the Differ's state is unchanged after export.
- `Import([]byte) error` - restores snapshots from bytes previously returned by `Export`. The opaque encoding contains HTML, nesting, child byte ranges and memoisation versions. Failed imports preserve the current baseline. On error, `Import` cleans up any already-allocated buffers so nothing leaks back to the pool.
- `Clear()` - releases all snapshot buffers back to `fluent.PutBuffer` and resets the Differ to its zero state. Useful after exporting when the Differ is no longer needed.

### DiffKey (targeted single-key diffs)

Re-render and diff a single Dynamic key without walking the full
tree. Returns a `*Patch` if the content changed, nil if unchanged.
Works on both Differ and Memoiser.

```go
// Re-render just the "row-47" key
patch := differ.DiffKey("row-47", renderRow(items[47]))
if patch != nil {
    // send patch to client
}
```

DiffKey refreshes the target and nested snapshots, and splices the new
bytes into enclosing snapshots. Subsequent diffs compare against the
content already sent to the client. Enclosing memoisation versions are
invalidated after a targeted change; unrelated regions retain their hits. It does not check
memoisation versions (on the Memoiser) because the developer is
explicitly targeting this key.

### Dynamic keys

Elements are marked dynamic with `.Dynamic("key")`:

```go
span.Text(count).Dynamic("count")  // Tracked by the Differ
span.Text(value).Dynamic("")        // Empty key - JIT-dynamic but not diff-tracked
span.Static("hello")                // Static - invisible to the Differ
```

## Memoiser

An alternative to the Differ that skips unchanged subtrees. Each
Dynamic region carries a cache version. When the version matches the
previous render at the same tree position, the region is skipped
entirely - no HTML is produced and no comparison is done.

A region gets its version one of two ways:

- **Chained** - `.Memoise(version)` on a keyed element:
  `div.New(rows...).Dynamic("board").Memoise(version)`. The subtree is
  built eagerly, but on a hit it is neither rendered nor diffed. The
  most ergonomic form when the subtree is cheap to build.
- **Wrapped** - `jit.Memoise(version, func() node.Node)`, a lazy node
  whose closure is skipped on a hit. Use this form when building the
  subtree is itself expensive, so construction is deferred too.

Versions are converted to strings using scalar formatting, with
`fmt.Sprint` for other types. Prefer counters or stable string keys
that fully identify the region's content. A parent version governs its
entire subtree: a parent hit skips child version checks. Leave the
parent unversioned when children should update independently. A keyed
child's chained version applies to that child, never to its parent.

```go
memoiser := jit.NewMemoiser()

// Initial render - stores snapshots and memoisation versions
html := memoiser.RenderBytes(tree)

// After state change - skips unchanged subtrees
patches, change := memoiser.Diff(newTree)
```

The Memoiser is a standalone engine, not a wrapper around the
Differ. Use one or the other per session, not both. Both support
DiffKey for targeted single-key diffs.

### Render function pattern

The wrapped `jit.Memoise` form has two valid nesting positions. Both
work: the Memoiser propagates the memo version from an ancestor
`jit.Memoise` node down to a descendant Dynamic node.

**Pattern 1: Memoise inside Dynamic (preferred)**

The Memoiser finds the version on the Dynamic node's child. On a cache
hit, the closure never executes - maximum performance.

```go
div.New(
    jit.Memoise(s.Items.Version(), func() node.Node {
        return renderTable(s.Items.Val)
    }),
).Dynamic("items")
```

**Pattern 2: Memoise wrapping Dynamic**

The Memoiser propagates the ancestor version to the Dynamic descendant.
On a cache hit, the snapshot comparison is skipped. The closure still
executes to produce the tree structure, but no HTML is generated.

```go
jit.Memoise(s.Items.Version(), func() node.Node {
    return itemsTable(s.Items.Val) // returns node with .Dynamic("items")
})
```

Pattern 1 is preferred because the closure is fully skipped on a
hit. Pattern 2 is supported for convenience when the Dynamic key is
set inside a component.

**Full example:**

```go
func render(s State) node.Node {
    return div.New(
        div.New(
            jit.Memoise(s.Items.Version(), func() node.Node {
                return renderTable(s.Items.Val)
            }),
        ).Dynamic("items"),
        div.New(
            span.Text(strconv.Itoa(s.Count)),
        ).Dynamic("counter"),
    )
}
```

Dynamic regions with no version (no chained `.Memoise`, and no
`jit.Memoise` child or ancestor) are always re-rendered (treated as a
miss), then compared with their previous HTML snapshots.

### Shared regions (across sessions)

A plain memoised region is cached within one session. `jit.Shared`
marks a region whose rendered bytes may be reused across every session
in the process, via a process-global fragment cache. The first session
to render a given key populates the cache; every other session with
the same key is served those bytes instead of running the closure. Use
it for regions that render identically for every user - a shared
header, a navigation bar, a live scoreboard broadcast to a room.

```go
div.New(
    jit.Shared("leaderboard:"+boardVersion, func() node.Node {
        return renderBoard(board)
    }),
).Dynamic("board")
```

The contract is stricter than plain memoisation: the key MUST be
globally unique and MUST fully determine the rendered bytes. Namespace
it and derive it from the content (`"nav:v3"`, `"board:"+hash`), never
from per-session state - two sessions with the same key are served the
same bytes. `jit.Shared` enforces none of this; correctness is the
caller's.

The cache is bounded by a two-generation scheme with a per-generation
entry cap (default 2048) and byte budget (default 32MB), so total
residency is at most about twice each figure. Tune it at startup,
before serving traffic:

```go
jit.SetSharedCacheSize(4096)      // per-generation entry cap
jit.SetSharedCacheBudget(64 << 20) // per-generation byte budget
jit.ResetSharedCache()             // empty the cache (keeps size/budget)
n := jit.SharedCacheLen()          // distinct fragments currently resident
```

### Stats

After each Diff (or seeding Render) call, `Stats()` returns the hit and
miss counts:

```go
patches, change := memoiser.Diff(tree)
hits, misses := memoiser.Stats()
sharedHits, sharedMisses := memoiser.SharedStats() // subset resolved via jit.Shared cache
regions := memoiser.Memoised()                     // Dynamic regions that carried a version
```

A hit means the version matched and the subtree was skipped. A miss
means the version differed (or was absent) and the subtree was
re-rendered. `SharedStats()` reports how many regions were resolved
through the process-global `jit.Shared` cache - a subset of the miss
count, since a shared region only reaches the cache when its
per-session version changed. `Memoised()` reports how many Dynamic
regions carried a version in the most recent Diff; zero means the
render tree used no memoisation at all, so the Memoiser degrades to
plain diff behaviour. Overhead is a pair of integer increments per
memoised node during the existing tree walk.

### Key differences from Differ

| | Differ | Memoiser |
|---|---|---|
| Skips unchanged subtrees | No - always re-renders and compares HTML | Yes - matching versions skip entirely |
| Requires a cache version | No | Yes, per Dynamic region (chained `.Memoise` or `jit.Memoise`) |
| Cross-session caching | No | Yes, via `jit.Shared` |
| Content-based diffing | Yes - compares rendered HTML | Only for misses |
| DiffKey | Yes | Yes |
| Export/Import | Yes | Yes (includes memoisation versions) |

## Package Structure

```
fluent-jit/
├── doc.go       # Package overview: strategy selection, Differ vs Memoiser
├── jit.go       # Sentinel errors, config structs, dynamic detection
├── compile.go   # Compiler: execution plan building and rendering
├── tune.go      # Tuner: adaptive buffer sizing wrapper
├── adaptive.go  # AdaptiveSizer: two-phase buffer sizing logic
├── flatten.go   # Flattener: static content pre-rendering
├── diff.go      # Differ: keyed element tracking and targeted patches
├── memoise.go   # Memoiser: version-aware subtree skipping, Stats, DiffKey
├── regions.go   # Shared nested snapshots, rendering and patch selection
├── regions_codec.go # Snapshot hierarchy persistence and validation
├── memonode.go  # Memoise, Shared node constructors; Memoised interface
├── shared.go    # Process-global shared-fragment cache and tuning
├── global.go    # Global API: sync.Map registries and helpers
└── go.mod       # Module definition
```

## Profile-Guided Optimisation (PGO)

Applications using Fluent JIT benefit from [PGO](https://go.dev/doc/pgo) (Go 1.21+). Collect a CPU profile from production, place it as `default.pgo` in the main package, and `go build` applies it automatically. Expect 10-20% speed improvements across compile, tune, and flatten paths with no code changes. Allocations are unaffected - PGO improves inlining decisions only.

## Licence

MIT
