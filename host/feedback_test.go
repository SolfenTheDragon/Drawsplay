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
