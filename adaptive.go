package jit

import (
	"sync"
	"sync/atomic"
)

// AdaptiveSizer implements adaptive buffer sizing with minimal lock contention.
// It operates in two phases:
//
// 1. Sampling Phase: Collects render size samples to establish optimal buffer size.
// 2. Baseline Phase: Uses established size with variance monitoring for pattern changes.
//
// Performance characteristics:
// - Hot path (GetBaseline): lock-free atomic read — called on every render.
// - Warm path (variance checks): occasional mutex for pattern change detection.
// - Cold path (sampling): mutex for statistical calculations during startup.
type AdaptiveSizer struct {
	// Atomic fields — read on every render without locking
	baseline int64 // current optimal buffer size (atomic)
	active   int64 // 1 if sampling, 0 if using baseline (atomic)

	// Mutex-protected fields — only accessed during phase transitions
	mu           sync.Mutex
	sum          int // running sum during sampling phase
	count        int // sample count during sampling phase
	max          int // maximum samples before establishing baseline
	variance     int // variance threshold percentage (e.g. 20 for 20%)
	growthFactor int // growth factor percentage (e.g. 115 for 115%)
}

// NewAdaptiveSizer creates a sizer with sensible defaults.
// Default configuration:
// - max: 5 samples (quick baseline establishment).
// - variance: 20% (detects significant size changes).
// - growthFactor: 115% (prevents buffer resizing on small variations).
// - active: true (starts in sampling phase).
func NewAdaptiveSizer() *AdaptiveSizer {
	as := &AdaptiveSizer{
		max:          5,
		variance:     20,
		growthFactor: 115,
	}
	atomic.StoreInt64(&as.active, 1) // start in sampling phase
	return as
}

// Configure sets custom parameters and resets all statistics.
// This forces the sizer to restart sampling with new parameters,
// because stale statistics from previous configuration would produce
// an incorrect baseline.
//
// Parameters:
// - max: number of samples to collect before establishing baseline.
// - variance: threshold percentage for detecting significant size changes (e.g. 20).
// - growthFactor: multiplier percentage applied to average size (e.g. 115).
func (as *AdaptiveSizer) Configure(max int, variance, growthFactor int) {
	as.mu.Lock()
	defer as.mu.Unlock()

	as.max = max
	as.variance = variance
	as.growthFactor = growthFactor

	// Stale statistics from previous configuration would skew the new baseline
	as.sum = 0
	as.count = 0
	atomic.StoreInt64(&as.baseline, 0)
	atomic.StoreInt64(&as.active, 1) // restart sampling
}

// GetBaseline returns the current optimal buffer size.
// This is the hot path — called on every render — so it uses a lock-free
// atomic read to avoid contention.
func (as *AdaptiveSizer) GetBaseline() int {
	return int(atomic.LoadInt64(&as.baseline))
}

// Active returns true if currently in sampling phase.
// Lock-free read for performance.
func (as *AdaptiveSizer) Active() bool {
	return atomic.LoadInt64(&as.active) == 1
}

// Reset clears all statistics and restarts sampling.
// Useful when content patterns change significantly.
func (as *AdaptiveSizer) Reset() {
	as.mu.Lock()
	defer as.mu.Unlock()

	as.sum = 0
	as.count = 0
	atomic.StoreInt64(&as.baseline, 0)
	atomic.StoreInt64(&as.active, 1) // return to sampling
}

// UpdateStats updates sizing statistics based on actual render size.
// This automatically chooses between sampling and variance checking
// based on the current phase.
func (as *AdaptiveSizer) UpdateStats(size int) {
	if as.Active() {
		as.sample(size)
	} else {
		as.check(size)
	}
}

// sample adds a size sample and calculates baseline when enough samples collected.
// This method is called during the sampling phase to build up statistics.
// Once we have enough samples, it calculates the baseline and switches to baseline phase.
func (as *AdaptiveSizer) sample(size int) {
	as.mu.Lock()
	defer as.mu.Unlock()

	// Another goroutine may have completed sampling between the Active() check
	// and acquiring the lock — re-check to avoid corrupting a fresh baseline
	if atomic.LoadInt64(&as.active) == 0 {
		return
	}

	as.sum += size
	as.count++

	// Check if we have enough samples to establish baseline
	if as.count >= as.max {
		// Growth factor prevents tight buffer fits that would cause reallocations
		// on renders slightly larger than average
		average := as.sum / as.count
		newBaseline := (average * as.growthFactor) / 100

		atomic.StoreInt64(&as.baseline, int64(newBaseline))
		atomic.StoreInt64(&as.active, 0) // switch to baseline phase
	}
}

// check monitors deviation from baseline and reactivates sampling if needed.
// This method is called during the baseline phase to detect when content patterns
// have changed significantly, triggering a return to sampling phase.
func (as *AdaptiveSizer) check(size int) {
	baseline := as.GetBaseline()
	if baseline == 0 {
		return // no baseline established yet
	}

	// Integer math equivalent of: abs(size - baseline) / baseline > variance / 100
	// This avoids floating point on the hot path
	diff := abs(size - baseline)
	if diff*100 > baseline*as.variance {
		// Significant change detected — restart sampling to establish a new baseline
		as.mu.Lock()
		as.sum = size // seed new sampling with the value that triggered the change
		as.count = 1
		atomic.StoreInt64(&as.active, 1) // return to sampling phase
		as.mu.Unlock()
	}
}

// abs returns the absolute value of an integer.
// Used for variance calculation to avoid importing math.
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
