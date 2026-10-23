package main

import (
	"bufio"
	"io"
	"net"
	"testing"
	"time"

	"drawsplay/host/internal/wifilan"
)

func TestWiFiVideoConnWritesGoThroughBridgeAndReadBlocksUntilClose(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	var got []byte
	sink := writerFunc(func(p []byte) (int, error) { got = append(got, p...); return len(p), nil })
	c := newWiFiVideoConn(a, sink)
	if n, err := c.Write([]byte{1, 2, 3}); err != nil || n != 3 || string(got) != "\x01\x02\x03" {
		t.Fatalf("write %d %v %v", n, err, got)
	}
	res := make(chan error, 1)
	go func() { _, err := c.Read(make([]byte, 4)); res <- err }()
	select {
	case err := <-res:
		t.Fatalf("read returned early: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	c.Close()
	if err := <-res; err != io.EOF {
		t.Fatalf("read after close: %v", err)
	}
	if _, err := b.Write([]byte{1}); err == nil {
		t.Fatal("underlying conn not closed")
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestWiFiVideoConnYieldsNegotiatedTabletStatsAsJSONLines(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c := newWiFiVideoConnWithStats(a, io.Discard, 9)
	defer c.Close()
	w := wifilan.NewFrameWriter(b)
	send := func(seq uint32, body string) {
		raw, err := wifilan.EncodeVideoStats([]byte(body))
		if err != nil {
			t.Error(err)
			return
		}
		if err := w.WriteFrame(wifilan.FrameHeader{Type: wifilan.FrameControl, Epoch: 9, Sequence: seq, PayloadLength: uint32(len(raw))}, raw); err != nil {
			t.Error(err)
		}
	}
	go func() {
		send(2, `{"type":"stats","framesReceived":60}`)
		send(3, `{"type":"stats","framesReceived":59}`)
	}()
	scanner := bufio.NewScanner(c)
	for _, want := range []string{`{"type":"stats","framesReceived":60}`, `{"type":"stats","framesReceived":59}`} {
		if !scanner.Scan() || scanner.Text() != want {
			t.Fatalf("got %q (%v), want %q", scanner.Text(), scanner.Err(), want)
		}
	}
}

func TestWiFiVideoConnRejectsReplayedOrForeignUpstreamRecords(t *testing.T) {
	stats, _ := wifilan.EncodeVideoStats([]byte(`{"type":"stats"}`))
	for name, h := range map[string]wifilan.FrameHeader{
		"attach sequence replayed": {Type: wifilan.FrameControl, Epoch: 9, Sequence: 1},
		"other epoch":              {Type: wifilan.FrameControl, Epoch: 8, Sequence: 2},
		"not a control record":     {Type: wifilan.FrameInput, Epoch: 9, Sequence: 2},
	} {
		a, b := net.Pipe()
		c := newWiFiVideoConnWithStats(a, io.Discard, 9)
		h.PayloadLength = uint32(len(stats))
		go wifilan.NewFrameWriter(b).WriteFrame(h, stats)
		if _, err := c.Read(make([]byte, 128)); err == nil || err == io.EOF {
			t.Fatalf("%s: read err %v", name, err)
		}
		c.Close()
		b.Close()
	}
}

func TestWiFiVideoConnTurnsAKeyframeRequestIntoAKeyframeLine(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c := newWiFiVideoConnWithStats(a, io.Discard, 9)
	defer c.Close()
	go wifilan.NewFrameWriter(b).WriteFrame(wifilan.FrameHeader{Type: wifilan.FrameControl, Epoch: 9, Sequence: 2, PayloadLength: 4}, wifilan.KeyframeRequest[:])
	scanner := bufio.NewScanner(c)
	if !scanner.Scan() || scanner.Text() != `{"type":"keyframe"}` {
		t.Fatalf("got %q (%v)", scanner.Text(), scanner.Err())
	}
}
