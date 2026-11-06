package main

import "strings"

// Device names a client reports when it has no real one. The tray and the
// status page show these as no device.
const (
	unknownClientDevice = "unknown-client"
	unknownTabletDevice = "unknown-tablet"
)

// trayDeviceName returns the device name to show, on one line, or "" when the
// name is empty or a placeholder.
func trayDeviceName(device string) string {
	device = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(device))
	if device == unknownClientDevice || device == unknownTabletDevice {
		return ""
	}
	return device
}

// trayStatusText builds the tray's connection line and detail line, shared by
// the Linux and Windows trays. running is false while the host runtime is
// stopped from the tray (Windows).
func trayStatusText(running bool, device string, vs videoStatusInfo) (status, detail string) {
	device = trayDeviceName(device)
	if !running || device == "" {
		return "🔴 Disconnected", "Waiting for tablet"
	}
	status = "🟢 Connected: " + device
	if !vs.Streaming {
		return status, "Not streaming"
	}
	mode := "Streaming"
	switch vs.DisplayMode {
	case "extend":
		mode = "Extended display"
	case "mirror":
		mode = "Mirroring desktop"
	}
	if vs.Encoder != "" {
		mode += " · " + vs.Encoder
	}
	return status, mode
}
