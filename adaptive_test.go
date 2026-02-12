package jit

import "testing"

// TestAdaptiveSizerSamplingPhase verifies that the sizer starts in sampling
// phase, collects the configured number of samples, then transitions to
// baseline phase with the correct buffer size prediction.
func TestAdaptiveSizerSamplingPhase(t *testing.T) {
	as := NewAdaptiveSizer()

	if !as.Active() {
		t.Fatal("sizer should start in sampling phase so it can learn buffer sizes")
	}
	if as.GetBaseline() != 0 {
		t.Fatal("baseline should be zero before any samples are collected")
	}

	// Feed 4 of the default 5 samples — should remain in sampling phase
	// because the sizer needs enough data before committing to a baseline
	for i := 0; i < 4; i++ {
		as.UpdateStats(100)
	}

	if !as.Active() {
		t.Fatal("sizer should still be sampling after 4 of 5 required samples")
	}

	// Fifth sample should establish the baseline and transition to baseline phase
	as.UpdateStats(100)

	if as.Active() {
		t.Fatal("sizer should transition to baseline phase after collecting 5 samples")
	}

	// Baseline = average * growthFactor / 100 = 100 * 115 / 100 = 115
	// The growth factor adds headroom to avoid buffer reallocations
	if baseline := as.GetBaseline(); baseline != 115 {
		t.Errorf("baseline should be average (100) * growthFactor (115%%) = 115, got %d", baseline)
	}
}

// TestAdaptiveSizerVariedSamples verifies that the baseline is calculated from
// the average of all samples, not just the most recent value. This ensures the
// sizer produces stable predictions from variable render sizes.
func TestAdaptiveSizerVariedSamples(t *testing.T) {
	as := NewAdaptiveSizer()

	sizes := []int{80, 100, 120, 90, 110}
	for _, size := range sizes {
		as.UpdateStats(size)
	}

	// Average is (80+100+120+90+110)/5 = 100, baseline = 100 * 115 / 100 = 115
	if baseline := as.GetBaseline(); baseline != 115 {
		t.Errorf("baseline from varied samples should average to 115, got %d", baseline)
	}
}

// TestAdaptiveSizerVarianceDetection verifies that the sizer ignores small
// deviations but triggers resampling when render sizes change significantly.
// This allows the sizer to adapt when content patterns change (e.g. a page
// starts rendering more data) without reacting to normal variation.
func TestAdaptiveSizerVarianceDetection(t *testing.T) {
	as := NewAdaptiveSizer()

	// Establish baseline of 115 (average 100 * 115% growthFactor)
	for i := 0; i < 5; i++ {
		as.UpdateStats(100)
	}

	// Small deviation within 20% variance should NOT trigger resampling.
	// Baseline is 115, 20% of 115 = 23, so values within ~92–138 are fine.
	as.UpdateStats(130)
	if as.Active() {
		t.Fatal("deviation within 20%% variance (130 vs baseline 115) should not trigger resampling")
	}

	// Large deviation beyond 20% variance SHOULD trigger resampling
	// so the sizer can adapt to the new content pattern
	as.UpdateStats(200)
	if !as.Active() {
		t.Fatal("deviation beyond 20%% variance (200 vs baseline 115) should trigger resampling")
	}
}

// TestAdaptiveSizerReset verifies that Reset returns the sizer to its initial
// state — sampling phase with no baseline — so it can re-learn buffer sizes
// from scratch when content patterns change significantly.
func TestAdaptiveSizerReset(t *testing.T) {
	as := NewAdaptiveSizer()

	// Establish baseline
	for i := 0; i < 5; i++ {
		as.UpdateStats(100)
	}
	if as.Active() {
		t.Fatal("sizer should be in baseline phase before reset")
	}

	as.Reset()

	if !as.Active() {
		t.Fatal("sizer should return to sampling phase after reset")
	}
	if as.GetBaseline() != 0 {
		t.Fatal("baseline should be zero after reset so the sizer starts fresh")
	}
}

// TestAdaptiveSizerConfigure verifies that custom parameters take effect:
// the max controls how many samples are needed, and growthFactor controls
// the headroom applied to the average.
func TestAdaptiveSizerConfigure(t *testing.T) {
	as := NewAdaptiveSizer()

	// Configure: max=3 samples, variance=10%, growthFactor=200%
	as.Configure(3, 10, 200)

	if !as.Active() {
		t.Fatal("configure should restart sampling with new parameters")
	}

	for i := 0; i < 3; i++ {
		as.UpdateStats(100)
	}

	if as.Active() {
		t.Fatal("sizer should establish baseline after 3 samples (custom max=3)")
	}

	// Baseline = average * growthFactor / 100 = 100 * 200 / 100 = 200
	if baseline := as.GetBaseline(); baseline != 200 {
		t.Errorf("baseline should be average (100) * growthFactor (200%%) = 200, got %d", baseline)
	}
}

// TestAdaptiveSizerResamplingEstablishesNewBaseline verifies the full lifecycle:
// establish baseline → detect significant change → resample → establish new
// baseline. This is the mechanism that allows the sizer to adapt when content
// patterns shift (e.g. a user's page grows over time).
func TestAdaptiveSizerResamplingEstablishesNewBaseline(t *testing.T) {
	as := NewAdaptiveSizer()

	// Establish initial baseline from small sizes
	for i := 0; i < 5; i++ {
		as.UpdateStats(100)
	}
	firstBaseline := as.GetBaseline()

	// Trigger resampling with a large deviation
	as.UpdateStats(500)
	if !as.Active() {
		t.Fatal("large deviation (500 vs baseline 115) should trigger resampling")
	}

	// Complete resampling with larger sizes — the deviation value (500)
	// was seeded as the first sample, so we need 4 more
	for i := 0; i < 4; i++ {
		as.UpdateStats(500)
	}

	if as.Active() {
		t.Fatal("sizer should establish new baseline after completing resampling")
	}

	// New baseline = 500 * 115 / 100 = 575
	secondBaseline := as.GetBaseline()
	if secondBaseline <= firstBaseline {
		t.Errorf("new baseline (%d) should be larger than initial (%d) to reflect changed content", secondBaseline, firstBaseline)
	}
	if secondBaseline != 575 {
		t.Errorf("new baseline should be average (500) * growthFactor (115%%) = 575, got %d", secondBaseline)
	}
}
