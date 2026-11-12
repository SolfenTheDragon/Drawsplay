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

	bindClientSession(9001, clientSession{Device: "test-tablet"})
	if reapDisconnectedDisplay() {
		t.Fatal("session torn down while the tablet is connected")
	}
	if ctx.Err() != nil {
		t.Fatal("session context cancelled while the tablet is connected")
	}

	unbindClientSession(9001)
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
	bindClientSession(9002, clientSession{Device: "idle-tablet"})
	if tr.check(now.Add(time.Hour), 10*time.Minute) {
		t.Fatal("released while a tablet is connected")
	}
	unbindClientSession(9002)
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

func TestDisconnectedDisplayReaperWaitsOutInputReconnects(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	setActiveVideoSession(cancel, server)
	defer setActiveVideoSession(nil, nil)

	var r disconnectedDisplayReaper
	now := time.Now()
	grace := 30 * time.Second

	// Input connection dropped for one tick (settings change) and back.
	unbindClientSession(9003)
	if r.check(now, grace) {
		t.Fatal("reaped on the first disconnected sample")
	}
	bindClientSession(9003, clientSession{Device: "test-tablet"})
	if r.check(now.Add(grace), grace) {
		t.Fatal("reaped after the tablet reconnected")
	}
	if ctx.Err() != nil {
		t.Fatal("session cancelled across a brief input reconnect")
	}

	// A genuine disconnect persists across ticks.
	unbindClientSession(9003)
	if r.check(now.Add(2*grace), grace) {
		t.Fatal("reaped before the grace period after a fresh disconnect")
	}
	if !r.check(now.Add(3*grace), grace) {
		t.Fatal("stale session not reaped after the grace period")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("session context was not cancelled")
	}
}
