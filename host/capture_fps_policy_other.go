//go:build !windows

package main

// linuxCaptureFPSCeiling bounds capture on both Linux backends.
//
// PipeWire: Mutter negotiates the virtual monitor on the compositor's frame
// clock, so anything above the desktop's 60 Hz refresh only risks failing caps
// negotiation.
//
// screencopy: the compositor draws the output at its refresh rate, 60 Hz for
// the outputs the host creates.
//
// x11grab: bounded here too unless x11HighFPSSupported. The 470-490 fps
// free-run once seen at 120 was on an Xwayland root without a real X server;
// on a real X server the cause is the unpaced raw H.264 output, handled by
// -r in vaapiEncoderArgs.
const linuxCaptureFPSCeiling = 60

// platformFirstConnectFPSCeiling bounds the default rate for a new tablet.
const platformFirstConnectFPSCeiling = linuxCaptureFPSCeiling

// x11HighFPSCeiling is the x11grab capture ceiling when a hardware encoder
// can sustain it. Cinnamon/Muffin on X11 composites at 60 Hz, so the extra
// frames are duplicates; the gain is a shorter capture-phase wait (avg 8.3 ms
// at 60 fps, 4.2 ms at 120) and the tablet's 120 Hz panel presenting sooner.
// Measured on Intel UHD (iHD): x11grab -> scale_vaapi -> h264_vaapi -level 5.1
// sustains 119 fps; libx264 on one thread does not reach 80.
const x11HighFPSCeiling = 120

// x11HighFPSSupported reports whether the x11grab path can encode above
// linuxCaptureFPSCeiling. Replaced on Linux, where the VAAPI candidate exists.
var x11HighFPSSupported = func(ffmpeg string) bool { return false }

func effectiveCaptureFPS(target captureTarget, configuredFPS int, ffmpeg string) int {
	ceiling := linuxCaptureFPSCeiling
	if target.Kind == captureX11Grab && x11HighFPSSupported(ffmpeg) {
		ceiling = x11HighFPSCeiling
	}
	switch target.Kind {
	case capturePipeWire, captureX11Grab, captureScreencopy:
		if configuredFPS > ceiling {
			return ceiling
		}
	}
	return configuredFPS
}
