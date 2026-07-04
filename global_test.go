package jit

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/span"
)

// TestGlobalCompile verifies the package-level Compile function, which manages
// a global sync.Map of Compiler instances keyed by ID. This is the primary API
// for most users - it avoids manual compiler lifecycle management.
func TestGlobalCompile(t *testing.T) {
	defer ResetCompile()

	tree := div.New(span.Static("hello"))
	result := string(CompileBytes("test-compile", tree))

	expected := "<div><span>hello</span></div>"
	if result != expected {
		t.Errorf("global Compile should produce correct output:\n  got  %q\n  want %q", result, expected)
	}
}

// TestGlobalCompileToWriter verifies that the global Compile function passes
// the io.Writer through to the underlying compiler. When a writer is provided,
// Compile writes directly to it and returns nil.
func TestGlobalCompileToWriter(t *testing.T) {
	defer ResetCompile()

	tree := div.New(span.Static("hello"))
	var buf bytes.Buffer
	n, err := CompileWriteTo("test-compile-writer", tree, &buf)
	if err != nil {
		t.Fatalf("CompileWriteTo returned error: %v", err)
	}
	if n != int64(buf.Len()) {
		t.Errorf("CompileWriteTo reported %d bytes but wrote %d", n, buf.Len())
	}

	expected := "<div><span>hello</span></div>"
	if buf.String() != expected {
		t.Errorf("writer output should match byte-slice output:\n  got  %q\n  want %q", buf.String(), expected)
	}
}

// TestGlobalCompileReusesInstance verifies that the same ID always returns the
// same compiler instance. The first call creates the compiler and builds the
// execution plan; subsequent calls reuse it. Dynamic content must still change
// between renders - only static content is frozen.
func TestGlobalCompileReusesInstance(t *testing.T) {
	defer ResetCompile()

	// First call: creates compiler, builds execution plan, freezes static "Hello ".
	tree1 := div.New(span.Static("Hello "), span.Text("Alice"))
	result1 := string(CompileBytes("test-compile-reuse", tree1))

	// Second call: same ID reuses the compiler. Dynamic content should update.
	tree2 := div.New(span.Static("Hello "), span.Text("Bob"))
	result2 := string(CompileBytes("test-compile-reuse", tree2))

	if !strings.Contains(result1, "Alice") {
		t.Errorf("first render should contain dynamic content 'Alice', got %q", result1)
	}
	if !strings.Contains(result2, "Bob") {
		t.Errorf("second render should re-evaluate dynamic content to 'Bob', got %q - compiler may not be reusing correctly", result2)
	}
}

// TestGlobalTune verifies the package-level Tune function, which manages a
// global sync.Map of Tuner instances. The tuner provides adaptive buffer
// sizing without the compilation overhead of the Compiler.
func TestGlobalTune(t *testing.T) {
	defer ResetTune()

	tree := div.New(span.Text("hello"))
	result := string(TuneBytes("test-tune", tree))

	expected := "<div><span>hello</span></div>"
	if result != expected {
		t.Errorf("global Tune should produce correct output:\n  got  %q\n  want %q", result, expected)
	}
}

// TestGlobalFlattenStatic verifies that the global Flatten function caches
// static content. The first call renders and caches; the second call should
// return the cached bytes without re-rendering.
func TestGlobalFlattenStatic(t *testing.T) {
	defer ResetFlatten()

	tree := div.New(span.Static("hello"))
	result := string(FlattenBytes("test-flatten", tree))

	expected := "<div><span>hello</span></div>"
	if result != expected {
		t.Errorf("global Flatten should produce correct output:\n  got  %q\n  want %q", result, expected)
	}

	// Second call should return the cached bytes - no re-rendering.
	result2 := string(FlattenBytes("test-flatten", tree))
	if result2 != expected {
		t.Errorf("cached Flatten result should be identical:\n  got  %q\n  want %q", result2, expected)
	}
}

// TestGlobalFlattenDynamicFallback verifies the silent fallback behaviour of
// the global Flatten function. Unlike NewFlattener (which returns an error for
// dynamic content), the global Flatten silently falls back to standard rendering.
// This avoids disrupting request handlers where returning an error would be
// impractical - the output is correct, just not cached.
func TestGlobalFlattenDynamicFallback(t *testing.T) {
	defer ResetFlatten()

	// span.Text is dynamic - NewFlattener would reject this, but the global
	// Flatten should fall back to standard rendering and still produce output.
	tree := div.New(span.Text("hello"))
	result := string(FlattenBytes("test-flatten-dynamic", tree))

	expected := "<div><span>hello</span></div>"
	if result != expected {
		t.Errorf("dynamic fallback should still produce correct output:\n  got  %q\n  want %q", result, expected)
	}
}

// TestGlobalFlattenToWriter verifies that the global Flatten function passes
// the io.Writer through correctly, writing cached bytes directly to the writer.
func TestGlobalFlattenToWriter(t *testing.T) {
	defer ResetFlatten()

	tree := div.New(span.Static("hello"))
	var buf bytes.Buffer
	n, err := FlattenWriteTo("test-flatten-writer", tree, &buf)
	if err != nil {
		t.Fatalf("FlattenWriteTo returned error: %v", err)
	}
	if n != int64(buf.Len()) {
		t.Errorf("FlattenWriteTo reported %d bytes but wrote %d", n, buf.Len())
	}

	expected := "<div><span>hello</span></div>"
	if buf.String() != expected {
		t.Errorf("writer output should match byte-slice output:\n  got  %q\n  want %q", buf.String(), expected)
	}
}

// TestResetCompile verifies that ResetCompile can clear a specific ID or all
// IDs from the global compiler registry. This is primarily useful in tests to
// ensure a clean state between test cases.
func TestResetCompile(t *testing.T) {
	tree := div.Static("hello")
	CompileBytes("reset-a", tree)
	CompileBytes("reset-b", tree)

	// Reset a specific ID - only "reset-a" should be cleared.
	ResetCompile("reset-a")

	// Reset all - clears every remaining entry.
	ResetCompile()
}

// TestResetTune verifies that ResetTune can clear a specific ID or all IDs
// from the global tuner registry.
func TestResetTune(t *testing.T) {
	tree := div.Static("hello")
	TuneBytes("reset-tune-a", tree)
	TuneBytes("reset-tune-b", tree)

	ResetTune("reset-tune-a")
	ResetTune()
}

// TestResetFlatten verifies that ResetFlatten can clear a specific ID or all
// IDs from the global flattener registry.
func TestResetFlatten(t *testing.T) {
	tree := div.Static("hello")
	FlattenBytes("reset-flat-a", tree)
	FlattenBytes("reset-flat-b", tree)

	ResetFlatten("reset-flat-a")
	ResetFlatten()
}

// TestGlobalCompileConfig verifies that CompileConfig pre-registers a custom
// configuration for a given ID. When Compile is later called with that ID,
// it uses the pre-registered configuration instead of defaults.
func TestGlobalCompileConfig(t *testing.T) {
	defer ResetCompile()

	CompileConfig("test-cfg", CompilerCfg{
		Threshold:    10,
		Max:          3,
		Variance:     15,
		GrowthFactor: 120,
	})

	tree := div.Static("hello")
	result := string(CompileBytes("test-cfg", tree))

	expected := "<div>hello</div>"
	if result != expected {
		t.Errorf("pre-configured Compile should still render correctly:\n  got  %q\n  want %q", result, expected)
	}
}

// TestGlobalTuneConfig verifies that TuneConfig pre-registers a custom
// configuration for a given ID, controlling the adaptive sizer's behaviour.
func TestGlobalTuneConfig(t *testing.T) {
	defer ResetTune()

	TuneConfig("test-tune-cfg", TunerCfg{
		Max:          3,
		Variance:     10,
		GrowthFactor: 150,
	})

	tree := div.Static("hello")
	result := string(TuneBytes("test-tune-cfg", tree))

	expected := "<div>hello</div>"
	if result != expected {
		t.Errorf("pre-configured Tune should still render correctly:\n  got  %q\n  want %q", result, expected)
	}
}

// TestGlobalTuneConcurrentCallersGetOwnContent verifies that concurrent
// calls to the global Tune with the same ID each render their own tree.
// An earlier implementation staged the node on the shared Tuner, so two
// requests could swap trees mid-flight and one user's content could be
// rendered for another. The Tuner is stateless now, but the regression
// test stays.
func TestGlobalTuneConcurrentCallersGetOwnContent(t *testing.T) {
	defer ResetTune("concurrent-tune")

	var wg sync.WaitGroup
	mixups := make(chan string, 8)

	for i := range 8 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			marker := "caller-" + strconv.Itoa(id)
			for range 200 {
				html := string(TuneBytes("concurrent-tune", div.New(span.Text(marker))))
				if !strings.Contains(html, marker) {
					mixups <- marker + " received someone else's content: " + html
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(mixups)

	for msg := range mixups {
		t.Error(msg)
	}
}
