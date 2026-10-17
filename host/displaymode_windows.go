//go:build windows

package main

// Display-mode helpers for the session's virtual monitor.
//
// DrawSplay's own IddCx driver advertises the requested mode (marked
// preferred) plus the tablet's native size and standard 720p-4K modes. An
// EDID-less monitor can still be activated by Windows at another advertised
// mode (observed: the largest one), so the host applies the requested mode
// once after plugging (setModeOf). currentModeOf reads the live mode for the
// capture frame-rate policy.
//
// A user adjusting the resolution in Windows display settings mid-session is
// still honored: the host detects the change (video.go), streams at the new
// mode and pushes it to the tablet, which stores it as the new preference.

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// DEVMODEW field offsets (64-bit layout, no driver extra), total size.
	devmodeSize     = 220
	dmOffSize       = 68 // u16
	dmOffFields     = 72
	dmOffPelsWidth  = 172
	dmOffPelsHeight = 176
	dmOffFrequency  = 184

	enumCurrentSettingsIdx = ^uintptr(0) // ENUM_CURRENT_SETTINGS (-1)

	dmPelsWidth        = 0x00080000
	dmPelsHeight       = 0x00100000
	dmDisplayFrequency = 0x00400000
	cdsUpdateRegistry  = 0x00000001
	dispChangeSuccess  = 0
)

var procChangeDisplaySettingsExW = user32.NewProc("ChangeDisplaySettingsExW")

// setModeOf switches a display to w x h @ hz and persists it for that monitor.
func setModeOf(deviceName string, w, h, hz int) error {
	var dm [devmodeSize]byte
	putU32(dm[:], dmOffSize, devmodeSize)
	putU32(dm[:], dmOffFields, dmPelsWidth|dmPelsHeight|dmDisplayFrequency)
	putU32(dm[:], dmOffPelsWidth, uint32(w))
	putU32(dm[:], dmOffPelsHeight, uint32(h))
	putU32(dm[:], dmOffFrequency, uint32(hz))
	name, err := windows.UTF16PtrFromString(deviceName)
	if err != nil {
		return err
	}
	ret, _, _ := procChangeDisplaySettingsExW.Call(
		uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&dm[0])),
		0, cdsUpdateRegistry, 0)
	if int32(ret) != dispChangeSuccess {
		return fmt.Errorf("ChangeDisplaySettingsEx(%s, %dx%d@%d) = %d", deviceName, w, h, hz, int32(ret))
	}
	return nil
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
