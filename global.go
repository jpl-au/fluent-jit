package jit

import (
	"io"
	"sync"

	"github.com/jpl-au/fluent"
	"github.com/jpl-au/fluent/node"
)

var (
	compilers sync.Map
	tuners    sync.Map
	flattened sync.Map
)

// Compile looks up a compiler by ID in a global registry, creating it if it
// doesn't exist, and renders it using the compilation strategy.
// If CompileConfig() was called first, that config will be used.
//
// The node is used both to build the plan (on first call) and to provide
// dynamic content for rendering. Static content is frozen from the first call.
//
// Warning: The global registry grows indefinitely. Do not use dynamic IDs
// without manually calling ResetCompile(id) to free memory.
func Compile(id string, n node.Node, w ...io.Writer) []byte {
	val, _ := compilers.LoadOrStore(id, NewCompiler())
	compiler := val.(*Compiler)
	return compiler.Render(n, w...)
}

// Tune looks up a tuner by ID in a global registry, creating it if it
// doesn't exist, and renders it using the adaptive tuning strategy.
// If TuneConfig() was called first, that config will be used.
//
// Warning: The global registry grows indefinitely. Do not use dynamic IDs
// without manually calling ResetTune(id) to free memory.
func Tune(id string, n node.Node, w ...io.Writer) []byte {
	val, _ := tuners.LoadOrStore(id, NewTuner())
	tuner := val.(*Tuner)
	return tuner.Tune(n).Render(w...)
}

// ResetCompile removes compiled templates from the global registry,
// allowing them to be re-compiled on next use.
// Call with no arguments to clear all entries, or pass specific IDs to remove.
func ResetCompile(ids ...string) {
	if len(ids) == 0 {
		compilers.Clear()
		return
	}
	for _, id := range ids {
		compilers.Delete(id)
	}
}

// ResetTune removes tuned templates from the global registry,
// causing their tuning statistics to be reset on next use.
// Call with no arguments to clear all entries, or pass specific IDs to remove.
func ResetTune(ids ...string) {
	if len(ids) == 0 {
		tuners.Clear()
		return
	}
	for _, id := range ids {
		tuners.Delete(id)
	}
}

// Flatten looks up flattened static content in the global registry.
// On first call with a node, it validates the content is static, renders it once,
// and stores the result. Subsequent calls retrieve the stored bytes.
//
// Warning: The global registry grows indefinitely. Do not use dynamic IDs
// without manually calling ResetFlatten(id) to free memory.
func Flatten(id string, n node.Node, w ...io.Writer) []byte {
	// Try to load existing flattened content
	val, loaded := flattened.Load(id)

	if !loaded {
		// First time - validate static, render, store
		// If dynamic, fallback to standard rendering without caching
		if dynamic(n) {
			return n.Render(w...)
		}

		buf := fluent.NewBuffer()
		defer fluent.PutBuffer(buf)
		n.RenderBuilder(buf)
		bytes := append([]byte{}, buf.Bytes()...)

		flattened.Store(id, bytes)
		val = bytes
	}

	bytes := val.([]byte)

	// Handle output destination
	if len(w) > 0 && w[0] != nil {
		w[0].Write(bytes)
		return nil
	}
	return bytes
}

// ResetFlatten removes flattened static content from the global registry.
// Call with no arguments to clear all entries, or pass specific IDs to remove.
func ResetFlatten(ids ...string) {
	if len(ids) == 0 {
		flattened.Clear()
		return
	}
	for _, id := range ids {
		flattened.Delete(id)
	}
}

// CompileConfig creates a compiler instance with custom configuration.
// Must be called before first Compile() call for the given ID.
func CompileConfig(id string, cfg CompilerCfg) {
	compilers.Store(id, NewCompiler(&cfg))
}

// TuneConfig creates a tuner instance with custom configuration.
// Must be called before first Tune() call for the given ID.
func TuneConfig(id string, cfg TunerCfg) {
	tuners.Store(id, NewTuner(&cfg))
}
