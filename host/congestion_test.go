package main

import (
	"testing"
	"time"
)

func newTestGate(confirmations int, window time.Duration) (*congestionGate, *testClock) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	gate := newCongestionGate(confirmations, window)
	gate.now = clock.now
	return gate, clock
}

func TestCongestionGateImmediateFiresOnFirstReport(t *testing.T) {
	gate, _ := newTestGate(1, 0)
	for i := 0; i < 3; i++ {
		if !gate.observe() {
			t.Fatalf("report %d: immediate gate should fire on every report", i)
		}
	}
}

func TestCongestionGateNilFires(t *testing.T) {
	var gate *congestionGate
	if !gate.observe() {
		t.Fatal("nil gate should report every congestion event")
	}
}

func TestCongestionGateRequiresSustainedReports(t *testing.T) {
	gate, clock := newTestGate(2, 2*time.Second)
	if gate.observe() {
		t.Fatal("first report should not fire a two-confirmation gate")
	}
	clock.advance(250 * time.Millisecond)
	if !gate.observe() {
		t.Fatal("second report inside the window should fire")
	}
	// Firing rearms: the next step needs its own pair of reports.
	clock.advance(250 * time.Millisecond)
	if gate.observe() {
		t.Fatal("gate should rearm after firing")
	}
	clock.advance(250 * time.Millisecond)
	if !gate.observe() {
		t.Fatal("second report of the next run should fire")
	}
}

func TestCongestionGateForgetsStaleReports(t *testing.T) {
	gate, clock := newTestGate(2, 2*time.Second)
	if gate.observe() {
		t.Fatal("first report should not fire")
	}
	clock.advance(3 * time.Second)
	if gate.observe() {
		t.Fatal("a report past the window starts a new episode, not a step down")
	}
	clock.advance(250 * time.Millisecond)
	if !gate.observe() {
		t.Fatal("second report of the new episode should fire")
	}
}

// congestedMonitor returns a monitor whose next consumeCongestion reports
// congestion once, the way runStatsReader leaves it after a bad stats line.
func congestedMonitor() *sessionMonitor {
	mon := &sessionMonitor{}
	mon.congested.Store(true)
	return mon
}

func TestAdaptToLinkFeedbackStepsDownOnCongestion(t *testing.T) {
	rc, _ := newTestRC("8M")
	action := adaptToLinkFeedback(congestedMonitor(), rc, nil, newCongestionGate(1, 0), pipelineSpec{Name: "test"})
	if action != congestionActionRestart {
		t.Fatalf("action = %v, want restart", action)
	}
	if rc.idx != 1 {
		t.Fatalf("ladder idx = %d, want 1", rc.idx)
	}
}

func TestAdaptToLinkFeedbackHoldsCleanWindowWhenGateWithholds(t *testing.T) {
	rc, clock := newTestRC("8M")
	gate, _ := newTestGate(2, 2*time.Second)
	rc.cleanSince = clock.t.Add(-stepUpAfter - time.Second)
	rc.idx = 1

	action := adaptToLinkFeedback(congestedMonitor(), rc, nil, gate, pipelineSpec{Name: "test"})
	if action != congestionActionNone {
		t.Fatalf("action = %v, want none", action)
	}
	if rc.idx != 1 {
		t.Fatalf("ladder idx = %d, want 1 (unwithheld congestion must not move the rung)", rc.idx)
	}
	// The withheld report still counts as "not clean", so no step up either.
	if rc.shouldStepUp() {
		t.Fatal("congestion must reset the clean window even when the gate withholds")
	}
}

func TestAdaptToLinkFeedbackStepsUpAfterCleanInterval(t *testing.T) {
	rc, clock := newTestRC("8M")
	rc.idx = 1
	clock.advance(stepUpAfter + time.Second)

	action := adaptToLinkFeedback(&sessionMonitor{}, rc, nil, newCongestionGate(1, 0), pipelineSpec{Name: "test"})
	if action != congestionActionRestart {
		t.Fatalf("action = %v, want restart", action)
	}
	if rc.idx != 0 {
		t.Fatalf("ladder idx = %d, want 0", rc.idx)
	}
}

func TestAdaptToLinkFeedbackTemporalDropAvoidsRestart(t *testing.T) {
	rc, _ := newTestRC("8M")
	spec := pipelineSpec{Name: "h264_nvenc", SupportsTemporalDrop: true}

	action := adaptToLinkFeedback(congestedMonitor(), rc, newTemporalController(), nil, spec)
	if action != congestionActionAdapted {
		t.Fatalf("action = %v, want adapted", action)
	}
	if rc.idx != 0 {
		t.Fatalf("ladder idx = %d, want 0 (temporal drop absorbs the first episode)", rc.idx)
	}
}

func TestAdaptToLinkFeedbackIdleStreamDoesNothing(t *testing.T) {
	rc, _ := newTestRC("8M")
	if action := adaptToLinkFeedback(&sessionMonitor{}, rc, nil, newCongestionGate(1, 0), pipelineSpec{Name: "test"}); action != congestionActionNone {
		t.Fatalf("action = %v, want none", action)
	}
}
