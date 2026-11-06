package main

import (
	"encoding/json"
	"testing"
)

func TestCodecNegotiationScaffoldKeepsH264(t *testing.T) {
	for _, offered := range [][]string{nil, {"h264"}, {"future-codec"}} {
		if got := negotiateVideoCodec([]string{"h264"}, offered); got != "h264" {
			t.Errorf("offered %v selected %q, want h264", offered, got)
		}
	}
}

func TestCodecNegotiationPrefersTheHostOrder(t *testing.T) {
	if got := negotiateVideoCodec([]string{"hevc", "h264"}, []string{"h264", "hevc"}); got != "hevc" {
		t.Fatalf("selected %q, want hevc", got)
	}
	if got := newClientSession(packet{VideoCodecs: []string{" H265 ", "H264"}}).Codecs; len(got) != 2 || got[0] != "hevc" || got[1] != "h264" {
		t.Fatalf("hello codecs normalized to %v, want [hevc h264]", got)
	}
}

func TestPacketsDecodeTheKeysTheTabletSends(t *testing.T) {
	var in packet
	if err := json.Unmarshal([]byte(`{"t":"input","a":"down","x":1,"y":2,"p":0.5,"ti":0.3,"tt":"stylus","id":4,"w":100,"h":50,"ts":7,"sq":9}`), &in); err != nil {
		t.Fatal(err)
	}
	if in.kind() != "input" || in.Action != "down" || in.Pressure != 0.5 || in.ToolType != "stylus" ||
		in.PointerID != 4 || in.Width != 100 || in.Height != 50 || in.Timestamp != 7 || in.InputSeq != 9 {
		t.Fatalf("input packet = %+v", in)
	}
	var hello packet
	if err := json.Unmarshal([]byte(`{"type":"hello","device":"d"}`), &hello); err != nil || hello.kind() != "hello" {
		t.Fatalf("hello kind %q, %v", hello.kind(), err)
	}
}
