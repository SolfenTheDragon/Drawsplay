package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"testing"
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
