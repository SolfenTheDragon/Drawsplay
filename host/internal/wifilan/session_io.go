package wifilan

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"time"
)

func preparePairedExchange(ctx context.Context, conn *tls.Conn, pin [32]byte, expires time.Time) (time.Time, func() bool, error) {
	if conn == nil || !conn.ConnectionState().HandshakeComplete {
		return time.Time{}, nil, ErrPairedIdentity
	}
	state := conn.ConnectionState()
	if err := VerifyPairedCertificate(state.PeerCertificates, pin, true, time.Now()); err != nil {
		return time.Time{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	if end, ok := ctx.Deadline(); ok {
		deadline = minTime(deadline, end)
	}
	if !expires.IsZero() {
		deadline = minTime(deadline, expires)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return time.Time{}, nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.NetConn().Close() })
	return deadline, stop, nil
}

func completePairedExchange(ctx context.Context, conn *tls.Conn, deadline time.Time, stop func() bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	if !stop() {
		return context.Canceled
	}
	return conn.SetDeadline(time.Time{})
}

// NegotiatePairedControl owns conn on failure. No capture/injector may start
// until this and AcceptPairedVideo both succeed. First post-offer control
// sequence numbers start at 2; video has its own sequence space.
func NegotiatePairedControl(ctx context.Context, conn *tls.Conn, local SessionHello, paired [32]byte, registry *SessionRegistry) (_ *SessionBinding, _ SessionHello, result error) {
	var pending *SessionBinding
	defer func() {
		if result != nil {
			if pending != nil {
				pending.Close()
			}
			if conn != nil {
				result = errors.Join(result, conn.Close())
			}
		}
	}()
	deadline, stop, err := preparePairedExchange(ctx, conn, paired, time.Time{})
	if err != nil {
		return nil, SessionHello{}, err
	}
	defer stop()
	if registry == nil || local.Role != SessionRoleHost {
		return nil, SessionHello{}, ErrSessionHello
	}
	if _, err := rand.Read(local.Nonce[:]); err != nil {
		return nil, SessionHello{}, err
	}
	var payload [SessionHelloSize]byte
	defer clear(payload[:])
	header, body, err := NewFrameReader(conn).ReadFrame(payload[:])
	if err != nil {
		return nil, SessionHello{}, err
	}
	if header.Type != FrameHello || header.CaptureMonoUS != 0 {
		return nil, SessionHello{}, ErrSessionHello
	}
	peer, err := DecodeSessionHello(body, SessionRoleTablet, paired)
	if err != nil {
		return nil, SessionHello{}, err
	}
	negotiated, err := NegotiateSession(local, peer)
	if err != nil {
		return nil, SessionHello{}, err
	}
	if err := negotiated.Encode(payload[:]); err != nil {
		return nil, SessionHello{}, err
	}
	pending, err = registry.NewSession(paired, time.Now())
	if err != nil {
		return nil, SessionHello{}, err
	}
	writer := NewFrameWriter(conn)
	if err := writer.WriteFrame(FrameHeader{Type: FrameHello, PayloadLength: SessionHelloSize}, payload[:]); err != nil {
		return nil, SessionHello{}, err
	}
	offer := pending.Offer()
	defer clear(offer.Ticket[:])
	if err := offer.Encode(payload[:56]); err != nil {
		return nil, SessionHello{}, err
	}
	if err := writer.WriteFrame(FrameHeader{Type: FrameControl, Epoch: offer.Epoch, Sequence: 1, PayloadLength: 56}, payload[:56]); err != nil {
		return nil, SessionHello{}, err
	}
	if err := completePairedExchange(ctx, conn, deadline, stop); err != nil {
		return nil, SessionHello{}, err
	}
	return pending, negotiated, nil
}

// AcceptPairedVideo consumes one ticket from the same authenticated identity
// as control. Failure invalidates the binding; callers must close control too.
// Success sends video-ready subtype 3, schema 1, at video sequence 1.
func AcceptPairedVideo(ctx context.Context, conn *tls.Conn, binding *SessionBinding, paired [32]byte) (result error) {
	defer func() {
		if result != nil {
			if binding != nil {
				binding.Close()
			}
			if conn != nil {
				result = errors.Join(result, conn.Close())
			}
		}
	}()
	if binding == nil {
		return ErrVideoAttach
	}
	deadline, stop, err := preparePairedExchange(ctx, conn, paired, binding.started.Add(5*time.Second))
	if err != nil {
		return err
	}
	defer stop()
	var payload [52]byte
	defer clear(payload[:])
	header, body, err := NewFrameReader(conn).ReadFrame(payload[:])
	if err != nil {
		return err
	}
	if header.Type != FrameAttach || header.Sequence != 1 || header.CaptureMonoUS != 0 {
		return ErrVideoAttach
	}
	offer, err := DecodeVideoAttach(body, header.Epoch)
	if err != nil {
		return err
	}
	defer clear(offer.Ticket[:])
	if err := binding.Attach(paired, offer, time.Now()); err != nil {
		return err
	}
	ready := [4]byte{3, 0, 0, 1}
	if err := NewFrameWriter(conn).WriteFrame(FrameHeader{Type: FrameControl, Epoch: offer.Epoch, Sequence: 1, PayloadLength: 4}, ready[:]); err != nil {
		return err
	}
	return completePairedExchange(ctx, conn, deadline, stop)
}
