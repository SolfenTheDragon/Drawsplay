package wifilan

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// AuthenticateEnrollmentHost owns raw on failure. The host initiates TCP but
// remains the TLS server. Success is pending enrollment, not session admission:
// callers must obtain local approval and persist trust before enabling input or
// capture. The invitation and connection each have a single owner.
func AuthenticateEnrollmentHost(ctx context.Context, raw net.Conn, identity tls.Certificate, invitation *PairingInvitation) (_ *tls.Conn, peer [32]byte, result error) {
	defer func() {
		if result != nil {
			result = errors.Join(result, raw.Close())
		}
	}()
	if invitation == nil {
		return nil, peer, ErrPairing
	}
	if err := ctx.Err(); err != nil {
		return nil, peer, err
	}
	// Validate the local signing key before advertising its identity.
	config, err := PairedServerTLS(identity, [32]byte{1})
	if err != nil {
		return nil, peer, err
	}
	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil {
		return nil, peer, err
	}
	host := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	deadline := time.Now().Add(120 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	if invitation.expires.Before(deadline) {
		deadline = invitation.expires
	}
	if !deadline.After(time.Now()) {
		return nil, peer, ErrPairing
	}
	if err := raw.SetDeadline(deadline); err != nil {
		return nil, peer, err
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	var preface [40]byte
	copy(preface[:8], []byte{'D', 'S', 'E', 'P', 0, 2, 0, 0})
	copy(preface[8:], host[:])
	if err := writeAll(raw, preface[:]); err != nil {
		return nil, peer, fmt.Errorf("send Wi-Fi enrollment greeting: %w", err)
	}
	if _, err := io.ReadFull(raw, peer[:]); err != nil {
		return nil, peer, fmt.Errorf("read Wi-Fi enrollment identity: %w", err)
	}
	server, challenge, err := invitation.Begin(host, peer, time.Now())
	if err != nil {
		return nil, peer, err
	}
	defer server.Close()
	var encoded [16 + 32 + pairingPublicSize]byte
	copy(encoded[:16], challenge.Salt[:])
	copy(encoded[16:48], challenge.Nonce[:])
	copy(encoded[48:], challenge.Public[:])
	if err := writeAll(raw, encoded[:]); err != nil {
		return nil, peer, fmt.Errorf("send Wi-Fi pairing challenge: %w", err)
	}
	var answer [pairingPublicSize + 32]byte
	if _, err := io.ReadFull(raw, answer[:]); err != nil {
		return nil, peer, fmt.Errorf("read Wi-Fi pairing confirmation: %w", err)
	}
	proof, err := server.Confirm(answer[:pairingPublicSize], answer[pairingPublicSize:], time.Now())
	if err != nil {
		return nil, peer, err
	}
	defer clear(proof)
	if err := writeAll(raw, proof); err != nil {
		return nil, peer, fmt.Errorf("send Wi-Fi host confirmation: %w", err)
	}
	config.VerifyConnection = func(state tls.ConnectionState) error {
		return VerifyPairedCertificate(state.PeerCertificates, peer, true, time.Now())
	}
	secured := tls.Server(raw, config)
	handshakeContext, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := secured.HandshakeContext(handshakeContext); err != nil {
		return nil, peer, fmt.Errorf("authenticate Wi-Fi enrollment identities: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, peer, err
	}
	if !stop() {
		return nil, peer, context.Canceled
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		return nil, peer, err
	}
	return secured, peer, nil
}
