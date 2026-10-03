//go:build !windows

package main

func runVDDAdminMode(setup bool, width, height int, restart bool) bool {
	return false
}
