//go:build linux

package main

import (
	"fmt"
	"os"
	"time"
)

// Variables so tests can stub the probes and the browser launch.
var (
	instanceLockHeldFn = instanceLockHeld
	instanceRunningFn  = anotherInstanceRunning
	openSettingsWebUIF = openSettingsWebUI
	handOffWait        = 10 * time.Second
	handOffPoll        = 250 * time.Millisecond
)

// handOffToRunningInstance opens the running host's settings page when the
// menu entry is launched again, instead of failing on the instance lock in a
// process with no terminal. Only this user's host counts (host.lock, not the
// shared settings port). Autostart (-background) keeps the old behavior, so a
// racing restart never opens a browser.
func handOffToRunningInstance(background bool) bool {
	if background || !instanceLockHeldFn() {
		return false
	}
	// A host that is still starting opens its settings page only after its
	// startup checks, so give it a moment.
	deadline := time.Now().Add(handOffWait)
	for !instanceRunningFn() {
		if !instanceLockHeldFn() {
			return false // it quit meanwhile: start normally
		}
		if time.Now().After(deadline) {
			return false // the instance-lock error explains it
		}
		time.Sleep(handOffPoll)
	}
	fmt.Fprintf(os.Stderr, "DrawSplay is already running; opening its settings at http://127.0.0.1:%d\n", webuiPort)
	openSettingsWebUIF()
	return true
}
