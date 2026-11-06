package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"drawsplay/host/internal/wifilan"
)

type sinkFixture struct {
	out   bytes.Buffer
	sink  *wifiVideoSink
	ready int
}

func newSinkFixture() *sinkFixture {
	f := &sinkFixture{}
	f.sink = newWiFiVideoSink(&f.out, 5, 1<<16, func() error { f.ready++; return nil })
	return f
}

func (f *sinkFixture) frames(t *testing.T) (headers []wifilan.FrameHeader, payloads [][]byte) {
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

var (
	testSPS = []byte{0, 0, 0, 1, 0x67, 0x42, 0xc0, 0x28, 0xda, 0x02, 0x80, 0xbf, 0xe5, 0xc0, 0x44, 0, 0, 3, 0, 4, 0, 0, 3, 0, 0xf2, 0x3c, 0x60, 0xca, 0x80}
	testPPS = []byte{0, 0, 0, 1, 0x68, 0xce, 0x0f, 0xc8}
)

func TestWiFiVideoSinkEmitsConfigOnceThenAccessUnits(t *testing.T) {
	f := newSinkFixture()
	if err := f.sink.WritePacket(videoPacketConfig, []byte(`{"codec":"h264","fps":30}`)); err != nil {
		t.Fatal(err)
	}
	for _, nal := range [][]byte{testSPS, testPPS, {0, 0, 0, 1, 0x65, 0xb8, 0x60}, {0, 0, 0, 1, 0x41, 0x9a, 0x18}} {
		if err := f.sink.WritePacket(videoPacketNAL, nal); err != nil {
			t.Fatal(err)
		}
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
		int(binary.BigEndian.Uint16(cfg[8:])) != len(testSPS) || int(binary.BigEndian.Uint16(cfg[10:])) != len(testPPS) ||
		!bytes.Equal(cfg[16:], append(append([]byte{}, testSPS...), testPPS...)) {
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

func TestWiFiVideoSinkFailsClosed(t *testing.T) {
	cases := map[string]struct {
		kind    byte
		payload []byte
	}{
		"picture before parameter sets": {videoPacketNAL, []byte{0, 0, 0, 1, 0x65, 0xb8}},
		"unknown packet type":           {9, []byte{1}},
		"oversized packet":              {videoPacketNAL, make([]byte, 1<<16+1)},
	}
	for name, c := range cases {
		f := newSinkFixture()
		f.sink.WritePacket(videoPacketConfig, []byte(`{"fps":30}`))
		if err := f.sink.WritePacket(c.kind, c.payload); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if err := f.sink.WritePacket(videoPacketConfig, []byte(`{"fps":30}`)); err == nil {
			t.Errorf("%s: sink kept accepting after failure", name)
		}
	}
	f := newSinkFixture()
	f.sink.ready = func() error { return errors.New("tablet never became ready") }
	f.sink.WritePacket(videoPacketConfig, []byte(`{"fps":30}`))
	f.sink.WritePacket(videoPacketNAL, testSPS)
	if err := f.sink.WritePacket(videoPacketNAL, testPPS); err == nil {
		t.Error("ready failure not surfaced")
	}
}

func TestWiFiVideoSinkRejectsSPSWithUnreadableSize(t *testing.T) {
	f := newSinkFixture()
	f.sink.WritePacket(videoPacketConfig, []byte(`{"codec":"h264","fps":30}`))
	f.sink.WritePacket(videoPacketNAL, []byte{0, 0, 0, 1, 0x67, 0x42, 0xc0, 0x1f, 0xda})
	if err := f.sink.WritePacket(videoPacketNAL, testPPS); err == nil || f.out.Len() != 0 || f.ready != 0 {
		t.Fatalf("err=%v out=%d ready=%d", err, f.out.Len(), f.ready)
	}
}

func TestWiFiVideoConnFeedbackWaitsForCloseWithoutStats(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c := newWiFiVideoConn(a, 9, 1<<16, nil, false)
	res := make(chan error, 1)
	go func() { _, err := c.Feedback().Next(); res <- err }()
	select {
	case err := <-res:
		t.Fatalf("feedback returned early: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	c.Close()
	if err := <-res; err != io.EOF {
		t.Fatalf("feedback after close: %v", err)
	}
	if _, err := b.Write([]byte{1}); err == nil {
		t.Fatal("underlying conn not closed")
	}
	if _, err := c.Write([]byte{1}); err == nil {
		t.Fatal("raw write accepted")
	}
}

func TestWiFiFeedbackYieldsTabletStatsAndKeyframeRequests(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c := newWiFiVideoConn(a, 9, 1<<16, nil, true)
	defer c.Close()
	w := wifilan.NewFrameWriter(b)
	go func() {
		for i, body := range []string{`{"type":"stats","framesReceived":60}`, `{"type":"stats","framesReceived":59}`} {
			raw, _ := wifilan.EncodeVideoStats([]byte(body))
			w.WriteFrame(wifilan.FrameHeader{Type: wifilan.FrameControl, Epoch: 9, Sequence: uint32(2 + i), PayloadLength: uint32(len(raw))}, raw)
		}
		w.WriteFrame(wifilan.FrameHeader{Type: wifilan.FrameControl, Epoch: 9, Sequence: 4, PayloadLength: 4}, wifilan.KeyframeRequest[:])
	}()
	src := c.Feedback()
	for _, want := range []int64{60, 59} {
		if msg, err := src.Next(); err != nil || msg.Type != "stats" || msg.FramesReceived != want {
			t.Fatalf("got %+v (%v), want stats with %d frames", msg, err, want)
		}
	}
	if msg, err := src.Next(); err != nil || msg.Type != "keyframe" {
		t.Fatalf("got %+v (%v), want a keyframe request", msg, err)
	}
}

func TestWiFiFeedbackRejectsReplayedOrForeignUpstreamRecords(t *testing.T) {
	stats, _ := wifilan.EncodeVideoStats([]byte(`{"type":"stats"}`))
	for name, h := range map[string]wifilan.FrameHeader{
		"attach sequence replayed": {Type: wifilan.FrameControl, Epoch: 9, Sequence: 1},
		"other epoch":              {Type: wifilan.FrameControl, Epoch: 8, Sequence: 2},
		"not a control record":     {Type: wifilan.FrameInput, Epoch: 9, Sequence: 2},
	} {
		a, b := net.Pipe()
		c := newWiFiVideoConn(a, 9, 1<<16, nil, true)
		h.PayloadLength = uint32(len(stats))
		go wifilan.NewFrameWriter(b).WriteFrame(h, stats)
		if _, err := c.Feedback().Next(); err == nil || err == io.EOF {
			t.Fatalf("%s: err %v", name, err)
		}
		c.Close()
		b.Close()
	}
}
