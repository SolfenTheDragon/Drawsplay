package main

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"time"

	"drawsplay/host/internal/wifilan"
)

// runSession preconfigures public test pins, never paired product trust.
// Its two real loopback TLS channels exercise production session negotiation.
func runSession(controlAddress, videoAddress, encodedPin string, output io.Writer) (result error) {
	for _, address := range []string{controlAddress, videoAddress} {
		endpoint, err := netip.ParseAddrPort(address)
		if err != nil || !endpoint.Addr().IsLoopback() || endpoint.Port() == 0 {
			return errors.New("session test peer requires two numeric loopback addresses")
		}
	}
	pinBytes, err := hex.DecodeString(encodedPin)
	if err != nil || len(pinBytes) != 32 {
		return errors.New("session test peer requires the tablet test SPKI")
	}
	pin := [32]byte(pinBytes)
	directory, err := os.MkdirTemp("", "drawsplay-session-test-")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, os.RemoveAll(directory)) }()
	store, err := wifilan.OpenTrustStore(directory)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, store.Close()) }()
	hostPin, err := wifilan.IdentitySPKI(store.Identity())
	if err != nil {
		return err
	}
	config, err := wifilan.PairedServerTLS(store.Identity(), pin)
	if err != nil {
		return err
	}
	if _, err := output.Write(hostPin[:]); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dial := func(address string) (*tls.Conn, error) {
		raw, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		if err := raw.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return nil, errors.Join(err, raw.Close())
		}
		secured := tls.Server(raw, config)
		if err := secured.HandshakeContext(ctx); err != nil {
			return nil, errors.Join(err, raw.Close())
		}
		return secured, nil
	}
	control, err := dial(controlAddress)
	if err != nil {
		return err
	}
	defer control.NetConn().Close()
	local := wifilan.SessionHello{
		MinMajor: 2, MaxMajor: 2, Role: wifilan.SessionRoleHost, CodecMask: 1,
		MaxWidth: 1920, MaxHeight: 1200, MaxFPS: 60, H264Level: 42,
		MaxAccessUnit: 1024 * 1024, QueueBytes: 3 * 1024 * 1024, QueueSlots: 3,
		StatsVersion: 1, Features: 1, SPKI: hostPin,
	}
	var registry wifilan.SessionRegistry
	binding, limits, err := wifilan.NegotiatePairedControl(ctx, control, local, pin, &registry)
	if err != nil {
		return err
	}
	defer binding.Close()
	if limits.MaxWidth != 1280 || limits.MaxHeight != 720 || limits.MaxFPS != 30 || limits.QueueSlots != 2 {
		return errors.New("incorrect negotiated limits")
	}
	epoch := binding.Offer().Epoch
	video, err := dial(videoAddress)
	if err != nil {
		return err
	}
	defer video.NetConn().Close()
	if err := wifilan.AcceptPairedVideo(ctx, video, binding, pin); err != nil {
		return err
	}
	if err := video.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	if err := wifilan.NewFrameWriter(video).WriteFrame(wifilan.FrameHeader{
		Type: wifilan.FrameConfig, Epoch: epoch, Sequence: 2, PayloadLength: 1,
	}, []byte{42}); err != nil {
		return err
	}
	if err := control.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	ping := [4]byte{4, 0, 0, 1}
	if err := wifilan.NewFrameWriter(control).WriteFrame(wifilan.FrameHeader{
		Type: wifilan.FrameControl, Epoch: epoch, Sequence: 2, PayloadLength: 4,
	}, ping[:]); err != nil {
		return err
	}
	var answer [4]byte
	header, body, err := wifilan.NewFrameReader(control).ReadFrame(answer[:])
	if err != nil {
		return err
	}
	if header.Type != wifilan.FrameControl || header.Epoch != epoch || header.Sequence != 2 ||
		len(body) != 4 || answer != ping {
		return fmt.Errorf("incorrect authenticated control response")
	}
	_, err = output.Write([]byte{1})
	return err
}
