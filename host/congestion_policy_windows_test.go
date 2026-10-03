//go:build windows

package main

import "testing"

// TestWindowsCaptureStepsDownOnFirstCongestion pins the Windows response as
// unchanged by the Linux congestion-gate work: the first report moves the
// ladder, exactly as the pre-gate code did.
func TestWindowsCaptureStepsDownOnFirstCongestion(t *testing.T) {
	gate := newSessionCongestionGate(captureTarget{Kind: captureDDAGrab})
	rc, _ := newTestRC("24M")

	if action := adaptToLinkFeedback(congestedMonitor(), rc, nil, gate, pipelineSpec{Name: "h264_nvenc"}); action != congestionActionRestart {
		t.Fatalf("action = %v, want restart", action)
	}
	if rc.idx != 1 {
		t.Fatalf("ladder idx = %d, want 1", rc.idx)
	}
}
