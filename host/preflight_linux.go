//go:build linux

package main

import (
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/unix"
)

func x11SessionActive() bool { return currentLinuxSessionBackend() == "x11" }

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
	if currentLinuxSessionBackend() == "x11" {
		if w := x11VirtualDisplayWarning(); w != "" {
			warnings["vkms"] = w
		}
	}
	if err := unix.Access("/dev/uinput", unix.W_OK); err != nil {
		warnings["uinput"] = "/dev/uinput is not writable; pen/touch/keyboard injection is disabled. Run Linux setup for uinput and pen mapping (see README), then reconnect."
	}
	return warnings
}

// x11VirtualDisplayWarning reports why the tablet would mirror the desktop on
// X11 instead of extending it, or "" when a virtual output is available (or
// one is chosen explicitly through DRAWSPLAY_X11_OUTPUT).
func x11VirtualDisplayWarning() string {
	if strings.TrimSpace(os.Getenv(x11OutputEnv)) != "" {
		return ""
	}
	st, err := queryXrandr(os.Getenv("DISPLAY"))
	if err != nil {
		return ""
	}
	return x11VirtualDisplayWarningFor(st, vkmsModuleLoaded(), vkmsModuleAvailable())
}

func x11VirtualDisplayWarningFor(st xrandrState, loaded, available bool) string {
	for _, o := range st.Outputs {
		if !x11AutoOutputPattern.MatchString(o.Name) {
			continue
		}
		if strings.HasPrefix(o.Name, "Virtual-") && !o.Connected {
			continue
		}
		return ""
	}
	switch {
	case loaded:
		return "The vkms module is loaded but X11 shows no virtual output; log out and back in so the X server picks it up, or the tablet will mirror the desktop"
	case available:
		return "X11 has no virtual display, so the tablet mirrors the desktop; load vkms (sudo modprobe vkms create_default_dev=1) to extend it"
	default:
		return "X11 has no virtual display and this kernel has no vkms module; the tablet will mirror the desktop"
	}
}

func vkmsModuleLoaded() bool {
	_, err := os.Stat("/sys/module/vkms")
	return err == nil
}

func vkmsModuleAvailable() bool {
	cmd := exec.Command("modinfo", "vkms")
	cmd.Env = append(os.Environ(), "PATH="+os.Getenv("PATH")+":/usr/sbin:/sbin")
	return cmd.Run() == nil
}
