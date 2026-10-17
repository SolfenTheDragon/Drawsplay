package main

import "testing"

func TestUserModeTrackerIgnoresDisplaySwaps(t *testing.T) {
	ext := func(w, h int) captureTarget { return captureTarget{Kind: captureDDAGrab, Width: w, Height: h} }
	mirror := func(w, h int) captureTarget {
		return captureTarget{Kind: captureDDAGrab, Width: w, Height: h, Mirrored: true}
	}

	tr := userModeTracker{ignoreDisplaySwaps: true}
	if tr.observe(ext(1920, 1200)) {
		t.Fatal("first observation must only set the baseline")
	}
	if !tr.observe(ext(2960, 1848)) {
		t.Fatal("same-display resolution change must be a user change")
	}
	tr.displayReplaced()
	if tr.observe(mirror(2880, 1920)) {
		t.Fatal("mirror fallback must not read as a user change")
	}
	if tr.observe(mirror(1920, 1080)) {
		t.Fatal("mirrored host monitor mode must never become the tablet preference")
	}
	tr.displayReplaced()
	if tr.observe(ext(1920, 1200)) {
		t.Fatal("recovered display must start a new baseline")
	}

	legacy := userModeTracker{}
	legacy.observe(ext(1920, 1200))
	legacy.displayReplaced()
	if !legacy.observe(mirror(2880, 1920)) {
		t.Fatal("without ignoreDisplaySwaps behaviour must stay unchanged")
	}
}
