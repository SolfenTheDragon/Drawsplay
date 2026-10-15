package main

import (
	"context"
	"log"
	"time"
)

// displayWatchdogInterval paces the connectivity check that removes the
// virtual display of a session whose tablet is gone (e.g. a stale video
// session left behind by a suspend/resume or a dead link that never closed
// the socket).
const displayWatchdogInterval = 30 * time.Second

// startDisplayWatchdog checks connectivity every displayWatchdogInterval and
// ends the video session — which removes its virtual display — when the
// tablet is disconnected.
func startDisplayWatchdog(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(displayWatchdogInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reapDisconnectedDisplay()
			}
		}
	}()
}

// reapDisconnectedDisplay tears down the active video session when no tablet
// is connected (the same status the tray shows as "Disconnected"). Teardown
// runs the session's deferred display Close, so the virtual display assigned
// to the session is removed. It reports whether a session was torn down.
func reapDisconnectedDisplay() bool {
	if activeInputDevice() != "" {
		return false
	}
	disposition, cancel, conn := activeVideoSession.requestRestart(true, time.Now())
	if disposition != videoRestartRequested {
		return false
	}
	log.Printf("Tablet disconnected: removing the virtual display of the stale video session")
	stopActiveVideoSession(cancel, conn)
	return true
}
