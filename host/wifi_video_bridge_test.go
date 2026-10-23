package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"drawsplay/host/internal/wifilan"
)

func bridgePacket(kind byte, payload []byte) []byte {
	out := []byte{kind, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(out[1:], uint32(len(payload)))
	return append(out, payload...)
}

type bridgeFixture struct {
	out    bytes.Buffer
	bridge *wifiVideoBridge
	ready  int
}

func newBridgeFixture(t *testing.T) *bridgeFixture {
	f := &bridgeFixture{}
	f.bridge = newWiFiVideoBridge(&f.out, 5, 640, 360, 1<<16, func() error { f.ready++; return nil })
	return f
}

func (f *bridgeFixture) frames(t *testing.T) (headers []wifilan.FrameHeader, payloads [][]byte) {
	reader := wifilan.NewFrameReader(&f.out)
	for f.out.Len() > 0 {
		h, p, err := reader.ReadFrame(make([]byte, 1<<16))
		if err != nil {
			t.Fatal(err)
		}
		headers = append(headers, h)
		payloads = append(payloads, append([]byte(nil), p...))
	}
	return
}

func TestWiFiVideoBridgeEmitsConfigOnceThenAccessUnits(t *testing.T) {
	f := newBridgeFixture(t)
	sps := []byte{0, 0, 0, 1, 0x67, 0x42, 0xc0, 0x1f, 0xda, 0x02, 0x80, 0xbf}
	pps := []byte{0, 0, 0, 1, 0x68, 0xce, 0x0f, 0xc8}
	stream := bridgePacket(0, []byte(`{"codec":"h264","fps":30}`))
	for _, nal := range [][]byte{sps, pps, {0, 0, 0, 1, 0x65, 0xb8, 0x60}, {0, 0, 0, 1, 0x41, 0x9a, 0x18}} {
		stream = append(stream, bridgePacket(1, nal)...)
	}
	// Feed in awkward chunks: packet boundaries must not matter.
	for len(stream) > 0 {
		n := min(7, len(stream))
		if w, err := f.bridge.Write(stream[:n]); err != nil || w != n {
			t.Fatalf("Write = %d, %v", w, err)
		}
		stream = stream[n:]
	}
	headers, payloads := f.frames(t)
	if len(headers) != 3 || f.ready != 1 {
		t.Fatalf("frames=%d ready=%d", len(headers), f.ready)
	}
	if headers[0].Type != wifilan.FrameConfig || headers[0].Sequence != 2 || headers[0].Epoch != 5 {
		t.Fatalf("config header %+v", headers[0])
	}
	cfg := payloads[0]
	if binary.BigEndian.Uint16(cfg) != 1 || binary.BigEndian.Uint16(cfg[2:]) != 640 ||
		binary.BigEndian.Uint16(cfg[4:]) != 360 || binary.BigEndian.Uint16(cfg[6:]) != 30 ||
		int(binary.BigEndian.Uint16(cfg[8:])) != len(sps) || int(binary.BigEndian.Uint16(cfg[10:])) != len(pps) ||
		!bytes.Equal(cfg[16:], append(append([]byte{}, sps...), pps...)) {
		t.Fatalf("config payload % x", cfg)
	}
	for i, want := range []struct {
		seq uint32
		au  []byte
	}{{3, []byte{0, 0, 0, 1, 9, 0x10, 0, 0, 0, 1, 0x65, 0xb8, 0x60}}, {4, []byte{0, 0, 0, 1, 9, 0x30, 0, 0, 0, 1, 0x41, 0x9a, 0x18}}} {
		h := headers[i+1]
		if h.Type != wifilan.FrameVideoAU || h.Sequence != want.seq || h.DurationUS != 33333 ||
			!bytes.Equal(payloads[i+1], want.au) {
			t.Fatalf("AU %d header %+v payload % x", i, h, payloads[i+1])
		}
	}
}

func TestWiFiVideoBridgeFailsClosed(t *testing.T) {
	cases := map[string][]byte{
		"picture before parameter sets": bridgePacket(1, []byte{0, 0, 0, 1, 0x65, 0xb8}),
		"unknown packet type":           bridgePacket(9, []byte{1}),
		"oversized packet":              {1, 0xff, 0xff, 0xff, 0xff},
	}
	for name, stream := range cases {
		f := newBridgeFixture(t)
		f.bridge.Write(bridgePacket(0, []byte(`{"fps":30}`)))
		if _, err := f.bridge.Write(stream); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := f.bridge.Write(bridgePacket(0, []byte(`{"fps":30}`))); err == nil {
			t.Errorf("%s: bridge kept accepting after failure", name)
		}
	}
	f := newBridgeFixture(t)
	f.bridge.ready = func() error { return errors.New("tablet never became ready") }
	f.bridge.Write(bridgePacket(0, []byte(`{"fps":30}`)))
	f.bridge.Write(bridgePacket(1, []byte{0, 0, 0, 1, 0x67, 0x42, 0xc0, 0x1f, 0xda, 0x02, 0x80, 0xbf}))
	if _, err := f.bridge.Write(bridgePacket(1, []byte{0, 0, 0, 1, 0x68, 0xce, 0x0f, 0xc8})); err == nil {
		t.Error("ready failure not surfaced")
	}
}
