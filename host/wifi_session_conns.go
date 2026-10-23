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
	return in, newWiFiVideoConn(video, bridge), nil
}
