package main

import (
	"net"
	"time"

	"drawsplay/host/internal/wifilan"
)

// newWiFiSessionConns turns an attached paired control/video socket pair into
// the two net.Conns the accept loops expect: an input conn (control socket as
// a byte stream) and a video conn whose videoEndpoint sends Wi-Fi frames once
// the tablet reports its decoder ready. Closing either conn closes its socket.
func newWiFiSessionConns(control, video net.Conn, epoch uint64, limits wifilan.SessionHello, readyTimeout time.Duration) (input, videoConn net.Conn, err error) {
	guard, err := wifilan.NewSequenceGuard(epoch)
	if err != nil {
		return nil, nil, err
	}
	gate := newWiFiDecoderGate()
	in := newWiFiInputConn(control, epoch, guard, 2)
	in.SetControlHandler(gate.Handle)
	ready := func() error { return gate.Wait(readyTimeout) }
	stats := limits.Features&wifilan.FeatureVideoStats != 0
	return in, newWiFiVideoConn(video, epoch, int(limits.MaxAccessUnit), ready, stats), nil
}
