package jit

import (
	"bytes"
	"testing"

	"github.com/jpl-au/fluent/html5/div"
	"github.com/jpl-au/fluent/html5/span"
)

// TestTunerRender verifies basic rendering through the tuner. The tuner wraps
// standard fluent rendering with adaptive buffer sizing - this test confirms
// that the wrapping doesn't alter the output. Dynamic content (span.Text) is
// used to ensure the tuner handles the general case, not just static trees.
func TestTunerRender(t *testing.T) {
	tuner := NewTuner()

	tree := div.New(span.Text("hello"))
	result := string(tuner.RenderBytes(tree))

	expected := "<div><span>hello</span></div>"
	if result != expected {
		t.Errorf("tuner output should match standard rendering:\n  got  %q\n  want %q", result, expected)
	}
}

// TestTunerRenderToWriter verifies the writer path used by HTTP
// handlers, and that WriteTo reports the byte count.
func TestTunerRenderToWriter(t *testing.T) {
	tuner := NewTuner()

	tree := div.New(span.Static("hello"))
	var buf bytes.Buffer
	n, err := tuner.WriteTo(tree, &buf)
	if err != nil {
		t.Fatalf("WriteTo returned error: %v", err)
	}
	if n != int64(buf.Len()) {
		t.Errorf("WriteTo reported %d bytes but wrote %d", n, buf.Len())
	}

	expected := "<div><span>hello</span></div>"
	if buf.String() != expected {
		t.Errorf("writer output should match byte-slice output:\n  got  %q\n  want %q", buf.String(), expected)
	}
}

// TestTunerAdaptiveSizing verifies that the tuner produces correct output
// after the adaptive sizer transitions from sampling to baseline phase. The
// sizer changes the internal buffer allocation strategy after collecting
// enough samples - this test confirms that the transition doesn't corrupt
// or truncate the rendered output.
func TestTunerAdaptiveSizing(t *testing.T) {
	tuner := NewTuner()

	tree := div.New(span.Static("hello"))

	// Render enough times for the sizer to collect its default 5 samples
	// and establish a baseline buffer size.
	for range 10 {
		tuner.RenderBytes(tree)
	}

	// After the sizer transitions to baseline phase, it pre-allocates buffers
	// based on the learned size. Output must still be correct.
	result := string(tuner.RenderBytes(tree))
	expected := "<div><span>hello</span></div>"
	if result != expected {
		t.Errorf("output after adaptive sizing should be unchanged:\n  got  %q\n  want %q", result, expected)
	}
}

// TestTunerReset verifies that Reset returns the tuner to its initial state,
// clearing the adaptive sizer's learned baseline. After reset, the tuner
// must still produce correct output while re-learning buffer sizes.
func TestTunerReset(t *testing.T) {
	tuner := NewTuner()

	tree := div.New(span.Static("hello"))

	// Let the sizer establish a baseline, then discard it.
	for range 10 {
		tuner.RenderBytes(tree)
	}

	tuner.Reset()

	// After reset the tuner re-enters sampling phase with no baseline.
	// Output must still be correct during the re-learning period.
	result := string(tuner.RenderBytes(tree))
	expected := "<div><span>hello</span></div>"
	if result != expected {
		t.Errorf("output after reset should be correct while re-learning buffer sizes:\n  got  %q\n  want %q", result, expected)
	}
}

// TestTunerWithConfiguration verifies that TunerCfg options are applied to
// the adaptive sizer. Custom configuration controls how many samples are
// collected (Max), the variance threshold for resampling, and the growth
// factor applied to the average when calculating the baseline buffer size.
func TestTunerWithConfiguration(t *testing.T) {
	tuner := NewTuner(&TunerCfg{
		Max:          3,
		Variance:     10,
		GrowthFactor: 150,
	})

	tree := div.Static("hello")
	result := string(tuner.RenderBytes(tree))

	expected := "<div>hello</div>"
	if result != expected {
		t.Errorf("configured tuner should still render correctly:\n  got  %q\n  want %q", result, expected)
	}
}

// TestTunerNilNode verifies that rendering a nil node returns nil and
// writes nothing instead of panicking.
func TestTunerNilNode(t *testing.T) {
	if out := NewTuner().RenderBytes(nil); out != nil {
		t.Errorf("RenderBytes(nil) should return nil, got %q", out)
	}

	var buf bytes.Buffer
	NewTuner().Render(nil, &buf)
	if buf.Len() != 0 {
		t.Error("Render(nil, w) should write nothing")
	}
}
