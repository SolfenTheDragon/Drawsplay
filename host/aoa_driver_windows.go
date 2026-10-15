//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const winusbInstallTimeout = 2 * time.Minute

var winusbInstallAttempts sync.Map // "vid:pid" -> struct{}

// ensureWinUSBDriver installs WinUSB for one VID/PID through an elevated
// pnputil (UAC prompt), at most once per identity per host run. It reports
// whether the install succeeded so the caller can rescan. Disabled by
// -no-driver-install, like the display driver.
func ensureWinUSBDriver(vid, pid uint16) bool {
	if loadSettings().NoDriverInstall {
		return false
	}
	key := fmt.Sprintf("%04x:%04x", vid, pid)
	if _, loaded := winusbInstallAttempts.LoadOrStore(key, struct{}{}); loaded {
		return false
	}

	dir, err := os.MkdirTemp("", "drawsplay-winusb-")
	if err != nil {
		log.Printf("AOA: create temp dir for %s WinUSB INF: %v", key, err)
		return false
	}
	defer os.RemoveAll(dir)
	inf := filepath.Join(dir, "drawsplay-winusb.inf")
	if err := os.WriteFile(inf, []byte(aoaWinUSBInf(vid, pid)), 0o600); err != nil {
		log.Printf("AOA: write WinUSB INF for %s: %v", key, err)
		return false
	}

	log.Printf("AOA: installing WinUSB driver for %s (approve the UAC prompt)", key)
	args := `/add-driver "` + inf + `" /install`
	if err := runElevatedAndWait(filepath.Join(os.Getenv("SystemRoot"), "System32", "pnputil.exe"), args, winusbInstallTimeout); err != nil {
		log.Printf("AOA: WinUSB install for %s failed: %v (the INF must be signed or accepted by Windows)", key, err)
		return false
	}
	return true
}
