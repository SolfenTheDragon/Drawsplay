//go:build !linux

package main

// releaseVirtualDisplaysOnExit is a no-op: only the Linux X11 extended
// display outlives its session (see x11_extend_linux.go).
func releaseVirtualDisplaysOnExit() {}
