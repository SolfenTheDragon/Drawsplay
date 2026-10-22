package wifilan

import (
	"context"
	"crypto/tls"
	"errors"
	"time"
)

// DialPairedLAN authenticates saved identities on a fresh host-initiated TCP
// connection. It never enrolls, changes trust or uses an unselected route.
func DialPairedLAN(ctx context.Context, interfaceName, endpoint string, identity tls.Certificate, pairedSPKI [32]byte) (_ *tls.Conn, result error) {
	config, err := PairedServerTLS(identity, pairedSPKI)
	if err != nil {
		return nil, err
	}
	handshake, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := DialSelectedLAN(handshake, interfaceName, endpoint)
	if err != nil {
		return nil, err
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, raw.Close())
		}
	}()
	deadline, _ := handshake.Deadline()
	if err := raw.SetDeadline(deadline); err != nil {
		return nil, err
	}
	secured := tls.Server(raw, config)
	if err := secured.HandshakeContext(handshake); err != nil {
		return nil, err
	}
	if err := handshake.Err(); err != nil {
		return nil, err
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return secured, nil
}
