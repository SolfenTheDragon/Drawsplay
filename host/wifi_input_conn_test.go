package main

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"drawsplay/host/internal/wifilan"
)

func newInputPair(t *testing.T) (*wifiInputConn, net.Conn, *wifilan.FrameWriter, *wifilan.FrameReader) {
	host, tablet := net.Pipe()
	t.Cleanup(func() { host.Close(); tablet.Close() })
	guard, err := wifilan.NewSequenceGuard(9)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Accept(wifilan.FrameHeader{Epoch: 9, Sequence: 3}); err != nil { // earlier control records
		t.Fatal(err)
	}
	conn := newWiFiInputConn(host, 9, guard, 4)
	return conn, tablet, wifilan.NewFrameWriter(tablet), wifilan.NewFrameReader(tablet)
}

func TestWiFiInputConnReadsInputFramePayloadsAsStream(t *testing.T) {
	conn, tablet, w, _ := newInputPair(t)
	go func() {
		w.WriteFrame(wifilan.FrameHeader{Type: wifilan.FrameInput, Epoch: 9, Sequence: 10, PayloadLength: 3}, []byte("abc"))
		w.WriteFrame(wifilan.FrameHeader{Type: wifilan.FrameInput, Epoch: 9, Sequence: 11, PayloadLength: 3}, []byte("def"))
		tablet.Close()
	}()
	got, err := io.ReadAll(io.LimitReader(conn, 6))
	if err != nil || string(got) != "abcdef" {
		t.Fatalf("read %q, %v", got, err)
	}
}

func TestWiFiInputConnWritesBoundedInputFrames(t *testing.T) {
	conn, _, _, r := newInputPair(t)
	payload := bytes.Repeat([]byte{'x'}, wifilan.MaxControlSize+10)
	done := make(chan error, 1)
	go func() { _, err := conn.Write(payload); done <- err }()
	buf := make([]byte, wifilan.MaxControlSize)
	var total, wantSeq = 0, uint32(4)
	for total < len(payload) {
		h, p, err := r.ReadFrame(buf)
		if err != nil || h.Type != wifilan.FrameInput || h.Epoch != 9 || h.Sequence != wantSeq {
			t.Fatalf("frame %+v err=%v", h, err)
		}
		total += len(p)
		wantSeq++
	}
	if err := <-done; err != nil || total != len(payload) {
		t.Fatalf("write err=%v total=%d", err, total)
	}
}

func TestWiFiInputConnRejectsBadFrames(t *testing.T) {
	for name, h := range map[string]wifilan.FrameHeader{
		"wrong epoch":    {Type: wifilan.FrameInput, Epoch: 8, Sequence: 10, PayloadLength: 1},
		"stale sequence": {Type: wifilan.FrameInput, Epoch: 9, Sequence: 2, PayloadLength: 1},
		"wrong type":     {Type: wifilan.FrameControl, Epoch: 9, Sequence: 10, PayloadLength: 1},
	} {
		conn, _, w, _ := newInputPair(t)
		go w.WriteFrame(h, []byte("a"))
		conn.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := conn.Read(make([]byte, 8)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestWiFiInputConnRoutesControlFramesToHandler(t *testing.T) {
	conn, _, w, _ := newInputPair(t)
	var seen [][]byte
	conn.SetControlHandler(func(h wifilan.FrameHeader, payload []byte) error {
		seen = append(seen, append([]byte(nil), payload...))
		return nil
	})
	go func() {
		w.WriteFrame(wifilan.FrameHeader{Type: wifilan.FrameControl, Epoch: 9, Sequence: 10, PayloadLength: 2}, []byte{7, 0})
		w.WriteFrame(wifilan.FrameHeader{Type: wifilan.FrameInput, Epoch: 9, Sequence: 11, PayloadLength: 1}, []byte("z"))
	}()
	buf := make([]byte, 4)
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "z" || len(seen) != 1 || seen[0][0] != 7 {
		t.Fatalf("n=%d err=%v seen=%v", n, err, seen)
	}
}

func TestWiFiDecoderGateOpensOnlyOnReadyRecord(t *testing.T) {
	g := newWiFiDecoderGate()
	if err := g.Handle(wifilan.FrameHeader{}, []byte{9, 9, 9, 9}); err == nil {
		t.Fatal("bad record accepted")
	}
	if err := g.Wait(time.Millisecond); err == nil {
		t.Fatal("gate opened without ready")
	}
	if err := g.Handle(wifilan.FrameHeader{}, []byte{7, 0, 0, 1}); err != nil {
		t.Fatal(err)
	}
	if err := g.Wait(time.Second); err != nil {
		t.Fatal(err)
	}
}
