// Command wifi-pairing-test-peer is a stdio/loopback interoperability fixture.
// It never listens on the LAN or persists paired trust.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"time"

	"drawsplay/host/internal/wifilan"
)

func main() {
	var err error
	if len(os.Args) == 3 && os.Args[1] == "--enrollment" {
		err = runEnrollment(os.Args[2], os.Stdout)
	} else if len(os.Args) == 1 {
		err = run(os.Stdin, os.Stdout)
	} else {
		err = fmt.Errorf("expected no arguments or --enrollment loopback-IP:port")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Wi-Fi pairing interoperability fixture:", err)
		os.Exit(1)
	}
}

// Network mode is loopback-only, with disposable identities and no saved peers.
func runEnrollment(address string, output io.Writer) (result error) {
	endpoint, err := netip.ParseAddrPort(address)
	if err != nil || !endpoint.Addr().IsLoopback() || endpoint.Port() == 0 {
		return fmt.Errorf("enrollment test peer requires a numeric loopback address")
	}
	directory, err := os.MkdirTemp("", "drawsplay-enrollment-test-")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, os.RemoveAll(directory)) }()
	store, err := wifilan.OpenTrustStore(directory)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, store.Close()) }()
	invitation, err := wifilan.NewPairingInvitation(time.Now())
	if err != nil {
		return err
	}
	defer invitation.Close()
	if _, err := io.WriteString(output, invitation.Code()); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", endpoint.String())
	if err != nil {
		return err
	}
	secured, _, err := wifilan.AuthenticateEnrollmentHost(ctx, raw, store.Identity(), invitation)
	if err != nil {
		return err
	}
	defer secured.Close()
	if err := secured.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	if _, err := secured.Write([]byte{42}); err != nil {
		return err
	}
	var answer [1]byte
	if _, err := io.ReadFull(secured, answer[:]); err != nil {
		return err
	}
	if answer[0] != 43 {
		return fmt.Errorf("incorrect authenticated reply")
	}
	return nil
}

func run(input io.Reader, output io.Writer) error {
	host := sha256.Sum256([]byte("public test host identity"))
	tablet := sha256.Sum256([]byte("public test tablet identity"))
	server, challenge, err := wifilan.NewPairingServer("01234567", host, tablet, time.Now())
	if err != nil {
		return err
	}
	defer server.Close()
	hello := struct {
		Magic  uint32
		Host   [32]byte
		Tablet [32]byte
		Salt   [16]byte
		Nonce  [32]byte
		Public [384]byte
	}{0x44535032, host, tablet, challenge.Salt, challenge.Nonce, challenge.Public}
	if err := binary.Write(output, binary.BigEndian, hello); err != nil {
		return err
	}
	var public [384]byte
	var proof [32]byte
	if _, err := io.ReadFull(input, public[:]); err != nil {
		return err
	}
	if _, err := io.ReadFull(input, proof[:]); err != nil {
		return err
	}
	confirmation, err := server.Confirm(public[:], proof[:], time.Now())
	if err := binary.Write(output, binary.BigEndian, err == nil); err != nil {
		return err
	}
	if err != nil {
		return nil
	}
	return binary.Write(output, binary.BigEndian, confirmation)
}
