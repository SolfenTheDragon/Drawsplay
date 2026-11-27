//go:build linux

package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/unix"
)

func x11SessionActive() bool { return currentLinuxSessionBackend() == "x11" }

func waylandSessionActive() bool { return currentLinuxSessionBackend() == "wayland" }

func platformDependencyWarnings() map[string]string {
	warnings := make(map[string]string)
	// The GStreamer tooling records the GNOME and KDE Plasma virtual
	// outputs; ffmpeg records sway's and Hyprland's.
	pipeWire := true
	if waylandSessionActive() {
		if b := detectWlExtBackend(); b != nil {
			pipeWire = wlExtNeedsPipeWire(b)
		} else if ok, err := mutterScreenCastAvailable(); err == nil && !ok {
			pipeWire = false
			warnings["wayland-compositor"] = waylandDesktopWarning(os.Getenv("XDG_CURRENT_DESKTOP"))
		}
	}
	if currentLinuxSessionBackend() != "x11" && pipeWire {
		if _, err := exec.LookPath("gst-launch-1.0"); err != nil {
			warnings["gstreamer"] = "gst-launch-1.0 is missing; Wayland virtual-display capture is unavailable (install gstreamer1.0-tools)"
		} else {
			if err := exec.Command("gst-inspect-1.0", "pipewiresrc").Run(); err != nil {
				warnings["pipewire-gst"] = "GStreamer PipeWire source plugin is missing (install gstreamer1.0-pipewire for Wayland capture)"
			}
			if err := exec.Command("gst-inspect-1.0", "x264enc").Run(); err != nil {
				warnings["gst-x264enc"] = "GStreamer x264enc is missing (install gstreamer1.0-plugins-ugly); Wayland capture has no software-encoder fallback"
			}
			// Measured before a tablet connects, so the first session
			// does not wait for it.
			startVAUploadProbe()
		}
	}
	if currentLinuxSessionBackend() == "x11" {
		if w := x11VirtualDisplayWarning(); w != "" {
			warnings["vkms"] = w
		}
	}
	if err := unix.Access("/dev/uinput", unix.W_OK); err != nil {
		warnings["uinput"] = "/dev/uinput is not writable; pen/touch/keyboard injection is disabled. Run the DrawSplay install script again, log out and back in, then reconnect."
	}
	return warnings
}

// waylandDesktopWarning explains what a Wayland session without a virtual
// output the host can create gives the tablet. desktop is
// XDG_CURRENT_DESKTOP.
func waylandDesktopWarning(desktop string) string {
	name, session := "This Wayland desktop", "an X11 session (or GNOME, KDE Plasma, sway or Hyprland)"
	for _, d := range strings.Split(desktop, ":") {
		if strings.Contains(strings.ToLower(d), "cinnamon") {
			name, session = "Cinnamon on Wayland", "the Cinnamon (X11) session"
		}
	}
	return name + " has no virtual monitor the host can create, so the tablet mirrors XWayland's screen, where only X11 applications show (native Wayland windows appear black); log into " + session + " to extend the desktop (see README)"
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
	// A held vkms (loaded, not yet checked) is invisible to X: report it like
	// an unloaded one, whose fix is to set the display up.
	return x11VirtualDisplayWarningFor(st, vkmsModuleLoaded() && !vkmsIsHeld(), vkmsModuleAvailable())
}

func x11VirtualDisplayReady() bool {
	st, err := queryXrandr(os.Getenv("DISPLAY"))
	if err != nil {
		return false
	}
	_, err = pickX11VirtualOutput(st, strings.TrimSpace(os.Getenv(x11OutputEnv)))
	return err == nil
}

func refreshX11VkmsWarning() {
	setOperationalWarning("vkms", x11VirtualDisplayWarning())
}

// x11DisplayFix gives the checklist button and explanation for the X11
// virtual display item: unload a vkms module the kernel failed on, or set the
// display up (vkms, else a spare connector). There is no button when only a
// new login helps (vkms loaded after X started) or nothing can be set up.
func x11DisplayFix() (fix, label, action string) {
	loaded, blocked := vkmsModuleLoaded() && !vkmsIsHeld(), vkmsBlockedFn()
	switch {
	case loaded && blocked:
		return setupFixUnloadVkms, "Unload vkms",
			"This kernel fails on vkms. Unloading it lets DrawSplay use a spare HDMI connector instead; you may be asked for your password. If the X server still uses vkms, log out and back in afterwards."
	case loaded:
		return "", "", "Log out and back in so the X server picks up the virtual display."
	case blocked && !spareHDMIConnectorFn():
		return "", "", "This computer has no free HDMI connector to use instead of vkms, so the tablet mirrors the desktop."
	}
	return setupFixVirtualDisplay, "Set up virtual display",
		"The X server reconfigures displays when the virtual display is added; save your work first. You may be asked for your password. A running stream moves onto the extended desktop by itself."
}

// vkmsUnloadOffered reports whether the Web UI offers to unload vkms: it is
// loaded in an X11 session on a kernel that failed on it.
func vkmsUnloadOffered() bool {
	return x11SessionActive() && vkmsModuleLoaded() && vkmsBlockedFn()
}

// vkmsUnloadFn is replaced in tests.
var vkmsUnloadFn = func() error { return unloadVkmsPrivileged(true) }

// unloadBlockedVkms unloads vkms (one password prompt when needed) and
// refreshes the warning it caused.
func unloadBlockedVkms() error {
	if !vkmsUnloadOffered() {
		return errors.New("vkms is not loaded, or this kernel has not failed on it; nothing to unload")
	}
	err := vkmsUnloadFn()
	refreshX11VkmsWarning()
	return err
}

// spareHDMIConnectorFn reports whether an unplugged HDMI connector is free
// for the forced-connector fallback.
var spareHDMIConnectorFn = func() bool {
	for _, c := range listHDMIConnectors() {
		if c.Status == "disconnected" && !displayClaimedByOther(c.XName) {
			return true
		}
	}
	return false
}

func x11VirtualDisplayWarningFor(st xrandrState, loaded, available bool) string {
	for _, o := range st.Outputs {
		if !isX11VirtualOutputName(o.Name) {
			continue
		}
		if strings.HasPrefix(o.Name, "Virtual-") && !o.Connected {
			continue
		}
		if vkmsOutputPattern.MatchString(o.Name) && vkmsBlockedFn() {
			continue
		}
		return ""
	}
	switch {
	case loaded && vkmsBlockedFn():
		return "The vkms module is loaded, but this kernel failed on it (vkms_prepare_fb), so it is not used and the tablet mirrors the desktop; click Unload vkms to let DrawSplay use a spare HDMI connector instead"
	case vkmsBlockedFn():
		// vkms is off on this kernel; the spare connector takes over when a
		// tablet connects, so do not suggest loading vkms.
		if spareHDMIConnectorFn() {
			return ""
		}
		return "This kernel fails on vkms (vkms_prepare_fb) and no spare HDMI connector is free, so the tablet mirrors the desktop"
	case loaded:
		return "The vkms module is loaded but X11 shows no virtual output; log out and back in so the X server picks it up, or the tablet will mirror the desktop"
	case available:
		return "X11 has no virtual display, so the tablet mirrors the desktop; click Set up virtual display to extend it"
	default:
		return "X11 has no virtual display and this kernel has no vkms module; click Set up virtual display to use a spare HDMI connector, or the tablet will mirror the desktop"
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

// waylandVirtualDisplayName names the virtual output the host makes under
// this Wayland session.
func waylandVirtualDisplayName() string {
	switch b := detectWlExtBackend(); {
	case b == nil:
		return "GNOME virtual monitor"
	case wlExtNeedsPipeWire(b):
		return "KDE Plasma virtual output"
	default:
		return b.Label() + " virtual output"
	}
}
