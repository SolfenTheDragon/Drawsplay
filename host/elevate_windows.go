//go:build windows

package main

// Elevation helper: launch a process with the "runas" verb (UAC prompt) and
// wait for it to finish. Used by the installer and by on-demand elevated
// setup steps (driver install, WinUSB bind).

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// shellExecuteInfo mirrors SHELLEXECUTEINFOW for ShellExecuteExW.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIconOrMon   windows.Handle
	hProcess     windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040 // SEE_MASK_NOCLOSEPROCESS
	swHide                = 0
)

var (
	shell32             = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = shell32.NewProc("ShellExecuteExW")
)

// runElevatedAndWait launches file with args via the "runas" verb (UAC
// elevation prompt) and waits for the process to exit or the timeout.
func runElevatedAndWait(file, args string, timeout time.Duration) error {
	verb, _ := windows.UTF16PtrFromString("runas")
	filep, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	argsp, _ := windows.UTF16PtrFromString(args)

	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess,
		lpVerb:       verb,
		lpFile:       filep,
		lpParameters: argsp,
		nShow:        swHide,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))

	ret, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return fmt.Errorf("elevation declined by user")
		}
		return fmt.Errorf("ShellExecuteEx: %v", callErr)
	}
	if info.hProcess == 0 {
		return fmt.Errorf("process started but no handle returned")
	}
	defer windows.CloseHandle(info.hProcess)

	ev, err := windows.WaitForSingleObject(info.hProcess, uint32(timeout.Milliseconds()))
	if err != nil {
		return fmt.Errorf("waiting for elevated process: %w", err)
	}
	if ev == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("elevated process did not finish within %s", timeout)
	}

	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err == nil && code != 0 {
		return fmt.Errorf("elevated process exited with code %d", code)
	}
	return nil
}
