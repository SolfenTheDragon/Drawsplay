package main

// ADB integration: availability check and reverse-tunnel maintenance.
//
// The Android client connects to 127.0.0.1 on its own network stack; adb
// forwards those connections to the host over USB. Because devices can be
// plugged in after startup or reconnected after a cable drop, the reverse
// rules are re-asserted on an interval rather than configured once.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

const (
	adbReverseRetryDelay    = 2 * time.Second
	adbReverseMaxRetryDelay = 30 * time.Second
	adbCommandTimeout       = 5 * time.Second
	adbLaunchTimeout        = 8 * time.Second
)

var (
	errNoAuthorizedADBDevices    = errors.New("no authorized devices attached")
	errPreferredADBUnavailable   = errors.New("preferred ADB device is not attached or authorized")
	errStrictADBMissingPreferred = errors.New("strict device trust is enabled but preferred ADB serial is not set")
	errADBDeviceUnauthorized     = errors.New("adb detected a connected device that is not authorized")
	errADBDeviceNoPermissions    = errors.New("adb detected a connected device but the host user lacks USB permissions")
)

// ensureADBAvailable starts the adb server, verifying the binary works.
func ensureADBAvailable(adbPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := hiddenCommandContext(ctx, adbPath, "start-server")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("start-server failed: %w (%s)", err, string(out))
	}
	return nil
}

// maintainADBReverse keeps `adb reverse tcp:<port> tcp:<port>` configured for
// every given port on the configured target devices for the life of the
// process. With a preferred serial, only that device is targeted; otherwise
// every authorized attached device receives the rules. Rules use `-s`, so a
// phone plus the tablet does not trigger adb's ambiguous-device error.
// Failures are retried with bounded exponential backoff and jitter.
func maintainADBReverse(ctx context.Context, store *settingsStore, ports ...int) {
	readySerials := ""
	failureLogged := false
	failures := 0
	launchedForSerial := map[string]bool{}
	for {
		cfg := store.get()
		if !cfg.ADBTunnelEnabled {
			if readySerials != "" || failures > 0 || failureLogged {
				setOperationalWarning("adb-runtime", "")
				readySerials, failures, failureLogged = "", 0, false
				clear(launchedForSerial)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		serials, err := attachedDeviceSerials(ctx, cfg.ADB)
		targets := []string(nil)
		if err == nil {
			targets, err = selectADBTargets(serials, cfg.PreferredADBSerial, cfg.RequirePreferredADB)
			if err == nil {
				err = assertReverseRules(ctx, cfg.ADB, targets, ports)
			}
		}
		publishADBStatus(err == nil, cfg.PreferredADBSerial, serials, targets, err)
		if cfg.AutoLaunchAndroidApp && err == nil {
			launchConfiguredAppOnNewTargets(ctx, cfg.ADB, targets, launchedForSerial)
		} else if !cfg.AutoLaunchAndroidApp {
			clear(launchedForSerial)
		}

		delay := adbReverseRetryDelay
		if err != nil {
			operationalCounters.adbRetryAttempts.Add(1)
			setOperationalWarning("adb-runtime", fmt.Sprintf("ADB reverse is degraded: %v", err))
			if !failureLogged {
				log.Printf("ADB reverse not configured yet (device connected?), retrying: %v", err)
				failureLogged = true
			}
			readySerials = ""
			delay = retryDelayForADBError(err, failures)
			if shouldRestartADBServer(err, failures) {
				restartADBServerBestEffort(cfg.ADB)
			}
			failures++
		} else if joined := strings.Join(targets, ","); joined != readySerials {
			if failures > 0 {
				operationalCounters.adbRecoveries.Add(1)
			}
			failures = 0
			setOperationalWarning("adb-runtime", "")
			readySerials = joined
			failureLogged = false
			log.Printf("ADB reverse ready for ports %v on %v", ports, targets)
			if len(targets) > 0 {
				ensureX11VirtualDisplayAsync("device connected")
			}
		} else {
			failures = 0
			setOperationalWarning("adb-runtime", "")
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func launchConfiguredAppOnNewTargets(ctx context.Context, adbPath string, targets []string, launched map[string]bool) {
	current := make(map[string]struct{}, len(targets))
	for _, serial := range targets {
		current[serial] = struct{}{}
	}
	for serial := range launched {
		if _, ok := current[serial]; !ok {
			delete(launched, serial)
		}
	}
	for _, serial := range targets {
		if launched[serial] {
			continue
		}
		if err := launchDrawSplayApp(ctx, adbPath, serial); err != nil {
			log.Printf("ADB app launch failed for %s: %v", serial, err)
			continue
		}
		launched[serial] = true
		log.Printf("ADB app launch requested for %s", serial)
	}
}

func launchDrawSplayApp(ctx context.Context, adbPath, serial string) error {
	if err := runADBCommand(ctx, adbPath, serial,
		"shell", "am", "start", "-n", "com.drawsplay.tablet/.MainActivity"); err == nil {
		return nil
	}
	// Fallback: launcher-category entry point when explicit component start fails.
	if err := runADBCommand(ctx, adbPath, serial,
		"shell", "monkey", "-p", "com.drawsplay.tablet",
		"-c", "android.intent.category.LAUNCHER", "1"); err != nil {
		return err
	}
	return nil
}

func runADBCommand(ctx context.Context, adbPath, serial string, args ...string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, adbLaunchTimeout)
	defer cancel()
	fullArgs := make([]string, 0, len(args)+2)
	fullArgs = append(fullArgs, "-s", serial)
	fullArgs = append(fullArgs, args...)
	out, err := hiddenCommandContext(cmdCtx, adbPath, fullArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %w (%s)", fullArgs, err, strings.TrimSpace(string(out)))
	}
	if text := strings.TrimSpace(string(out)); strings.Contains(text, "Error:") {
		return fmt.Errorf("%v: %s", fullArgs, text)
	}
	return nil
}

func selectADBTargets(serials []string, preferred string, requirePreferred bool) ([]string, error) {
	if preferred == "" {
		if requirePreferred {
			return nil, fmt.Errorf("%w", errStrictADBMissingPreferred)
		}
		if len(serials) == 0 {
			return nil, fmt.Errorf("%w", errNoAuthorizedADBDevices)
		}
		return serials, nil
	}
	for _, serial := range serials {
		if serial == preferred {
			return []string{serial}, nil
		}
	}
	return nil, fmt.Errorf("%w: %q", errPreferredADBUnavailable, preferred)
}

func retryDelayForADBError(err error, failures int) time.Duration {
	switch {
	case errors.Is(err, errNoAuthorizedADBDevices), errors.Is(err, errPreferredADBUnavailable):
		// Keep a fast probe cadence for common unplug/replug states so
		// reconnects are detected quickly instead of waiting for long
		// exponential backoff windows.
		return adbReverseRetryDelay
	case errors.Is(err, errADBDeviceUnauthorized), errors.Is(err, errADBDeviceNoPermissions):
		// Authorization and udev permission states usually clear quickly once
		// the user accepts prompts or reapplies USB rules.
		return adbReverseRetryDelay
	default:
		return exponentialRetryDelay(failures)
	}
}

func shouldRestartADBServer(err error, failures int) bool {
	if failures < 3 || failures%5 != 0 {
		return false
	}
	if errors.Is(err, errNoAuthorizedADBDevices) ||
		errors.Is(err, errPreferredADBUnavailable) ||
		errors.Is(err, errStrictADBMissingPreferred) ||
		errors.Is(err, errADBDeviceUnauthorized) ||
		errors.Is(err, errADBDeviceNoPermissions) {
		return false
	}
	return true
}

func restartADBServerBestEffort(adbPath string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stopCmd := hiddenCommandContext(ctx, adbPath, "kill-server")
	if out, err := stopCmd.CombinedOutput(); err != nil {
		log.Printf("ADB server restart (kill-server) failed: %v (%s)", err, strings.TrimSpace(string(out)))
		return
	}
	startCmd := hiddenCommandContext(ctx, adbPath, "start-server")
	if out, err := startCmd.CombinedOutput(); err != nil {
		log.Printf("ADB server restart (start-server) failed: %v (%s)", err, strings.TrimSpace(string(out)))
		return
	}
	log.Printf("ADB server restarted after repeated reverse failures")
}

func exponentialRetryDelay(failures int) time.Duration {
	if failures > 4 {
		failures = 4
	}
	delay := adbReverseRetryDelay << failures
	if delay > adbReverseMaxRetryDelay {
		delay = adbReverseMaxRetryDelay
	}
	jitterMax := int64(delay / 4)
	if jitterMax > 0 {
		delay += time.Duration(rand.Int64N(jitterMax + 1))
	}
	return delay
}

type adbStatusInfo struct {
	Ready            bool     `json:"ready"`
	PreferredSerial  string   `json:"preferredSerial,omitempty"`
	AvailableSerials []string `json:"availableSerials"`
	TargetSerials    []string `json:"targetSerials"`
	Error            string   `json:"error,omitempty"`
}

var adbStatusState struct {
	sync.RWMutex
	value adbStatusInfo
}

func publishADBStatus(ready bool, preferred string, available, targets []string, err error) {
	status := adbStatusInfo{
		Ready:            ready,
		PreferredSerial:  preferred,
		AvailableSerials: append([]string(nil), available...),
		TargetSerials:    append([]string(nil), targets...),
	}
	if err != nil {
		status.Error = err.Error()
	}
	adbStatusState.Lock()
	adbStatusState.value = status
	adbStatusState.Unlock()
}

func currentADBStatus() adbStatusInfo {
	adbStatusState.RLock()
	status := adbStatusState.value
	adbStatusState.RUnlock()
	status.AvailableSerials = append([]string(nil), status.AvailableSerials...)
	status.TargetSerials = append([]string(nil), status.TargetSerials...)
	return status
}

// attachedDeviceSerials lists serials of devices in the "device" state.
// Unauthorized or offline devices are skipped (reverse would fail anyway).
func attachedDeviceSerials(ctx context.Context, adbPath string) ([]string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, adbCommandTimeout)
	defer cancel()
	out, err := hiddenCommandContext(cmdCtx, adbPath, "devices").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("adb devices: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	serials, unauthorized, noPermissions := parseADBDevicesOutput(string(out))
	if len(serials) == 0 {
		switch {
		case noPermissions:
			return nil, fmt.Errorf("%w", errADBDeviceNoPermissions)
		case unauthorized:
			return nil, fmt.Errorf("%w", errADBDeviceUnauthorized)
		}
	}
	return serials, nil
}

func parseADBDevicesOutput(output string) (serials []string, unauthorized bool, noPermissions bool) {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" ||
			strings.HasPrefix(trimmed, "List of devices attached") ||
			strings.HasPrefix(trimmed, "* daemon") {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "no permissions") {
			noPermissions = true
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		serial := fields[0]
		state := fields[1]
		switch state {
		case "unauthorized":
			unauthorized = true
		case "device":
			// ADB can list synthetic mDNS service endpoints (for wireless
			// debugging) as "device" entries, e.g.:
			//   adb-<serial>._adb-tls-connect._tcp
			// They are not shell-capable device serials and break reverse/app
			// launch commands when targeted with -s.
			if strings.HasPrefix(serial, "adb-") &&
				(strings.Contains(serial, "._adb-tls-connect._tcp") ||
					strings.Contains(serial, "._adb-tls-pairing._tcp")) {
				continue
			}
			serials = append(serials, serial)
		}
	}
	return serials, unauthorized, noPermissions
}

// assertReverseRules applies one reverse rule per port per device. A failure
// on one device does not abort the others; the first error is returned.
func assertReverseRules(ctx context.Context, adbPath string, serials []string, ports []int) error {
	var firstErr error
	for _, serial := range serials {
		for _, port := range ports {
			arg := fmt.Sprintf("tcp:%d", port)
			cmdCtx, cancel := context.WithTimeout(ctx, adbCommandTimeout)
			cmd := hiddenCommandContext(cmdCtx, adbPath, "-s", serial, "reverse", arg, arg)
			out, err := cmd.CombinedOutput()
			cancel()
			if err != nil && firstErr == nil {
				firstErr = fmt.Errorf("%s %s: %w (%s)", serial, arg, err, strings.TrimSpace(string(out)))
			}
			if err != nil {
				break // device likely gone; skip its remaining ports
			}
		}
	}
	return firstErr
}
