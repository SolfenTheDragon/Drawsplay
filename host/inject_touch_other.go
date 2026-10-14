//go:build !linux

package main

// Windows injects multi-touch through its own synthetic touch device
// (inject_windows.go); there is no per-session touch mapping to clear.
func clearTouchMapped() {}
