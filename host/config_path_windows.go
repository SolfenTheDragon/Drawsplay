//go:build windows

package main

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const driveRemote = 4

var (
	kernel32ConfigPath = windows.NewLazySystemDLL("kernel32.dll")
	procGetDriveTypeW  = kernel32ConfigPath.NewProc("GetDriveTypeW")
)

func isNetworkExecutablePath(path string) bool {
	volume := filepath.VolumeName(path)
	if volume == "" {
		return true
	}
	root := volume + `\`
	rootp, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return true
	}
	driveType, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(rootp)))
	return driveType == driveRemote
}

func validateLocalExecutable(path string, _ fs.FileInfo) error {
	if !strings.EqualFold(filepath.Ext(path), ".exe") {
		return fmt.Errorf("must name a local .exe file")
	}
	return nil
}
