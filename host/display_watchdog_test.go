package main

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestReapDisconnectedDisplayEndsTheSessionOnlyWhenDisconnected(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	setActiveVideoSession(cancel, server)
	defer setActiveVideoSession(nil, nil)

	bindSessionDevice(9001, "test-tablet")
	if reapDisconnectedDisplay() {
		t.Fatal("session torn down while the tablet is connected")
	}
	if ctx.Err() != nil {
		t.Fatal("session context cancelled while the tablet is connected")
	}

	unbindSessionDevice(9001)
	if !reapDisconnectedDisplay() {
		t.Fatal("stale session was not torn down while disconnected")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("session context was not cancelled")
	}
	if reapDisconnectedDisplay() {
		t.Fatal("session torn down twice")
	}
}

func TestReapDisconnectedDisplayWithoutSession(t *testing.T) {
	setActiveVideoSession(nil, nil)
	if reapDisconnectedDisplay() {
		t.Fatal("reported a teardown with no active session")
	}
}

func TestIdleDisplayTrackerWaitsForTimeout(t *testing.T) {
	var tr idleDisplayTracker
	now := time.Now()
	bindSessionDevice(9002, "idle-tablet")
	if tr.check(now.Add(time.Hour), 10*time.Minute) {
		t.Fatal("released while a tablet is connected")
	}
	unbindSessionDevice(9002)
	if tr.check(now, 10*time.Minute) || tr.check(now.Add(9*time.Minute), 10*time.Minute) {
		t.Fatal("released before the timeout")
	}
	if !tr.check(now.Add(10*time.Minute), 10*time.Minute) {
		t.Fatal("did not release after the timeout")
	}
	if tr.check(now.Add(20*time.Minute), 10*time.Minute) {
		t.Fatal("released twice in one idle period")
	}
	var off idleDisplayTracker
	if off.check(now, 0) {
		t.Fatal("released with the check disabled")
	}
}
