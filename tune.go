package jit

import (
	"bytes"
	"io"
	"sync"

	"github.com/jpl-au/fluent"
	"github.com/jpl-au/fluent/node"
)

// Tuner provides dynamic adaptive buffer sizing for changing content patterns.
// Unlike the compiler which pre-optimises static content, the tuner adapts
// to content that changes over time by continuously monitoring render sizes.
//
// The tuner uses shared AdaptiveSizer logic with two-phase operation:
// 1. Sampling phase: Collects render size samples to establish optimal buffer size
// 2. Baseline phase: Uses established size with variance monitoring for pattern changes
//
// This approach is ideal for templates with dynamic content that varies significantly.
type Tuner struct {
	rootNode node.Node      // current template to render
	sizer    *AdaptiveSizer // shared adaptive sizing logic
	mu       sync.RWMutex   // protects rootNode access during concurrent usage
	cfg      *TunerCfg      // optional custom configuration
}

// NewTuner creates a tuner with adaptive sizing defaults.
// Uses shared AdaptiveSizer with standard configuration:
// - 5 samples for baseline establishment.
// - 20% variance threshold for pattern change detection.
// - 115% growth factor to prevent tight buffer fits.
func NewTuner(cfg ...*TunerCfg) *Tuner {
	jt := &Tuner{
		sizer: NewAdaptiveSizer(),
	}

	// Apply custom config if provided
	if len(cfg) > 0 && cfg[0] != nil {
		jt.cfg = cfg[0]
		jt.sizer.Configure(cfg[0].Max, cfg[0].Variance, cfg[0].GrowthFactor)
	}

	return jt
}

// Configure customises the adaptive sizing parameters and resets statistics.
// This forces the tuner to restart sampling with new parameters.
//
// Parameters:
// - max: number of samples to collect before establishing baseline.
// - variance: threshold percentage for detecting significant size changes (e.g. 20).
// - growthFactor: multiplier percentage applied to average size (e.g. 115).
func (jt *Tuner) Configure(max int, variance, growthFactor int) *Tuner {
	jt.cfg = &TunerCfg{
		Max:          max,
		Variance:     variance,
		GrowthFactor: growthFactor,
	}
	jt.sizer.Configure(max, variance, growthFactor)
	return jt
}

// Tune sets the template to render with adaptive buffer sizing.
// Thread-safe for concurrent usage. Returns the same instance for method chaining.
func (jt *Tuner) Tune(root node.Node) *Tuner {
	jt.mu.Lock()
	jt.rootNode = root
	jt.mu.Unlock()
	return jt
}

// Render executes the configured template with adaptive buffer sizing.
// This method automatically optimises buffer allocation based on historical render sizes
// and continuously updates statistics for future optimisation.
func (jt *Tuner) Render(w ...io.Writer) []byte {
	var writer io.Writer
	if len(w) > 0 {
		writer = w[0]
	}

	// Snapshot rootNode under read lock so the template can't change mid-render
	jt.mu.RLock()
	rootNode := jt.rootNode
	jt.mu.RUnlock()

	return jt.tune(rootNode, writer)
}

// tune performs the core adaptive rendering logic:
// 1. Pre-allocates a buffer using the predicted size from adaptive sizing.
// 2. Renders the template into the buffer.
// 3. Feeds the actual size back to the sizer so future predictions improve.
// 4. The sizer automatically detects pattern changes via variance monitoring.
func (jt *Tuner) tune(n node.Node, w io.Writer) []byte {
	// With writer: use pooled buffer to avoid allocation, then return it to the pool
	if w != nil {
		buf := fluent.NewBuffer(jt.sizer.GetBaseline())
		n.RenderBuilder(buf)
		jt.sizer.UpdateStats(buf.Len())
		_, _ = buf.WriteTo(w)
		fluent.PutBuffer(buf)
		return nil
	}

	// Without writer: use local buffer with predicted capacity
	buf := bytes.NewBuffer(make([]byte, 0, jt.sizer.GetBaseline()))
	n.RenderBuilder(buf)
	jt.sizer.UpdateStats(buf.Len())
	return buf.Bytes()
}

// Reset clears all collected statistics and restarts adaptive sizing.
// Useful when content patterns change significantly or for testing scenarios.
// Returns the same instance for method chaining.
func (jt *Tuner) Reset() *Tuner {
	jt.sizer.Reset()
	return jt
}
