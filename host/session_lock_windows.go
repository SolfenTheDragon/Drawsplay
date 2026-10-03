//go:build windows

package main

import (
	"context"
	"log"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	desktopSwitchDesktop = 0x0100
	uoiName              = 2
)

var (
	procOpenInputDesktop         = user32.NewProc("OpenInputDesktop")
	procGetUserObjectInfoW       = user32.NewProc("GetUserObjectInformationW")
	procCloseDesktop             = user32.NewProc("CloseDesktop")
	sessionLockPollInterval      = 500 * time.Millisecond
	sessionLockUnknownBackoff    = 2 * time.Second
	sessionLockDesktopWinlogon   = "winlogon"
	sessionLockDesktopDefault    = "default"
	sessionLockDesktopDisconnect = "disconnect"
)

func startSessionLockMonitor(ctx context.Context) {
	go func() {
		last := false
		haveLast := false
		for {
			locked, known := windowsSessionLocked()
			if known {
				if !haveLast || locked != last {
					setSessionDisplayBlackout(locked)
					if locked {
						log.Printf("Windows session locked: streaming blackout frames")
					} else {
						log.Printf("Windows session unlocked: resuming desktop capture")
					}
					restartActiveVideoSession()
					last = locked
					haveLast = true
				}
			} else if !haveLast {
				// Unknown lock state at startup: fail open until resolved.
				setSessionDisplayBlackout(false)
			}

			delay := sessionLockPollInterval
			if !known {
				delay = sessionLockUnknownBackoff
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	}()
}

func windowsSessionLocked() (bool, bool) {
	hd, _, _ := procOpenInputDesktop.Call(0, 0, desktopSwitchDesktop)
	if hd == 0 {
		return false, false
	}
	desktop := windows.Handle(hd)
	defer procCloseDesktop.Call(uintptr(desktop))

	name, ok := desktopName(desktop)
	if !ok {
		return false, false
	}
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case sessionLockDesktopDefault:
		return false, true
	case sessionLockDesktopWinlogon, sessionLockDesktopDisconnect:
		return true, true
	default:
		// Unknown desktop names are treated as unlocked so capture behavior
		// remains fail-open unless this is explicitly a secure desktop.
		return false, true
	}
}

func desktopName(desktop windows.Handle) (string, bool) {
	var needed uint32
	r1, _, _ := procGetUserObjectInfoW.Call(
		uintptr(desktop),
		uoiName,
		0,
		0,
		uintptr(unsafe.Pointer(&needed)),
	)
	if r1 == 0 || needed < 2 {
		return "", false
	}
	buf := make([]uint16, needed/2)
	r1, _, _ = procGetUserObjectInfoW.Call(
		uintptr(desktop),
		uoiName,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(needed),
		uintptr(unsafe.Pointer(&needed)),
	)
	if r1 == 0 {
		return "", false
	}
	return windows.UTF16ToString(buf), true
}
