//go:build !windows

package main

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// acquireInstanceLock takes an exclusive lock on host.lock in the config
// folder for the life of the process. It returns false when another host
// holds it. (This used to be implied by binding the input port, which is now
// only opened while the adb tunnel is on.)
func acquireInstanceLock() (bool, error) {
	dir, err := configDir()
	if err != nil {
		return true, err // cannot tell; let the caller continue
	}
	f, err := os.OpenFile(filepath.Join(dir, "host.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return true, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if err == unix.EWOULDBLOCK {
			return false, nil
		}
		return true, err
	}
	instanceLockFile = f // kept open (and locked) until exit
	return true, nil
}

var instanceLockFile *os.File
