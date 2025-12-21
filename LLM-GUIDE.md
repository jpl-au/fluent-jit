# Fluent JIT LLM Guide

Fluent JIT provides Just-In-Time optimisation strategies for HTML rendering. It depends on [Fluent](https://github.com/jpl-au/fluent) and provides three optimisation approaches:

1. **Flatten** - Pre-render fully static content to raw bytes
2. **Tune** - Adaptive buffer sizing without compilation
3. **Compile** - Pre-render static content, path-based dynamic node evaluation, adaptive buffer sizing

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
- `node.Func()`, `node.FuncNodes()` - function components
- `security.Sanitise()` - sanitised content validation

```go
div.New(
    h1.Static("Welcome"),      // Static - compiled once
    p.Text(user.Name),         // Dynamic - re-evaluated
    node.When(user.IsAdmin,    // Dynamic - condition evaluated at render
        span.Static("Admin"),
    ),
).Class("card")  // Static - attribute frozen at first compile
```

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
output := flattener.Render()
flattener.Render(w)  // Writes to w, returns nil
```

### Tuner

Adaptive buffer sizing without compilation. Learns optimal buffer sizes over repeated renders to reduce allocations.

```go
// Instance API
tuner := jit.NewTuner()
tuner.Tune(node).Render(w)

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

// Render returns []byte if no writer provided
output := compiler.Render(node)
compiler.Render(node, w)  // Writes to w, returns nil
```

### Global API

String-keyed registry using `sync.Map`:

```go
// Flatten (falls back to normal render if dynamic)
jit.Flatten("id", node, w)
output := jit.Flatten("id", node)

// Tune
jit.Tune("id", node, w)
output := jit.Tune("id", node)

// Compile
jit.Compile("id", node, w)
output := jit.Compile("id", node)

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
            img.New().Src("/logo.svg").Alt("Company Logo"),
            span.Static("Company Name"),
        ).Class("logo"),
    ).Class("site-header"),
)

// Common head elements
var headFlattener, _ = jit.NewFlattener(
    node.Fragment(
        meta.New().Charset("utf-8"),
        meta.New().Name("viewport").Content("width=device-width, initial-scale=1"),
        link.New().Rel("stylesheet").Href("/styles.css"),
        link.New().Rel("icon").Href("/favicon.ico"),
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
func Page(title string, items []Item) node.Node {
    return html.New(
        head.New(
            title.Text(title),           // Dynamic
            link.Stylesheet("/app.css"), // Static
        ),
        body.New(
            header.Static("My Site"),    // Static
            primary.New(
                h1.Text(title),          // Dynamic
                ItemList(items),         // Dynamic (contains Func)
            ),
            footer.Static("Footer"),     // Static
        ),
    )
}

func ItemList(items []Item) node.Node {
    return node.FuncNodes(func() []node.Node {
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

## Package Structure

```
fluent-jit/
├── jit.go       # Package docs, dynamic detection, config structs
├── compile.go   # Compiler: execution plan building and rendering
├── tune.go      # Tuner: adaptive buffer sizing wrapper
├── adaptive.go  # AdaptiveSizer: two-phase buffer sizing logic
├── flatten.go   # Flattener: static content pre-rendering
├── global.go    # Global API: sync.Map registries and helpers
└── go.mod       # Module definition
```

## License

MIT
