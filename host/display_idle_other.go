//go:build !linux && !windows

package main

// releaseIdleVirtualDisplays is a no-op on platforms without virtual displays.
func releaseIdleVirtualDisplays() {}
