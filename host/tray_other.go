//go:build !windows

package main

import "context"

func runBackgroundTrayController(background bool, parent context.Context, cancel context.CancelFunc, store *settingsStore) bool {
	return false
}
