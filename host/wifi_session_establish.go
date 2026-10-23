package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"drawsplay/host/internal/wifilan"
)

const wifiDecoderReadyTimeout = 5 * time.Second

// establishPairedWiFi dials the tablet's control and video endpoints with the
// saved pinned identity, negotiates the session and returns the conns for the
// accept loops. It starts no capture or input; both conns are closed on error.
func establishPairedWiFi(ctx context.Context, iface string, store *wifilan.TrustStore, c wifilan.Candidate) (input, video net.Conn, err error) {
	peer, paired := store.Peer()
	if !paired {
		return nil, nil, wifilan.ErrPairedIdentity
	}
	if len(c.Addresses) == 0 {
		return nil, nil, fmt.Errorf("tablet %q advertised no usable address", c.Name)
	}
	pin, err := wifilan.IdentitySPKI(store.Identity())
	if err != nil {
		return nil, nil, err
	}
	address := c.Addresses[0].String()
	control, err := wifilan.DialPairedLAN(ctx, iface, net.JoinHostPort(address, fmt.Sprint(c.InputPort)), store.Identity(), peer)
	if err != nil {
		return nil, nil, fmt.Errorf("authenticate control channel: %w", err)
	}
	local := wifilan.SessionHello{
		MinMajor: 2, MaxMajor: 2, Role: wifilan.SessionRoleHost, CodecMask: 1,
		MaxWidth: 1920, MaxHeight: 1200, MaxFPS: 60, H264Level: 42,
		MaxAccessUnit: 1024 * 1024, QueueBytes: 3 * 1024 * 1024, QueueSlots: 3,
		StatsVersion: 1, Features: 1, SPKI: pin,
	}
	var registry wifilan.SessionRegistry
	binding, limits, err := wifilan.NegotiatePairedControl(ctx, control, local, peer, &registry)
	if err != nil {
		return nil, nil, fmt.Errorf("negotiate control channel: %w", err)
	}
	videoConn, err := wifilan.DialPairedLAN(ctx, iface, net.JoinHostPort(address, fmt.Sprint(c.VideoPort)), store.Identity(), peer)
	if err != nil {
		binding.Close()
		return nil, nil, errors.Join(fmt.Errorf("authenticate video channel: %w", err), control.NetConn().Close())
	}
	if err := wifilan.AcceptPairedVideo(ctx, videoConn, binding, peer); err != nil {
		return nil, nil, errors.Join(fmt.Errorf("attach video channel: %w", err), control.NetConn().Close())
	}
	return newWiFiSessionConns(control, videoConn, binding.Offer().Epoch, limits, wifiDecoderReadyTimeout)
}
