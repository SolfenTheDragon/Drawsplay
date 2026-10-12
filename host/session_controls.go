package main

import (
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

var inputLockState atomic.Bool
var displayBlackoutState atomic.Bool

func setSessionInputLocked(locked bool) {
	inputLockState.Store(locked)
}

func sessionInputLocked() bool {
	return inputLockState.Load()
}

func setSessionDisplayBlackout(blackout bool) {
	displayBlackoutState.Store(blackout)
}

func sessionDisplayBlackout() bool {
	return displayBlackoutState.Load()
}

type setupCheckItem struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail"`
	Action  string `json:"action,omitempty"`
	Command string `json:"command,omitempty"`
}

type setupChecklist struct {
	GeneratedAt string           `json:"generatedAt"`
	AllReady    bool             `json:"allReady"`
	Items       []setupCheckItem `json:"items"`
}

func currentSetupChecklist() setupChecklist {
	hostOS := runtime.GOOS
	adb := currentADBStatus()
	video := currentVideoStatus()
	activeDevice := activeInputDevice()
	warnings := currentOperationalWarnings()

	items := []setupCheckItem{
		{
			Key:     "adb-ready",
			Label:   "ADB is connected",
			OK:      adb.Ready,
			Detail:  detailOrDefault(adb.Error, "Ready"),
			Action:  adbSetupAction(hostOS, adb.Error),
			Command: adbSetupCommand(hostOS),
		},
		{
			Key:     "ffmpeg-ready",
			Label:   "Video encoder available",
			OK:      !containsWarning(warnings, "ffmpeg"),
			Detail:  warningDetail(warnings, "ffmpeg", "Ready"),
			Action:  ffmpegSetupAction(hostOS),
			Command: ffmpegSetupCommand(hostOS),
		},
		{
			Key:    "device-connected",
			Label:  "Client connected and authorized",
			OK:     activeDevice != "",
			Detail: detailOrDefault(activeDevice, "Waiting for client hello"),
			Action: "Open DrawSplay on the client device and keep the app in foreground.",
		},
		{
			Key:    "stream-live",
			Label:  "Desktop stream active",
			OK:     video.Streaming,
			Detail: detailOrDefault(video.Encoder, "Waiting for stream"),
			Action: "If this stays idle, lower resolution/FPS or reconnect the session.",
		},
	}
	if hostOS == "linux" && video.Streaming {
		detail := "Extended desktop"
		if video.DisplayMode == "mirror" {
			detail = detailOrDefault(video.Display, "Desktop mirror mode")
		}
		items = append(items, setupCheckItem{
			Key:    "linux-display-mode",
			Label:  "Display mode",
			OK:     true,
			Detail: detail,
		})
	}
	if hostOS == "linux" {
		items = append(items, setupCheckItem{
			Key:    "linux-uinput",
			Label:  "uinput injection access",
			OK:     !containsWarning(warnings, "uinput"),
			Detail: warningDetail(warnings, "uinput", "Ready"),
			Action: `Create /etc/udev/rules.d/99-drawsplay-uinput.rules with: KERNEL=="uinput", MODE="0660", GROUP="input"; add user to input group; reload udev; then reconnect.`,
		})
		if x11SessionActive() {
			items = append(items, setupCheckItem{
				Key:     "linux-x11-vkms",
				Label:   "X11 virtual display (vkms)",
				OK:      !containsWarning(warnings, "vkms"),
				Detail:  warningDetail(warnings, "vkms", "Ready"),
				Action:  "The X server reconfigures displays when vkms loads; save your work first. After loading it, click Run setup repair checks to restart the stream onto the extended desktop. To load it automatically at boot, configure the module and its options.",
				Command: "sudo modprobe vkms create_default_dev=1",
			})
		}
		if waylandSessionActive() {
			items = append(items, setupCheckItem{
				Key:    "linux-wayland-virtual-display",
				Label:  "Wayland virtual display",
				OK:     !containsWarning(warnings, "XWayland"),
				Detail: warningDetail(warnings, "XWayland", "Ready ("+waylandVirtualDisplayName()+")"),
				Action: "Extending the desktop under Wayland needs GNOME, KDE Plasma, sway or Hyprland. Log into one of them, or into an X11 session (such as Cinnamon) and load vkms.",
			})
		}
		if containsWarning(warnings, "gstreamer") || containsWarning(warnings, "pipewire") {
			items = append(items, setupCheckItem{
				Key:   "linux-wayland-capture",
				Label: "Wayland capture dependencies",
				OK: !containsWarning(warnings, "gstreamer") &&
					!containsWarning(warnings, "pipewire"),
				Detail:  firstWarningDetail(warnings, []string{"pipewire", "gstreamer"}, "Ready"),
				Action:  "Install the GStreamer and PipeWire capture dependencies.",
				Command: "sudo apt install gstreamer1.0-tools gstreamer1.0-plugins-good gstreamer1.0-plugins-bad gstreamer1.0-plugins-ugly gstreamer1.0-pipewire",
			})
		}
	}

	allReady := true
	for _, item := range items {
		if !item.OK {
			allReady = false
			break
		}
	}
	return setupChecklist{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		AllReady:    allReady,
		Items:       items,
	}
}

func containsWarning(warnings []string, needle string) bool {
	for _, warning := range warnings {
		if warning != "" && containsFold(warning, needle) {
			return true
		}
	}
	return false
}

func warningDetail(warnings []string, needle, fallback string) string {
	for _, warning := range warnings {
		if warning != "" && containsFold(warning, needle) {
			return warning
		}
	}
	return fallback
}

func firstWarningDetail(warnings []string, needles []string, fallback string) string {
	for _, needle := range needles {
		for _, warning := range warnings {
			if warning != "" && containsFold(warning, needle) {
				return warning
			}
		}
	}
	return fallback
}

func adbSetupAction(hostOS, detail string) string {
	lower := strings.ToLower(detail)
	if hostOS == "linux" {
		if strings.Contains(lower, "not authorized") {
			return "Unlock the tablet and accept the USB-debugging prompt, then restart ADB."
		}
		if strings.Contains(lower, "lacks usb permissions") || strings.Contains(lower, "no permissions") {
			return "Install Android udev rules, add the user to plugdev, reload udev, unplug and reconnect the tablet, then restart ADB."
		}
		return "Confirm USB cable + debugging prompt; if still blocked, add Android udev rules and plugdev membership, then restart ADB."
	}
	return "Confirm USB cable and accept Android USB-debugging prompts."
}

func adbSetupCommand(hostOS string) string {
	if hostOS == "linux" {
		return "adb kill-server && adb start-server"
	}
	return ""
}

func ffmpegSetupAction(hostOS string) string {
	if hostOS == "linux" {
		return "Set a valid ffmpeg path or install ffmpeg."
	}
	return "Set a valid ffmpeg path or allow automatic tool install."
}

func ffmpegSetupCommand(hostOS string) string {
	if hostOS == "linux" {
		return "sudo apt install ffmpeg"
	}
	return ""
}

func containsFold(s, sub string) bool {
	// strings.ContainsFold does not exist; keep this tiny and allocation-free.
	return len(sub) == 0 || indexFold(s, sub) >= 0
}

func indexFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFoldASCII(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ai := a[i]
		bi := b[i]
		if ai >= 'A' && ai <= 'Z' {
			ai += 'a' - 'A'
		}
		if bi >= 'A' && bi <= 'Z' {
			bi += 'a' - 'A'
		}
		if ai != bi {
			return false
		}
	}
	return true
}

func detailOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
