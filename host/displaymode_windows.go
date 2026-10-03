//go:build windows

package main

// Display-mode control for the session's virtual monitor.
//
// When the VDD monitor comes up, its mode and refresh rate are set from the
// host's persisted web UI/CLI settings. This keeps the selected resolution
// authoritative instead of silently switching to a tablet-native mode that
// may exceed the real-time capture and encoder throughput.
//
// The change is applied dynamically (never written to the registry), so a
// user adjusting the resolution in Windows display settings mid-session is
// never fought: the host detects the change (video.go), streams at the new
// mode and pushes it to the tablet, which stores it as the new preference
// for future sessions.
//
// The Parsec VDD exposes a fixed mode list; when the exact resolution is
// not in it, the closest supported mode (aspect ratio first) is used and
// the exact mode is registered best-effort under the driver's custom
// resolution registry key (effective after a driver restart/reboot).

import (
	"fmt"
	"log"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var procChangeDisplaySettingsExW = user32.NewProc("ChangeDisplaySettingsExW")

const (
	// DEVMODEW field offsets (64-bit layout, no driver extra), total size.
	devmodeSize      = 220
	dmOffSize        = 68 // u16
	dmOffFields      = 72 // u32
	dmOffPelsWidth   = 172
	dmOffPelsHeight  = 176
	dmOffFrequency   = 184
	dmFieldPelsWidth = 0x00080000 // DM_PELSWIDTH
	dmFieldPelsHght  = 0x00100000 // DM_PELSHEIGHT
	dmFieldFrequency = 0x00400000 // DM_DISPLAYFREQUENCY

	cdsTest                = 0x00000002 // CDS_TEST
	dispChangeSuccessful   = 0
	enumCurrentSettingsIdx = ^uintptr(0) // ENUM_CURRENT_SETTINGS (-1)

	vddCustomResolutionRegPath = `SOFTWARE\Parsec\vdd\0`
)

// applyTabletDisplayMode sets the named display to the configured host mode.
// Returns changed=true when a mode change was applied (callers should then
// re-enumerate outputs, as the desktop rect changed) and reloadNeeded=true
// when a new custom mode was registered whose exact resolution only becomes
// available after the VDD adapter reloads its mode list. The reload itself
// is the caller's responsibility: only the owner of the VDD session can
// safely unplug the monitor and recycle the control handle around an
// adapter restart (see reloadVDDDisplay). Restarting the adapter here would
// destroy the live monitor and invalidate deviceName.
func applyTabletDisplayMode(deviceName string, wantW, wantH, fps int) (changed bool, reloadNeeded bool) {
	hz := fps
	if hz < 60 {
		hz = 60
	}
	if hz > 120 {
		hz = 120
	}
	curW, curH, curHz, ok := currentModeOf(deviceName)
	if ok && curW == wantW && curH == wantH && curHz >= hz {
		return false, false
	}

	if trySetModeFlexible(deviceName, wantW, wantH, hz) {
		log.Printf("Display mode set to %dx%d @ %d Hz (host settings)", wantW, wantH, hz)
		return true, false
	}

	// Exact mode unsupported by the driver: register it in the VDD custom
	// resolution key (best-effort) and pick the best available mode now.
	// The exact mode appears once the adapter restarts (reload or reboot).
	registered, newlyRegistered := registerVDDCustomResolution(wantW, wantH)

	// Keep capture behavior stable when a high/native request is unsupported:
	// prefer a known-good 1080p fallback over lower resolutions.
	if wantW*wantH > 1920*1080 && trySetModeFlexible(deviceName, 1920, 1080, hz) {
		hint := ""
		if registered {
			hint = " (exact mode registered, needs VDD adapter reload)"
		}
		log.Printf("Display mode %dx%d unsupported; using safe fallback 1920x1080%s", wantW, wantH, hint)
		return true, newlyRegistered
	}

	// For high/native requests, never downshift below 1080p automatically.
	if wantW*wantH > 1920*1080 {
		log.Printf("Display mode %dx%d unsupported and 1920x1080 fallback unavailable; keeping %dx%d", wantW, wantH, curW, curH)
		return false, registered
	}

	bw, bh, found := bestSupportedMode(deviceName, wantW, wantH)
	if !found || (ok && bw == curW && bh == curH) {
		log.Printf("Display mode %dx%d unsupported; keeping %dx%d", wantW, wantH, curW, curH)
		return false, registered
	}
	if trySetModeFlexible(deviceName, bw, bh, hz) {
		hint := ""
		if registered {
			hint = " (exact mode registered, needs VDD adapter reload)"
		}
		log.Printf("Display mode %dx%d unsupported; using closest supported %dx%d%s",
			wantW, wantH, bw, bh, hint)
		return true, registered
	}
	log.Printf("Display mode change to %dx%d failed; keeping %dx%d", wantW, wantH, curW, curH)
	return false, registered || newlyRegistered
}

// currentModeOf returns the display's active resolution and refresh rate.
func currentModeOf(deviceName string) (w, h, hz int, ok bool) {
	var dm [devmodeSize]byte
	putU32(dm[:], dmOffSize, devmodeSize)
	name, err := windows.UTF16PtrFromString(deviceName)
	if err != nil {
		return 0, 0, 0, false
	}
	ret, _, _ := procEnumDisplaySettingsExW.Call(
		uintptr(unsafe.Pointer(name)), enumCurrentSettingsIdx,
		uintptr(unsafe.Pointer(&dm[0])), 0)
	if ret == 0 {
		return 0, 0, 0, false
	}
	return int(getU32(dm[:], dmOffPelsWidth)),
		int(getU32(dm[:], dmOffPelsHeight)),
		int(getU32(dm[:], dmOffFrequency)), true
}

// trySetModeFlexible applies one resolution while tolerating refresh-rate
// mismatches in the driver mode list.
func trySetModeFlexible(deviceName string, w, h, hz int) bool {
	if trySetMode(deviceName, w, h, hz, true) {
		return true
	}
	// VDD often exposes custom and fallback modes only at 60 Hz.
	if hz != 60 && trySetMode(deviceName, w, h, 60, true) {
		return true
	}
	// Last resort: let Windows choose a valid refresh for this size.
	return trySetMode(deviceName, w, h, 0, false)
}

// trySetMode tests and applies one resolution (and optional refresh rate) on
// the named display dynamically (no registry persistence).
func trySetMode(deviceName string, w, h, hz int, includeFrequency bool) bool {
	name, err := windows.UTF16PtrFromString(deviceName)
	if err != nil {
		return false
	}
	var dm [devmodeSize]byte
	putU32(dm[:], dmOffSize, devmodeSize)
	fields := uint32(dmFieldPelsWidth | dmFieldPelsHght)
	if includeFrequency {
		fields |= dmFieldFrequency
		putU32(dm[:], dmOffFrequency, uint32(hz))
	}
	putU32(dm[:], dmOffFields, fields)
	putU32(dm[:], dmOffPelsWidth, uint32(w))
	putU32(dm[:], dmOffPelsHeight, uint32(h))

	ret, _, _ := procChangeDisplaySettingsExW.Call(
		uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&dm[0])), 0, cdsTest, 0)
	if ret != dispChangeSuccessful {
		return false
	}
	ret, _, _ = procChangeDisplaySettingsExW.Call(
		uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&dm[0])), 0, 0, 0)
	return ret == dispChangeSuccessful
}

// bestSupportedMode scans the display's mode list for the closest match:
// aspect ratio dominates (avoids stretching on the tablet panel), pixel
// count breaks ties.
func bestSupportedMode(deviceName string, wantW, wantH int) (int, int, bool) {
	name, err := windows.UTF16PtrFromString(deviceName)
	if err != nil {
		return 0, 0, false
	}
	wantAspect := float64(wantW) / float64(wantH)
	wantArea := float64(wantW * wantH)
	bestW, bestH, bestScore := 0, 0, 0.0
	for i := uintptr(0); ; i++ {
		var dm [devmodeSize]byte
		putU32(dm[:], dmOffSize, devmodeSize)
		ret, _, _ := procEnumDisplaySettingsExW.Call(
			uintptr(unsafe.Pointer(name)), i, uintptr(unsafe.Pointer(&dm[0])), 0)
		if ret == 0 {
			break
		}
		w := int(getU32(dm[:], dmOffPelsWidth))
		h := int(getU32(dm[:], dmOffPelsHeight))
		if w <= 0 || h <= 0 || w > wantW || h > wantH {
			continue // never exceed the panel: downscale beats crop/stretch
		}
		aspectDiff := float64(w)/float64(h) - wantAspect
		if aspectDiff < 0 {
			aspectDiff = -aspectDiff
		}
		areaDiff := (wantArea - float64(w*h)) / wantArea
		score := aspectDiff*10 + areaDiff
		if bestW == 0 || score < bestScore {
			bestW, bestH, bestScore = w, h, score
		}
	}
	return bestW, bestH, bestW > 0
}

// registerVDDCustomResolution adds the resolution to the Parsec VDD's
// custom-mode registry list (HKLM\SOFTWARE\Parsec\vdd\0) so the exact mode
// exists after the driver restarts. Best-effort: needs admin rights.
// Returns true when the registration succeeded.
func registerVDDCustomResolution(w, h int) (registered bool, changed bool) {
	prevW, prevH, prevHz, hasPrev, readErr := readVDDCustomResolutionValues()
	if readErr != nil {
		log.Printf("Could not read existing custom VDD resolution (%v)", readErr)
	}
	if err := writeVDDCustomResolution(w, h); err != nil {
		log.Printf("Could not register custom VDD resolution %dx%d (%v); continuing with best available built-in mode", w, h, err)
		return false, false
	}
	if !hasPrev {
		return true, true
	}
	return true, prevW != w || prevH != h || prevHz != 60
}

func writeVDDCustomResolution(w, h int) error {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, vddCustomResolutionRegPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open HKLM\\%s for write: %w", vddCustomResolutionRegPath, err)
	}
	defer key.Close()
	werr := key.SetDWordValue("width", uint32(w))
	herr := key.SetDWordValue("height", uint32(h))
	zerr := key.SetDWordValue("hz", 60)
	if werr != nil || herr != nil || zerr != nil {
		return fmt.Errorf("write HKLM\\%s values: %v", vddCustomResolutionRegPath, fmt.Sprint(werr, herr, zerr))
	}
	return nil
}

func readVDDCustomResolutionValues() (w, h, hz int, has bool, err error) {
	key, openErr := registry.OpenKey(registry.LOCAL_MACHINE, vddCustomResolutionRegPath, registry.QUERY_VALUE)
	if openErr != nil {
		if openErr == registry.ErrNotExist {
			return 0, 0, 0, false, nil
		}
		return 0, 0, 0, false, fmt.Errorf("open HKLM\\%s for read: %w", vddCustomResolutionRegPath, openErr)
	}
	defer key.Close()
	w64, _, wErr := key.GetIntegerValue("width")
	if wErr != nil {
		if wErr == registry.ErrNotExist {
			return 0, 0, 0, false, nil
		}
		return 0, 0, 0, false, fmt.Errorf("read HKLM\\%s width: %w", vddCustomResolutionRegPath, wErr)
	}
	h64, _, hErr := key.GetIntegerValue("height")
	if hErr != nil {
		if hErr == registry.ErrNotExist {
			return 0, 0, 0, false, nil
		}
		return 0, 0, 0, false, fmt.Errorf("read HKLM\\%s height: %w", vddCustomResolutionRegPath, hErr)
	}
	hz64, _, hzErr := key.GetIntegerValue("hz")
	if hzErr != nil {
		if hzErr == registry.ErrNotExist {
			hz64 = 60
		} else {
			return 0, 0, 0, false, fmt.Errorf("read HKLM\\%s hz: %w", vddCustomResolutionRegPath, hzErr)
		}
	}
	return int(w64), int(h64), int(hz64), true, nil
}
