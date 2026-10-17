//go:build windows

package main

import "strings"

// shouldRespectWindowsResolutionSetting reports whether the live Windows
// display settings (resolution and refresh rate) are the source of truth for
// the session. For Android tablet sessions the user may change the mode in
// Windows display settings and the host follows it; other clients use the
// host-configured mode.
func shouldRespectWindowsResolutionSetting() bool {
	ident := activeClientIdentity()
	return strings.EqualFold(strings.TrimSpace(ident.Platform), "android")
}

// platformFirstConnectFPSCeiling bounds the default rate for a new tablet.
const platformFirstConnectFPSCeiling = 240

func effectiveCaptureFPS(target captureTarget, configuredFPS int, ffmpeg string) int {
	if !shouldRespectWindowsResolutionSetting() {
		return configuredFPS
	}
	if target.DeviceName == "" {
		return configuredFPS
	}
	_, _, hz, ok := currentModeOf(target.DeviceName)
	if !ok || hz <= 0 {
		return configuredFPS
	}
	if hz < 30 {
		return 30
	}
	if hz > 240 {
		hz = 240
	}
	if panelHz := tabletPanelHz(); hz > panelHz {
		return panelHz
	}
	return hz
}

// defaultTabletPanelHz is assumed when the tablet reports no refresh rate:
// Android tablets top out at 120 Hz in practice.
const defaultTabletPanelHz = 120

// tabletPanelHz is the tablet's panel refresh rate. Capture never exceeds
// it: frames beyond what the panel can show back the decoder up until it
// skips a burst of stale pictures, a visible hitch during fast motion
// (seen with a 144 Hz virtual display on a 120 Hz tablet).
func tabletPanelHz() int {
	if hz := activeClientIdentity().maxRefreshRate(); hz > 0 {
		return hz
	}
	if st := currentClientVideoStats(); st != nil && st.RefreshHz >= minClientRefreshHz {
		return int(st.RefreshHz + 0.5)
	}
	return defaultTabletPanelHz
}
