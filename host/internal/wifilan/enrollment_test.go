package wifilan

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func TestEnrollmentBindsActualKeysBeforeReturningMutualTLS(t *testing.T) {
	for _, mode := range []string{"correct", "wrong-code", "changed-host", "changed-tablet", "wrong-tls-key"} {
		t.Run(mode, func(t *testing.T) {
			host, hostPin := testIdentity(t)
			tablet, tabletPin := testIdentity(t)
			invitation, err := NewPairingInvitation(time.Now())
			if err != nil {
				t.Fatal(err)
			}
			defer invitation.Close()
			hostRaw, tabletRaw := net.Pipe()
			defer tabletRaw.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() {
				connection, peer, err := AuthenticateEnrollmentHost(ctx, hostRaw, host, invitation)
				if err == nil {
					defer connection.Close()
					if peer != tabletPin {
						result <- ErrPairedIdentity
						return
					}
					_, err = connection.Write([]byte{42})
				}
				result <- err
			}()
			tabletRaw.SetDeadline(time.Now().Add(3 * time.Second))
			var preface [40]byte
			if _, err := io.ReadFull(tabletRaw, preface[:]); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(preface[:8], []byte{'D', 'S', 'E', 'P', 0, 2, 0, 0}) || !bytes.Equal(preface[8:], hostPin[:]) {
				t.Fatal("host did not send major-2 preface with its actual identity")
			}
			if _, err := tabletRaw.Write(tabletPin[:]); err != nil {
				t.Fatal(err)
			}
			var challenge PairingChallenge
			if err := binary.Read(tabletRaw, binary.BigEndian, &challenge); err != nil {
				t.Fatal(err)
			}
			code, transcriptHost, transcriptTablet := invitation.Code(), hostPin, tabletPin
			switch mode {
			case "wrong-code":
				code = "00000000"
				if code == invitation.Code() {
					code = "11111111"
				}
			case "changed-host":
				transcriptHost[0] ^= 1
			case "changed-tablet":
				transcriptTablet[0] ^= 1
			}
			public, proof, hostProof := testPairingClient(t, challenge, code, transcriptHost, transcriptTablet)
			if _, err := tabletRaw.Write(append(public, proof...)); err != nil {
				t.Fatal(err)
			}
			var received [32]byte
			_, proofError := io.ReadFull(tabletRaw, received[:])
			switch mode {
			case "correct", "wrong-tls-key":
				if proofError != nil || !bytes.Equal(received[:], hostProof) {
					t.Fatalf("matching exchange failed: %v", proofError)
				}
				if mode == "wrong-tls-key" {
					tablet, _ = testIdentity(t)
				}
				secured := tls.Client(tabletRaw, &tls.Config{
					MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{tablet},
					InsecureSkipVerify: true, // Test client uses the PAKE-confirmed pin.
					VerifyConnection: func(state tls.ConnectionState) error {
						return VerifyPairedCertificate(state.PeerCertificates, hostPin, false, time.Now())
					},
				})
				var message [1]byte
				_, err = io.ReadFull(secured, message[:])
				if mode == "correct" && (err != nil || message[0] != 42) {
					t.Fatalf("authenticated application data failed: %v", err)
				}
				if mode == "wrong-tls-key" && err == nil {
					t.Fatal("PAKE proof authorized a different TLS key")
				}
			default:
				if proofError == nil {
					t.Fatal("host proof exposed before checking the code and identity transcript")
				}
			}
			tabletRaw.Close()
			if err := <-result; (err == nil) != (mode == "correct") {
				t.Fatalf("unexpected admission: %v", err)
			}
		})
	}
}

func TestEnrollmentCancellationClosesPeerAndDoesNotCreateTrust(t *testing.T) {
	host, _ := testIdentity(t)
	invitation, err := NewPairingInvitation(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer invitation.Close()
	for _, mode := range []string{"cancelled", "deadline", "zero-identity"} {
		t.Run(mode, func(t *testing.T) {
			hostRaw, tabletRaw := net.Pipe()
			defer tabletRaw.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			result := make(chan error, 1)
			go func() {
				secured, _, err := AuthenticateEnrollmentHost(ctx, hostRaw, host, invitation)
				if secured != nil {
					secured.Close()
				}
				result <- err
			}()
			tabletRaw.SetDeadline(time.Now().Add(time.Second))
			if mode == "zero-identity" {
				var preface [40]byte
				io.ReadFull(tabletRaw, preface[:])
				tabletRaw.Write(make([]byte, 32))
			}
			if err := <-result; err == nil {
				t.Fatal("incomplete enrollment returned an authenticated connection")
			}
			if _, err := tabletRaw.Write([]byte{1}); err == nil {
				t.Fatal("failed enrollment retained a live socket")
			}
		})
	}
}

func TestEnrollmentHostRejectsMismatchedPrivateIdentity(t *testing.T) {
	identity, _ := testIdentity(t)
	other, _ := testIdentity(t)
	identity.PrivateKey = other.PrivateKey
	hostRaw, tabletRaw := net.Pipe()
	defer tabletRaw.Close()
	invitation, err := NewPairingInvitation(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer invitation.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if connection, _, err := AuthenticateEnrollmentHost(ctx, hostRaw, identity, invitation); err == nil || connection != nil {
		t.Fatal("invalid local identity started enrollment")
	}
	// No unauthenticated private-key mismatch can reach the challenge stage.
	tabletRaw.SetDeadline(time.Now().Add(time.Second))
	if _, err := tabletRaw.Read(make([]byte, 40)); err == nil {
		t.Fatal("invalid identity sent an enrollment preface")
	}
}
