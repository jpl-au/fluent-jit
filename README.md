# Fluent JIT

Just-In-Time optimisation strategies for the [Fluent](https://github.com/jpl-au/fluent) HTML5 component framework.

## Three types of JIT

**Compile static content.** Pre-render static portions of your templates once into a []byte, then execute a linear plan on subsequent renders. Dynamic content is re-evaluated at runtime using path-based navigation.

**Adaptive buffer sizing.** Learn optimal buffer sizes over repeated renders. Reduces memory allocations and garbage collection pressure without manual tuning.

**Flatten for maximum speed.** For fully static content, pre-render everything to a single `[]byte` that's returned directly on every call. Strategy largely expects the use of `.Static()` calls. Dynamic nodes will cause an error when attempting to render.

## Install

```bash
go get github.com/jpl-au/fluent-jit
```

Requires [Fluent](https://github.com/jpl-au/fluent) as a dependency.

## Optimisation Strategies

Fluent JIT provides three strategies, each with an Instance API and a Global API.

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
- Dynamic nodes (`Text()`, `Textf()`, `RawText()`, `RawTextf()`, `Condition()`, `Func()`, `FuncNodes()`, `security.Sanitise()`) store paths for tree navigation
- Adaptive buffer sizing optimises memory allocation over time

**Important:** The compiler expects the same tree structure on each call. Static content is frozen at first render; dynamic content is re-evaluated from the new tree.

### Tune

Adaptive buffer sizing for any content. Learns optimal buffer sizes without compile-time analysis.

```go
// Instance API
tuner := jit.NewTuner()
tuner.Tune(myTemplate).Render(w)

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

## Documentation for LLMs

- `LLM-GUIDE.md` - Technical reference for JIT optimisation strategies

## When to Use JIT

The base Fluent API already performs well with automatic buffer pooling. JIT optimisation is for squeezing out extra performance in high-throughput scenarios.

**Recommendations:**
1. Build and test without JIT first
2. Profile to identify actual bottlenecks
3. Apply JIT selectively where it matters

## Licence

MIT
