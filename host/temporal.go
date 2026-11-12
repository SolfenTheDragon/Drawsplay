package main

import "time"

// temporalController toggles forwarding of non-reference P-slices at runtime.
// When enabled, congestion first drops the enhancement layer without
// restarting the encoder; prolonged congestion still falls back to bitrate
// rung changes.
type temporalController struct {
	now        func() time.Time
	dropNonRef bool
	cleanSince time.Time
}

func newTemporalController() *temporalController {
	now := time.Now
	return &temporalController{
		now:        now,
		dropNonRef: false,
		cleanSince: now(),
	}
}

// onCongestion returns true when it changed runtime policy (no restart
// needed). It returns false when policy was already at its floor.
func (tc *temporalController) onCongestion() bool {
	tc.cleanSince = tc.now()
	if tc.dropNonRef {
		return false
	}
	tc.dropNonRef = true
	return true
}

// onClean returns true when it restored full temporal quality.
func (tc *temporalController) onClean() bool {
	if !tc.dropNonRef {
		return false
	}
	if tc.now().Sub(tc.cleanSince) < stepUpAfter {
		return false
	}
	tc.dropNonRef = false
	tc.cleanSince = tc.now()
	return true
}

func (tc *temporalController) droppingNonRef() bool {
	return tc != nil && tc.dropNonRef
}
