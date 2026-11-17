//go:build !linux && !windows

package main

func platformSetupFix(action string, cfg settings) (msg string, handled bool, err error) {
	return "", false, nil
}
