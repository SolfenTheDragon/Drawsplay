//go:build !windows

package main

import "time"

// Linux step-down damping.
//
// Restarting the encode subprocess is the only congestion relief the Linux
// capture backends have: no Linux pipelineSpec sets SupportsTemporalDrop,
// and temporal layering is switched off outright for Android clients
// (handleVideoSession in video.go), so nothing absorbs a congestion report
// ahead of the bitrate ladder.
//
// A restart is never free. On the Mutter virtual-monitor path only
// gst-launch-1.0 is respawned against the same PipeWire node
// (mutterDisplay.Refresh() only re-reads the monitor's place in the desktop,
// so the ScreenCast session, the virtual monitor and the client's video
// socket all survive); on x11grab the whole
// ffmpeg capture and encode process starts over. Either way the picture
// holds until the new encoder's first keyframe, so a lone report should not
// cause one: a stats sample that caught a deep decode queue, or one slow
// write while Wi-Fi roamed between access points, describes a stall that is
// already over, and restarting for it only adds a second freeze straight
// after the first.
//
// The client reports every 250 ms (VideoChannel.STATS_INTERVAL_MS), so
// requiring two reports inside 2 s delays the first step down by one stats
// interval under sustained congestion while rejecting isolated blips. Later
// steps are paced by stepDownCooldown (1.5 s), which dominates, so the rate
// at which the ladder descends is unchanged.
const (
	linuxCongestionConfirmations = 2
	linuxCongestionWindow        = 2 * time.Second
)

// newSessionCongestionGate returns the ladder step-down damping for a capture
// backend. Every Linux backend restarts its encoder to step down, so all of
// them share the same confirmation.
func newSessionCongestionGate(captureTarget) *congestionGate {
	return newCongestionGate(linuxCongestionConfirmations, linuxCongestionWindow)
}

func limitSessionBitrateKbps(kbps int) int { return kbps }
