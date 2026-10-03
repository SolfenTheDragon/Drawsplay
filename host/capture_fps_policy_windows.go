//go:build windows

package main

func effectiveCaptureFPS(target captureTarget, configuredFPS int) int {
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
		return 240
	}
	return hz
}
