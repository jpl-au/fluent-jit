package jit

import (
	"bytes"
	"io"
	"sync"

	"github.com/jpl-au/fluent/node"
)

var (
	compilers sync.Map
	tuners    sync.Map
	flattened sync.Map
)

// compiler returns the registered Compiler for id, creating it on first
// use. Load first to avoid allocating a NewCompiler on every call -
// LoadOrStore evaluates its arguments eagerly, so calling it directly
// would allocate even when the key already exists.
func compiler(id string) *Compiler {
	val, loaded := compilers.Load(id)
	if !loaded {
		val, _ = compilers.LoadOrStore(id, NewCompiler())
	}
	return val.(*Compiler) //nolint:forcetypeassert // type guaranteed by LoadOrStore
}

// tuner returns the registered Tuner for id, creating it on first use.
func tuner(id string) *Tuner {
	val, loaded := tuners.Load(id)
	if !loaded {
		val, _ = tuners.LoadOrStore(id, NewTuner())
	}
	return val.(*Tuner) //nolint:forcetypeassert // type guaranteed by LoadOrStore
}

// Compile looks up a compiler by ID in a global registry, creating it if
// it doesn't exist, and renders the node to w using the compilation
// strategy. If CompileConfig() was called first, that config will be
// used. Write errors are discarded - use CompileWriteTo to observe them.
//
// The node is used both to build the plan (on first call) and to provide
// dynamic content for rendering. Static content is frozen from the first call.
//
// Warning: The global registry grows indefinitely. Do not use dynamic IDs
// without manually calling ResetCompile(id) to free memory.
func Compile(id string, n node.Node, w io.Writer) {
	compiler(id).Render(n, w)
}

// CompileWriteTo is [Compile] returning the byte count and any write error.
func CompileWriteTo(id string, n node.Node, w io.Writer) (int64, error) {
	return compiler(id).WriteTo(n, w)
}

// CompileBytes is [Compile] returning the HTML as a byte slice.
func CompileBytes(id string, n node.Node) []byte {
	return compiler(id).RenderBytes(n)
}

// Tune looks up a tuner by ID in a global registry, creating it if it
// doesn't exist, and renders the node to w using the adaptive tuning
// strategy. If TuneConfig() was called first, that config will be used.
// Write errors are discarded - use TuneWriteTo to observe them.
//
// Concurrent calls with the same ID are safe: the node renders directly
// and only the sizing statistics are shared between callers.
//
// Warning: The global registry grows indefinitely. Do not use dynamic IDs
// without manually calling ResetTune(id) to free memory.
func Tune(id string, n node.Node, w io.Writer) {
	tuner(id).Render(n, w)
}

// TuneWriteTo is [Tune] returning the byte count and any write error.
func TuneWriteTo(id string, n node.Node, w io.Writer) (int64, error) {
	return tuner(id).WriteTo(n, w)
}

// TuneBytes is [Tune] returning the HTML as a byte slice.
func TuneBytes(id string, n node.Node) []byte {
	return tuner(id).RenderBytes(n)
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

// flattenCached returns the cached static bytes for id, rendering and
// caching them on first use. ok is false when the content is dynamic
// and cannot be cached - callers fall back to a direct render, since
// the global API is typically called in request handlers where
// returning an error would be disruptive.
func flattenCached(id string, n node.Node) (b []byte, ok bool) {
	if val, loaded := flattened.Load(id); loaded {
		return val.([]byte), true //nolint:forcetypeassert // type guaranteed by Store below
	}

	if isDynamic(n) {
		return nil, false
	}

	var buf bytes.Buffer
	n.RenderBuilder(&buf)
	flattened.Store(id, buf.Bytes())
	return buf.Bytes(), true
}

// Flatten looks up flattened static content in the global registry and
// writes it to w. On first call with a node, it validates the content
// is static, renders it once, and stores the result. Subsequent calls
// write the stored bytes. Write errors are discarded - use
// FlattenWriteTo to observe them.
//
// Unlike NewFlattener which returns an error for dynamic content, this
// silently falls back to uncached rendering.
//
// Warning: The global registry grows indefinitely. Do not use dynamic IDs
// without manually calling ResetFlatten(id) to free memory.
func Flatten(id string, n node.Node, w io.Writer) {
	_, _ = FlattenWriteTo(id, n, w)
}

// FlattenWriteTo is [Flatten] returning the byte count and any write error.
func FlattenWriteTo(id string, n node.Node, w io.Writer) (int64, error) {
	b, ok := flattenCached(id, n)
	if !ok {
		return n.WriteTo(w)
	}
	written, err := w.Write(b)
	return int64(written), err
}

// FlattenBytes is [Flatten] returning the content as a byte slice.
func FlattenBytes(id string, n node.Node) []byte {
	b, ok := flattenCached(id, n)
	if !ok {
		return n.RenderBytes()
	}
	return b
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
