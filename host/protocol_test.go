package main

import "testing"

func TestCodecNegotiationScaffoldKeepsH264(t *testing.T) {
	for _, offered := range [][]string{nil, {"h264"}, {"H264"}, {"future-codec"}} {
		publishClientVideoCodecs(offered)
		if got := negotiateVideoCodec(); got != "h264" {
			t.Errorf("offered %v selected %q, want h264", offered, got)
		}
	}
	publishClientVideoCodecs(nil)
}
