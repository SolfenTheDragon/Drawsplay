package main

import (
	"net"
	"testing"
	"time"
)

func feedWiFiStatsLines(t *testing.T, lines ...string) *sessionMonitor {
	t.Helper()
	hostConn, clientConn := net.Pipe()
	defer hostConn.Close()
	mon := newWiFiSessionMonitor()
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

// A paired tablet drops the odd frame to presentation jitter; a lower bitrate
// cannot prevent that, so only a substantial share of drops is congestion.
func TestWiFiMonitorIgnoresOccasionalJitterDrops(t *testing.T) {
	mon := feedWiFiStatsLines(t, `{"type":"stats","intervalMs":500,"framesReceived":30,"framesRendered":28,"queuedFrames":1,"frameDrops":2}`)
	if mon.consumeCongestion() {
		t.Fatal("2 of 30 dropped frames stepped the Wi-Fi bitrate down")
	}
}

func TestWiFiMonitorCongestsOnSubstantialDrops(t *testing.T) {
	mon := feedWiFiStatsLines(t, `{"type":"stats","intervalMs":500,"framesReceived":30,"framesRendered":24,"queuedFrames":1,"frameDrops":6}`)
	if !mon.consumeCongestion() {
		t.Fatal("6 of 30 dropped frames did not report congestion")
	}
}

// After a Wi-Fi airtime stall the tablet decodes the delayed burst and shows
// only the newest picture. Those catch-up skips mean the stall already ended;
// a lower bitrate would not have prevented it and its restart adds a freeze.
func TestWiFiMonitorIgnoresCatchUpSkipsAfterALinkStall(t *testing.T) {
	mon := feedWiFiStatsLines(t, `{"type":"stats","intervalMs":500,"framesReceived":30,"framesRendered":18,"queuedFrames":0,"frameDrops":12,"staleFrames":12}`)
	if mon.consumeCongestion() {
		t.Fatal("12 catch-up skips after a stall stepped the Wi-Fi bitrate down")
	}
}

func TestWiFiMonitorStillCongestsOnNonStaleDropsBesideCatchUp(t *testing.T) {
	mon := feedWiFiStatsLines(t, `{"type":"stats","intervalMs":500,"framesReceived":30,"framesRendered":18,"queuedFrames":0,"frameDrops":12,"staleFrames":4}`)
	if !mon.consumeCongestion() {
		t.Fatal("8 queue drops of 30 did not report congestion")
	}
}

func TestWiFiCongestionGateKeepsAWatchableFloor(t *testing.T) {
	rc := newRateControllerKbps(26666)
	gate := sessionCongestionGate(captureTarget{}, true)
	for steps := 0; steps < 20 && gate.allowsStepDown(rc); steps++ {
		rc.idx++
	}
	if low := rc.ladder[rc.idx]; low < 26666/8 || low > 26666/4 {
		t.Fatalf("Wi-Fi ladder floor is %dk, want between top/8 and top/4", low)
	}
	usb := sessionCongestionGate(captureTarget{}, false)
	if want := newSessionCongestionGate(captureTarget{}).floorDivisor; usb.floorDivisor != want {
		t.Fatalf("USB gate floor divisor = %d, want the platform default %d", usb.floorDivisor, want)
	}
}

func TestKeyframeRequestsAreHonouredAtMostOncePerCooldown(t *testing.T) {
	mon := feedWiFiStatsLines(t, `{"type":"keyframe"}`)
	start := time.Now()
	if !mon.consumeKeyframeRequest(start) {
		t.Fatal("keyframe request was not reported")
	}
	if mon.consumeKeyframeRequest(start) {
		t.Fatal("one request was reported twice")
	}
	mon.keyframeWanted.Store(true)
	if mon.consumeKeyframeRequest(start.Add(keyframeRequestCooldown / 2)) {
		t.Fatal("a second restart was allowed inside the cooldown")
	}
	if !mon.consumeKeyframeRequest(start.Add(keyframeRequestCooldown + time.Millisecond)) {
		t.Fatal("a request held through the cooldown was dropped")
	}
}
