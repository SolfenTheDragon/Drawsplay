package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"testing"
	"time"
)

func isolatedWiFiCertificate(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv("HOME", dir)
	cert, fingerprint, err := ensureWiFiTLSCertificate()
	if err != nil {
		t.Fatal(err)
	}
	return cert, fingerprint
}

func TestWiFiCertificateFingerprint(t *testing.T) {
	if _, err := certificateFingerprint(tls.Certificate{}); err == nil {
		t.Fatal("empty chain accepted")
	}
	got, err := certificateFingerprint(tls.Certificate{Certificate: [][]byte{[]byte("abc")}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("unexpected fingerprint: %s", got)
	}
}

func TestWiFiCertificatePersistsIdentity(t *testing.T) {
	first, fingerprint := isolatedWiFiCertificate(t)
	second, secondFingerprint, err := ensureWiFiTLSCertificate()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Certificate[0], second.Certificate[0]) || fingerprint != secondFingerprint {
		t.Fatal("certificate identity changed between loads")
	}
	leaf, err := x509.ParseCertificate(first.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("expected Android-compatible ECDSA identity, got %s", leaf.PublicKeyAlgorithm)
	}
	if err := leaf.VerifyHostname("drawsplay.local"); err != nil {
		t.Fatal(err)
	}
}

func TestWiFiCertificateTLSHandshake(t *testing.T) {
	cert, fingerprint := isolatedWiFiCertificate(t)
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		for _, correctPin := range []bool{true, false} {
			t.Run(fmt.Sprintf("%x/correctPin=%v", version, correctPin), func(t *testing.T) {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				clientConn, err := net.DialTimeout("tcp", listener.Addr().String(), 5*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer clientConn.Close()
				serverConn, err := listener.Accept()
				if err != nil {
					t.Fatal(err)
				}
				defer serverConn.Close()
				deadline := time.Now().Add(5 * time.Second)
				if err := serverConn.SetDeadline(deadline); err != nil {
					t.Fatal(err)
				}
				if err := clientConn.SetDeadline(deadline); err != nil {
					t.Fatal(err)
				}
				server := tls.Server(serverConn, &tls.Config{
					MinVersion: version, MaxVersion: version,
					Certificates: []tls.Certificate{cert},
				})
				expectedPin := fingerprint
				if !correctPin {
					expectedPin = "not-the-paired-certificate"
				}
				client := tls.Client(clientConn, &tls.Config{
					MinVersion: version, MaxVersion: version,
					// Test the exact leaf pin instead of a public CA/hostname.
					InsecureSkipVerify: true,
					VerifyConnection: func(state tls.ConnectionState) error {
						got, err := certificateFingerprint(tls.Certificate{
							Certificate: [][]byte{state.PeerCertificates[0].Raw},
						})
						if err != nil {
							return err
						}
						if got != expectedPin {
							return fmt.Errorf("certificate pin mismatch")
						}
						return nil
					},
				})
				serverResult := make(chan error, 1)
				go func() { serverResult <- server.Handshake() }()
				clientErr := client.Handshake()
				serverErr := <-serverResult
				if correctPin {
					if clientErr != nil || serverErr != nil {
						t.Fatalf("TLS handshake failed: client=%v server=%v", clientErr, serverErr)
					}
				} else if clientErr == nil || serverErr == nil {
					t.Fatalf("wrong pin accepted: client=%v server=%v", clientErr, serverErr)
				}
			})
		}
	}
}
