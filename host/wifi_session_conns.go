package main

import (
	"net"
	"time"

	"drawsplay/host/internal/wifilan"
)

// newWiFiSessionConns turns an attached paired control/video socket pair into
// the two net.Conns the existing accept loops expect: an input conn (control
// socket as a byte stream) and a video conn (writes become Wi-Fi frames after
// the tablet reports its decoder ready). Closing either conn closes its socket.
func newWiFiSessionConns(control, video net.Conn, epoch uint64, limits wifilan.SessionHello, readyTimeout time.Duration) (input, videoConn net.Conn, err error) {
	guard, err := wifilan.NewSequenceGuard(epoch)
	if err != nil {
		return nil, nil, err
	}
	gate := newWiFiDecoderGate()
	in := newWiFiInputConn(control, epoch, guard, 2)
	in.SetControlHandler(gate.Handle)
	bridge := newWiFiVideoBridge(video, epoch, int(limits.MaxAccessUnit), func() error {
		return gate.Wait(readyTimeout)
	})
	var out *wifiVideoConn
	if limits.Features&wifilan.FeatureVideoStats != 0 {
		out = newWiFiVideoConnWithStats(video, bridge, epoch)
	} else {
		out = newWiFiVideoConn(video, bridge)
	}
	out.maxFPS = int(limits.MaxFPS)
	return in, out, nil
}

// capWiFiSessionFPS clamps a session frame rate to the paired tablet's
// negotiated Hello MaxFPS, which the tablet derives from its decoder's rated
// throughput at its maximum size. Other conns are returned unchanged.
func capWiFiSessionFPS(fps int, conn net.Conn) int {
	if wrapped, ok := conn.(*closeNotifyConn); ok {
		conn = wrapped.Conn
	}
	if c, ok := conn.(*wifiVideoConn); ok && c.maxFPS > 0 && fps > c.maxFPS {
		return c.maxFPS
	}
	return fps
}
