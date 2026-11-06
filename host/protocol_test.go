package main

import "testing"

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
