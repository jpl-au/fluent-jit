package jit

import (
	"bytes"
	"io"

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
// This approach is ideal for templates with dynamic content that varies
// significantly. The tuner holds no template state - the node to render
// is passed to each call - so one Tuner is safe to share across
// concurrent requests; only the sizing statistics are shared.
type Tuner struct {
	sizer *AdaptiveSizer // shared adaptive sizing logic
	cfg   *TunerCfg      // optional custom configuration
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

// Render renders the node to w with adaptive buffer sizing, feeding the
// measured size back so future predictions improve. Write errors are
// discarded - use WriteTo to observe them. A nil node renders nothing.
func (jt *Tuner) Render(n node.Node, w io.Writer) {
	_, _ = jt.WriteTo(n, w)
}

// WriteTo renders the node to w with adaptive buffer sizing, returning
// the byte count and any write error. The buffer is pooled and sized by
// the sizer's current baseline; the measured size feeds back into the
// sizer, which detects pattern changes via variance monitoring.
func (jt *Tuner) WriteTo(n node.Node, w io.Writer) (int64, error) {
	// A nil node renders nothing - beats a nil dereference.
	if n == nil {
		return 0, nil
	}

	buf := fluent.NewBuffer(jt.sizer.GetBaseline())
	n.RenderBuilder(buf)
	jt.sizer.UpdateStats(buf.Len())
	written, err := buf.WriteTo(w)
	fluent.PutBuffer(buf)
	return written, err
}

// RenderBytes renders the node with adaptive buffer sizing and returns
// the HTML as a byte slice. A nil node returns nil.
func (jt *Tuner) RenderBytes(n node.Node) []byte {
	if n == nil {
		return nil
	}

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
