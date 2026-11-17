package main

// One-click fixes for the setup checklist. Each checklist item that can be
// repaired names one of these actions (setupCheckItem.Fix); the Web UI shows
// it as a button that posts the action to /api/setup/remediate. Fixes that
// need root go through the display helper and may show one polkit password
// prompt (see runDisplayHelperAction).

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	setupFixRestartADB     = "restart-adb-server"
	setupFixRetryUSB       = "retry-usb"
	setupFixRestartVideo   = "restart-video"
	setupFixUSBRule        = "install-usb-rule"
	setupFixUinputRule     = "install-uinput-rule"
	setupFixFFmpeg         = "install-ffmpeg"
	setupFixWaylandCapture = "install-wayland-capture"
	setupFixVirtualDisplay = "setup-virtual-display"
	setupFixUnloadVkms     = "unload-vkms"
)

// errPasswordPromptDismissed: the user closed the polkit prompt, or is not
// allowed to administer this computer.
var errPasswordPromptDismissed = errors.New("the password prompt was dismissed")

var errUnknownSetupFix = errors.New("unknown setup action")

// runPreflightNow re-runs the dependency checks before a fix answers, so the
// checklist the Web UI shows next already reflects the fix. Tests replace it.
var runPreflightNow = runDependencyPreflight

type setupFixResult struct {
	Message        string
	VideoRestarted bool
}

// runSetupFix performs one checklist fix and re-checks the setup.
func runSetupFix(action string, cfg settings) (setupFixResult, error) {
	var res setupFixResult
	switch action {
	case setupFixRestartADB:
		if !cfg.ADBTunnelEnabled {
			return res, errors.New("the ADB tunnel is off in settings")
		}
		if err := restartADBServer(cfg.ADB); err != nil {
			return res, err
		}
		res.Message = "ADB restarted."
	case setupFixRetryUSB:
		if !cfg.AOAEnabled {
			return res, errors.New("USB accessory mode is off in settings")
		}
		requestAOASwitch()
		res.Message = "Looking for the tablet again. Keep it unlocked with DrawSplay open."
	case setupFixRestartVideo:
		if !restartActiveVideoSession() {
			return res, errors.New("no stream is running to restart; reconnect the tablet")
		}
		res.Message = "Video restarted."
		res.VideoRestarted = true
	default:
		msg, handled, err := platformSetupFix(action, cfg)
		if !handled {
			return res, fmt.Errorf("%w: %s", errUnknownSetupFix, action)
		}
		if err != nil {
			runPreflightNow(cfg)
			return res, err
		}
		res.Message = msg
	}
	runPreflightNow(cfg)
	if !res.VideoRestarted && shouldRestartX11AfterSetupRepair(x11SessionActive(), currentVideoStatus(), x11VirtualDisplayReady()) {
		res.VideoRestarted = restartActiveVideoSession()
	}
	return res, nil
}

// restartADBServer is `adb kill-server && adb start-server`.
func restartADBServer(adbPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// kill-server fails when no server runs, which is fine.
	_ = hiddenCommandContext(ctx, adbPath, "kill-server").Run()
	if err := ensureADBAvailable(adbPath); err != nil {
		return fmt.Errorf("adb restart failed: %w", err)
	}
	return nil
}
