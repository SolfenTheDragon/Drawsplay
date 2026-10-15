package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

type fakeAOADevice struct {
	protocol   uint16
	failString int
	controls   []fakeControl
	pipe       aoaPipe
}

type fakeControl struct {
	reqType, request byte
	index            uint16
	data             string
}

func (d *fakeAOADevice) Control(reqType, request byte, value, index uint16, data []byte) (int, error) {
	if request == aoaRequestGetProtocol {
		binary.LittleEndian.PutUint16(data, d.protocol)
		d.controls = append(d.controls, fakeControl{reqType, request, index, ""})
		return 2, nil
	}
	if request == aoaRequestSendString && d.failString >= 0 && int(index) == d.failString {
		return 0, errors.New("stall")
	}
	d.controls = append(d.controls, fakeControl{reqType, request, index, string(bytes.TrimRight(data, "\x00"))})
	return len(data), nil
}
func (d *fakeAOADevice) OpenPipe() (aoaPipe, error) { return d.pipe, nil }
func (d *fakeAOADevice) Close() error               { return nil }

func TestAOASwitchSendsIdentificationThenStart(t *testing.T) {
	dev := &fakeAOADevice{protocol: 2, failString: -1}
	if err := aoaSwitchToAccessory(dev); err != nil {
		t.Fatal(err)
	}
	if dev.controls[0].request != aoaRequestGetProtocol || dev.controls[0].reqType != aoaReqTypeIn {
		t.Fatalf("first control = %+v", dev.controls[0])
	}
	got := map[uint16]string{}
	for _, c := range dev.controls[1 : len(dev.controls)-1] {
		if c.request != aoaRequestSendString || c.reqType != aoaReqTypeOut {
			t.Fatalf("unexpected control %+v", c)
		}
		got[c.index] = c.data
	}
	if got[aoaStringManufacturer] != "DrawSplay" || got[aoaStringModel] != "Tablet" {
		t.Fatalf("identification strings = %v", got)
	}
	if _, sentVersion := got[aoaStringVersion]; sentVersion {
		t.Fatal("version string must be omitted so the Android filter matches manufacturer+model only")
	}
	last := dev.controls[len(dev.controls)-1]
	if last.request != aoaRequestStart {
		t.Fatalf("last control = %+v", last)
	}
}

func TestAOASwitchRejectsUnsupportedAndStalledDevices(t *testing.T) {
	if err := aoaSwitchToAccessory(&fakeAOADevice{protocol: 0, failString: -1}); !errors.Is(err, errAOANotSupported) {
		t.Fatalf("protocol 0 err = %v", err)
	}
	if err := aoaSwitchToAccessory(&fakeAOADevice{protocol: 1, failString: aoaStringModel}); err == nil {
		t.Fatal("expected error when a string transfer stalls")
	}
}

func TestParseAOAInterfacePrefersAccessoryOverADB(t *testing.T) {
	cfg := []byte{9, 2, 0, 0, 2, 1, 0, 0x80, 250}
	iface := func(n, sub, proto byte) []byte { return []byte{9, 4, n, 0, 2, 0xff, sub, proto, 0} }
	ep := func(addr byte, mps uint16) []byte { return []byte{7, 5, addr, 2, byte(mps), byte(mps >> 8), 0} }
	// ADB interface first on purpose: it must be skipped.
	var d []byte
	d = append(d, cfg...)
	d = append(d, iface(0, 0x42, 0x01)...)
	d = append(d, ep(0x81, 512)...)
	d = append(d, ep(0x01, 512)...)
	d = append(d, iface(1, 0xff, 0x00)...)
	d = append(d, ep(0x82, 512)...)
	d = append(d, ep(0x02, 1024)...)
	got, err := parseAOAInterface(d)
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 1 || got.EpIn != 0x82 || got.EpOut != 0x02 || got.MaxPacketOut != 1024 || got.ConfigurationValue != 1 {
		t.Fatalf("iface = %+v", got)
	}
	if _, err := parseAOAInterface(append(append([]byte{}, cfg...), iface(0, 0x42, 0x01)...)); err == nil {
		t.Fatal("ADB-only descriptors must not yield an accessory interface")
	}
}

func TestAOAWriteNeedsZLP(t *testing.T) {
	cases := []struct {
		n, mps int
		want   bool
	}{{512, 512, true}, {1024, 512, true}, {513, 512, false}, {0, 512, false}, {64, 0, false}}
	for _, c := range cases {
		if got := aoaWriteNeedsZLP(c.n, c.mps); got != c.want {
			t.Errorf("aoaWriteNeedsZLP(%d,%d)=%v", c.n, c.mps, got)
		}
	}
}

func TestAOAVendorProbeAllowed(t *testing.T) {
	t.Setenv(aoaExtraVendorsEnv, "0xBEEF, 1234")
	for _, vid := range []uint16{0x04e8, 0xbeef, 0x1234} {
		if !aoaVendorProbeAllowed(vid) {
			t.Errorf("vendor %04x should be probed", vid)
		}
	}
	if aoaVendorProbeAllowed(0x046d) {
		t.Error("unrelated vendor must not be probed")
	}
}

// tablet drives the tablet side of the link protocol over a net.Pipe.
type tablet struct{ c net.Conn }

func (tb tablet) send(t *testing.T, typ byte, id uint16, payload []byte) {
	t.Helper()
	buf := make([]byte, aoaFrameHeader+len(payload))
	buf[0] = typ
	binary.BigEndian.PutUint16(buf[1:3], id)
	binary.BigEndian.PutUint16(buf[3:5], uint16(len(payload)))
	copy(buf[aoaFrameHeader:], payload)
	if _, err := tb.c.Write(buf); err != nil {
		t.Fatal(err)
	}
}

func (tb tablet) recv(t *testing.T) (byte, uint16, []byte) {
	t.Helper()
	_ = tb.c.SetReadDeadline(time.Now().Add(3 * time.Second))
	typ, id, p, err := readAOAFrame(tb.c)
	if err != nil {
		t.Fatal(err)
	}
	return typ, id, p
}

func startLink(t *testing.T) (*aoaLink, tablet, chan *aoaConn, chan error) {
	t.Helper()
	hostSide, tabSide := net.Pipe()
	link := newAOALink(hostSide)
	opened := make(chan *aoaConn, 4)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		done <- link.serve(ctx, func(kind byte, c *aoaConn) { opened <- c })
	}()
	tb := tablet{tabSide}
	tb.send(t, aoaFrameHello, 0, aoaHelloPayload)
	if typ, _, p := tb.recv(t); typ != aoaFrameHello || !bytes.Equal(p, aoaHelloPayload) {
		t.Fatalf("host hello = %d %q", typ, p)
	}
	return link, tb, opened, done
}

func TestAOALinkRoundTripAndClose(t *testing.T) {
	_, tb, opened, _ := startLink(t)
	tb.send(t, aoaFrameOpen, 7, []byte{aoaKindInput})
	conn := <-opened
	tb.send(t, aoaFrameData, 7, []byte("hello"))

	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "hello" {
		t.Fatalf("read = %q, %v", buf[:n], err)
	}

	go conn.Write([]byte("pong"))
	typ, id, p := tb.recv(t)
	if typ != aoaFrameData || id != 7 || string(p) != "pong" {
		t.Fatalf("frame = %d %d %q", typ, id, p)
	}

	tb.send(t, aoaFrameClose, 7, nil)
	if _, err := conn.Read(buf); err != io.EOF {
		t.Fatalf("read after remote close = %v", err)
	}
}

func TestAOAConnLargeWriteIsChunked(t *testing.T) {
	_, tb, opened, _ := startLink(t)
	tb.send(t, aoaFrameOpen, 1, []byte{aoaKindVideo})
	conn := <-opened
	payload := bytes.Repeat([]byte{0xAB}, 3*aoaMaxFramePayload+17)
	go conn.Write(payload)
	var got []byte
	for len(got) < len(payload) {
		typ, _, p := tb.recv(t)
		if typ != aoaFrameData || len(p) > aoaMaxFramePayload {
			t.Fatalf("bad frame type=%d len=%d", typ, len(p))
		}
		got = append(got, p...)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("payload corrupted")
	}
}

func TestAOAConnReadDeadline(t *testing.T) {
	_, tb, opened, _ := startLink(t)
	tb.send(t, aoaFrameOpen, 2, []byte{aoaKindInput})
	conn := <-opened
	_ = conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	start := time.Now()
	_, err := conn.Read(make([]byte, 1))
	if !errors.Is(err, os.ErrDeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
	_ = conn.SetReadDeadline(time.Time{})
}

func TestAOAConnLocalCloseNotifiesTablet(t *testing.T) {
	_, tb, opened, _ := startLink(t)
	tb.send(t, aoaFrameOpen, 3, []byte{aoaKindInput})
	conn := <-opened
	go conn.Close()
	if typ, id, _ := tb.recv(t); typ != aoaFrameClose || id != 3 {
		t.Fatalf("expected CLOSE for 3, got %d %d", typ, id)
	}
}

func TestAOALinkEndsWhenPipeDrops(t *testing.T) {
	_, tb, opened, done := startLink(t)
	tb.send(t, aoaFrameOpen, 4, []byte{aoaKindInput})
	conn := <-opened
	tb.c.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("serve should report the pipe failure")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not return")
	}
	if _, err := conn.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("conn read after link loss = %v", err)
	}
}

func TestAOALinkRejectsBadHello(t *testing.T) {
	hostSide, tabSide := net.Pipe()
	link := newAOALink(hostSide)
	done := make(chan error, 1)
	go func() { done <- link.serve(context.Background(), func(byte, *aoaConn) {}) }()
	tablet{tabSide}.send(t, aoaFrameHello, 0, []byte("NOTDS!"))
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected hello error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not reject the hello")
	}
}

func TestAOAPollOnceSwitchesThenServes(t *testing.T) {
	switched := &fakeAOADevice{protocol: 2, failString: -1}
	lastSwitch := map[string]time.Time{}
	plain := aoaCandidate{VID: 0x04e8, PID: 0x6860, Key: "1-2@5", Open: func() (aoaDeviceHandle, error) { return switched, nil }}
	scan := func() ([]aoaCandidate, error) { return []aoaCandidate{plain}, nil }

	if served := aoaPollOnce(context.Background(), scan, lastSwitch, func(byte, *aoaConn) {}); served {
		t.Fatal("no link expected before the device re-enumerates")
	}
	if len(switched.controls) == 0 || switched.controls[len(switched.controls)-1].request != aoaRequestStart {
		t.Fatalf("expected the switch sequence, got %+v", switched.controls)
	}
	n := len(switched.controls)
	aoaPollOnce(context.Background(), scan, lastSwitch, func(byte, *aoaConn) {})
	if len(switched.controls) != n {
		t.Fatal("a device must not be re-probed within the backoff window")
	}

	hostSide, tabSide := net.Pipe()
	acc := aoaCandidate{VID: aoaGoogleVID, PID: 0x2d00, Key: "1-2@6", Open: func() (aoaDeviceHandle, error) {
		return &fakeAOADevice{pipe: hostSide, failString: -1}, nil
	}}
	opened := make(chan byte, 1)
	go func() {
		tb := tablet{tabSide}
		tb.send(t, aoaFrameHello, 0, aoaHelloPayload)
		tb.recv(t)
		tb.send(t, aoaFrameOpen, 1, []byte{aoaKindVideo})
		time.Sleep(50 * time.Millisecond)
		tabSide.Close()
	}()
	served := aoaPollOnce(context.Background(), func() ([]aoaCandidate, error) { return []aoaCandidate{acc}, nil }, lastSwitch,
		func(kind byte, c *aoaConn) { opened <- kind })
	if !served {
		t.Fatal("expected the accessory link to be served")
	}
	if kind := <-opened; kind != aoaKindVideo {
		t.Fatalf("kind = %d", kind)
	}
}
