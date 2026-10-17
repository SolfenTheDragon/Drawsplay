//go:build windows

package main

import (
	"testing"
	"time"
)

func newTestWindowsGate(clock *testClock) *congestionGate {
	gate := newCongestionGate(windowsCongestionConfirmations, windowsCongestionWindow)
	gate.now = clock.now
	return gate.withSettle(windowsCongestionSettle).withFloorDivisor(windowsLadderFloorDivisor)
}

func TestWindowsGateIgnoresRestartSettleAndBlips(t *testing.T) {
	rc, clock := newTestRC("24M")
	gate := newTestWindowsGate(clock)
	spec := pipelineSpec{Name: "h264_qsv"}

	// Reports right after the encoder (re)start describe the restart.
	for i := 0; i < 4; i++ {
		if a := adaptToLinkFeedback(congestedMonitor(), rc, nil, gate, spec); a != congestionActionNone {
			t.Fatalf("settle report %d: action = %v, want none", i, a)
		}
		clock.t = clock.t.Add(500 * time.Millisecond)
	}
	clock.t = clock.t.Add(windowsCongestionSettle)

	// One isolated report is ignored; a second within the window steps down.
	if a := adaptToLinkFeedback(congestedMonitor(), rc, nil, gate, spec); a != congestionActionNone {
		t.Fatalf("first report: action = %v, want none", a)
	}
	clock.t = clock.t.Add(250 * time.Millisecond)
	if a := adaptToLinkFeedback(congestedMonitor(), rc, nil, gate, spec); a != congestionActionRestart {
		t.Fatalf("second report: action = %v, want restart", a)
	}
	if rc.idx != 1 {
		t.Fatalf("ladder idx = %d, want 1", rc.idx)
	}
}

func TestWindowsGateLadderFloor(t *testing.T) {
	rc, clock := newTestRC("24M")
	top := rc.topKbps()
	spec := pipelineSpec{Name: "h264_qsv"}
	for i := 0; i < 50; i++ {
		// Each step restarts the encoder, which builds a fresh gate.
		gate := newTestWindowsGate(clock)
		clock.t = clock.t.Add(windowsCongestionSettle + time.Second)
		adaptToLinkFeedback(congestedMonitor(), rc, nil, gate, spec)
		clock.t = clock.t.Add(250 * time.Millisecond)
		adaptToLinkFeedback(congestedMonitor(), rc, nil, gate, spec)
	}
	cur := parseBitrateKbps(rc.current())
	if cur < top/windowsLadderFloorDivisor {
		t.Fatalf("bitrate %dk fell below floor %dk", cur, top/windowsLadderFloorDivisor)
	}
	if rc.idx == 0 {
		t.Fatal("sustained congestion should still step down")
	}
}

func TestWindowsSessionBitrateLimit(t *testing.T) {
	for _, tc := range []struct {
		in, want int
	}{
		{in: 24000, want: 24000},
		{in: 60000, want: windowsMaxSessionBitrateKbps},
		{in: scaledBitrateKbps("24M", 2960, 1848), want: windowsMaxSessionBitrateKbps},
	} {
		if got := limitSessionBitrateKbps(tc.in); got != tc.want {
			t.Errorf("limitSessionBitrateKbps(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestWindowsVBVHoldsKeyframes(t *testing.T) {
	specs := []pipelineSpec{{Args: []string{"-b:v", "26666k", "-bufsize", "231k", "pipe:1"}}}
	got := withWindowsVBV(specs, videoOptions{Bitrate: "26666k"})
	if got[0].Args[3] != "3333k" {
		t.Fatalf("bufsize = %s, want 3333k", got[0].Args[3])
	}
}
