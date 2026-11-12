package wifilan

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
)

// HostHello is the session hello the DrawSplay host offers a paired tablet.
// The host product and the cmd/wifi-pair diagnostics both use it, so the tool
// tests the same offer the product makes.
func HostHello(pin [32]byte) SessionHello {
	return SessionHello{
		MinMajor: 2, MaxMajor: 2, Role: SessionRoleHost, CodecMask: 1,
		MaxWidth: 1920, MaxHeight: 1200, MaxFPS: 60, H264Level: 50,
		MaxAccessUnit: 1024 * 1024, QueueBytes: 3 * 1024 * 1024, QueueSlots: 3,
		StatsVersion: 1, Features: FeatureLowLatency | FeatureVideoStats, SPKI: pin,
	}
}

// HostSession is an authenticated, negotiated paired session: the control and
// video channels, their binding and the negotiated limits.
type HostSession struct {
	Control, Video *tls.Conn
	Binding        *SessionBinding
	Limits         SessionHello
}

// Close closes both channels and ends the binding.
func (s *HostSession) Close() error {
	s.Binding.Close()
	return errors.Join(s.Control.NetConn().Close(), s.Video.NetConn().Close())
}

// ConnectPairedHost dials the tablet's control and video endpoints with the
// saved pinned identity, negotiates the session with HostHello and attaches
// the video channel. It starts no capture or input. On error everything it
// opened is closed.
func ConnectPairedHost(ctx context.Context, iface, controlEndpoint, videoEndpoint string, store *TrustStore) (*HostSession, error) {
	peer, paired := store.Peer()
	if !paired {
		return nil, ErrPairedIdentity
	}
	pin, err := IdentitySPKI(store.Identity())
	if err != nil {
		return nil, err
	}
	control, err := DialPairedLAN(ctx, iface, controlEndpoint, store.Identity(), peer)
	if err != nil {
		return nil, fmt.Errorf("authenticate control channel: %w", err)
	}
	var registry SessionRegistry
	// NegotiatePairedControl closes control on failure.
	binding, limits, err := NegotiatePairedControl(ctx, control, HostHello(pin), peer, &registry)
	if err != nil {
		return nil, fmt.Errorf("negotiate control channel: %w", err)
	}
	video, err := DialPairedLAN(ctx, iface, videoEndpoint, store.Identity(), peer)
	if err != nil {
		binding.Close()
		return nil, errors.Join(fmt.Errorf("authenticate video channel: %w", err), control.NetConn().Close())
	}
	// AcceptPairedVideo closes video and the binding on failure.
	if err := AcceptPairedVideo(ctx, video, binding, peer); err != nil {
		return nil, errors.Join(fmt.Errorf("attach video channel: %w", err), control.NetConn().Close())
	}
	return &HostSession{Control: control, Video: video, Binding: binding, Limits: limits}, nil
}
