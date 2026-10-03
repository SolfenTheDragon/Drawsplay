//go:build windows

package main

// newSessionCongestionGate: Windows acts on the first congestion report, as
// it always has. DXGI specs can carry SupportsTemporalDrop (h264_nvenc
// temporal layering, see pipeline_windows.go), which already absorbs a first
// blip without restarting the encoder, so no extra damping is applied here.
func newSessionCongestionGate(captureTarget) *congestionGate {
	return newCongestionGate(1, 0)
}
