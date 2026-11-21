//go:build linux

package main

import (
	"testing"
	"time"
)

func stubHandOff(t *testing.T, lockHeld, running func() bool) *int {
	t.Helper()
	prevLock, prevRunning, prevOpen := instanceLockHeldFn, instanceRunningFn, openSettingsWebUIF
	prevWait, prevPoll := handOffWait, handOffPoll
	t.Cleanup(func() {
		instanceLockHeldFn, instanceRunningFn, openSettingsWebUIF = prevLock, prevRunning, prevOpen
		handOffWait, handOffPoll = prevWait, prevPoll
	})
	opened := new(int)
	instanceLockHeldFn, instanceRunningFn = lockHeld, running
	openSettingsWebUIF = func() { *opened++ }
	handOffWait, handOffPoll = 50*time.Millisecond, time.Millisecond
	return opened
}

func always(v bool) func() bool { return func() bool { return v } }

func TestLinuxHandOffOpensRunningInstance(t *testing.T) {
	opened := stubHandOff(t, always(true), always(true))
	if !handOffToRunningInstance(false) || *opened != 1 {
		t.Fatalf("menu launch with a running host should open its settings (opened=%d)", *opened)
	}
	if handOffToRunningInstance(true) || *opened != 1 {
		t.Fatalf("autostart must not hand off (opened=%d)", *opened)
	}
}

func TestLinuxHandOffIgnoresOtherUsersHost(t *testing.T) {
	// Something answers on the settings port, but this user's lock is free.
	opened := stubHandOff(t, always(false), always(true))
	if handOffToRunningInstance(false) || *opened != 0 {
		t.Fatalf("must start normally without this user's host (opened=%d)", *opened)
	}
}

func TestLinuxHandOffWaitsForStartingHost(t *testing.T) {
	polls := 0
	opened := stubHandOff(t, always(true), func() bool { polls++; return polls > 3 })
	if !handOffToRunningInstance(false) || *opened != 1 {
		t.Fatalf("should wait for the settings page of a starting host (opened=%d)", *opened)
	}
}

func TestLinuxHandOffGivesUpWithoutSettingsPage(t *testing.T) {
	opened := stubHandOff(t, always(true), always(false))
	if handOffToRunningInstance(false) || *opened != 0 {
		t.Fatalf("no settings page: fall back to the lock error (opened=%d)", *opened)
	}
	lockChecks := 0
	stubHandOff(t, func() bool { lockChecks++; return lockChecks == 1 }, always(false))
	if handOffToRunningInstance(false) {
		t.Fatal("a host that quit while waiting: start normally")
	}
}
