//go:build linux

package main

import (
	"os/exec"

	"golang.org/x/sys/unix"
)

func platformDependencyWarnings() map[string]string {
	warnings := make(map[string]string)
	if currentLinuxSessionBackend() != "x11" {
		if _, err := exec.LookPath("gst-launch-1.0"); err != nil {
			warnings["gstreamer"] = "gst-launch-1.0 is missing; Wayland virtual-display capture is unavailable (install gstreamer1.0-tools)"
		} else {
			if err := exec.Command("gst-inspect-1.0", "pipewiresrc").Run(); err != nil {
				warnings["pipewire-gst"] = "GStreamer PipeWire source plugin is missing (install gstreamer1.0-pipewire for Wayland capture)"
			}
			if err := exec.Command("gst-inspect-1.0", "x264enc").Run(); err != nil {
				warnings["gst-x264enc"] = "GStreamer x264enc is missing (install gstreamer1.0-plugins-ugly); Wayland capture has no software-encoder fallback"
			}
		}
	}
	if err := unix.Access("/dev/uinput", unix.W_OK); err != nil {
		warnings["uinput"] = "/dev/uinput is not writable; pen/touch/keyboard injection is disabled. Run Linux setup for uinput and pen mapping (see README), then reconnect."
	}
	return warnings
}
