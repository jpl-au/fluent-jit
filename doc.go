// Package jit provides Just-In-Time optimisation strategies for Fluent
// HTML rendering. Each strategy targets a different performance profile.
// The base Fluent API already performs well with automatic buffer pooling;
// JIT is for squeezing out extra performance in high-throughput scenarios.
//
// # Choosing a Strategy
//
// Start by asking what your content looks like and what you need back.
//
// Is the content fully static (no Text, Func, Condition)?
//
//	Yes -> Flatten. Pre-renders once to []byte. Subsequent calls are
//	       a memory copy. Use for headers, footers, navigation, any
//	       content built entirely from Static() calls.
//
// Does the content mix static and dynamic parts?
//
//	Yes, and I just need fast renders -> Compile. Freezes static
//	       portions on first render, re-evaluates only dynamic nodes
//	       via path-based navigation. Best general-purpose strategy.
//
//	Yes, but the tree structure changes between renders -> Tune.
//	       Adaptive buffer sizing without structural assumptions.
//	       No compilation step, no frozen plan. Works with any content
//	       that changes shape over time.
//
// Do I need to know what changed between renders (patches)?
//
//	Yes, and rebuilding the tree is cheap -> Differ. You rebuild the
//	       full tree on each state change. The Differ walks both trees,
//	       compares keyed elements, and returns Patch values for only
//	       the elements whose HTML actually changed.
//
//	Yes, but some subtrees are expensive to rebuild -> Memoiser.
//	       Wraps expensive subtrees in node.Memoise with a cache key.
//	       When the key matches the previous render, the closure never
//	       runs. Only changed subtrees produce new HTML for diffing.
//
// # Differ vs Memoiser
//
// These two share the same interface (Render, Diff, DiffKey, Export,
// Import) and produce the same output ([]Patch, *StructuralChange).
// They differ in how they decide whether content changed.
//
// The Differ is content-based. It renders every keyed element on every
// Diff call and compares the HTML bytes against stored snapshots. If
// the bytes match, no patch is emitted. This is simple and correct but
// means every closure runs on every diff, even if nothing changed.
//
//	differ := jit.NewDiffer()
//	html := differ.Render(buildTree(state))     // initial render
//	patches, change := differ.Diff(buildTree(newState))  // full re-render + compare
//
// The Memoiser is key-based. Each Dynamic region wraps its content in
// node.Memoise with a cache key (typically a version counter, hash, or
// timestamp). When the key matches, the closure is skipped entirely -
// no HTML is produced, no comparison is needed. When the key differs,
// the closure runs and the result is compared against the snapshot.
//
//	memoiser := jit.NewMemoiser()
//	html := memoiser.Render(buildTree(state))   // initial render
//	patches, change := memoiser.Diff(buildTree(newState)) // skips unchanged closures
//
// Use Differ when:
//   - Rebuilding the tree is cheap (simple templates, small data)
//   - You don't have natural cache keys for your data
//   - You want the simplest mental model (rebuild everything, diff finds changes)
//
// Use Memoiser when:
//   - Some subtrees are expensive (database queries, complex formatting)
//   - You have natural version identifiers (timestamps, counters, hashes)
//   - You want to avoid running closures for unchanged regions
//   - You have many keyed elements but few change on each update
//
// Both support DiffKey for targeted single-key diffs when you know
// exactly which element changed. DiffKey is over 1,000x faster than a
// full Diff for targeting one key out of many.
//
// Use one or the other per session, not both. They maintain independent
// snapshot state and mixing them produces incorrect diffs.
//
// # Keying Nodes for Tracking
//
// The diff engine tracks any node that satisfies [node.Dynamic] and returns
// a non-empty key from DynamicKey(). Elements use .Dynamic("key") to set
// this. Function and conditional nodes support the same method:
//
//	// Element - tracked as "counter"
//	span.Textf("Count: %d", n).Dynamic("counter")
//
//	// Function - tracked as "greeting"
//	node.Func(func() node.Node {
//	    return div.Text(greetUser())
//	}).Dynamic("greeting")
//
//	// Conditional - tracked as "auth"
//	node.When(loggedIn, welcomePanel).Dynamic("auth")
//
//	// Multi-node function - tracked as "items"
//	node.Funcs(func() []node.Node {
//	    return buildItems(data)
//	}).Dynamic("items")
//
// Without a key, the differ cannot produce targeted patches for that node.
// Keys must be unique within a render tree.
//
// # Combining Strategies
//
// Strategies operate at different levels and can be combined:
//
//   - Compile + Differ: Compile handles the render path (freezing static
//     content), Differ handles the diff path (tracking changes). Use
//     Compile for the initial full render, Differ for subsequent updates.
//
//   - Flatten for static regions, Compile for dynamic templates: Different
//     parts of the page can use different strategies. A static nav bar
//     uses Flatten; the main content area uses Compile.
//
// # Instance API vs Global API
//
// Each strategy has two ways to use it:
//
//   - Instance API: Create with NewCompiler, NewTuner, NewFlattener,
//     NewDiffer, or NewMemoiser. You own the lifecycle. Use when you
//     need per-session or per-component control.
//
//   - Global API: Package-level Compile, Tune, and Flatten functions
//     with string IDs. Managed in a global registry. Convenient for
//     application-wide templates with fixed IDs. The registry grows
//     indefinitely - use constant IDs, not user-derived strings.
//
// The Differ and Memoiser are instance-only. They track per-session
// state (snapshots, keys, ordering) that doesn't suit a global registry.
package jit
