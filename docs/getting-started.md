# Getting Started with Fluent JIT

This guide walks through adding JIT optimisation to a Fluent application,
starting simple and building up to live updates.

## Prerequisites

A working [Fluent](https://github.com/jpl-au/fluent) application that
renders HTML. Install fluent-jit alongside it:

```bash
go get github.com/jpl-au/fluent-jit
```

## Step 1: Tune buffer sizes

The simplest optimisation. No code changes to your templates - just
wrap the render call. Tune learns optimal buffer sizes over repeated
renders, reducing allocations and GC pressure.

```go
import "github.com/jpl-au/fluent-jit"

var tuner = jit.NewTuner()

func handler(w http.ResponseWriter, r *http.Request) {
    page := buildPage(r)
    tuner.Render(page, w)
}
```

Or use the global API if you have multiple templates:

```go
func homeHandler(w http.ResponseWriter, r *http.Request) {
    jit.Tune("home", buildHomePage(), w)
}

func aboutHandler(w http.ResponseWriter, r *http.Request) {
    jit.Tune("about", buildAboutPage(), w)
}
```

## Step 2: Flatten static content

Headers, footers, navigation - anything that never changes. Flatten
pre-renders to raw bytes once and returns them directly on every call.

```go
var headerFlat, _ = jit.NewFlattener(
    header.New(
        nav.New(
            a.Static("Home").Href("/"),
            a.Static("About").Href("/about"),
        ).Class("nav"),
    ).Class("site-header"),
)

func handler(w http.ResponseWriter, r *http.Request) {
    headerFlat.Render(w) // raw bytes, no rendering
    tuner.Render(page, w) // dynamic content
}
```

Use `Static()` for all content passed to Flatten. Dynamic nodes
(`Text()`, `Func()`, etc.) cause an error at initialisation.

## Step 3: Compile mixed templates

For templates with both static and dynamic parts. The compiler
analyses the tree once, pre-renders the static portions, and uses
path-based navigation to re-evaluate only the dynamic nodes.

```go
var pageCompiler = jit.NewCompiler()

func handler(w http.ResponseWriter, r *http.Request) {
    page := buildPage(r) // same tree structure, different data
    pageCompiler.Render(page, w)
}
```

The compiler expects the same tree structure on each call. Static
content is frozen at first render; dynamic content (`Text()`,
`Condition()`, `Func()`, etc.) is re-evaluated from the new tree.

## Step 4: Add reactive tracking

For live updates (over any transport you wire up), mark elements that
change between renders:

```go
func render(state State) node.Node {
    return div.New(
        span.Textf("Count: %d", state.Count).Dynamic("count"),
        span.Text(state.Status).Dynamic("status"),
        span.Static("Footer"),  // not tracked
    )
}
```

`.Dynamic()` is a chainable method every Fluent element provides as an
engine hook - plain rendering ignores it (beyond rendering the key as
the element's `id` attribute), so the same tree renders identically
outside the JIT.

The Differ compares renders and produces targeted patches:

```go
differ := jit.NewDiffer()

// Initial render
html := differ.RenderBytes(render(state))

// After state change
state.Count++
patches, change := differ.Diff(render(state))

if change != nil {
    // Keys were added, removed, or reordered - full re-render needed
    html = differ.RenderBytes(render(state))
} else {
    for _, p := range patches {
        // p.Key = "count", p.HTML = new rendered content
    }
}
```

See [diff.md](diff.md) for the full Differ API.

## Step 5: Memoise expensive subtrees

When some parts of your tree are expensive to render and rarely
change, use `jit.Memoise` to skip them entirely:

```go
func render(state State) node.Node {
    return div.New(
        div.New(
            jit.Memoise(state.Items.Version(), func() node.Node {
                return renderLargeTable(state.Items.Val)
            }),
        ).Dynamic("items"),
        span.Textf("Count: %d", state.Count).Dynamic("count"),
    )
}
```

When the version matches the previous render, the closure never runs
and the stored snapshot is reused. For a subtree that is cheap to
build, you can skip the closure and chain the version on the keyed
element instead:

```go
div.New(renderRows(state.Items.Val)...).Dynamic("items").Memoise(state.Items.Version())
```

For regions that render identically for every user - a shared header,
a broadcast leaderboard - `jit.Shared` caches the rendered bytes across
sessions in the process. See [memoise.md](memoise.md) for the full
Memoiser API, including shared regions and their cache tuning.

## Choosing a strategy

| Strategy | When to use |
|----------|-------------|
| Tune | First optimisation to apply - zero code changes, reduces allocations |
| Flatten | Fully static content (headers, footers, boilerplate) |
| Compile | Templates rendered many times with same structure, different data |
| Differ | Live updates - track what changed between renders |
| Memoiser | Live updates with expensive subtrees that rarely change |

Start with Tune. Add Flatten for static fragments. Move to Compile
for hot templates. Add Differ/Memoiser only when you need live
updates or targeted patching.
