//go:build !windows

package main

import (
	"testing"
	"time"
)

// TestPipeWireSessionsStepDownOnSustainedCongestion is the regression test
// for the Linux port's blanket "adaptiveBitrate := target.Kind !=
// capturePipeWire" exclusion: GNOME/PipeWire sessions consumed the congestion
// signal and did nothing with it, so a client whose decode queue backed up
// stayed backed up until its 128-NAL backlog overflowed and it stalled
// waiting for the next 2-second keyframe.
func TestPipeWireSessionsStepDownOnSustainedCongestion(t *testing.T) {
	target := captureTarget{Kind: capturePipeWire, Node: 42, Width: 2560, Height: 1440}
	gate := newSessionCongestionGate(target)
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	gate.now = clock.now
	rc, _ := newTestRC("42M")
	spec := pipelineSpec{Name: "vah264enc", BaseEncoder: "vah264enc"}

	if action := adaptToLinkFeedback(congestedMonitor(), rc, nil, gate, spec); action != congestionActionNone {
		t.Fatalf("first congestion report: action = %v, want none (blip damping)", action)
	}
	if rc.idx != 0 {
		t.Fatalf("ladder idx = %d, want 0 after a single report", rc.idx)
	}

	clock.advance(250 * time.Millisecond)
	if action := adaptToLinkFeedback(congestedMonitor(), rc, nil, gate, spec); action != congestionActionRestart {
		t.Fatalf("sustained congestion: action = %v, want restart", action)
	}
	if rc.idx != 1 {
		t.Fatalf("ladder idx = %d, want 1 (bitrate must step down)", rc.idx)
	}
}

func TestPipeWireIsolatedBlipDoesNotRestart(t *testing.T) {
	gate := newSessionCongestionGate(captureTarget{Kind: capturePipeWire})
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	gate.now = clock.now
	rc, rcClock := newTestRC("42M")
	spec := pipelineSpec{Name: "vah264enc"}

	// Two blips, each alone in its window: neither may restart the encoder.
	for i := 0; i < 2; i++ {
		if action := adaptToLinkFeedback(congestedMonitor(), rc, nil, gate, spec); action != congestionActionNone {
			t.Fatalf("blip %d: action = %v, want none", i, action)
		}
		clock.advance(pipewireCongestionWindow + time.Second)
		rcClock.advance(pipewireCongestionWindow + time.Second)
	}
	if rc.idx != 0 {
		t.Fatalf("ladder idx = %d, want 0 (isolated blips must not walk the ladder)", rc.idx)
	}
}

// TestX11GrabKeepsImmediateStepDown pins the unchanged behaviour of the
// other Linux backend: only the PipeWire path gains damping.
func TestX11GrabKeepsImmediateStepDown(t *testing.T) {
	gate := newSessionCongestionGate(captureTarget{Kind: captureX11Grab})
	rc, _ := newTestRC("24M")

	if action := adaptToLinkFeedback(congestedMonitor(), rc, nil, gate, pipelineSpec{Name: "h264_vaapi"}); action != congestionActionRestart {
		t.Fatalf("action = %v, want restart", action)
	}
	if rc.idx != 1 {
		t.Fatalf("ladder idx = %d, want 1", rc.idx)
	}
}
