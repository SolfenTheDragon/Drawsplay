//go:build !windows

package main

// resetWinUSBInstallAttempts: only Windows needs a WinUSB driver for AOA.
func resetWinUSBInstallAttempts() {}
