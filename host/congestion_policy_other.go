//go:build !windows

package main

import "time"

// PipeWire step-down damping.
//
// Restarting the encode subprocess is the only congestion relief the Mutter
// virtual-monitor path has: no Linux pipelineSpec sets SupportsTemporalDrop,
// and temporal layering is switched off outright for Android clients
// (handleVideoSession in video.go), so nothing absorbs a congestion report
// ahead of the bitrate ladder.
//
// The restart itself is cheap on this backend — mutterDisplay.Refresh() is a
// no-op, so the ScreenCast session, the virtual monitor, its PipeWire node
// and the client's video socket all survive, and only gst-launch-1.0 is
// respawned against the same node — but it is not free, so a lone stats
// sample that happened to catch a deep decode queue should not cause one.
//
// The client reports every 250 ms (VideoChannel.STATS_INTERVAL_MS), so
// requiring two reports inside 2 s delays the first step down by one stats
// interval under sustained congestion while rejecting isolated blips. Later
// steps are paced by stepDownCooldown (1.5 s), which dominates, so the rate
// at which the ladder descends is unchanged.
const (
	pipewireCongestionConfirmations = 2
	pipewireCongestionWindow        = 2 * time.Second
)

// newSessionCongestionGate returns the ladder step-down damping for a capture
// backend. x11grab keeps the immediate response it has always had; its
// pathological restart storms came from over-*rate* capture, which the fps
// clamp in capture_fps_policy_other.go removed at the source.
func newSessionCongestionGate(target captureTarget) *congestionGate {
	if target.Kind == capturePipeWire {
		return newCongestionGate(pipewireCongestionConfirmations, pipewireCongestionWindow)
	}
	return newCongestionGate(1, 0)
}
