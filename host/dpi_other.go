//go:build !windows

package main

// declareDPIAware is Windows-only (coordinate virtualization); no-op elsewhere.
func declareDPIAware() {}
