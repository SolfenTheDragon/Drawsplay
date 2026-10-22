package wifilan

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"math/big"
	"testing"
	"time"

	"github.com/1Password/srp"
)

func testPairingClient(t *testing.T, challenge PairingChallenge, code string, host, tablet [32]byte) ([]byte, []byte, []byte) {
	t.Helper()
	inner := sha256.Sum256([]byte("DrawSplay-WiFi-v2:" + code))
	xHash := sha256.Sum256(append(append([]byte{}, challenge.Salt[:]...), inner[:]...))
	client := srp.NewClientStd(srp.KnownGroups[srp.RFC5054Group3072], new(big.Int).SetBytes(xHash[:]))
	if client == nil {
		t.Fatal("SRP client initialization failed")
	}
	public := client.EphemeralPublic().FillBytes(make([]byte, 384))
	if err := client.SetOthersPublic(new(big.Int).SetBytes(challenge.Public[:])); err != nil {
		t.Fatal(err)
	}
	key, err := client.Key()
	if err != nil {
		t.Fatal(err)
	}
	// Independent wire construction, not the production transcript helper.
	transcript := bytes.NewBufferString("DrawSplay numeric pairing\x00")
	_ = binary.Write(transcript, binary.BigEndian, uint32(2))
	transcript.Write(host[:])
	transcript.Write(tablet[:])
	transcript.Write(challenge.Nonce[:])
	transcript.Write(challenge.Salt[:])
	transcript.Write(public)
	transcript.Write(challenge.Public[:])
	proof := hmac.New(sha256.New, key)
	proof.Write([]byte("tablet confirms\x00"))
	proof.Write(transcript.Bytes())
	tabletProof := proof.Sum(nil)
	proof.Reset()
	proof.Write([]byte("host confirms\x00"))
	proof.Write(transcript.Bytes())
	return public, tabletProof, proof.Sum(nil)
}

func TestPairingRequiresCodeAndBothIdentitiesBeforeConfirmation(t *testing.T) {
	now := time.Now()
	host, tablet := sha256.Sum256([]byte("host")), sha256.Sum256([]byte("tablet"))
	for _, mode := range []string{"correct", "wrong-code", "changed-host", "changed-tablet", "bad-proof", "expired"} {
		t.Run(mode, func(t *testing.T) {
			server, challenge, err := NewPairingServer("01234567", host, tablet, now)
			if err != nil {
				t.Fatal(err)
			}
			code, clientHost, clientTablet := "01234567", host, tablet
			when := now.Add(time.Second)
			switch mode {
			case "wrong-code":
				code = "12345678"
			case "changed-host":
				clientHost[0] ^= 1
			case "changed-tablet":
				clientTablet[0] ^= 1
			case "expired":
				when = now.Add(120 * time.Second)
			}
			public, proof, expectedConfirmation := testPairingClient(t, challenge, code, clientHost, clientTablet)
			if mode == "bad-proof" {
				proof[0] ^= 1
			}
			confirmation, err := server.Confirm(public, proof, when)
			if mode == "correct" {
				if err != nil || !hmac.Equal(confirmation, expectedConfirmation) {
					t.Fatalf("valid pairing failed: %x %v", confirmation, err)
				}

			} else if err == nil || len(confirmation) != 0 {
				t.Fatalf("untrusted pairing produced confirmation: %x %v", confirmation, err)
			}
			if _, err := server.Confirm(public, proof, when); err == nil {
				t.Fatal("single-use challenge accepted twice")
			}
		})
	}
}

func TestPairingRejectsInvalidCodesAndPublicValues(t *testing.T) {
	host, tablet := sha256.Sum256([]byte("host")), sha256.Sum256([]byte("tablet"))
	for _, code := range []string{"", "1234567", "123456789", "１２３４５６７８", "1234567a", " 1234567"} {
		if _, _, err := NewPairingServer(code, host, tablet, time.Now()); err == nil {
			t.Fatalf("invalid code accepted: %q", code)
		}
	}
	for _, public := range [][]byte{nil, make([]byte, 383), make([]byte, 385), make([]byte, 384), append(make([]byte, 383), 1),
		srp.KnownGroups[srp.RFC5054Group3072].N().FillBytes(make([]byte, 384))} {
		server, _, err := NewPairingServer("12345678", host, tablet, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := server.Confirm(public, make([]byte, 32), time.Now()); err == nil {
			t.Fatal("invalid SRP public accepted")
		}
	}
	if _, _, err := NewPairingServer("12345678", [32]byte{}, tablet, time.Now()); err == nil {
		t.Fatal("unbound host identity accepted")
	}
}

func TestPairingInvitationBoundsAttemptsAndExpiresWithoutRenewal(t *testing.T) {
	now := time.Now()
	host, tablet := sha256.Sum256([]byte("host")), sha256.Sum256([]byte("tablet"))
	invitation, err := NewPairingInvitation(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(invitation.Code()) != 8 {
		t.Fatal("invitation code length is not eight digits")
	}
	for attempt := range 5 {
		server, _, err := invitation.Begin(host, tablet, now.Add(time.Duration(attempt)*time.Second))
		if err != nil {
			t.Fatalf("attempt %d rejected: %v", attempt+1, err)
		}
		if _, _, err := invitation.Begin(host, tablet, now); err == nil {
			t.Fatal("concurrent challenge accepted")
		}
		_, _ = server.Confirm(make([]byte, 384), make([]byte, 32), now)
	}
	if _, _, err := invitation.Begin(host, tablet, now); err == nil {
		t.Fatal("sixth online guess accepted")
	}
	invitation, err = NewPairingInvitation(now)
	if err != nil {
		t.Fatal(err)
	}
	server, challenge, err := invitation.Begin(host, tablet, now.Add(119*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	public, proof, _ := testPairingClient(t, challenge, invitation.Code(), host, tablet)
	if _, err := server.Confirm(public, proof, now.Add(120*time.Second)); err == nil {
		t.Fatal("late challenge renewed the invitation expiry")
	}
	invitation.Close()
	if _, _, err := invitation.Begin(host, tablet, now); err == nil {
		t.Fatal("closed invitation accepted")
	}
}

func TestPairingUninitializedStateFailsClosed(t *testing.T) {
	public := make([]byte, 384)
	public[383] = 2
	if _, err := new(PairingServer).Confirm(public, make([]byte, 32), time.Now()); err == nil {
		t.Fatal("uninitialized pairing accepted")
	}
	invitation := new(PairingInvitation)
	if _, _, err := invitation.Begin([32]byte{1}, [32]byte{2}, time.Now()); err == nil {
		t.Fatal("uninitialized invitation accepted")
	}
}
