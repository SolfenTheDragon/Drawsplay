package main

import (
	"net"
	"testing"
	"time"

	"drawsplay/host/internal/wifilan"
)

func TestWiFiSessionConnsSendConfigOnlyAfterTabletDecoderReady(t *testing.T) {
	hostCtl, tabCtl := net.Pipe()
	hostVid, tabVid := net.Pipe()
	defer tabCtl.Close()
	defer tabVid.Close()
	limits := wifilan.SessionHello{MaxWidth: 1920, MaxHeight: 1200, MaxAccessUnit: 1 << 16}
	input, video, err := newWiFiSessionConns(hostCtl, hostVid, 5, limits, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer video.Close()

	go func() { // the input handler's reader owns the control socket
		buf := make([]byte, 64)
		for {
			if _, err := input.Read(buf); err != nil {
				return
			}
		}
	}()
	stream := bridgePacket(0, []byte(`{"codec":"h264","fps":30}`))
	for _, nal := range [][]byte{
		{0, 0, 0, 1, 0x67, 0x42, 0xc0, 0x28, 0xda, 0x02, 0x80, 0xbf, 0xe5, 0xc0, 0x44, 0, 0, 3, 0, 4, 0, 0, 3, 0, 0xf2, 0x3c, 0x60, 0xca, 0x80},
		{0, 0, 0, 1, 0x68, 0xce, 0x0f, 0xc8},
		{0, 0, 0, 1, 0x65, 0xb8, 0x60},
	} {
		stream = append(stream, bridgePacket(1, nal)...)
	}
	written := make(chan error, 1)
	go func() { _, err := video.Write(stream); written <- err }()

	tabletVideo := wifilan.NewFrameReader(tabVid)
	got := make(chan byte, 4)
	go func() {
		for {
			h, _, err := tabletVideo.ReadFrame(make([]byte, 1<<16))
			if err != nil {
				return
			}
			got <- h.Type
		}
	}()
	if typ := <-got; typ != wifilan.FrameConfig {
		t.Fatalf("first frame %v", typ)
	}
	select {
	case typ := <-got:
		t.Fatalf("AU %v sent before decoder-ready", typ)
	case <-time.After(50 * time.Millisecond):
	}
	if err := wifilan.NewFrameWriter(tabCtl).WriteFrame(
		wifilan.FrameHeader{Type: wifilan.FrameControl, Epoch: 5, Sequence: 2, PayloadLength: 4}, []byte{7, 0, 0, 1}); err != nil {
		t.Fatal(err)
	}
	if typ := <-got; typ != wifilan.FrameVideoAU {
		t.Fatalf("after ready got %v", typ)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}

func TestWiFiSessionConnsReadTabletStatsOnlyWhenNegotiated(t *testing.T) {
	hostCtl, tabCtl := net.Pipe()
	hostVid, tabVid := net.Pipe()
	defer tabCtl.Close()
	defer tabVid.Close()
	limits := wifilan.SessionHello{MaxWidth: 1920, MaxHeight: 1200, MaxAccessUnit: 1 << 16, Features: wifilan.FeatureVideoStats}
	input, video, err := newWiFiSessionConns(hostCtl, hostVid, 5, limits, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer video.Close()
	raw, _ := wifilan.EncodeVideoStats([]byte(`{"type":"stats"}`))
	go wifilan.NewFrameWriter(tabVid).WriteFrame(wifilan.FrameHeader{Type: wifilan.FrameControl, Epoch: 5, Sequence: 2, PayloadLength: uint32(len(raw))}, raw)
	buf := make([]byte, 64)
	n, err := video.Read(buf)
	if err != nil || string(buf[:n]) != "{\"type\":\"stats\"}\n" {
		t.Fatalf("read %q %v", buf[:n], err)
	}
}

func TestPairedHostOffersVideoStats(t *testing.T) {
	if wifiHostHello([32]byte{1}).Features&wifilan.FeatureVideoStats == 0 {
		t.Fatal("host hello does not offer upstream video stats")
	}
}
