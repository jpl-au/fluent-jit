package jit

import (
	"bytes"
	"io"
	"sync"

	"github.com/jpl-au/fluent"
	"github.com/jpl-au/fluent/node"
	"github.com/jpl-au/fluent/text"
)

// CompiledElement represents a single rendering operation in the execution plan.
// Elements are either pre-rendered static content or dynamic node references.
type CompiledElement interface {
	Render(originalTree node.Node, buf *bytes.Buffer)
}

// StaticContent holds pre-rendered static HTML content as raw bytes.
// Adjacent static nodes are merged into single StaticContent elements for efficiency.
type StaticContent struct {
	Content []byte // Pre-rendered HTML bytes ready for direct buffer writes
}

// Render writes the pre-compiled static content directly to the buffer.
// This is extremely fast as it's just a memory copy operation.
func (sc *StaticContent) Render(_ node.Node, buf *bytes.Buffer) {
	buf.Write(sc.Content)
}

// DynamicPath holds the path to a dynamic node in the tree structure.
// The path is a slice of indices that navigates from root to the dynamic node.
// This enables re-evaluation with new tree instances that share the same structure.
type DynamicPath struct {
	Path []int // Indices to navigate: e.g., [0, 1] means root.Nodes()[0].Nodes()[1]
}

// Render navigates the tree using the stored path and renders the dynamic node.
// This allows different tree instances (with same structure) to render different values.
func (dp *DynamicPath) Render(root node.Node, buf *bytes.Buffer) {
	n := root
	for _, idx := range dp.Path {
		children := n.Nodes()
		if idx >= len(children) {
			return // Path invalid for this tree - safety check
		}
		n = children[idx]
	}
	n.RenderBuilder(buf)
}

// ExecutionPlan contains the compiled sequence of static and dynamic elements.
// The plan is a linear sequence that can be executed without tree traversal.
type ExecutionPlan struct {
	Elements []CompiledElement // Linear sequence of rendering operations
}

// Compiler builds immutable execution plans with optimised buffer sizing.
// It separates static and dynamic content during compilation, then uses
// conditional statistical updates to maintain optimal buffer allocation.
type Compiler struct {
	executionPlan *ExecutionPlan // Built once using sync.Once
	planOnce      sync.Once      // Ensures single plan compilation
	sizer         *AdaptiveSizer // Shared adaptive buffer sizing
	threshold     int            // Deviation threshold percentage for conditional updates
	cfg           *CompilerCfg   // Optional custom configuration
}

// NewCompiler creates a compiler with sensible defaults.
// Default threshold: 15% deviation before updating buffer size statistics.
func NewCompiler(cfg ...*CompilerCfg) *Compiler {
	jc := &Compiler{
		sizer:     NewAdaptiveSizer(),
		threshold: 15, // Default: update stats when >15% size deviation
	}

	// Apply custom config if provided
	if len(cfg) > 0 && cfg[0] != nil {
		jc.cfg = cfg[0]
		jc.threshold = cfg[0].Threshold
		jc.sizer.Configure(cfg[0].Max, cfg[0].Variance, cfg[0].GrowthFactor)
	}

	return jc
}

// Configure customises the compiler's threshold and adaptive sizing parameters.
// Returns the same instance for method chaining.
func (jc *Compiler) Configure(threshold int, max int, variance, growthFactor int) *Compiler {
	jc.cfg = &CompilerCfg{
		Threshold:    threshold,
		Max:          max,
		Variance:     variance,
		GrowthFactor: growthFactor,
	}
	jc.threshold = threshold
	jc.sizer.Configure(max, variance, growthFactor)
	return jc
}

// Render builds the execution plan on first call, then renders the node.
// Subsequent calls reuse the existing plan with fresh dynamic content from the provided tree.
//
// Static content (including attributes) is frozen from the first call.
// Dynamic content is re-evaluated from the provided tree on each call.
//
// Example:
//
//	compiler := jit.NewCompiler()
//	compiler.Render(UserCard("Alice", 30), w)  // builds plan + renders Alice
//	compiler.Render(UserCard("Bob", 25), w)    // reuses plan, renders Bob
//	compiler.Render(UserCard("Dan", 40), w)    // reuses plan, renders Dan
func (jc *Compiler) Render(root node.Node, w ...io.Writer) []byte {
	jc.planOnce.Do(func() {
		jc.executionPlan = jc.plan(root)
	})

	plan := jc.executionPlan
	if plan == nil {
		return nil
	}

	// Use adaptive sizing for optimal buffer allocation
	predictedSize := jc.sizer.GetBaseline()
	buf := fluent.NewBuffer(predictedSize)
	defer fluent.PutBuffer(buf)

	// Execute the linear plan: static content writes + dynamic node renders
	// Dynamic nodes are fetched from the provided tree using stored paths
	for _, element := range plan.Elements {
		element.Render(root, buf)
	}

	// Conditional update: only adjust sizing when prediction is significantly wrong
	// This reduces overhead by ~95% after size patterns stabilise
	actualSize := buf.Len()
	if jc.shouldUpdateStats(predictedSize, actualSize) {
		jc.sizer.UpdateStats(actualSize)
	}

	// Handle output destination
	if len(w) > 0 && w[0] != nil {
		_, _ = buf.WriteTo(w[0])
		return nil
	}
	return buf.Bytes()
}

// plan performs the complete planning operation: compilation + initial size sampling.
//
// Step 1: Tree Analysis & Plan Compilation
// - Recursively walk the node tree to identify static vs dynamic content.
// - Merge adjacent static nodes into single []byte chunks for efficiency.
// - Store direct references to dynamic nodes.
//
// Step 2: Initial Size Sampling
// - Execute the compiled plan once to seed buffer size optimisation.
// - This provides the initial data point for adaptive sizing.
func (jc *Compiler) plan(rootNode node.Node) *ExecutionPlan {
	plan := &ExecutionPlan{}
	var staticBuffer bytes.Buffer

	// Build execution plan by walking tree and compiling static/dynamic elements
	// Pass empty path slice - will be built up as we recurse
	jc.walk(rootNode, &staticBuffer, plan, []int{})

	// Flush any remaining static content accumulated in the buffer
	if staticBuffer.Len() > 0 {
		plan.Elements = append(plan.Elements, &StaticContent{
			Content: staticBuffer.Bytes(),
		})
	}

	// Execute the plan once to seed adaptive sizing with actual output size
	buf := fluent.NewBuffer()
	defer fluent.PutBuffer(buf)

	for _, element := range plan.Elements {
		element.Render(rootNode, buf)
	}

	// Seed the adaptive sizing mechanism with first render size
	initialSize := buf.Len()
	jc.sizer.UpdateStats(initialSize)

	return plan
}

// shouldUpdateStats determines if we should update sizing statistics based on deviation.
// Only updates when the actual size deviates significantly from our prediction,
// reducing overhead while maintaining buffer optimisation.
func (jc *Compiler) shouldUpdateStats(predicted, actual int) bool {
	// Always update on first render (no baseline yet)
	if predicted == 0 {
		return true
	}

	// Calculate percentage deviation from prediction using integer math
	// diff * 100 > predicted * threshold
	diff := abs(actual - predicted)
	return diff*100 > predicted*jc.threshold
}

// walk recursively builds the execution plan by separating static and dynamic content.
// This is the core compilation algorithm that determines what can be pre-rendered.
//
// Static Content Strategy:
// - Static nodes are immediately rendered to a temporary buffer.
// - Adjacent static content is merged into single chunks for efficiency.
// - Only flushed to plan when dynamic content is encountered.
//
// Dynamic Content Strategy:
// - Dynamic nodes store their path (slice of child indices from root).
// - On render, the path is traversed on the NEW tree to get fresh values.
// - This enables re-evaluation of dynamic content with different data.
func (jc *Compiler) walk(n node.Node, staticBuffer *bytes.Buffer, plan *ExecutionPlan, path []int) {
	if jc.dynamic(n) {
		// Dynamic node found - flush accumulated static content first
		if staticBuffer.Len() > 0 {
			plan.Elements = append(plan.Elements, &StaticContent{
				Content: append([]byte{}, staticBuffer.Bytes()...), // Copy bytes to avoid buffer reuse issues
			})
			staticBuffer.Reset()
		}

		// Store path to dynamic node (copy to avoid slice mutation issues)
		pathCopy := make([]int, len(path))
		copy(pathCopy, path)
		plan.Elements = append(plan.Elements, &DynamicPath{Path: pathCopy})
		return
	}

	// Check if this static node has any dynamic children
	children := n.Nodes()
	hasDynamicChildren := false
	for _, child := range children {
		if jc.hasDynamicContent(child) {
			hasDynamicChildren = true
			break
		}
	}

	if hasDynamicChildren {
		// Node has dynamic children - render opening tag, process children, render closing tag
		if elem, ok := n.(node.Element); ok {
			// Render opening tag to static buffer
			elem.RenderOpen(staticBuffer)

			// Process children individually to separate static/dynamic content
			for i, child := range children {
				childPath := append(path, i) // Extend path with child index
				jc.walk(child, staticBuffer, plan, childPath)
			}

			// Render closing tag to static buffer
			elem.RenderClose(staticBuffer)
		} else {
			// Not an Element, fall back to processing children only
			for i, child := range children {
				childPath := append(path, i)
				jc.walk(child, staticBuffer, plan, childPath)
			}
		}
	} else {
		// Completely static subtree - render to buffer for merging
		n.RenderBuilder(staticBuffer)
	}
}

// hasDynamicContent recursively checks if a node or its children contain dynamic content.
// Used during compilation to determine if a subtree can be pre-rendered.
func (jc *Compiler) hasDynamicContent(n node.Node) bool {
	if jc.dynamic(n) {
		return true
	}

	// Recursively check all children
	for _, child := range n.Nodes() {
		if child != nil && jc.hasDynamicContent(child) {
			return true
		}
	}
	return false
}

// isDynamic determines if a single node contains dynamic content that cannot be pre-compiled.
// This check happens once during planning - dynamic nodes are never re-evaluated.
//
// Marked as dynamic: Text(), Textf(), RawText(), RawTextf(), Conditionals, Func(), Funcs().
//
// CAVEAT: Variables used in attributes (e.g., .Class(variable)) are treated as static after
// first render. The variable's value is copied into the element at compile time. If values
// need to change between renders, use jit.Tune() instead of jit.Compile().
func (jc *Compiler) dynamic(n node.Node) bool {
	// Check if node implements Dynamic interface
	if dynamic, ok := n.(node.Dynamic); ok {
		return dynamic.Dynamic()
	}

	// Check known dynamic types that require runtime evaluation
	switch typed := n.(type) {
	case *node.FunctionComponent, *node.FunctionsComponent, *node.ConditionalBuilder:
		return true
	case *text.Node:
		return typed.Dynamic()
	default:
		return false
	}
}
