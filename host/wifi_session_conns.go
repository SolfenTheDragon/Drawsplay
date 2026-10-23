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
	out.maxFPS, out.maxWidth, out.maxHeight = int(limits.MaxFPS), int(limits.MaxWidth), int(limits.MaxHeight)
	return in, out, nil
}

// capWiFiSessionFPS clamps a session frame rate to the paired tablet's
// decoder budget: the negotiated Hello MaxFPS holds at MaxWidth x MaxHeight
// and scales with the session's macroblock count. Other conns are unchanged.
func capWiFiSessionFPS(fps, width, height int, conn net.Conn) int {
	if wrapped, ok := conn.(*closeNotifyConn); ok {
		conn = wrapped.Conn
	}
	c, ok := conn.(*wifiVideoConn)
	if !ok || c.maxFPS <= 0 {
		return fps
	}
	limit := c.maxFPS
	if sessionMBs := h264Macroblocks(width, height); sessionMBs > 0 {
		limit = c.maxFPS * h264Macroblocks(c.maxWidth, c.maxHeight) / sessionMBs
	}
	return min(fps, max(limit, c.maxFPS))
}

func h264Macroblocks(width, height int) int {
	return ((width + 15) / 16) * ((height + 15) / 16)
}
