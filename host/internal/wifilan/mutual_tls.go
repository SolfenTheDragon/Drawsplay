package wifilan

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"slices"
	"time"
)

var ErrPairedIdentity = errors.New("Wi-Fi peer identity is unpaired, revoked, expired or incompatible; re-pair locally")

// VerifyPairedCertificate checks a previously PAKE-confirmed SPKI. Public CA
// names, discovery labels and supplied intermediate certificates confer no trust.
func VerifyPairedCertificate(chain []*x509.Certificate, expected [32]byte, client bool, now time.Time) error {
	if len(chain) == 0 || len(chain) > 3 || chain[0] == nil || expected == [32]byte{} {
		return ErrPairedIdentity
	}
	leaf := chain[0]
	key, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) ||
		len(leaf.UnhandledCriticalExtensions) != 0 ||
		(leaf.KeyUsage != 0 && leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0) {
		return ErrPairedIdentity
	}
	purpose := x509.ExtKeyUsageServerAuth
	if client {
		purpose = x509.ExtKeyUsageClientAuth
	}
	if len(leaf.ExtKeyUsage)+len(leaf.UnknownExtKeyUsage) != 0 && !slices.Contains(leaf.ExtKeyUsage, purpose) &&
		!slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageAny) {
		return ErrPairedIdentity
	}
	actual := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
		return ErrPairedIdentity
	}
	return nil
}

// PairedServerTLS keeps the host as TLS server even on host-initiated TCP.
// Callers must close sessions and discard this config when revoking a device.
func PairedServerTLS(identity tls.Certificate, pairedSPKI [32]byte) (*tls.Config, error) {
	if pairedSPKI == [32]byte{} || len(identity.Certificate) == 0 {
		return nil, ErrPairedIdentity
	}
	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil {
		return nil, err
	}
	ownPin := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	if err := VerifyPairedCertificate([]*x509.Certificate{leaf}, ownPin, false, time.Now()); err != nil {
		return nil, err
	}
	signer, ok := identity.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, ErrPairedIdentity
	}
	public, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || sha256.Sum256(public) != ownPin {
		return nil, ErrPairedIdentity
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		},
		Certificates: []tls.Certificate{identity},
		ClientAuth:   tls.RequireAnyClientCert,
		// A resumed session must never bypass a fresh revocation check.
		SessionTicketsDisabled: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			return VerifyPairedCertificate(state.PeerCertificates, pairedSPKI, true, time.Now())
		},
	}, nil
}
