//go:build !windows

package main

// ensureVDDDriver is Windows-only (Parsec VDD); no-op elsewhere. Linux
// extend mode uses Mutter RecordVirtual and needs no driver install.
func ensureVDDDriver(bool) {}
