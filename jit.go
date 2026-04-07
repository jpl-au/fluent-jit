package jit

import (
	"errors"
	"slices"

	"github.com/jpl-au/fluent/node"
)

// Sentinel errors for programmatic error checking via errors.Is().

// ErrDynamicContent is returned when attempting to flatten dynamic content.
// The flattener can only cache static content - dynamic nodes must be
// re-evaluated on each render, which defeats the purpose of flattening.
var ErrDynamicContent = errors.New("NewFlattener() requires static content - use NewCompiler() for dynamic content")

// ErrStructureMismatch indicates that a node tree passed to Compiler.Validate
// has a different structure than the tree used to build the execution plan.
// The compiler navigates dynamic nodes by their position in the tree (stored as
// index paths), so a structural change means those paths no longer resolve to
// the correct nodes - producing truncated or incorrect output.
var ErrStructureMismatch = errors.New("node tree structure does not match the compiled execution plan")

// CompilerCfg holds configuration for JIT compiler instances.
type CompilerCfg struct {
	Threshold    int // deviation threshold percentage for conditional stats updates
	Max          int // samples before establishing baseline
	Variance     int // threshold percentage for detecting size changes
	GrowthFactor int // multiplier percentage for average size
}

// TunerCfg holds configuration for JIT tuner instances.
type TunerCfg struct {
	Max          int // samples before establishing baseline
	Variance     int // threshold percentage for detecting size changes
	GrowthFactor int // multiplier percentage for average size
}

// isDynamicNode reports whether a single node contains dynamic content
// that requires runtime evaluation and cannot be pre-rendered.
func isDynamicNode(n node.Node) bool {
	d, ok := n.(node.Dynamic)
	return ok && d.IsDynamic()
}

// isDynamic reports whether a node or any of its descendants contain dynamic content.
func isDynamic(n node.Node) bool {
	if isDynamicNode(n) {
		return true
	}
	return slices.ContainsFunc(n.Nodes(), isDynamic)
}
