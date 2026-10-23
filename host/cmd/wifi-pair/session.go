package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"drawsplay/host/internal/wifilan"
)

func verifySession(ctx context.Context, iface, controlEndpoint, videoEndpoint string, store *wifilan.TrustStore, testVideo string, forceRecovery bool, out io.Writer) (result error) {
	// Trust the paired identity, never identity claims from discovery metadata.
	peer, paired := store.Peer()
	if !paired {
		return wifilan.ErrPairedIdentity
	}
	pin, err := wifilan.IdentitySPKI(store.Identity())
	if err != nil {
		return err
	}
	control, err := wifilan.DialPairedLAN(ctx, iface, controlEndpoint, store.Identity(), peer)
	if err != nil {
		return fmt.Errorf("saved trust could not authenticate a fresh control channel: %w", err)
	}
	defer func() { result = errors.Join(result, control.NetConn().Close()) }()
	local := wifilan.SessionHello{
		MinMajor: 2, MaxMajor: 2, Role: wifilan.SessionRoleHost, CodecMask: 1,
		MaxWidth: 1920, MaxHeight: 1200, MaxFPS: 60, H264Level: 42,
		MaxAccessUnit: 1024 * 1024, QueueBytes: 3 * 1024 * 1024, QueueSlots: 3,
		StatsVersion: 1, Features: 1, SPKI: pin,
	}
	var registry wifilan.SessionRegistry
	binding, limits, err := wifilan.NegotiatePairedControl(ctx, control, local, peer, &registry)
	if err != nil {
		return fmt.Errorf("paired control negotiation failed: %w", err)
	}
	defer binding.Close()
	video, err := wifilan.DialPairedLAN(ctx, iface, videoEndpoint, store.Identity(), peer)
	if err != nil {
		return fmt.Errorf("saved trust could not authenticate a fresh video channel: %w", err)
	}
	defer func() { result = errors.Join(result, video.NetConn().Close()) }()
	if err := wifilan.AcceptPairedVideo(ctx, video, binding, peer); err != nil {
		return fmt.Errorf("video channel could not attach to paired control: %w", err)
	}
	if _, err = fmt.Fprintln(out, "Paired control/video channels verified using saved trust. No capture or input was started."); err != nil {
		return err
	}
	if testVideo != "" {
		return streamSyntheticVideo(ctx, testVideo, control, video, binding, limits, forceRecovery, out)
	}
	return nil
}
