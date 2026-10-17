package main

// Congestion response, shared by all platforms.
//
// sessionMonitor (feedback.go) condenses host write stalls and the client's
// upstream decode statistics into a single boolean. This file turns that raw
// signal into the two mitigations a session can apply:
//
//	temporal drop — stop forwarding non-reference enhancement frames; the
//	                encoder keeps running (temporal.go, spec-gated).
//	ladder step   — restart the encode subprocess one rung lower
//	                (ratecontrol.go).
//
// Capture backends differ in whether temporal drop is available at all, so
// how eagerly a congestion report may become a restart is a per-backend
// policy: see newSessionCongestionGate in
// congestion_policy_windows.go / congestion_policy_other.go.

import (
	"log"
	"time"
)

// congestionGate folds repeated congestion reports into the decision to move
// the bitrate ladder. Requiring more than one report inside a window keeps an
// isolated blip — a single client stats sample that happened to catch a deep
// decode queue — from restarting the encoder, while sustained congestion
// still gets through after one extra stats interval.
//
// A nil gate reports every congestion event, so callers need not check.
// Not safe for concurrent use; the session goroutine owns it.
type congestionGate struct {
	confirmations int
	window        time.Duration
	now           func() time.Time // injectable clock for tests
	seen          int
	firstSeen     time.Time
	// settleUntil ignores reports until this time: the first stats after an
	// encoder (re)start describe the restart hiccup, not the link. Zero
	// (the default) disables it.
	settleUntil time.Time
	// floorDivisor refuses ladder steps below top/floorDivisor
	// (0 = down to the ladder floor).
	floorDivisor int
}

// withSettle makes the gate ignore reports for d after now.
func (g *congestionGate) withSettle(d time.Duration) *congestionGate {
	g.settleUntil = g.now().Add(d)
	return g
}

// withFloorDivisor makes the gate refuse ladder steps below 1/div of the
// ladder's top rate.
func (g *congestionGate) withFloorDivisor(div int) *congestionGate {
	g.floorDivisor = div
	return g
}

// allowsStepDown reports whether the gate permits rc's next lower rung.
func (g *congestionGate) allowsStepDown(rc *rateController) bool {
	if g == nil || g.floorDivisor <= 0 {
		return true
	}
	next, ok := rc.nextLowerKbps()
	return ok && next >= rc.topKbps()/g.floorDivisor
}

// newCongestionGate builds a gate that fires once confirmations reports have
// arrived within window. A window of zero never expires partial progress,
// which is what a single-confirmation (immediate) gate wants.
func newCongestionGate(confirmations int, window time.Duration) *congestionGate {
	if confirmations < 1 {
		confirmations = 1
	}
	return &congestionGate{confirmations: confirmations, window: window, now: time.Now}
}

// observe records one congestion report and reports whether the episode is
// sustained enough to act on. Firing rearms the gate, so each ladder step
// needs its own fresh run of confirmations.
func (g *congestionGate) observe() bool {
	if g == nil {
		return true
	}
	t := g.now()
	if t.Before(g.settleUntil) {
		return false
	}
	if g.seen > 0 && g.window > 0 && t.Sub(g.firstSeen) > g.window {
		// The earlier reports belong to an episode that resolved on its
		// own; they must not add up with this one.
		g.seen = 0
	}
	if g.seen == 0 {
		g.firstSeen = t
	}
	g.seen++
	if g.seen < g.confirmations {
		return false
	}
	g.seen = 0
	return true
}

// congestionAction is what one poll of the congestion signal asks the
// pipeline pump to do.
type congestionAction int

const (
	// congestionActionNone: keep streaming, nothing changed.
	congestionActionNone congestionAction = iota
	// congestionActionAdapted: runtime forwarding policy changed; the
	// encoder keeps running.
	congestionActionAdapted
	// congestionActionRestart: restart at the rate controller's new rung.
	congestionActionRestart
)

// adaptToLinkFeedback folds one poll of the congestion signal into the rate
// and temporal controllers. The pipeline pump calls it after every chunk it
// forwards; rc is required, temporal and gate may be nil.
func adaptToLinkFeedback(monitor *sessionMonitor, rc *rateController, temporal *temporalController, gate *congestionGate, spec pipelineSpec) congestionAction {
	if monitor.consumeCongestion() {
		// Congestion means the stream is not clean whether or not it is
		// acted on below, so ladder step-ups stay blocked either way.
		rc.resetCleanWindow()
		if temporal != nil && spec.SupportsTemporalDrop && temporal.onCongestion() {
			log.Printf("Temporal adaptation enabled (%s): dropping non-reference enhancement frames", spec.Name)
			return congestionActionAdapted
		}
		if gate.observe() && gate.allowsStepDown(rc) && rc.onCongestion() {
			return congestionActionRestart
		}
	} else if temporal != nil && spec.SupportsTemporalDrop && temporal.onClean() {
		rc.resetCleanWindow()
		log.Printf("Temporal adaptation recovered (%s): forwarding full temporal stream", spec.Name)
		return congestionActionAdapted
	}
	if rc.shouldStepUp() {
		return congestionActionRestart
	}
	return congestionActionNone
}
