package main

import (
	"errors"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// acquireInstanceLock opens host.lock in the config folder without sharing for
// the life of the process; a second host gets a sharing violation. (This used
// to be implied by binding the input port, which is now only opened while the
// adb tunnel is on.)
func acquireInstanceLock() (bool, error) {
	dir, err := configDir()
	if err != nil {
		return true, err
	}
	p, err := windows.UTF16PtrFromString(filepath.Join(dir, "host.lock"))
	if err != nil {
		return true, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return false, nil
		}
		return true, err
	}
	instanceLockHandle = h // kept open until exit
	return true, nil
}

var instanceLockHandle windows.Handle
