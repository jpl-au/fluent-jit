// Package jit provides Just-In-Time optimisation for HTML rendering.
// It offers two distinct optimisation strategies:
//
// 1. Compile: Pre-processes node trees to cache static content for extremely
// fast subsequent renders. This is ideal for templates that are rendered
// many times with different dynamic data.
//
// 2. Tune: Adaptively sizes rendering buffers based on live statistics.
// This reduces memory allocations and garbage collection pressure for
// components with highly variable output sizes.
//
// The package provides two APIs:
//   - Instance API: Create specific instances (`jit.NewCompiler()`, `jit.NewTuner()`)
//     for fine-grained control over a specific template's lifecycle.
//   - Global API: Use package-level functions (`jit.Compile`, `jit.Tune`) for
//     a simple, globally-managed cache of templates identified by a string ID.
//
// Memory Management Warning:
// The global API uses unbounded maps to store compiled/tuned templates.
// These maps never shrink automatically. If you use dynamic IDs (e.g. user IDs),
// the memory usage will grow indefinitely.
//
// Best Practices:
//  1. Use constant string IDs for templates (e.g. "header", "footer").
//  2. If you must use dynamic IDs, manually call `jit.ResetCompile(id)` or
//     `jit.ResetTune(id)` when the template is no longer needed.
package jit

import (
	"errors"
	"slices"

	"github.com/jpl-au/fluent/node"
	"github.com/jpl-au/fluent/text"
)

// Sentinel errors for programmatic error checking via errors.Is().

// ErrDynamicContent is returned when attempting to flatten dynamic content.
// The flattener can only cache static content — dynamic nodes must be
// re-evaluated on each render, which defeats the purpose of flattening.
var ErrDynamicContent = errors.New("NewFlattener() requires static content - use NewCompiler() for dynamic content")

// ErrStructureMismatch indicates that a node tree passed to Compiler.Validate
// has a different structure than the tree used to build the execution plan.
// The compiler navigates dynamic nodes by their position in the tree (stored as
// index paths), so a structural change means those paths no longer resolve to
// the correct nodes — producing truncated or incorrect output.
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
	if d, ok := n.(node.Dynamic); ok && d.Dynamic() {
		return true
	}

	switch t := n.(type) {
	case *node.FunctionComponent, *node.FunctionsComponent, *node.ConditionalBuilder:
		return true
	case *text.Node:
		return t.Dynamic()
	}

	return false
}

// isDynamic reports whether a node or any of its descendants contain dynamic content.
func isDynamic(n node.Node) bool {
	if isDynamicNode(n) {
		return true
	}
	return slices.ContainsFunc(n.Nodes(), isDynamic)
}
