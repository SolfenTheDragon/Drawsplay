package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMuxFrameRoundTrip(t *testing.T) {
	in := muxFrame{
		StreamID: muxStreamVideo,
		Flags:    0x2,
		Seq:      42,
		Payload:  []byte{1, 2, 3, 4, 5},
	}
	var buf bytes.Buffer
	if err := writeMuxFrame(&buf, in); err != nil {
		t.Fatalf("writeMuxFrame: %v", err)
	}
	out, err := readMuxFrame(&buf)
	if err != nil {
		t.Fatalf("readMuxFrame: %v", err)
	}
	if out.StreamID != in.StreamID || out.Flags != in.Flags || out.Seq != in.Seq {
		t.Fatalf("header mismatch: got %+v want %+v", out, in)
	}
	if !bytes.Equal(out.Payload, in.Payload) {
		t.Fatalf("payload mismatch: got %v want %v", out.Payload, in.Payload)
	}
}

func TestMuxFrameRejectsBadMagic(t *testing.T) {
	var bad bytes.Buffer
	bad.Write([]byte{0x00, 0x01, usbMuxVersion, muxStreamInput, 0x00, 0, 0, 0, 1, 0, 0, 0, 0})
	_, err := readMuxFrame(&bad)
	if err == nil || !strings.Contains(err.Error(), "invalid mux magic") {
		t.Fatalf("expected bad magic error, got: %v", err)
	}
}

func TestMuxFrameRejectsOversizedPayload(t *testing.T) {
	tooBig := bytes.Repeat([]byte{0xAA}, usbMuxMaxPayload+1)
	err := writeMuxFrame(&bytes.Buffer{}, muxFrame{
		StreamID: muxStreamVideo,
		Seq:      1,
		Payload:  tooBig,
	})
	if err == nil || !strings.Contains(err.Error(), "payload too large") {
		t.Fatalf("expected payload too large error, got: %v", err)
	}
}

// The input channel refuses a frame over its packet limit from the header
// alone, before the payload is allocated or read.
func TestMuxFrameLimitRejectsLargeInputPacketBeforeReading(t *testing.T) {
	var buf bytes.Buffer
	if err := writeMuxFrame(&buf, muxFrame{StreamID: muxStreamInput, Seq: 1, Payload: make([]byte, maxPacketBytes+1)}); err != nil {
		t.Fatalf("writeMuxFrame: %v", err)
	}
	header := buf.Bytes()[:usbMuxHeaderSize]
	_, err := readMuxFrameLimit(bytes.NewReader(header), maxPacketBytes)
	if err == nil || !strings.Contains(err.Error(), "payload too large") {
		t.Fatalf("expected payload too large error, got: %v", err)
	}
	buf.Reset()
	if err := writeMuxFrame(&buf, muxFrame{StreamID: muxStreamInput, Seq: 2, Payload: make([]byte, maxPacketBytes)}); err != nil {
		t.Fatalf("writeMuxFrame: %v", err)
	}
	if _, err := readMuxFrameLimit(&buf, maxPacketBytes); err != nil {
		t.Fatalf("packet at the limit refused: %v", err)
	}
}
