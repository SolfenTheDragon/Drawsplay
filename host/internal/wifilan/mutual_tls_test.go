package wifilan

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"
)

func testIdentity(t *testing.T) (tls.Certificate, [32]byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Wi-Fi test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, sha256.Sum256(public)
}

func TestMutualTLSRejectsUnpairedClientsBeforeApplicationData(t *testing.T) {
	host, hostPin := testIdentity(t)
	tablet, tabletPin := testIdentity(t)
	rogue, _ := testIdentity(t)
	for _, test := range []struct {
		name    string
		cert    *tls.Certificate
		pass    bool
		version uint16
	}{
		{"paired-tls12", &tablet, true, tls.VersionTLS12},
		{"paired-tls13", &tablet, true, tls.VersionTLS13},
		{"rogue", &rogue, false, tls.VersionTLS13},
		{"no-certificate", nil, false, tls.VersionTLS13},
		{"obsolete-tls11", &tablet, false, tls.VersionTLS11},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, err := PairedServerTLS(host, tabletPin)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serverResult := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					serverResult <- err
					return
				}
				defer conn.Close()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
				defer cancel()
				serverResult <- tls.Server(conn, config).HandshakeContext(ctx)
			}()
			clientConfig := &tls.Config{
				MinVersion: test.version,
				MaxVersion: test.version,
				// Test clients pin the host instead of requiring a public CA.
				InsecureSkipVerify: true,
				VerifyConnection: func(state tls.ConnectionState) error {
					return VerifyPairedCertificate(state.PeerCertificates, hostPin, false, time.Now())
				},
			}
			if test.cert != nil {
				clientConfig.Certificates = []tls.Certificate{*test.cert}
			}
			connection, _ := tls.DialWithDialer(&net.Dialer{Timeout: time.Second * 3}, "tcp4", listener.Addr().String(), clientConfig)
			if connection != nil {
				connection.Close()
			}
			if err := <-serverResult; (err == nil) != test.pass {
				t.Fatalf("authentication result %v; want accepted=%v", err, test.pass)
			}
		})
	}
}

func TestPairedCertificatePolicyRejectsExpiredWrongPurposeAndChangedKeys(t *testing.T) {
	identity, pin := testIdentity(t)
	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPairedCertificate([]*x509.Certificate{leaf}, pin, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		leaf x509.Certificate
		pin  [32]byte
		now  time.Time
	}{
		{"expired", *leaf, pin, leaf.NotAfter.Add(time.Second)},
		{"not-yet-valid", *leaf, pin, leaf.NotBefore.Add(-time.Second)},
		{"changed-pin", *leaf, [32]byte{1}, time.Now()},
		{"revoked", *leaf, [32]byte{}, time.Now()},
		{"wrong-purpose", func() x509.Certificate {
			v := *leaf
			v.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			return v
		}(), pin, time.Now()},
		{"no-signing", func() x509.Certificate { v := *leaf; v.KeyUsage = x509.KeyUsageKeyEncipherment; return v }(), pin, time.Now()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := VerifyPairedCertificate([]*x509.Certificate{&test.leaf}, test.pin, true, test.now); err == nil {
				t.Fatal("unsafe paired certificate accepted")
			}
		})
	}
	if _, err := PairedServerTLS(identity, [32]byte{}); err == nil {
		t.Fatal("server without a paired identity accepted")
	}
}
