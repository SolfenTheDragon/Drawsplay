package main

import (
	"cmp"
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
	Key    string `json:"key"`
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Action string `json:"action,omitempty"`
	// Fix names a POST /api/setup/remediate action (setup_fixes.go) the Web
	// UI offers as a button labelled FixLabel; it may show a password prompt.
	// Every step the user can take on this computer is a fix; Action only
	// describes what happens or what to do on the tablet.
	Fix      string `json:"fix,omitempty"`
	FixLabel string `json:"fixLabel,omitempty"`
}

type setupChecklist struct {
	GeneratedAt string           `json:"generatedAt"`
	AllReady    bool             `json:"allReady"`
	Items       []setupCheckItem `json:"items"`
}

func currentSetupChecklist(cfg settings) setupChecklist {
	hostOS := runtime.GOOS
	video := currentVideoStatus()
	activeDevice := activeInputDevice()
	warnings := currentOperationalWarnings()

	var items []setupCheckItem
	if cfg.ADBTunnelEnabled {
		adb := currentADBStatus()
		item := setupCheckItem{
			Key:    "adb-ready",
			Label:  "ADB is connected",
			OK:     adb.Ready,
			Detail: cmp.Or(adb.Error, "Ready"),
			Action: adbSetupAction(hostOS, adb.Error),
		}
		item.Fix, item.FixLabel = adbSetupFix(hostOS, adb.Error)
		items = append(items, item)
	}
	if cfg.AOAEnabled {
		items = append(items, aoaSetupItem(hostOS, currentAOAStatus()))
	}
	items = append(items, []setupCheckItem{
		ffmpegSetupItem(hostOS, warnings),
		{
			Key:    "device-connected",
			Label:  "Client connected and authorized",
			OK:     activeDevice != "",
			Detail: cmp.Or(activeDevice, "Waiting for client hello"),
			Action: "Open DrawSplay on the client device and keep the app in foreground.",
		},
		streamSetupItem(video, activeDevice),
	}...)
	if hostOS == "linux" && video.Streaming {
		detail := "Extended desktop"
		if video.DisplayMode == "mirror" {
			detail = cmp.Or(video.Display, "Desktop mirror mode")
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
			Key:      "linux-uinput",
			Label:    "uinput injection access",
			OK:       !containsWarning(warnings, "uinput"),
			Detail:   warningDetail(warnings, "uinput", "Ready"),
			Action:   "Allows programs in your logged-in session to create the tablet's pen, touch and keyboard devices; you may be asked for your password.",
			Fix:      setupFixUinputRule,
			FixLabel: "Allow pen and touch input",
		})
		if x11SessionActive() {
			item := setupCheckItem{
				Key:    "linux-x11-vkms",
				Label:  "X11 virtual display (vkms)",
				OK:     !containsWarning(warnings, "vkms"),
				Detail: warningDetail(warnings, "vkms", "Ready"),
			}
			item.Fix, item.FixLabel, item.Action = x11DisplayFix()
			items = append(items, item)
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
				Detail:   firstWarningDetail(warnings, []string{"pipewire", "gstreamer"}, "Ready"),
				Action:   "Installs the GStreamer and PipeWire capture packages; you may be asked for your password.",
				Fix:      setupFixWaylandCapture,
				FixLabel: "Install capture packages",
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

func aoaSetupItem(hostOS string, st aoaStatusInfo) setupCheckItem {
	item := setupCheckItem{Key: "aoa-ready", Label: "USB accessory link"}
	switch st.State {
	case "linked":
		item.OK = true
		item.Detail = "Tablet linked over USB"
	case "waiting-app":
		item.Detail = cmp.Or(st.Detail, "Waiting for the tablet app")
		item.Action = "Accept the USB prompt on the tablet and keep DrawSplay open."
	case "switching":
		item.Detail = "Switching the tablet to accessory mode"
	case "error":
		item.Detail = st.Detail
		if hostOS == "linux" {
			item.Action = "Lets your user open the tablet over USB; you may be asked for your password."
			item.Fix, item.FixLabel = setupFixUSBRule, "Allow USB access"
		} else {
			item.Action = "Retrying lets DrawSplay install its signed WinUSB driver (in normal mode and again after the tablet re-enumerates in accessory mode). If it still fails, rerun the DrawSplay installer."
			item.Fix, item.FixLabel = setupFixRetryUSB, "Retry"
		}
	default:
		item.Detail = "No tablet detected"
		item.Action = "Connect the tablet with a USB data cable and unlock it."
		item.Fix, item.FixLabel = setupFixRetryUSB, "Look for the tablet again"
	}
	return item
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

// adbSetupFix: on Linux a permissions error needs the USB udev rule (which
// also restarts adb); anything else is retried by restarting the adb server.
func adbSetupFix(hostOS, detail string) (fix, label string) {
	lower := strings.ToLower(detail)
	if hostOS == "linux" && (strings.Contains(lower, "lacks usb permissions") || strings.Contains(lower, "no permissions")) {
		return setupFixUSBRule, "Allow USB access"
	}
	return setupFixRestartADB, "Restart ADB"
}

func ffmpegSetupItem(hostOS string, warnings []string) setupCheckItem {
	item := setupCheckItem{
		Key:    "ffmpeg-ready",
		Label:  "Video encoder available",
		OK:     !containsWarning(warnings, "ffmpeg"),
		Detail: warningDetail(warnings, "ffmpeg", "Ready"),
	}
	switch hostOS {
	case "linux":
		item.Action = "Installs ffmpeg with your package manager; you may be asked for your password. A custom ffmpeg path in settings must point to a working ffmpeg."
		item.Fix, item.FixLabel = setupFixFFmpeg, "Install ffmpeg"
	case "windows":
		item.Action = "Downloads ffmpeg next to DrawSplay. A custom ffmpeg path in settings must point to a working ffmpeg."
		item.Fix, item.FixLabel = setupFixFFmpeg, "Download ffmpeg"
	default:
		item.Action = "Install ffmpeg or set a valid ffmpeg path in settings."
	}
	return item
}

func streamSetupItem(video videoStatusInfo, activeDevice string) setupCheckItem {
	item := setupCheckItem{
		Key:    "stream-live",
		Label:  "Desktop stream active",
		OK:     video.Streaming,
		Detail: cmp.Or(video.Encoder, "Waiting for stream"),
		Action: "If this stays idle, lower the resolution or frame rate, or restart the video.",
	}
	if activeDevice != "" {
		item.Fix, item.FixLabel = setupFixRestartVideo, "Restart video"
	}
	return item
}

// containsFold reports whether sub occurs in s, ignoring case.
func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
