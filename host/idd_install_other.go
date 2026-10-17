//go:build !windows

package main

// Virtual display driver install is Windows-only (DrawSplay IddCx driver);
// no-op elsewhere. Linux extend mode uses Mutter RecordVirtual / vkms and
// needs no driver install.
func ensureVirtualDisplayDriver(bool) {}

func runDriverInstallAdminMode(bool) bool { return false }
