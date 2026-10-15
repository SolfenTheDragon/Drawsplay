//go:build !linux

package main

// releaseVirtualDisplaysOnExit is a no-op: only the Linux extended displays
// outlive their session (see x11_extend_linux.go and mutter_extend_linux.go).
func releaseVirtualDisplaysOnExit() {}

// releaseStaleVirtualDisplays is a no-op here: the Windows VDD driver drops
// its displays itself when the host process stops pinging it, and the GNOME
// virtual monitor dies with the host's D-Bus connection.
func releaseStaleVirtualDisplays() {}
