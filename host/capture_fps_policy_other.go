//go:build !windows

package main

// linuxCaptureFPSCeiling bounds capture on both Linux backends.
//
// PipeWire: Mutter negotiates the virtual monitor on the compositor's frame
// clock, so anything above the desktop's 60 Hz refresh only risks failing caps
// negotiation.
//
// x11grab: ffmpeg does not honour -framerate as a ceiling on this path. Asked
// for 120 it free-ran at ~470-490 frames/s, which the client cannot render;
// the resulting congestion walked the bitrate ladder down and produced 14
// pipeline restarts in 178 s. The same host is stable with zero restarts when
// the requested rate is 60.
const linuxCaptureFPSCeiling = 60

func effectiveCaptureFPS(target captureTarget, configuredFPS int) int {
	switch target.Kind {
	case capturePipeWire, captureX11Grab:
		if configuredFPS > linuxCaptureFPSCeiling {
			return linuxCaptureFPSCeiling
		}
	}
	return configuredFPS
}
