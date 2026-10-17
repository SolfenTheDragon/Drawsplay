//go:build windows

package main

import "time"

// Windows step-down damping.
//
// For Android clients temporal layering is off, so on Windows every
// congestion report used to become an encoder restart one rung lower, and
// each restart's own hiccup (fresh IDR, brief decode backlog) could raise
// the next report: a few seconds of a USB or decoder stall walked the
// ladder from ~27 Mbps to ~0.7 Mbps, where a 2960x1848 desktop turns into
// blocks, and recovering took 30 s per rung. Now:
//
//   - two reports within 2 s are needed (an isolated sample is ignored);
//   - reports in the first 3 s after an encoder (re)start are ignored, as
//     they describe the restart itself;
//   - the ladder never goes below a quarter of its top rate. The local
//     USB link carries far more than that; a stall that persists there is
//     not a bandwidth problem a lower bitrate would fix.
const (
	// windowsMaxSessionBitrateKbps caps the pixel-scaled ladder top. On the
	// USB accessory link 60 Mbps (and occasionally 36 Mbps) produced
	// 0.4-1.1 s send stalls at 2960x1848@120 — mostly keyframe bursts the
	// link could not drain — which the user sees as lag; ~22 Mbps ran clean.
	windowsMaxSessionBitrateKbps = 30000

	windowsCongestionConfirmations = 2
	windowsCongestionWindow        = 2 * time.Second
	windowsCongestionSettle        = 3 * time.Second
	windowsLadderFloorDivisor      = 4
)

func limitSessionBitrateKbps(kbps int) int {
	if kbps > windowsMaxSessionBitrateKbps {
		return windowsMaxSessionBitrateKbps
	}
	return kbps
}

func newSessionCongestionGate(captureTarget) *congestionGate {
	return newCongestionGate(windowsCongestionConfirmations, windowsCongestionWindow).
		withSettle(windowsCongestionSettle).
		withFloorDivisor(windowsLadderFloorDivisor)
}
