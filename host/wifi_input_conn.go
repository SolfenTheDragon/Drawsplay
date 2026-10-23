package main

import (
	"errors"
	"net"
	"sync"

	"drawsplay/host/internal/wifilan"
)

var errWiFiInputFrame = errors.New("unexpected Wi-Fi input frame type, epoch or sequence; reconnect")

// wifiInputConn exposes the paired Wi-Fi control socket as the plain byte
// stream the existing input handler expects. Tablet-to-host bytes arrive in
// FrameInput payloads and host-to-tablet bytes leave in FrameInput payloads,
// each direction with its own authenticated epoch/sequence space. Reads have
// one owner; writes are serialized.
type wifiInputConn struct {
	net.Conn
	reader  *wifilan.FrameReader
	guard   *wifilan.SequenceGuard
	epoch   uint64
	pending []byte
	buffer  []byte

	writeMu  sync.Mutex
	writer   *wifilan.FrameWriter
	sequence uint32
}

// newWiFiInputConn takes the guard already used for earlier control records
// and the next unused host-to-tablet control sequence.
func newWiFiInputConn(conn net.Conn, epoch uint64, guard *wifilan.SequenceGuard, nextSequence uint32) *wifiInputConn {
	return &wifiInputConn{
		Conn: conn, reader: wifilan.NewFrameReader(conn), guard: guard, epoch: epoch,
		buffer: make([]byte, wifilan.MaxControlSize), writer: wifilan.NewFrameWriter(conn), sequence: nextSequence,
	}
}

func (c *wifiInputConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(c.pending) == 0 {
		header, err := c.reader.ReadHeader()
		if err != nil {
			return 0, err
		}
		if header.Type != wifilan.FrameInput || header.Epoch != c.epoch {
			return 0, errWiFiInputFrame
		}
		if err := c.guard.Accept(header); err != nil {
			return 0, err
		}
		body, err := c.reader.ReadPayload(c.buffer)
		if err != nil {
			return 0, err
		}
		c.pending = body
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *wifiInputConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > wifilan.MaxControlSize {
			chunk = chunk[:wifilan.MaxControlSize]
		}
		header := wifilan.FrameHeader{Type: wifilan.FrameInput, Epoch: c.epoch, Sequence: c.sequence, PayloadLength: uint32(len(chunk))}
		if err := c.writer.WriteFrame(header, chunk); err != nil {
			return written, err
		}
		c.sequence++
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}
