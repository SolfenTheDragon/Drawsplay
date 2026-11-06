package main

import (
	"context"
	"fmt"
	"net"
	"time"

	"drawsplay/host/internal/wifilan"
)

const wifiDecoderReadyTimeout = 5 * time.Second

// establishPairedWiFi connects to a discovered paired tablet and returns the
// conns for the accept loops. It starts no capture or input; both conns are
// closed on error.
func establishPairedWiFi(ctx context.Context, iface string, store *wifilan.TrustStore, c wifilan.Candidate) (input, video net.Conn, err error) {
	if len(c.Addresses) == 0 {
		return nil, nil, fmt.Errorf("tablet %q advertised no usable address", c.Name)
	}
	address := c.Addresses[0].String()
	s, err := wifilan.ConnectPairedHost(ctx, iface,
		net.JoinHostPort(address, fmt.Sprint(c.InputPort)),
		net.JoinHostPort(address, fmt.Sprint(c.VideoPort)), store)
	if err != nil {
		return nil, nil, err
	}
	return newWiFiSessionConns(s.Control, s.Video, s.Binding.Offer().Epoch, s.Limits, wifiDecoderReadyTimeout)
}
