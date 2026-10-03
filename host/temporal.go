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

// isDroppableNonRefNAL reports whether a unit is a non-reference AVC
// VCL NAL (type 1 with nal_ref_idc=0), which can be dropped safely when the
// stream was encoded with non-reference P-frame support.
func isDroppableNonRefNAL(unit []byte) bool {
	offset := annexBNALHeaderOffset(unit)
	if offset < 0 || offset >= len(unit) {
		return false
	}
	header := unit[offset]
	nalType := header & 0x1F
	nalRefIDC := (header >> 5) & 0x03
	return nalType == 1 && nalRefIDC == 0
}

func annexBNALHeaderOffset(unit []byte) int {
	// 00 00 01 <hdr>
	if len(unit) >= 4 && unit[0] == 0 && unit[1] == 0 && unit[2] == 1 {
		return 3
	}
	// 00 00 00 01 <hdr>
	if len(unit) >= 5 && unit[0] == 0 && unit[1] == 0 && unit[2] == 0 && unit[3] == 1 {
		return 4
	}
	return -1
}
