//go:build linux

package main

import (
	"errors"

	"golang.org/x/sys/unix"
)

// platformSetupFix runs the Linux checklist fixes. handled is false for an
// action this platform does not know.
func platformSetupFix(action string, cfg settings) (msg string, handled bool, err error) {
	switch action {
	case setupFixUSBRule:
		if _, err := runDisplayHelperFn(true, "udev-rule", "aoa"); err != nil {
			return "", true, err
		}
		if cfg.ADBTunnelEnabled {
			// adb opened the tablet before the rule; it needs a fresh look.
			_ = restartADBServer(cfg.ADB)
		}
		if cfg.AOAEnabled {
			requestAOASwitch()
		}
		return "USB access for tablets is set up. If the tablet is still not found, unplug it and plug it back in.", true, nil
	case setupFixUinputRule:
		if _, err := runDisplayHelperFn(true, "udev-rule", "uinput"); err != nil {
			return "", true, err
		}
		if unix.Access("/dev/uinput", unix.W_OK) != nil {
			return "The input rule is installed, but this login session has not picked it up yet; log out and back in, then reconnect the tablet.", true, nil
		}
		return "Pen, touch and keyboard input are allowed. Reconnect the tablet to start using them.", true, nil
	case setupFixFFmpeg:
		if _, err := runDisplayHelperFn(true, "packages", "ffmpeg"); err != nil {
			return "", true, err
		}
		return "ffmpeg is installed.", true, nil
	case setupFixWaylandCapture:
		if _, err := runDisplayHelperFn(true, "packages", "wayland-capture"); err != nil {
			return "", true, err
		}
		return "The capture packages are installed.", true, nil
	case setupFixVirtualDisplay:
		if !x11SessionActive() {
			return "", true, errors.New("this is not an X11 session")
		}
		if err := setUpX11VirtualDisplayNow(); err != nil {
			return "", true, err
		}
		return "The virtual display is ready.", true, nil
	case setupFixUnloadVkms:
		if err := unloadBlockedVkms(); err != nil {
			return "", true, err
		}
		return "vkms is unloaded; DrawSplay uses a spare HDMI connector instead.", true, nil
	}
	return "", false, nil
}
