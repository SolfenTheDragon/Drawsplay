//go:build !linux && !windows

package main

import "errors"

func updateBundleName(string) string { return "" }

func updateInstallSupport() (bool, string) {
	return false, "Updates install themselves only on Windows and Linux. Download the update from the release page."
}

func applyUpdate(string, string) (applyResult, error) {
	return applyResult{}, errors.New("updates are not supported on this platform")
}

func execPendingRestart() {}
