//go:build !linux

package main

// releaseVirtualDisplaysOnExit is a no-op: only the Linux extended displays
// outlive their session (see x11_extend_linux.go and mutter_extend_linux.go).
func releaseVirtualDisplaysOnExit() {}
