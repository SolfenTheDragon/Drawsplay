//go:build windows

package main

import "log"

// releaseIdleVirtualDisplays unplugs every monitor the DrawSplay driver holds,
// including ones left by another session or process.
func releaseIdleVirtualDisplays() {
	dev, err := openIDDDevice()
	if err != nil {
		return
	}
	defer dev.Close()
	if err := dev.RemoveAllMonitors(); err != nil {
		log.Printf("Idle display cleanup: %v", err)
	}
}
