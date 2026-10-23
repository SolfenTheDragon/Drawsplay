package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// These isolate framing cost in memory; they do not measure USB hardware latency.
func BenchmarkUSBBaselineMuxRead(b *testing.B) {
	for _, size := range []int{128, 512, 16000} {
		b.Run(fmt.Sprintf("payload_%d", size), func(b *testing.B) {
			payload := bytes.Repeat([]byte{0x5a}, size)
			var wire bytes.Buffer
			if err := writeMuxFrame(&wire, muxFrame{
				StreamID: muxStreamInput, Seq: 42, Payload: payload,
			}); err != nil {
				b.Fatal(err)
			}
			var reader bytes.Reader
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				reader.Reset(wire.Bytes())
				frame, err := readMuxFrame(&reader)
				if err != nil {
					b.Fatal(err)
				}
				if frame.StreamID != muxStreamInput || frame.Seq != 42 ||
					len(frame.Payload) != size || frame.Payload[size-1] != 0x5a {
					b.Fatal("mux frame differs from fixture")
				}
			}
		})
	}
}

func BenchmarkUSBBaselineMuxWrite(b *testing.B) {
	for _, size := range []int{128, 512, 16000} {
		b.Run(fmt.Sprintf("payload_%d", size), func(b *testing.B) {
			frame := muxFrame{
				StreamID: muxStreamInput, Seq: 42,
				Payload: bytes.Repeat([]byte{0x5a}, size),
			}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := writeMuxFrame(io.Discard, frame); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkUSBBaselineAOARead(b *testing.B) {
	for _, size := range []int{128, 512, 16000} {
		b.Run(fmt.Sprintf("payload_%d", size), func(b *testing.B) {
			wire := make([]byte, aoaFrameHeader+size)
			wire[0] = aoaFrameData
			binary.BigEndian.PutUint16(wire[1:3], 7)
			binary.BigEndian.PutUint16(wire[3:5], uint16(size))
			for i := aoaFrameHeader; i < len(wire); i++ {
				wire[i] = 0x5a
			}
			var reader bytes.Reader
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				reader.Reset(wire)
				typ, id, payload, err := readAOAFrame(&reader)
				if err != nil {
					b.Fatal(err)
				}
				if typ != aoaFrameData || id != 7 ||
					len(payload) != size || payload[size-1] != 0x5a {
					b.Fatal("AOA frame differs from fixture")
				}
			}
		})
	}
}

func TestUSBBaselineAOAReconnectCycles(t *testing.T) {
	for cycle := 0; cycle < 100; cycle++ {
		t.Run(fmt.Sprintf("cycle_%03d", cycle), func(t *testing.T) {
			_, tb, opened, done := startLink(t)
			t.Cleanup(func() { _ = tb.c.Close() })
			if err := tb.c.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			tb.send(t, aoaFrameOpen, 7, []byte{aoaKindInput})
			var conn *aoaConn
			select {
			case conn = <-opened:
			case <-time.After(3 * time.Second):
				t.Fatal("input connection was not opened")
			}
			pending := make(chan error, 1)
			go func() {
				_, err := conn.Read(make([]byte, 1))
				pending <- err
			}()
			if err := tb.c.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-pending:
				if err == nil {
					t.Fatal("pending read succeeded after link loss")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("pending input read leaked after disconnect")
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("link loss returned success")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("link worker leaked after disconnect")
			}
		})
	}
}

// This test-only echo traverses the real mux and AOA link over net.Pipe.
// It excludes USB hardware, Android scheduling and host OS input injection.
func BenchmarkUSBBaselineInputEcho(b *testing.B) {
	host, device := net.Pipe()
	if err := device.SetDeadline(time.Now().Add(5 * time.Minute)); err != nil {
		b.Fatal(err)
	}
	link := newAOALink(host)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	handlerDone := make(chan error, 1)
	go func() {
		done <- link.serve(ctx, func(kind byte, conn *aoaConn) {
			go func() {
				for {
					frame, err := readMuxFrame(conn)
					if err != nil {
						handlerDone <- err
						return
					}
					if err := writeMuxFrame(conn, frame); err != nil {
						handlerDone <- err
						return
					}
				}
			}()
		})
	}()
	defer func() {
		cancel()
		_ = device.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			b.Error("link worker did not stop")
		}
		select {
		case err := <-handlerDone:
			if err == nil {
				b.Error("input echo stopped without error")
			}
		case <-time.After(3 * time.Second):
			b.Error("input echo worker did not stop")
		}
	}()

	send := func(typ byte, id uint16, payload []byte) {
		b.Helper()
		var header [5]byte
		header[0] = typ
		binary.BigEndian.PutUint16(header[1:3], id)
		binary.BigEndian.PutUint16(header[3:5], uint16(len(payload)))
		if _, err := device.Write(header[:]); err != nil {
			b.Fatal(err)
		}
		if len(payload) > 0 {
			if _, err := device.Write(payload); err != nil {
				b.Fatal(err)
			}
		}
	}
	send(aoaFrameHello, 0, aoaHelloPayload)
	typ, _, hello, err := readAOAFrame(device)
	if err != nil || typ != aoaFrameHello || !bytes.Equal(hello, aoaHelloPayload) {
		b.Fatalf("hello failed: type=%d err=%v", typ, err)
	}
	send(aoaFrameOpen, 7, []byte{aoaKindInput})
	var wire bytes.Buffer
	if err := writeMuxFrame(&wire, muxFrame{
		StreamID: muxStreamInput, Seq: 42, Payload: []byte(`{"type":"benchmarkEcho"}`),
	}); err != nil {
		b.Fatal(err)
	}
	expected := wire.Bytes()
	response := make([]byte, len(expected))
	b.ReportAllocs()
	b.SetBytes(int64(len(expected)))
	b.ResetTimer()
	for b.Loop() {
		send(aoaFrameData, 7, expected)
		n := 0
		for n < len(response) {
			typ, id, payload, err := readAOAFrame(device)
			if err != nil {
				b.Fatal(err)
			}
			if typ != aoaFrameData || id != 7 || len(payload) > len(response)-n {
				b.Fatal("unexpected echo frame")
			}
			n += copy(response[n:], payload)
		}
		if !bytes.Equal(response, expected) {
			b.Fatal("echo changed input bytes")
		}
	}
}

func TestUSBBaselineAOARejectsOversizedFrameBeforePayloadRead(t *testing.T) {
	header := []byte{aoaFrameData, 0, 7, 0, 0}
	binary.BigEndian.PutUint16(header[3:5], aoaMaxFramePayload+1)
	_, _, payload, err := readAOAFrame(bytes.NewReader(header))
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("oversized length must be rejected before reading payload: %v", err)
	}
	if payload != nil {
		t.Fatal("oversized frame allocated a payload")
	}
}
