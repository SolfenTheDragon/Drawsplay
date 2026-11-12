package main

// maxCaptureFPS is the stream-rate ceiling on every platform. DrawSplay
// supports 60 fps only; everything rate-related derives from this constant
// so higher rates can be re-enabled later by raising it:
//
//   - defaultSettings().FPS and settings.validate() (values above it are
//     clamped on load, rejected from the Web UI);
//   - effectiveCaptureFPS on Linux (PipeWire/x11grab/screencopy ceilings)
//     and Windows (display-refresh following), plus the tablet's
//     first-connect/remembered rate (client_identity.go);
//   - the refresh rate of the Windows virtual display (desiredDisplayMode);
//   - the Web UI, which reads it from /api/status (maxFps) and hides the
//     framerate choice and the X11 high-rate toggle while it is <= 60.
const maxCaptureFPS = 60

// clampFPS bounds fps to maxCaptureFPS; non-positive values pass through
// unchanged so callers keep their "unset" semantics.
func clampFPS(fps int) int {
	if fps > maxCaptureFPS {
		return maxCaptureFPS
	}
	return fps
}
