package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

func nativeTestFrame(kind, flags byte, seq, pipe uint32, payload []byte) []byte {
	var hdr [nativeFrameHeaderSize]byte
	binary.LittleEndian.PutUint32(hdr[0:], uint32(len(payload)))
	hdr[4], hdr[5] = kind, flags
	binary.LittleEndian.PutUint32(hdr[8:], seq)
	binary.LittleEndian.PutUint32(hdr[12:], pipe)
	return append(hdr[:], payload...)
}

func TestReadNativeFrame(t *testing.T) {
	stream := append(nativeTestFrame(1, 1, 7, 900, []byte{0, 0, 0, 1, 0x67}), nativeTestFrame(3, 0, 0, 0, []byte{1})...)
	r := bytes.NewReader(stream)
	h, p, err := readNativeFrame(r, nil)
	if err != nil || h.Kind != 1 || h.Flags != 1 || h.InputSeq != 7 || h.PipeUs != 900 || len(p) != 5 {
		t.Fatalf("frame 1: %+v %v %v", h, p, err)
	}
	h, p, err = readNativeFrame(r, p)
	if err != nil || h.Kind != 3 || len(p) != 1 || p[0] != 1 {
		t.Fatalf("frame 2: %+v %v %v", h, p, err)
	}
	if _, _, err = readNativeFrame(r, p); err == nil {
		t.Fatal("expected EOF")
	}
}

func TestReadNativeFrameRejectsHugePayload(t *testing.T) {
	var hdr [nativeFrameHeaderSize]byte
	binary.LittleEndian.PutUint32(hdr[0:], nativeFrameMaxPayload+1)
	if _, _, err := readNativeFrame(bytes.NewReader(hdr[:]), nil); err == nil {
		t.Fatal("expected size error")
	}
}

func TestAUIsDecodableStart(t *testing.T) {
	sc := []byte{0, 0, 0, 1}
	build := func(types ...byte) []byte {
		var b []byte
		for _, ty := range types {
			b = append(b, sc...)
			b = append(b, ty, 0x11, 0x22)
		}
		return b
	}
	if !auIsDecodableStart(build(0x67, 0x68, 0x65)) {
		t.Fatal("SPS+PPS+IDR must be accepted")
	}
	if auIsDecodableStart(build(0x41)) || auIsDecodableStart(build(0x67, 0x68)) {
		t.Fatal("non-IDR starts must be rejected")
	}
}

func TestNativeVendorAllowed(t *testing.T) {
	cases := []struct {
		mode, vendor string
		want         bool
	}{
		{"auto", "Intel iHD driver for Intel(R) Gen Graphics", true},
		{"auto", "Mesa Gallium driver for AMD", false},
		{"on", "Mesa Gallium driver for AMD", true},
		{"off", "Intel iHD", false},
	}
	for _, c := range cases {
		if got := nativeVendorAllowed(c.mode, c.vendor); got != c.want {
			t.Errorf("%s/%s = %v", c.mode, c.vendor, got)
		}
	}
}

func TestFrameMetaPayload(t *testing.T) {
	if got := frameMetaPayload(1, 2); !bytes.Equal(got, []byte{0, 0, 0, 1, 0, 0, 0, 2}) {
		t.Fatalf("%v", got)
	}
}

func TestWithNativeHelperCandidateOffKeepsSpecs(t *testing.T) {
	in := []pipelineSpec{{Name: "x"}}
	out := withNativeHelperCandidate(in, videoOptions{NativeMedia: "off"}, captureTarget{}, ":0")
	if len(out) != 1 || out[0].Framed {
		t.Fatalf("%+v", out)
	}
	t.Setenv(nativeHelperEnv, "/nonexistent/helper")
	out = withNativeHelperCandidate(in, videoOptions{NativeMedia: "auto"}, captureTarget{}, ":0")
	if len(out) != 1 {
		t.Fatalf("missing helper must keep ffmpeg only: %+v", out)
	}
}

func TestProbeRealHelper(t *testing.T) {
	path := os.Getenv(nativeHelperEnv)
	if path == "" {
		t.Skip("DRAWSPLAY_MEDIA_HELPER not set")
	}
	res := probeNativeHelper(path)
	if !res.OK || res.Vendor == "" || res.Version != 1 {
		t.Skipf("helper not usable on this machine: %+v", res)
	}
	args := nativeHelperArgs(":0", displayRect{}, 60, "8M", true)
	if len(args) == 0 {
		t.Fatal("no args")
	}
}
