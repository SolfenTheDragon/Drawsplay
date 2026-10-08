package main

import (
	"net"
	"testing"
	"time"
)

func TestSessionMonitorCongestsOnFrameDrops(t *testing.T) {
	hostConn, clientConn := net.Pipe()
	defer hostConn.Close()
	defer clientConn.Close()

	mon := &sessionMonitor{}
	done := make(chan struct{})
	go func() {
		mon.runStatsReader(hostConn)
		close(done)
	}()

	line := `{"type":"stats","intervalMs":250,"framesReceived":15,"framesRendered":15,"avgDecodeMs":4.2,"queuedFrames":1,"frameDrops":2}` + "\n"
	if _, err := clientConn.Write([]byte(line)); err != nil {
		t.Fatalf("write stats: %v", err)
	}
	_ = clientConn.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stats reader did not finish")
	}

	if !mon.consumeCongestion() {
		t.Fatal("expected congestion from frame drops")
	}
}

// feedStatsLines runs a stats reader over lines and waits for it to finish.
func feedStatsLines(t *testing.T, lines ...string) *sessionMonitor {
	t.Helper()
	hostConn, clientConn := net.Pipe()
	defer hostConn.Close()

	mon := &sessionMonitor{}
	done := make(chan struct{})
	go func() {
		mon.runStatsReader(hostConn)
		close(done)
	}()
	for _, line := range lines {
		if _, err := clientConn.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write stats: %v", err)
		}
	}
	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stats reader did not finish")
	}
	return mon
}

func TestSessionMonitorCongestsOnDeepDecodeQueue(t *testing.T) {
	mon := feedStatsLines(t,
		`{"type":"stats","intervalMs":250,"framesReceived":15,"framesRendered":11,"avgDecodeMs":21.0,"queuedFrames":12}`)
	if !mon.consumeCongestion() {
		t.Fatal("a decode queue that is not catching up must report congestion")
	}
	if got := mon.takeStaleFrames(); got != 0 {
		t.Fatalf("stale frames = %d, want 0", got)
	}
}

// TestSessionMonitorIgnoresStallBacklogBeingDrained: after a Wi-Fi roam or
// USB hiccup the client's queue is deep only because the stalled frames
// arrived at once, and it reports skipping them; that is not a reason to
// restart the encoder at a lower bitrate.
func TestSessionMonitorIgnoresStallBacklogBeingDrained(t *testing.T) {
	mon := feedStatsLines(t,
		`{"type":"stats","intervalMs":250,"framesReceived":71,"framesRendered":9,"avgDecodeMs":3.9,"queuedFrames":40,"staleFrames":58}`,
		`{"type":"stats","intervalMs":250,"framesReceived":15,"framesRendered":15,"avgDecodeMs":3.8,"queuedFrames":0,"staleFrames":4}`)
	if mon.consumeCongestion() {
		t.Fatal("draining a stall backlog must not report congestion")
	}
	if got := mon.takeStaleFrames(); got != 62 {
		t.Fatalf("stale frames = %d, want 62", got)
	}
	if got := mon.takeStaleFrames(); got != 0 {
		t.Fatalf("stale frames after take = %d, want 0", got)
	}
}
