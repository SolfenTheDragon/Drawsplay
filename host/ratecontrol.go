package main

// Adaptive bitrate control (milestone M4).
//
// The encode subprocess cannot retune mid-stream, so adaptation works by
// restarting the pipeline at a different rung of a bitrate ladder derived
// from the user's -bitrate flag. Restarts open with fresh parameter sets and
// an IDR frame (session-start behavior), so the client recovers instantly;
// the cost is one brief hiccup, which is why transitions are damped:
//
//   - Step DOWN on congestion (host write stalls or client-reported decode
//     queue growth), at most once per cooldown period.
//   - Step UP only after a sustained clean interval, and if congestion
//     recurs shortly after stepping up, that rung is held off for a penalty
//     period (prevents oscillation).
//
// Resolution is deliberately not adapted: the virtual display's mode is
// fixed at session start, and re-plugging a monitor mid-session would
// disrupt the desktop layout (windows move). Bitrate covers the practical
// congestion range of a local USB link.

import (
	"fmt"
	"log"
	"time"
)

const (
	// stepDownCooldown limits how often the rate can drop (restart storms
	// help nobody when the link is bad).
	stepDownCooldown = 1500 * time.Millisecond
	// stepUpAfter is the clean-streaming interval required before probing a
	// higher rung.
	stepUpAfter = 30 * time.Second
	// stepUpBackoff blocks re-probing a rung that congested soon after a
	// step up.
	stepUpBackoff = 5 * time.Minute
	// quickRelapse: congestion this soon after a step up blames the step.
	quickRelapse = 15 * time.Second
	// minLadderKbps is the floor below which video quality is useless.
	minLadderKbps = 500
	// maxLadderKbps caps the ladder top at the practical USB 2.0 budget.
	// The 480 Mbps bus delivers roughly 100-200 Mbps through adb's
	// forwarding path; 60 Mbps saturates visual quality at tablet
	// resolutions while leaving ample headroom for the input channel and
	// protocol overhead, so the top rung never congests the link by
	// construction.
	maxLadderKbps = 60000
	// queueRecurrence: queue-only congestion returning within this window of
	// a queue-driven step-down means the step did not help.
	queueRecurrence = 15 * time.Second
	// futileStepLimit consecutive futile steps mark the decoder as the bottleneck.
	futileStepLimit = 2
	// underusedFraction: a stream sending less than this share of its rung
	// is not limited by the link, so lowering the rung cannot help.
	underusedFraction = 0.5
)

// rateController owns the bitrate ladder position for one video session.
// Not safe for concurrent use; the session goroutine owns it.
type rateController struct {
	ladder     []int // kbps, best (index 0) to worst
	idx        int
	now        func() time.Time // injectable clock for tests
	lastChange time.Time
	lastStepUp time.Time
	holdUntil  time.Time // no step-ups before this (post-relapse penalty)
	cleanSince time.Time

	// Decoder-bound detection (see onQueueCongestion).
	bound          bool
	seriesStartIdx int       // rung before the current run of queue-driven step-downs
	lastQueueStep  time.Time // zero when no run is active
	lastQueueBytes int64
	futileSteps    int
}

// newRateController builds the ladder from the configured top bitrate:
// successive ~60% reductions down to the floor.
func newRateController(topBitrate string) *rateController {
	return newRateControllerKbps(parseBitrateKbps(topBitrate))
}

// newRateControllerKbps builds the ladder from a top bitrate in kbps.
func newRateControllerKbps(top int) *rateController {
	if top < minLadderKbps {
		top = minLadderKbps
	}
	if top > maxLadderKbps {
		top = maxLadderKbps
	}
	var ladder []int
	for r := top; r >= minLadderKbps; r = r * 3 / 5 {
		ladder = append(ladder, r)
	}
	rc := &rateController{ladder: ladder, now: time.Now}
	rc.cleanSince = rc.now()
	return rc
}

// scaledBitrateKbps scales the configured bitrate by the display's pixel
// count relative to full HD (1920x1080), the resolution the default budget
// is tuned for, keeping quality per pixel roughly constant. Unknown
// dimensions leave the configured rate untouched. The result is clamped to
// [minLadderKbps, maxLadderKbps] so high-resolution displays never scale
// the stream past the USB 2.0 link budget.
func scaledBitrateKbps(bitrate string, w, h int) int {
	top := parseBitrateKbps(bitrate)
	if w > 0 && h > 0 {
		top = top * (w * h) / (1920 * 1080)
	}
	if top < minLadderKbps {
		top = minLadderKbps
	}
	if top > maxLadderKbps {
		top = maxLadderKbps
	}
	return top
}

// current returns the active bitrate in ffmpeg syntax (e.g. "4800k").
func (rc *rateController) current() string {
	return fmt.Sprintf("%dk", rc.ladder[rc.idx])
}

// nextLowerKbps returns the rung below the current one, if any.
func (rc *rateController) nextLowerKbps() (int, bool) {
	if rc.idx >= len(rc.ladder)-1 {
		return 0, false
	}
	return rc.ladder[rc.idx+1], true
}

// topKbps returns the ladder's highest rung.
func (rc *rateController) topKbps() int { return rc.ladder[0] }

// onCongestion records a congestion event. It returns true when the caller
// should restart the pipeline at the new, lower rate.
func (rc *rateController) onCongestion() bool {
	t := rc.now()
	rc.cleanSince = t
	// A relapse right after probing a higher rung blames the probe: go back
	// down and hold that rung off for a while.
	if !rc.lastStepUp.IsZero() && t.Sub(rc.lastStepUp) < quickRelapse {
		rc.holdUntil = t.Add(stepUpBackoff)
	}

	if rc.idx >= len(rc.ladder)-1 {
		return false // already at the floor; ride it out
	}
	if t.Sub(rc.lastChange) < stepDownCooldown {
		return false // recently changed; don't storm restarts
	}
	rc.idx++
	rc.lastChange = t
	return true
}

// decoderBound reports whether this session's tablet decoder, not the link,
// was identified as the bottleneck.
func (rc *rateController) decoderBound() bool { return rc.bound }

// onQueueCongestion handles congestion signalled only by the client's decode
// queue (no slow socket write). It behaves like onCongestion, but if stepping
// down repeatedly fails to help while the stream uses well under its rung,
// the decoder is the limit: the rung before the series is restored once and
// the session stops reacting to queue-only spikes. bytesSent is the
// cumulative bytes written to the client.
func (rc *rateController) onQueueCongestion(bytesSent int64) bool {
	t := rc.now()
	if rc.bound {
		return false
	}
	inRun := !rc.lastQueueStep.IsZero() && t.Sub(rc.lastQueueStep) < queueRecurrence
	if inRun {
		elapsed := t.Sub(rc.lastQueueStep).Seconds()
		rungBps := float64(rc.ladder[rc.idx]) * 1000
		if elapsed > 0 && float64(bytesSent-rc.lastQueueBytes)*8/elapsed < rungBps*underusedFraction {
			rc.futileSteps++
		} else {
			rc.futileSteps = 0
		}
	} else {
		rc.seriesStartIdx = rc.idx
		rc.futileSteps = 0
	}
	if rc.futileSteps >= futileStepLimit {
		rc.bound = true
		rc.cleanSince = t
		log.Printf("Decoder-bound client detected: %d queue-driven step-downs did not help while sending under %d%% of the rung; restoring %dk and ignoring decode-queue spikes for this session", rc.futileSteps, int(underusedFraction*100), rc.ladder[rc.seriesStartIdx])
		if rc.idx == rc.seriesStartIdx {
			return false
		}
		rc.idx = rc.seriesStartIdx
		rc.lastChange = t
		return true
	}
	stepped := rc.onCongestion()
	if stepped {
		rc.lastQueueStep = t
		rc.lastQueueBytes = bytesSent
	}
	return stepped
}

// resetCleanWindow marks the stream as recently unstable for step-up gating
// without changing the current ladder rung.
func (rc *rateController) resetCleanWindow() {
	rc.cleanSince = rc.now()
}

// shouldStepUp reports whether a sustained clean period justifies probing
// the next-higher rung. Callers restart the pipeline when it returns true.
func (rc *rateController) shouldStepUp() bool {
	t := rc.now()
	if rc.bound || rc.idx == 0 || t.Before(rc.holdUntil) || t.Sub(rc.cleanSince) < stepUpAfter {
		return false
	}
	rc.idx--
	rc.lastChange = t
	rc.lastStepUp = t
	rc.cleanSince = t
	return true
}
