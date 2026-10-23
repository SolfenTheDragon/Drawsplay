package wifilan

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"testing"
)

const inputFixture = "0300002400000003010203040506070800000009000000000000000a0000000000000000616263"

func TestWiFiWireMatchesFixedBigEndianFixture(t *testing.T) {
	raw, err := hex.DecodeString(inputFixture)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 4096)
	reader := NewFrameReader(bytes.NewReader(raw))
	header, body, err := reader.ReadFrame(payload)
	if err != nil {
		t.Fatal(err)
	}
	if header.Type != FrameInput || header.Epoch != 0x0102030405060708 ||
		header.Sequence != 9 || header.CaptureMonoUS != 10 || string(body) != "abc" {
		t.Fatalf("incorrect decoded frame: %+v %q", header, body)
	}
	var output bytes.Buffer
	writer := NewFrameWriter(&output)
	if err := writer.WriteFrame(header, body); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), raw) {
		t.Fatalf("wire mismatch: %x", output.Bytes())
	}
}

func TestWiFiWireRejectsInvalidHeaderBeforeReadingPayload(t *testing.T) {
	for name, mutate := range map[string]func([]byte){
		"unknown type":   func(raw []byte) { raw[0] = 255 },
		"unknown flags":  func(raw []byte) { raw[1] = 1 },
		"wrong size":     func(raw []byte) { raw[3] = 35 },
		"huge payload":   func(raw []byte) { raw[4] = 255 },
		"reserved bits":  func(raw []byte) { raw[35] = 1 },
		"input duration": func(raw []byte) { raw[31] = 1 },
		"small receiver": func(raw []byte) { raw[7] = 4 },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := hex.DecodeString(inputFixture)
			mutate(raw)
			source := bytes.NewReader(raw)
			_, _, err := NewFrameReader(source).ReadFrame(make([]byte, 3))
			if err == nil || source.Len() != 3 {
				t.Fatalf("invalid header consumed payload or succeeded: remaining=%d error=%v", source.Len(), err)
			}
		})
	}
}

func TestWiFiWireTruncatedPayloadIsAnExplicitError(t *testing.T) {
	raw, _ := hex.DecodeString(inputFixture)
	_, _, err := NewFrameReader(bytes.NewReader(raw[:len(raw)-1])).ReadFrame(make([]byte, 3))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncation not reported: %v", err)
	}
}

func TestWiFiWireReadAndWriteAllocateNothingPerFrame(t *testing.T) {
	raw, _ := hex.DecodeString(inputFixture)
	source := bytes.NewReader(raw)
	reader := NewFrameReader(source)
	payload := make([]byte, 4096)
	writer := NewFrameWriter(io.Discard)
	var operationErr error
	allocations := testing.AllocsPerRun(1000, func() {
		source.Reset(raw)
		header, body, err := reader.ReadFrame(payload)
		if err != nil {
			operationErr = err
			return
		}
		operationErr = writer.WriteFrame(header, body)
	})
	if operationErr != nil || allocations != 0 {
		t.Fatalf("per-frame allocation contract failed: allocations=%v error=%v", allocations, operationErr)
	}
}

func TestWiFiSequenceGuardRejectsReplayAndPreviousEpoch(t *testing.T) {
	guard, err := NewSequenceGuard(7)
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []FrameHeader{
		{Epoch: 7, Sequence: 1}, {Epoch: 7, Sequence: 3},
	} {
		if err := guard.Accept(header); err != nil {
			t.Fatal(err)
		}
	}
	for _, header := range []FrameHeader{
		{Epoch: 7, Sequence: 3}, {Epoch: 7, Sequence: 2},
		{Epoch: 6, Sequence: 4}, {Epoch: 7, Sequence: 0},
	} {
		if err := guard.Accept(header); err == nil {
			t.Fatalf("stale frame accepted: %+v", header)
		}
	}
	if err := guard.Accept(FrameHeader{Epoch: 7, Sequence: 4}); err != nil {
		t.Fatal("rejected frame advanced valid sequence state:", err)
	}
	if _, err := NewSequenceGuard(0); err == nil {
		t.Fatal("zero session epoch accepted")
	}
}

func TestWiFiHeaderCanBeCheckedBeforePayloadBufferCheckout(t *testing.T) {
	raw, _ := hex.DecodeString(inputFixture)
	source := bytes.NewReader(raw)
	reader := NewFrameReader(source)
	header, err := reader.ReadHeader()
	if err != nil || header.PayloadLength != 3 || source.Len() != 3 {
		t.Fatalf("header consumed payload: %+v %d %v", header, source.Len(), err)
	}
	if _, err := reader.ReadHeader(); err == nil {
		t.Fatal("reading another header discarded pending payload")
	}
	if _, err := reader.ReadPayload(make([]byte, 2)); err == nil || source.Len() != 3 {
		t.Fatal("small buffer consumed pending payload")
	}
	body, err := reader.ReadPayload(make([]byte, 3))
	if err != nil || string(body) != "abc" {
		t.Fatalf("payload not returned into caller buffer: %q %v", body, err)
	}
	if _, err := reader.ReadPayload(make([]byte, 3)); err == nil {
		t.Fatal("repeated payload read accepted")
	}
}

func TestWiFiSequenceRequiresNewSessionAfterUint32Limit(t *testing.T) {
	guard, err := NewSequenceGuard(7)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Accept(FrameHeader{Epoch: 7, Sequence: ^uint32(0)}); err != nil {
		t.Fatal(err)
	}
	if err := guard.Accept(FrameHeader{Epoch: 7, Sequence: 1}); err == nil {
		t.Fatal("wrapped sequence accepted inside the same epoch")
	}
}

func FuzzWiFiEnvelope(f *testing.F) {
	raw, err := hex.DecodeString(inputFixture)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		source := bytes.NewReader(data)
		header, err := NewFrameReader(source).ReadHeader()
		if err != nil {
			return
		}
		if source.Len() != len(data)-36 || header.PayloadLength == 0 || header.PayloadLength > 1024*1024 {
			t.Fatalf("header accepted unsafe length or read payload: %+v", header)
		}
	})
}

type shortFrameWriter struct {
	bytes.Buffer
	stall bool
}

func (w *shortFrameWriter) Write(data []byte) (int, error) {
	if w.stall {
		return 0, nil
	}
	return w.Buffer.Write(data[:min(2, len(data))])
}

func TestWiFiFrameWriterHandlesShortWritesAndRejectsStalledWriter(t *testing.T) {
	raw, _ := hex.DecodeString(inputFixture)
	header, body, err := NewFrameReader(bytes.NewReader(raw)).ReadFrame(make([]byte, 3))
	if err != nil {
		t.Fatal(err)
	}
	output := new(shortFrameWriter)
	if err := NewFrameWriter(output).WriteFrame(header, body); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), raw) {
		t.Fatal("partial writes lost frame bytes")
	}
	if err := NewFrameWriter(&shortFrameWriter{stall: true}).WriteFrame(header, body); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("stalled writer must fail, got %v", err)
	}
}
