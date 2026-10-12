package main

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
)

var operationalCounters struct {
	adbRetryAttempts atomic.Uint64
	adbRecoveries    atomic.Uint64
	inputConnections atomic.Uint64
	inputRestarts    atomic.Uint64
	videoConnections atomic.Uint64
	videoRestarts    atomic.Uint64
	captureRestarts  atomic.Uint64
}

type operationalMetrics struct {
	ADBRetryAttempts uint64 `json:"adbRetryAttempts"`
	ADBRecoveries    uint64 `json:"adbRecoveries"`
	InputConnections uint64 `json:"inputConnections"`
	InputRestarts    uint64 `json:"inputRestarts"`
	VideoConnections uint64 `json:"videoConnections"`
	VideoRestarts    uint64 `json:"videoRestarts"`
	CaptureRestarts  uint64 `json:"captureRestarts"`
}

func currentOperationalMetrics() operationalMetrics {
	return operationalMetrics{
		ADBRetryAttempts: operationalCounters.adbRetryAttempts.Load(),
		ADBRecoveries:    operationalCounters.adbRecoveries.Load(),
		InputConnections: operationalCounters.inputConnections.Load(),
		InputRestarts:    operationalCounters.inputRestarts.Load(),
		VideoConnections: operationalCounters.videoConnections.Load(),
		VideoRestarts:    operationalCounters.videoRestarts.Load(),
		CaptureRestarts:  operationalCounters.captureRestarts.Load(),
	}
}

var operationalWarnings = struct {
	sync.RWMutex
	values map[string]string
}{values: make(map[string]string)}

func setOperationalWarning(key, warning string) {
	operationalWarnings.Lock()
	defer operationalWarnings.Unlock()
	if warning == "" {
		delete(operationalWarnings.values, key)
		return
	}
	operationalWarnings.values[key] = warning
}

func currentOperationalWarnings() []string {
	operationalWarnings.RLock()
	warnings := make([]string, 0, len(operationalWarnings.values))
	for _, warning := range operationalWarnings.values {
		warnings = append(warnings, warning)
	}
	operationalWarnings.RUnlock()
	sort.Strings(warnings)
	return warnings
}

// runBackgroundPreflight re-runs runDependencyPreflight without waiting for
// it, after a change that can affect it. A variable for tests.
var runBackgroundPreflight = func(s settings) { go runDependencyPreflight(s) }

// runDependencyPreflight records degraded-mode warnings without preventing
// input-only operation when optional runtime dependencies are unavailable.
func runDependencyPreflight(s settings) {
	if err := ensureADBAvailable(s.ADB); err != nil {
		setOperationalWarning("adb-preflight", fmt.Sprintf("ADB is unreachable: %v", err))
	} else {
		setOperationalWarning("adb-preflight", "")
	}
	if len(detectEncoders(s.FFmpeg, "h264")) == 0 {
		setOperationalWarning("ffmpeg", "ffmpeg is unavailable or has no supported H.264 encoder (NVENC, AMF, QSV, or libx264)")
	} else {
		setOperationalWarning("ffmpeg", "")
	}
	for _, key := range []string{"gstreamer", "pipewire-gst", "gst-x264enc", "uinput", "vkms", "wayland-compositor"} {
		setOperationalWarning(key, "")
	}
	for key, warning := range platformDependencyWarnings() {
		setOperationalWarning(key, warning)
	}
}
