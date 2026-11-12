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
func startDisplayWatchdog(ctx context.Context, store *settingsStore) {
	go func() {
		var reaper disconnectedDisplayReaper
		var idle idleDisplayTracker
		ticker := time.NewTicker(displayWatchdogInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now()
				reaper.check(now, displayWatchdogInterval)
				idle.check(now, time.Duration(store.get().IdleDisplayTimeoutSec)*time.Second)
			}
		}
	}()
}

// disconnectedDisplayReaper tears down the active video session once no
// tablet has been connected for a grace period (the same status the tray
// shows as "Disconnected"). Teardown runs the session's deferred display
// Close, so the virtual display assigned to the session is removed.
//
// The grace period matters: the input connection drops for about a second
// whenever the tablet reconnects it (an input setting changed, a transient
// link hiccup) while the video session keeps streaming. Reaping on a single
// sample would remove the virtual display, and with it the user's windows,
// whenever a tick happened to land in that window.
type disconnectedDisplayReaper struct {
	since time.Time
}

// check reports whether it tore down the session on this call. A zero grace
// period reaps as soon as the tablet is absent.
func (r *disconnectedDisplayReaper) check(now time.Time, grace time.Duration) bool {
	if activeInputDevice() != "" {
		r.since = time.Time{}
		return false
	}
	if r.since.IsZero() {
		r.since = now
	}
	if now.Sub(r.since) < grace {
		return false
	}
	return reapDisconnectedDisplay()
}

// reapDisconnectedDisplay tears down the active video session when no tablet
// is connected. It reports whether a session was torn down.
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

// idleDisplayTracker removes every virtual display once no tablet has been
// connected for the configured timeout. Unlike reapDisconnectedDisplay it does
// not depend on a live video session, so it also clears displays left behind
// by an earlier session or host process.
type idleDisplayTracker struct {
	since    time.Time
	released bool
}

// check reports whether it removed the displays on this call. A zero timeout
// disables the check.
func (t *idleDisplayTracker) check(now time.Time, timeout time.Duration) bool {
	if timeout <= 0 || activeInputDevice() != "" {
		t.since = time.Time{}
		t.released = false
		return false
	}
	if t.since.IsZero() {
		t.since = now
	}
	if t.released || now.Sub(t.since) < timeout {
		return false
	}
	t.released = true
	log.Printf("No tablet connected for %s: removing virtual displays", timeout)
	if disposition, cancel, conn := activeVideoSession.requestRestart(true, now); disposition == videoRestartRequested {
		stopActiveVideoSession(cancel, conn)
	}
	releaseIdleVirtualDisplays()
	return true
}
