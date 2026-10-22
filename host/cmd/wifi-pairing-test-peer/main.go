// Command wifi-pairing-test-peer is an offline, stdio-only interoperability
// fixture with a public test code. It never listens, enrolls or persists trust.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"

	"drawsplay/host/internal/wifilan"
)

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Wi-Fi pairing interoperability fixture:", err)
		os.Exit(1)
	}
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
