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
	"slices"

	"github.com/jpl-au/fluent/node"
)

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

// dynamic checks if a node or any of its children contain dynamic content.
func dynamic(n node.Node) bool {
	// Check if node implements Dynamic interface
	if d, ok := n.(node.Dynamic); ok && d.Dynamic() {
		return true
	}

	// Check known dynamic types
	switch n.(type) {
	case *node.FunctionComponent, *node.FunctionsComponent, *node.ConditionalBuilder:
		return true
	}

	// Recursively check children
	return slices.ContainsFunc(n.Nodes(), dynamic)
}

