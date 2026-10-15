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
