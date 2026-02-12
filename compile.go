package jit

import (
	"bytes"
	"fmt"
	"io"
	"sync"

	"github.com/jpl-au/fluent"
	"github.com/jpl-au/fluent/node"
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
	compileOnce   sync.Once      // Ensures single compilation
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

// Validate checks whether a node tree is structurally compatible with the
// compiled execution plan. It walks each DynamicPath in the plan and verifies
// that the path resolves to a valid node in the provided tree.
//
// This is a diagnostic tool for tests and development — it should NOT be called
// in production because it adds overhead to every render. In production, a
// structure mismatch will produce visibly broken output, which is sufficient
// signal to investigate.
//
// Returns nil if the tree is compatible, or ErrStructureMismatch with details
// about which path failed.
//
// Example (in a test):
//
//	compiler := jit.NewCompiler()
//	compiler.Render(baseTree)          // builds plan
//	if err := compiler.Validate(newTree); err != nil {
//	    t.Fatalf("tree structure changed: %v", err)
//	}
func (jc *Compiler) Validate(root node.Node) error {
	plan := jc.executionPlan
	if plan == nil {
		return nil // no plan compiled yet — nothing to validate against
	}

	for _, element := range plan.Elements {
		dp, ok := element.(*DynamicPath)
		if !ok {
			continue // static content — always valid
		}

		n := root
		for depth, idx := range dp.Path {
			children := n.Nodes()
			if idx >= len(children) {
				return fmt.Errorf("%w: path %v failed at depth %d — expected child index %d but node only has %d children",
					ErrStructureMismatch, dp.Path, depth, idx, len(children))
			}
			n = children[idx]
		}
	}

	return nil
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
	jc.compileOnce.Do(func() {
		jc.executionPlan = jc.compile(root)
	})

	plan := jc.executionPlan
	if plan == nil {
		return nil
	}

	predictedSize := jc.sizer.GetBaseline()

	// With writer: use pooled buffer, write, then return to pool
	if len(w) > 0 && w[0] != nil {
		buf := fluent.NewBuffer(predictedSize)
		for _, element := range plan.Elements {
			element.Render(root, buf)
		}
		actualSize := buf.Len()
		if jc.shouldUpdateStats(predictedSize, actualSize) {
			jc.sizer.UpdateStats(actualSize)
		}
		// Write errors are not actionable mid-render — a closed connection can't be
		// recovered, and the caller controls the writer's error handling.
		_, _ = buf.WriteTo(w[0])
		fluent.PutBuffer(buf)
		return nil
	}

	// Without writer: use local buffer with predicted capacity
	buf := bytes.NewBuffer(make([]byte, 0, predictedSize))
	for _, element := range plan.Elements {
		element.Render(root, buf)
	}
	actualSize := buf.Len()
	if jc.shouldUpdateStats(predictedSize, actualSize) {
		jc.sizer.UpdateStats(actualSize)
	}
	return buf.Bytes()
}

// compile builds the execution plan and seeds initial buffer sizing.
//
// Step 1: Tree Analysis
// - Recursively walk the node tree to identify static vs dynamic content.
// - Merge adjacent static nodes into single []byte chunks for efficiency.
// - Store direct references to dynamic nodes.
//
// Step 2: Initial Size Sampling
// - Execute the compiled plan once to seed buffer size optimisation.
// - This provides the initial data point for adaptive sizing.
func (jc *Compiler) compile(rootNode node.Node) *ExecutionPlan {
	plan := &ExecutionPlan{}
	var staticBuffer bytes.Buffer

	// Build execution plan by walking tree and compiling static/dynamic elements.
	// The empty path slice tracks position in the tree — extended with child indices
	// as we recurse, so dynamic nodes can record how to navigate back to themselves.
	jc.walk(rootNode, &staticBuffer, plan, []int{})

	// Static content is only flushed to the plan when a dynamic node is encountered,
	// so any trailing static content needs to be flushed here.
	if staticBuffer.Len() > 0 {
		plan.Elements = append(plan.Elements, &StaticContent{
			Content: staticBuffer.Bytes(),
		})
	}

	// Execute the plan once to seed adaptive sizing with an actual output size,
	// so the very first real render already has a reasonable buffer prediction.
	buf := fluent.NewBuffer()
	defer fluent.PutBuffer(buf)

	for _, element := range plan.Elements {
		element.Render(rootNode, buf)
	}

	jc.sizer.UpdateStats(buf.Len())

	return plan
}

// shouldUpdateStats determines if we should update sizing statistics based on deviation.
// Only updates when the actual size deviates significantly from our prediction,
// reducing overhead while maintaining buffer optimisation.
func (jc *Compiler) shouldUpdateStats(predicted, actual int) bool {
	// No baseline yet — must update to begin establishing one
	if predicted == 0 {
		return true
	}

	// Integer math equivalent of: abs(actual - predicted) / predicted > threshold / 100
	// This avoids floating point on the render path
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
	// Attributes (e.g. .Class(variable)) are treated as static after first render —
	// their values are frozen at compile time. Use Tune() if values must change between renders.
	if isDynamicNode(n) {
		// Flush accumulated static content before recording the dynamic path,
		// so the execution plan preserves the correct rendering order.
		if staticBuffer.Len() > 0 {
			plan.Elements = append(plan.Elements, &StaticContent{
				Content: append([]byte{}, staticBuffer.Bytes()...), // copy — staticBuffer is reset and reused below
			})
			staticBuffer.Reset()
		}

		// Explicit copy because append(path, i) in the loop below may share
		// the same backing array — without a copy, stored paths could be
		// silently corrupted by later iterations.
		pathCopy := make([]int, len(path))
		copy(pathCopy, path)
		plan.Elements = append(plan.Elements, &DynamicPath{Path: pathCopy})
		return
	}

	// Determine whether children need individual processing or if the
	// entire subtree can be rendered as a single static chunk.
	children := n.Nodes()
	hasDynamicChildren := false
	for _, child := range children {
		if isDynamic(child) {
			hasDynamicChildren = true
			break
		}
	}

	if hasDynamicChildren {
		// Node has dynamic children — render opening/closing tags as static content,
		// but process children individually so dynamic ones get their own paths.
		if elem, ok := n.(node.Element); ok {
			elem.RenderOpen(staticBuffer)

			for i, child := range children {
				// append may reuse path's backing array, which is safe here because
				// walk is depth-first: each recursive call completes before the next
				// iteration overwrites the same position. Stored paths use explicit
				// copies (pathCopy above) so they aren't affected.
				childPath := append(path, i)
				jc.walk(child, staticBuffer, plan, childPath)
			}

			elem.RenderClose(staticBuffer)
		} else {
			// Non-Element container (e.g. Fragment) — no opening/closing tags to render
			for i, child := range children {
				childPath := append(path, i)
				jc.walk(child, staticBuffer, plan, childPath)
			}
		}
	} else {
		// Entirely static subtree — render directly for merging with adjacent static content
		n.RenderBuilder(staticBuffer)
	}
}

