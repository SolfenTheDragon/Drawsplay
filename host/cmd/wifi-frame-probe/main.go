package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"drawsplay/host/internal/wifilan"
	"github.com/grandcat/zeroconf"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Wi-Fi framing probe:", err)
		os.Exit(1)
	}
}

func run() error {
	iface := flag.String("interface", "", "selected physical LAN interface (required)")
	tablet := flag.String("tablet", "", "numeric tablet IPv4 (required)")
	port := flag.Int("port", 0, "isolated tablet probe port")
	certFile := flag.String("cert", "", "temporary PEM test certificate")
	keyFile := flag.String("key", "", "temporary PEM test key")
	flag.Parse()
	_, prefixes, err := wifilan.SelectInterface(*iface)
	if err != nil {
		return err
	}
	address := net.ParseIP(*tablet)
	if address == nil || address.To4() == nil {
		return fmt.Errorf("require a numeric tablet IPv4 address")
	}
	entry := zeroconf.NewServiceEntry("Synthetic Wi-Fi frame probe", wifilan.TabletService, "local.")
	entry.Port, entry.TTL = *port, 1
	videoPort := 37184
	if *port == videoPort {
		videoPort++
	}
	entry.Text = []string{"v=2", fmt.Sprintf("videoPort=%d", videoPort)}
	entry.AddrIPv4 = []net.IP{address}
	candidate, err := wifilan.ParseCandidate(entry, prefixes)
	if err != nil {
		return err
	}
	var local net.IP
	for _, prefix := range prefixes {
		if prefix.Contains(candidate.Addresses[0]) {
			local = net.ParseIP(prefix.Addr().String())
			break
		}
	}
	certificate, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		return fmt.Errorf("load temporary probe identity: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dialer := net.Dialer{Timeout: 5 * time.Second, LocalAddr: &net.TCPAddr{IP: local}}
	raw, err := dialer.DialContext(ctx, "tcp4", net.JoinHostPort(candidate.Addresses[0].String(), fmt.Sprint(*port)))
	if err != nil {
		return err
	}
	defer raw.Close()
	if err := raw.SetDeadline(time.Now().Add(90 * time.Second)); err != nil {
		return err
	}
	connection := tls.Server(raw, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}})
	if err := connection.HandshakeContext(ctx); err != nil {
		return err
	}
	var epochBytes [8]byte
	if _, err := rand.Read(epochBytes[:]); err != nil {
		return err
	}
	epoch := binary.BigEndian.Uint64(epochBytes[:])
	if epoch == 0 {
		return fmt.Errorf("random epoch unexpectedly zero; rerun probe")
	}
	reader := wifilan.NewFrameReader(connection)
	writer := wifilan.NewFrameWriter(connection)
	guard, err := wifilan.NewSequenceGuard(epoch)
	if err != nil {
		return err
	}
	payload := make([]byte, wifilan.MaxAccessUnit)
	sizes := [...]int{4096, 65536, wifilan.MaxAccessUnit}
	total := 0
	start := time.Now()
	for sequence := uint32(1); sequence <= 9; sequence++ {
		size := sizes[(sequence-1)%3]
		for index := range size {
			payload[index] = byte(index*31 + int(sequence))
		}
		header := wifilan.FrameHeader{
			Type: wifilan.FrameVideoAU, PayloadLength: uint32(size),
			Epoch: epoch, Sequence: sequence, CaptureMonoUS: uint64(time.Since(start).Microseconds()),
			DurationUS: 16667,
		}
		if err := writer.WriteFrame(header, payload[:size]); err != nil {
			return err
		}
		echo, err := reader.ReadHeader()
		if err != nil {
			return err
		}
		if echo != header {
			return fmt.Errorf("echo header mismatch at sequence %d", sequence)
		}
		if err := guard.Accept(echo); err != nil {
			return err
		}
		body, err := reader.ReadPayload(payload)
		if err != nil {
			return err
		}
		for index, value := range body {
			if value != byte(index*31+int(sequence)) {
				return fmt.Errorf("echo payload mismatch at sequence %d offset %d", sequence, index)
			}
		}
		total += size
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Frames      int    `json:"verifiedFrames"`
		Payload     int    `json:"verifiedPayloadBytesEachDirection"`
		BufferBytes int    `json:"payloadBufferBytesPerEndpoint"`
		TCPStarter  string `json:"tcpInitiator"`
		TLSServer   string `json:"tlsServer"`
	}{9, total, len(payload), "host", "host"})
}
