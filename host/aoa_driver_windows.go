//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const winusbInstallTimeout = 3 * time.Minute

var winusbInstallAttempts sync.Map // "vid:pid" -> struct{}

// winusbRetryWindow is how long after the checklist's Retry a portable host
// may prompt again despite a recorded earlier attempt. It covers both tablet
// identities (normal, then accessory mode after re-enumeration) and expires,
// so a later plug-in never brings up an unasked-for UAC prompt.
const winusbRetryWindow = 2 * time.Minute

// winusbRetryUntil is the end of the current Retry window (Unix nanoseconds).
var winusbRetryUntil atomic.Int64

// resetWinUSBInstallAttempts lets scans in the next winusbRetryWindow try
// the WinUSB install again, once per device; it backs the setup checklist's
// Retry button.
func resetWinUSBInstallAttempts() {
	winusbInstallAttempts.Clear()
	winusbRetryUntil.Store(time.Now().Add(winusbRetryWindow).UnixNano())
}

func winusbRetryActive(now time.Time) bool {
	return now.UnixNano() < winusbRetryUntil.Load()
}

// ensureWinUSBDriver binds DrawSplay's signed WinUSB package to an attached
// tablet identity that has no WinUSB interface. Installed hosts run the
// elevated helper task registered by -install (no prompt); a portable host
// elevates itself once per identity ever (persisted in
// %APPDATA%\DrawSplay\winusb-attempts.json). It reports whether the install
// succeeded so the caller can rescan. This is independent of
// -no-driver-install, which controls only the virtual display driver.
func ensureWinUSBDriver(vid, pid uint16) bool {
	key := fmt.Sprintf("%04x:%04x", vid, pid)
	if _, loaded := winusbInstallAttempts.LoadOrStore(key, struct{}{}); loaded {
		return false
	}

	if m, installed := loadInstallManifest(); installed {
		if m.WinUSBTask == "" {
			log.Printf("AOA: WinUSB is not bound to %s and no driver helper task is registered; rerun the DrawSplay installer", key)
			return false
		}
		log.Printf("AOA: installing the signed WinUSB driver for %s", key)
		if err := runHelperTask(m.WinUSBTask, winusbBindResultName, winusbInstallTimeout); err != nil {
			log.Printf("AOA: WinUSB driver setup for %s failed: %v", key, err)
			return false
		}
		return true
	}

	stateDir, stateErr := userStateDir()
	if stateErr == nil && !winusbRetryActive(time.Now()) && winusbAttemptRecorded(stateDir, key) {
		log.Printf("AOA: WinUSB install for %s was already attempted; not prompting again (install DrawSplay with -install to bind it automatically)", key)
		return false
	}
	exe, err := os.Executable()
	if err == nil {
		log.Printf("AOA: installing the signed WinUSB driver for %s (approve the UAC prompt)", key)
		err = runElevatedAndWait(exe, "-winusb-bind-attached", winusbInstallTimeout)
	}
	result := "installed"
	if err != nil {
		result = "failed: " + err.Error()
	}
	if stateErr == nil {
		if recErr := recordWinUSBAttempt(stateDir, key, result, time.Now()); recErr != nil {
			log.Printf("AOA: could not persist WinUSB attempt for %s: %v", key, recErr)
		}
	}
	if err != nil {
		log.Printf("AOA: WinUSB driver setup for %s failed: %v", key, err)
		return false
	}
	return true
}
