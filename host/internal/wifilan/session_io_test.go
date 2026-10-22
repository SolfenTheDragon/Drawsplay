package wifilan

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"
)

func sessionTLSPair(t *testing.T, hostIdentity, tabletIdentity tls.Certificate, hostPin, tabletPin [32]byte) (*tls.Conn, *tls.Conn) {
	t.Helper()
	config, err := PairedServerTLS(hostIdentity, tabletPin)
	if err != nil {
		t.Fatal(err)
	}
	hostRaw, tabletRaw := net.Pipe()
	t.Cleanup(func() { hostRaw.Close(); tabletRaw.Close() })
	host := tls.Server(hostRaw, config)
	tablet := tls.Client(tabletRaw, &tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{tabletIdentity},
		InsecureSkipVerify: true, // Test client pins the actual paired host.
		VerifyConnection: func(state tls.ConnectionState) error {
			return VerifyPairedCertificate(state.PeerCertificates, hostPin, false, time.Now())
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- tablet.HandshakeContext(ctx) }()
	if err := host.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return host, tablet
}

func TestPairedSessionNegotiatesControlThenAssociatesSeparateVideoTLS(t *testing.T) {
	hostIdentity, hostPin := testIdentity(t)
	tabletIdentity, tabletPin := testIdentity(t)
	controlHost, controlTablet := sessionTLSPair(t, hostIdentity, tabletIdentity, hostPin, tabletPin)
	base, err := DecodeSessionHello(sessionHelloFixture(), SessionRoleTablet, [32]byte{
		7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
		7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	local := base
	local.Role, local.SPKI = SessionRoleHost, hostPin
	base.SPKI, base.MaxWidth, base.MaxHeight, base.MaxFPS = tabletPin, 1280, 720, 30
	tabletResult := make(chan error, 1)
	tabletOffer := make(chan SessionOffer, 1)
	go func() {
		var payload [96]byte
		if err := base.Encode(payload[:]); err != nil {
			tabletResult <- err
			return
		}
		if err := NewFrameWriter(controlTablet).WriteFrame(FrameHeader{Type: FrameHello, PayloadLength: 96}, payload[:]); err != nil {
			tabletResult <- err
			return
		}
		reader := NewFrameReader(controlTablet)
		header, body, err := reader.ReadFrame(payload[:])
		if err == nil && header.Type != FrameHello {
			err = ErrFrameHeader
		}
		if err == nil {
			var received SessionHello
			received, err = DecodeSessionHello(body, SessionRoleHost, hostPin)
			if err == nil && (received.MaxWidth != 1280 || received.MaxHeight != 720 || received.MaxFPS != 30) {
				err = ErrSessionHello
			}
		}
		if err == nil {
			header, body, err = reader.ReadFrame(payload[:])
		}
		if err == nil && (header.Type != FrameControl || header.Sequence != 1 || header.PayloadLength != 56) {
			err = ErrFrameHeader
		}
		if err == nil {
			var offer SessionOffer
			offer, err = DecodeSessionOffer(body, header.Epoch)
			if err == nil {
				tabletOffer <- offer
			}
		}
		tabletResult <- err
	}()
	var registry SessionRegistry
	binding, negotiated, err := NegotiatePairedControl(context.Background(), controlHost, local, tabletPin, &registry)
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Close()
	if err := <-tabletResult; err != nil {
		t.Fatal(err)
	}
	if negotiated.MaxFPS != 30 || negotiated.MaxWidth != 1280 {
		t.Fatal("host did not retain the negotiated resource bounds")
	}
	offer := <-tabletOffer
	videoHost, videoTablet := sessionTLSPair(t, hostIdentity, tabletIdentity, hostPin, tabletPin)
	go func() {
		var payload [52]byte
		err := offer.EncodeAttach(payload[:])
		if err == nil {
			err = NewFrameWriter(videoTablet).WriteFrame(FrameHeader{Type: FrameAttach, Epoch: offer.Epoch, Sequence: 1, PayloadLength: 52}, payload[:])
		}
		if err == nil {
			var header FrameHeader
			var body []byte
			header, body, err = NewFrameReader(videoTablet).ReadFrame(payload[:])
			if err == nil && (header.Type != FrameControl || header.Epoch != offer.Epoch || header.Sequence != 1 ||
				len(body) != 4 || body[0] != 3 || body[1] != 0 || body[2] != 0 || body[3] != 1) {
				err = ErrVideoAttach
			}
		}
		tabletResult <- err
	}()
	if err := AcceptPairedVideo(context.Background(), videoHost, binding, tabletPin); err != nil {
		t.Fatal(err)
	}
	if err := <-tabletResult; err != nil {
		t.Fatal(err)
	}
	if err := binding.Attach(tabletPin, offer, time.Now()); !errors.Is(err, ErrVideoAttach) {
		t.Fatal("successful video association left a reusable attachment credential")
	}
}

func TestSessionControlRejectsMissingMutualTLSBeforeAnyProtocolBytes(t *testing.T) {
	identity, _ := testIdentity(t)
	config, err := PairedServerTLS(identity, [32]byte{7})
	if err != nil {
		t.Fatal(err)
	}
	hostRaw, tabletRaw := net.Pipe()
	defer tabletRaw.Close()
	host := tls.Server(hostRaw, config)
	var registry SessionRegistry
	if _, _, err := NegotiatePairedControl(context.Background(), host, SessionHello{}, [32]byte{7}, &registry); err == nil {
		t.Fatal("unauthenticated channel entered capability negotiation")
	}
	if registry.count != 0 {
		t.Fatal("unauthenticated channel reserved a session")
	}
}

func TestPairedSessionCancellationClosesPartialHeaderWithoutReservingSession(t *testing.T) {
	hostIdentity, hostPin := testIdentity(t)
	tabletIdentity, tabletPin := testIdentity(t)
	host, tablet := sessionTLSPair(t, hostIdentity, tabletIdentity, hostPin, tabletPin)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var registry SessionRegistry
	done := make(chan error, 1)
	go func() {
		_, _, err := NegotiatePairedControl(ctx, host, SessionHello{Role: SessionRoleHost}, tabletPin, &registry)
		done <- err
	}()
	if _, err := tablet.Write([]byte{'D'}); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || registry.count != 0 {
			t.Fatal("cancelled partial header admitted a session")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation left a blocked session handshake or TLS close-notify")
	}
}

func TestPairedVideoRejectsUnhandshakenChannelAndInvalidatesBinding(t *testing.T) {
	identity, pin := testIdentity(t)
	config, err := PairedServerTLS(identity, pin)
	if err != nil {
		t.Fatal(err)
	}
	hostRaw, tabletRaw := net.Pipe()
	defer tabletRaw.Close()
	host := tls.Server(hostRaw, config)
	var registry SessionRegistry
	binding, err := registry.NewSession(pin, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	offer := binding.Offer()
	if err := AcceptPairedVideo(context.Background(), host, binding, pin); err == nil {
		t.Fatal("video attachment accepted incomplete TLS")
	}
	if err := binding.Attach(pin, offer, time.Now()); !errors.Is(err, ErrVideoAttach) {
		t.Fatal("failed channel left a valid session ticket")
	}
}
