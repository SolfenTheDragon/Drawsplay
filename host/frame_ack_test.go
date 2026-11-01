package main

import (
	"net"
	"testing"
	"time"
)

// resetFrameAckCounters puts the process-wide ack counters in a known state.
func resetFrameAckCounters(t *testing.T) {
	t.Helper()
	frameAcks.forwarded.Store(0)
	frameAcks.acked.Store(0)
	select {
	case <-frameAcks.signal:
	default:
	}
}

func TestHelperAckedFramesCountsOnlyOwnFrames(t *testing.T) {
	resetFrameAckCounters(t)
	// An earlier pipeline forwarded 10 frames, 7 of them acknowledged.
	for range 10 {
		noteFrameForwarded()
	}
	addFrameAcks(7)
	start := frameAcks.forwarded.Load()

	if got := helperAckedFrames(start); got != 0 {
		t.Fatalf("new helper sees %d acks before producing anything, want 0", got)
	}
	for range 4 {
		noteFrameForwarded()
	}
	// Acks arrive in order: the 3 outstanding old frames come first.
	addFrameAcks(3)
	if got := helperAckedFrames(start); got != 0 {
		t.Fatalf("old frames' acks credited to new helper: %d", got)
	}
	addFrameAcks(2)
	if got := helperAckedFrames(start); got != 2 {
		t.Fatalf("helperAckedFrames = %d, want 2", got)
	}
	// Never more than the helper produced, even if the client over-reports.
	addFrameAcks(50)
	if got := helperAckedFrames(start); got != 4 {
		t.Fatalf("helperAckedFrames = %d, want clamp to 4", got)
	}
}

func TestResetFrameAcksSettlesEarlierFrames(t *testing.T) {
	resetFrameAckCounters(t)
	for range 5 {
		noteFrameForwarded()
	}
	// The connection that should have acked them is gone.
	resetFrameAcks()
	start := frameAcks.forwarded.Load()
	noteFrameForwarded()
	addFrameAcks(1)
	if got := helperAckedFrames(start); got != 1 {
		t.Fatalf("helperAckedFrames after reconnect = %d, want 1", got)
	}
}

func TestNALStartsPicture(t *testing.T) {
	cases := []struct {
		name  string
		codec string
		unit  []byte
		want  bool
	}{
		{"h264 IDR first slice", "h264", []byte{0, 0, 0, 1, 0x65, 0x88}, true},
		{"h264 P first slice", "h264", []byte{0, 0, 1, 0x41, 0x9a}, true},
		{"h264 second slice", "h264", []byte{0, 0, 1, 0x41, 0x40}, false},
		{"h264 SPS", "h264", []byte{0, 0, 1, 0x67, 0x80}, false},
		{"h264 AUD", "h264", []byte{0, 0, 1, 0x09, 0xf0}, false},
		{"hevc IDR first segment", "hevc", []byte{0, 0, 1, 0x26, 0x01, 0xaf}, true},
		{"hevc dependent segment", "hevc", []byte{0, 0, 1, 0x02, 0x01, 0x40}, false},
		{"hevc VPS", "hevc", []byte{0, 0, 1, 0x40, 0x01, 0x80}, false},
		{"truncated", "h264", []byte{0, 0, 1, 0x65}, false},
	}
	for _, c := range cases {
		if got := nalStartsPicture(c.codec, c.unit); got != c.want {
			t.Errorf("%s: nalStartsPicture = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestStatsReaderCreditsCumulativeAcks(t *testing.T) {
	resetFrameAckCounters(t)
	for range 20 {
		noteFrameForwarded()
	}
	host, client := net.Pipe()
	defer client.Close()
	mon := &sessionMonitor{}
	done := make(chan struct{})
	go func() {
		mon.runStatsReader(host)
		close(done)
	}()

	// runStatsReader starts by settling everything sent so far.
	for _, line := range []string{
		`{"type":"ack","frames":3}`,
		`{"type":"ack","frames":2}`, // stale/out of order: ignored
		`{"type":"stats","intervalMs":500,"framesReceived":3}`,
		`{"type":"ack","frames":5}`,
	} {
		if _, err := client.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for frameAcks.acked.Load() != 25 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := frameAcks.acked.Load(); got != 25 {
		t.Fatalf("acked = %d, want 20 settled + 5 acknowledged", got)
	}
	select {
	case <-frameAcks.signal:
	default:
		t.Fatal("ack did not wake the helper feeder")
	}
	client.Close()
	<-done
	if !mon.gone() {
		t.Fatal("reader did not report the client gone")
	}
}
